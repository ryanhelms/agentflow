package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// HostSource is the bus.Event Source of lifecycle and delivery events.
const HostSource = "plugin.host"

// Worktree is one worktree the daemon serves.
type Worktree struct {
	Repo bus.RepoID
	ID   bus.WorktreeID
	Root string
	// Main marks the repository's main worktree: the default of a call
	// that names a repository but no worktree.
	Main bool
}

// EventBus is what the host needs of the daemon's bus.
type EventBus interface {
	bus.Publisher
	bus.Subscriber
}

// Options configure a Host. Source, Layers, Bus, Home and Served are
// required.
type Options struct {
	Source manifest.Source
	Layers Layers
	Bus    EventBus
	// Home is the agentflow home (config.HomeDir()); the host keeps its
	// files under Home/plugins.
	Home string
	// Served lists the worktrees the daemon serves.
	Served func() []Worktree
	// Commands looks up a command by Spec name; nil means command.Default.
	Commands func(name string) (command.Command, bool)
	// OpenApp opens a worktree's App; nil means app.Open.
	OpenApp func(ctx context.Context, dir, client string) (*app.App, error)
	// Launch starts a process plugin (jsonrpc.Launch); Environ builds its
	// environment from the parent's (jsonrpc.ScrubbedEnv). Without them a
	// process plugin fails to start with code launch.
	Launch  func(ctx context.Context, s jsonrpc.LaunchSpec) (jsonrpc.Process, error)
	Environ func(data string, extra map[string]string) ([]string, error)
	// Version is agentflow's version, sent in initialize.
	Version string
	// Now and After are the clock; nil means time.Now and time.After.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
	// PingInterval, PingTimeout, DeliverTimeout and InitializeTimeout default to the
	// constants of the same names. Tests shorten them.
	PingInterval      time.Duration
	PingTimeout       time.Duration
	DeliverTimeout    time.Duration
	InitializeTimeout time.Duration
}

// Host runs the installed, enabled plugins (ADR, "Phase 5"). Every plugin
// has its own runner goroutine and bus subscription, so a slow, hanging or
// crashing plugin holds up only itself.
type Host struct {
	o         Options
	factories map[manifest.Tier]Factory
	dl        *fileDeadLetter
	invoker   func(target Worktree) command.Invoker

	reload sync.Mutex // serializes Reload and Stop

	mu        sync.Mutex
	ctx       context.Context
	runners   map[string]*runner
	status    map[string]*Status
	installed map[string]manifest.Installed
	disabled  map[bus.RepoID]map[string]bool
	stopped   bool

	// limits are each plugin ID's rate limits, kept across restarts so a
	// crashing plugin cannot reset its budget.
	limitsMu sync.Mutex
	limits   map[string]*rateLimits
}

// rateLimits are one plugin's invoke and publish token buckets.
type rateLimits struct{ invokes, publishes *bucket }

// limitsFor returns plugin id's rate limits, made on first use.
func (h *Host) limitsFor(id string) *rateLimits {
	h.limitsMu.Lock()
	defer h.limitsMu.Unlock()
	if h.limits == nil {
		h.limits = map[string]*rateLimits{}
	}
	l, ok := h.limits[id]
	if !ok {
		now := h.o.Now
		l = &rateLimits{invokes: newBucket(InvokesPerMinute, InvokeBurst, now), publishes: newBucket(PublishesPerMinute, PublishBurst, now)}
		h.limits[id] = l
	}
	return l
}

// NewHost returns a Host. Call Start, then Reload whenever the registry,
// a configuration layer or the served worktrees change.
func NewHost(o Options) *Host {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.PingInterval <= 0 {
		o.PingInterval = PingInterval
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = PingTimeout
	}
	if o.DeliverTimeout <= 0 {
		o.DeliverTimeout = DeliverTimeout
	}
	if o.InitializeTimeout <= 0 {
		o.InitializeTimeout = InitializeTimeout
	}
	if o.Commands == nil {
		o.Commands = command.Default.Lookup
	}
	if o.OpenApp == nil {
		o.OpenApp = app.Open
	}
	if o.Served == nil {
		o.Served = func() []Worktree { return nil }
	}
	h := &Host{o: o, runners: map[string]*runner{}, status: map[string]*Status{}, installed: map[string]manifest.Installed{},
		disabled: map[bus.RepoID]map[string]bool{}, ctx: context.Background()}
	h.dl = openDeadLetter(filepath.Join(o.Home, manifest.PluginsDir, manifest.DeadLetterFile))
	h.factories = factories(h)
	h.invoker = func(target Worktree) command.Invoker {
		return command.NewInvoker(command.Options{Emitters: busEmitters(o.Bus, target), Auditor: command.AuditLog{}})
	}
	return h
}

// Start sets the context the plugins run under and loads them.
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	return h.Reload(ctx)
}

// Reload reads the registry, the personal layer and the repositories'
// plugins.disabled again, then starts, restarts, updates or stops plugins
// to match. A plugin in failed stays there until its hash, settings or
// enabled flag change.
func (h *Host) Reload(ctx context.Context) error {
	h.reload.Lock()
	defer h.reload.Unlock()
	h.mu.Lock()
	stopped, runCtx := h.stopped, h.ctx
	h.mu.Unlock()
	if stopped {
		return nil
	}
	installed, err := h.o.Source.Installed(ctx)
	if err != nil {
		return err
	}
	personal, err := h.o.Layers.Personal(ctx)
	if err != nil {
		return err
	}
	h.refreshDisabled(ctx)
	seen := map[string]bool{}
	for _, p := range installed {
		seen[p.ID] = true
		pp := personal[p.ID]
		settings := effectiveSettings(p.Manifest, pp.Settings)
		h.mu.Lock()
		h.installed[p.ID] = p
		r := h.runners[p.ID]
		h.mu.Unlock()
		if !pp.Enabled {
			if r != nil {
				r.stop(false)
				h.dropRunner(p.ID, r)
			} else if h.state(p.ID) == "" {
				h.transition(p, bus.PluginInstalled, "", nil)
			}
			continue
		}
		if r != nil && r.p.Hash == p.Hash {
			if reflect.DeepEqual(r.settings(), settings) {
				continue
			}
			if !needsRestart(p.Manifest, r.settings(), settings) && h.state(p.ID) != bus.PluginFailed {
				r.updateSettings(settings)
				continue
			}
		}
		if r != nil {
			r.stop(false)
			h.dropRunner(p.ID, r)
		}
		r = newRunner(h, runCtx, p, settings)
		h.mu.Lock()
		h.runners[p.ID] = r
		h.mu.Unlock()
		go r.run()
	}
	h.mu.Lock()
	var gone []string
	for id := range h.installed {
		if !seen[id] {
			gone = append(gone, id)
		}
	}
	h.mu.Unlock()
	for _, id := range gone {
		h.remove(id)
	}
	return nil
}

// remove stops a plugin that left the registry and forgets it.
func (h *Host) remove(id string) {
	h.mu.Lock()
	r, p := h.runners[id], h.installed[id]
	h.mu.Unlock()
	if r != nil {
		r.stop(true)
	} else {
		switch h.state(id) {
		case bus.PluginInstalled, bus.PluginStopped, bus.PluginFailed:
			h.transition(p, bus.PluginRemoved, "", nil)
		}
	}
	h.mu.Lock()
	delete(h.runners, id)
	delete(h.installed, id)
	delete(h.status, id)
	h.mu.Unlock()
}

func (h *Host) dropRunner(id string, r *runner) {
	h.mu.Lock()
	if h.runners[id] == r {
		delete(h.runners, id)
	}
	h.mu.Unlock()
}

// Stop stops every plugin (stopping, then stopped) and refuses later
// reloads.
func (h *Host) Stop() {
	h.reload.Lock()
	defer h.reload.Unlock()
	h.mu.Lock()
	h.stopped = true
	runners := make([]*runner, 0, len(h.runners))
	for _, r := range h.runners {
		runners = append(runners, r)
	}
	h.runners = map[string]*runner{}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Add(1)
		go func() { defer wg.Done(); r.stop(false) }()
	}
	wg.Wait()
}

// Status returns every known plugin's status, sorted by ID.
func (h *Host) Status() []Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Status, 0, len(h.status))
	for _, st := range h.status {
		c := *st
		c.Subscriptions = append([]string{}, st.Subscriptions...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Letters returns the persistent Dead Letter Channel's letters, oldest
// first.
func (h *Host) Letters() []bus.Letter { return h.dl.Letters() }

// DeadLetter is the host's persistent Dead Letter Channel.
func (h *Host) DeadLetter() bus.DeadLetter { return h.dl }

func (h *Host) state(id string) bus.PluginState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.status[id]; st != nil {
		return st.State
	}
	return ""
}

// transition moves plugin p to state to, if plugin.Transitions allows it,
// and publishes plugin.lifecycle.changed. It reports whether it moved.
func (h *Host) transition(p manifest.Installed, to bus.PluginState, code string, retryAt *time.Time) bool {
	h.mu.Lock()
	st := h.status[p.ID]
	if st == nil {
		st = &Status{ID: p.ID, Subscriptions: []string{}}
		h.status[p.ID] = st
	}
	from := st.State
	if from == to || !slices.Contains(Transitions[from], to) {
		h.mu.Unlock()
		return false
	}
	st.State, st.Code, st.Since, st.RetryAt = to, code, h.o.Now().UTC(), retryAt
	payload := bus.PluginLifecycleChanged{Plugin: p.ID, Version: p.Version, Tier: string(p.Tier), State: to, Previous: from,
		Code: code, Restarts: st.Restarts, RetryAt: retryAt}
	h.mu.Unlock()
	h.publishGlobal(bus.TypePluginLifecycleChanged, payload)
	return true
}

func (h *Host) publishGlobal(t bus.Type, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = h.o.Bus.Publish(context.Background(), bus.Event{Type: t, Source: HostSource, Repo: bus.GlobalRepo, Payload: raw})
}

// update changes plugin id's status under the lock.
func (h *Host) update(id string, f func(st *Status)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.status[id]; st != nil {
		f(st)
	}
}

// dead sends e to the Dead Letter Channel for plugin id and publishes
// plugin.delivery.failed. reason is a code, never a plugin's text.
func (h *Host) dead(id string, e bus.Event, reason string) {
	h.dl.Dead(context.Background(), bus.Letter{Event: e, Consumer: OriginPrefix + id, Reason: reason, Time: h.o.Now().UTC()})
	h.update(id, func(st *Status) { st.DeadLetters++ })
	h.publishGlobal(bus.TypePluginDeliveryFailed, bus.PluginDeliveryFailed{Plugin: id, EventID: e.ID, EventType: e.Type, EventRepo: e.Repo, Code: reason})
}

// refreshDisabled reads repo:plugins.disabled of every served repository,
// from its main worktree.
func (h *Host) refreshDisabled(ctx context.Context) {
	roots := map[bus.RepoID]string{}
	for _, w := range h.o.Served() {
		if _, ok := roots[w.Repo]; !ok || w.Main {
			roots[w.Repo] = w.Root
		}
	}
	disabled := map[bus.RepoID]map[string]bool{}
	for repo, root := range roots {
		ids, err := h.o.Layers.RepoDisabled(ctx, root)
		set := map[string]bool{}
		if err != nil {
			// An unreadable repository layer turns every plugin off there:
			// the stricter reading.
			set["*"] = true
		}
		for _, id := range ids {
			set[id] = true
		}
		disabled[repo] = set
	}
	h.mu.Lock()
	h.disabled = disabled
	h.mu.Unlock()
}

// repoAllows reports whether repo is served and does not disable plugin id.
func (h *Host) repoAllows(repo bus.RepoID, id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.disabled[repo]
	return ok && !set[id] && !set["*"]
}

// target resolves where a plugin's call runs: repo and worktree when given,
// else the cause's, else the repository's main worktree.
func (h *Host) target(id string, repo bus.RepoID, worktree bus.WorktreeID) (Worktree, error) {
	served := h.o.Served()
	var found *Worktree
	for i, w := range served {
		if w.Repo != repo {
			continue
		}
		if worktree != "" && w.ID == worktree || worktree == "" && (found == nil || w.Main) {
			found = &served[i]
		}
	}
	if found == nil {
		return Worktree{}, refuse(RefuseUnknownTree)
	}
	if !h.repoAllows(repo, id) {
		return Worktree{}, refuse(RefuseRepoDisabled)
	}
	return *found, nil
}

// effectiveSettings are m's defaults, then the personal values that pass
// validation.
func effectiveSettings(m manifest.Manifest, personal map[string]any) map[string]any {
	out := map[string]any{}
	for _, s := range m.Settings {
		if s.Default != nil {
			if v, ok := validSetting(s, s.Default); ok {
				out[s.Key] = v
			}
		}
		if v, ok := personal[s.Key]; ok {
			if v, ok := validSetting(s, v); ok {
				out[s.Key] = v
			}
		}
	}
	return out
}

// validSetting returns v in its setting's JSON form, if it is valid.
func validSetting(s manifest.Setting, v any) (any, bool) {
	switch s.Type {
	case manifest.SettingBoolean:
		b, ok := v.(bool)
		return b, ok
	case manifest.SettingString:
		str, ok := v.(string)
		if !ok || len(s.Enum) > 0 && !slices.Contains(s.Enum, str) {
			return nil, false
		}
		return str, true
	case manifest.SettingInteger:
		var n int
		switch x := v.(type) {
		case int:
			n = x
		case int64:
			n = int(x)
		case float64:
			if x != float64(int(x)) {
				return nil, false
			}
			n = int(x)
		default:
			return nil, false
		}
		if s.Min != nil && n < *s.Min || s.Max != nil && n > *s.Max {
			return nil, false
		}
		return n, true
	case manifest.SettingStringList:
		list, ok := v.([]any)
		if strs, isStrs := v.([]string); isStrs {
			return append([]string{}, strs...), true
		}
		if !ok {
			return nil, false
		}
		out := make([]string, 0, len(list))
		for _, x := range list {
			str, ok := x.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	}
	return nil, false
}

// needsRestart reports whether a setting that changed applies on restart.
func needsRestart(m manifest.Manifest, old, now map[string]any) bool {
	for _, s := range m.Settings {
		if s.Applies == config.AppliesRestart && !reflect.DeepEqual(old[s.Key], now[s.Key]) {
			return true
		}
	}
	return false
}

// busEmitters publishes a plugin call's op.* events on the daemon's bus,
// in target's repository.
func busEmitters(pub bus.Publisher, target Worktree) command.EmitterSource {
	return func(context.Context, *app.App) (command.Emitter, error) { return busEmitter{pub, target}, nil }
}

type busEmitter struct {
	pub    bus.Publisher
	target Worktree
}

func (b busEmitter) Emit(ctx context.Context, e bus.Event, worktree bool) error {
	e.Repo = b.target.Repo
	if worktree {
		e.Worktree = b.target.ID
	}
	_, err := b.pub.Publish(ctx, e)
	return err
}

var errNoFactory = errors.New("no factory for the plugin's tier")
