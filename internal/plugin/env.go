package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// rememberEvents is how many delivered events env keeps as possible
// causes of a plugin's calls.
const rememberEvents = 512

var operationIDRe = regexp.MustCompile(`^op_[0-9a-f]{32}$`)

// env is the Env Facade of one running instance: every call is checked
// against the plugin's grant, the phase 5 allow-lists, its rate limits and
// the repositories' plugins.disabled before it reaches the bus or the
// Invoker.
type env struct {
	r     *runner
	id    string
	perms manifest.Permissions
	log   *pluginLog

	invokes, publishes *bucket

	mu       sync.Mutex
	settings map[string]any
	subs     []bus.Pattern
	recent   map[string]bus.Event // by ID, payloads dropped
	order    []string
}

var _ Env = (*env)(nil)

func newEnv(r *runner, log *pluginLog) *env {
	l := r.h.limitsFor(r.p.ID)
	return &env{r: r, id: r.p.ID, perms: r.p.Manifest.Permissions, log: log, settings: r.settings(),
		invokes: l.invokes, publishes: l.publishes, recent: map[string]bus.Event{}}
}

func (e *env) Plugin() string { return e.id }

func (e *env) Settings() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return copySettings(e.settings)
}

func (e *env) setSettings(s map[string]any) {
	e.mu.Lock()
	e.settings = s
	e.mu.Unlock()
}

func copySettings(s map[string]any) map[string]any {
	out := make(map[string]any, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

func (e *env) Log(level, message string) { e.log.Line(level, message) }

// subscribable reports whether p selects only types a plugin may receive:
// a subscribable family, or one of SubscribableTypes.
func subscribable(p bus.Pattern) bool {
	first, _, _ := strings.Cut(string(p), ".")
	if slices.Contains(manifest.SubscribableFamilies, first) {
		return true
	}
	return slices.Contains(manifest.SubscribableTypes, bus.Type(p))
}

// receivable reports whether a plugin may ever receive an event of type t.
func receivable(t bus.Type) bool {
	first, _, _ := strings.Cut(string(t), ".")
	return slices.Contains(manifest.SubscribableFamilies, first) || slices.Contains(manifest.SubscribableTypes, t)
}

// granted reports whether pattern p equals a granted pattern, or is a
// single type a granted pattern matches.
func (e *env) granted(p string) bool {
	for _, g := range e.perms.Subscribe {
		if p == g {
			return true
		}
		if !strings.Contains(p, "*") && bus.Pattern(g).Match(bus.Type(p)) {
			return true
		}
	}
	return false
}

// Subscribe implements Env: it replaces the subscription set. Every
// pattern must be valid, granted and subscribable, else nothing changes.
func (e *env) Subscribe(_ context.Context, patterns []string) ([]string, error) {
	if len(patterns) > manifest.MaxPatterns {
		return nil, refuse(RefuseInvalid)
	}
	accepted := make([]bus.Pattern, 0, len(patterns))
	for _, p := range patterns {
		pat, err := bus.ParsePattern(p)
		if err != nil {
			return nil, refuse(RefuseInvalid)
		}
		if !subscribable(pat) {
			return nil, refuse(RefuseNotAllowed)
		}
		if !e.granted(p) {
			return nil, refuse(RefuseNotGranted)
		}
		if !slices.Contains(accepted, pat) {
			accepted = append(accepted, pat)
		}
	}
	e.mu.Lock()
	e.subs = accepted
	e.mu.Unlock()
	out := make([]string, len(accepted))
	for i, p := range accepted {
		out[i] = string(p)
	}
	e.r.h.update(e.id, func(st *Status) { st.Subscriptions = append([]string{}, out...) })
	return out, nil
}

// accepts is the plugin's Message Filter: a repository event (never
// global) of a receivable type the plugin subscribed to, in a served
// repository that does not disable it.
func (e *env) accepts(ev bus.Event) bool {
	if ev.Repo == "" || ev.Repo == bus.GlobalRepo || !receivable(ev.Type) {
		return false
	}
	e.mu.Lock()
	ok := false
	for _, p := range e.subs {
		if p.Match(ev.Type) {
			ok = true
			break
		}
	}
	e.mu.Unlock()
	return ok && e.r.h.repoAllows(ev.Repo, e.id)
}

// remember keeps ev's envelope as a possible cause of a later call.
func (e *env) remember(ev bus.Event) {
	ev.Payload = nil
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.recent[ev.ID]; ok {
		return
	}
	e.recent[ev.ID] = ev
	e.order = append(e.order, ev.ID)
	if len(e.order) > rememberEvents {
		delete(e.recent, e.order[0])
		e.order = e.order[1:]
	}
}

// cause returns the delivered event id: a plugin may only name an event
// it received as the cause of a call.
func (e *env) cause(id string) (bus.Event, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ev, ok := e.recent[id]
	return ev, ok
}

// where resolves the worktree of a call from its cause, repo and worktree.
func (e *env) where(cause string, repo bus.RepoID, worktree bus.WorktreeID) (Worktree, *bus.Event, error) {
	var ce *bus.Event
	if cause != "" {
		ev, ok := e.cause(cause)
		if !ok {
			return Worktree{}, nil, refuse(RefuseInvalid)
		}
		ce = &ev
		switch {
		case repo == "":
			repo, worktree = ev.Repo, ev.Worktree
		case repo != ev.Repo:
			// A cause from another repository would link the two.
			return Worktree{}, nil, refuse(RefuseInvalid)
		}
	}
	if repo == "" {
		return Worktree{}, nil, refuse(RefuseInvalid)
	}
	w, err := e.r.h.target(e.id, repo, worktree)
	return w, ce, err
}

// Publish implements Env: only plugin.annotation.added, granted, on a
// served repository, with a valid payload the host completes and clamps.
func (e *env) Publish(ctx context.Context, r PublishRequest) (PublishResult, error) {
	if !slices.Contains(manifest.PublishableTypes, r.Type) {
		return PublishResult{}, refuse(RefuseNotAllowed)
	}
	if !slices.Contains(e.perms.Publish, string(r.Type)) {
		return PublishResult{}, refuse(RefuseNotGranted)
	}
	if !e.publishes.Take() {
		return PublishResult{}, refuse(RefuseRateLimited)
	}
	var p bus.PluginAnnotationAdded
	dec := json.NewDecoder(bytes.NewReader(r.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return PublishResult{}, refuse(RefuseInvalid)
	}
	p.Plugin = e.id
	if p.Level == "" {
		p.Level = bus.AnnotationInfo
	}
	p = p.Clamp()
	p.Text = SafeText(p.Text, len(p.Text))
	if !operationIDRe.MatchString(p.OperationID) || p.Text == "" || p.Level != bus.AnnotationInfo && p.Level != bus.AnnotationWarning ||
		p.URL != "" && !httpsURL(p.URL) {
		return PublishResult{}, refuse(RefuseInvalid)
	}
	w, ce, err := e.where(r.Cause, r.Repo, r.Worktree)
	if err != nil {
		return PublishResult{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return PublishResult{}, refuse(RefuseInvalid)
	}
	ev := bus.Event{Type: r.Type, Source: OriginPrefix + e.id, Repo: w.Repo, CorrelationID: p.OperationID, Payload: raw}
	if r.Worktree != "" || ce != nil && ce.Worktree != "" {
		ev.Worktree = w.ID
	}
	if ce != nil {
		ev.CausationID = ce.ID
	}
	out, err := e.r.h.o.Bus.Publish(ctx, ev)
	if err != nil {
		return PublishResult{}, refuse(RefuseInvalid)
	}
	e.r.h.update(e.id, func(st *Status) { st.Published++ })
	return PublishResult{ID: out.ID}, nil
}
