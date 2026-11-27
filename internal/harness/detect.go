package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// SignInChecker runs one sign-in status command (lane 6.2: signin.go,
// *SignInCheck). The Detector calls it in the probe pass, after initialize,
// and stores the verdict in ProbeRecord.Auth. The checker applies the
// status profile, the verdict mapping, the credential-variable rule and the
// unsafe-environment rule, discards every byte of output, and returns only
// the verdict (with CheckedAt).
type SignInChecker interface {
	Check(ctx context.Context, f Found, r SignInRequest) SignInStatus
}

// SignInRequest says who asks for a sign-in check and under which settings
// (the same type as lane 6.2's signin.go; integration keeps one copy).
type SignInRequest struct {
	// Requester is RequesterPerson, RequesterAgent or RequesterDaemon.
	Requester string
	// AutoProbe and Enabled are the effective values of
	// ai.harnesses.auto_probe and ai.harnesses.sign_in_check.
	AutoProbe bool
	Enabled   bool
	// Previous is the stored verdict for this identity, if any.
	Previous *SignInStatus
}

// Options configure a Detector. Zero fields take production defaults.
type Options struct {
	// Home is the agentflow home. "" means config.HomeDir().
	Home string
	// Getenv reads PATH. nil means os.Getenv.
	Getenv func(string) string
	// WorkDirs are directories whose repositories' worktrees are skipped on
	// PATH (inside_repository): the CLI's working directory, or every
	// repository the daemon serves. Called once per pass.
	WorkDirs func() []string
	// Store is the probe cache. nil means an in-memory store (lane 6.2
	// wires cache.json).
	Store Store
	// Prober runs initialize. nil means NoProber (probe status none).
	Prober Prober
	// SignIn runs the sign-in check. nil means none: harnesses with a
	// status command show sign-in unknown/no_check.
	SignIn SignInChecker
	// Now is the clock. nil means time.Now.
	Now func() time.Time
	// Platform is the registry's binary key. "" means this machine's.
	Platform string
}

// New returns the Detector over the pinned registry, the overlay, enabled
// declarative plugins and the personal overrides.
func New(o Options) Detector {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Store == nil {
		o.Store = NewMemoryStore()
	}
	if o.Prober == nil {
		o.Prober = NoProber{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Platform == "" {
		o.Platform = acpregistry.CurrentPlatform()
	}
	return &detector{o: o}
}

type detector struct{ o Options }

// Process-wide limits: probes at once, and one sign-in check at a time.
var (
	probeSlots = make(chan struct{}, DefaultMaxProbes)
	signInMu   sync.Mutex
)

// settings are the personal values one pass uses.
type settings struct {
	home        string
	homeOK      bool
	autoProbe   bool
	signInCheck bool
	timeout     time.Duration
	user        *config.User
}

// Detect runs one detection pass.
func (d *detector) Detect(ctx context.Context, o DetectOptions) (Report, error) {
	now := d.o.Now()
	pathValue := d.o.Getenv("PATH")
	// The guard runs before any home or configuration is read.
	for _, dir := range filepath.SplitList(pathValue) {
		if filepath.IsAbs(dir) {
			if err := testGuard(dir); err != nil {
				return Report{}, err
			}
		}
	}
	cat, err := acpregistry.Load()
	if err != nil {
		return Report{}, err
	}
	overlay, err := LoadOverlay()
	if err != nil {
		return Report{}, err
	}
	st, problems := d.settings()
	layers := Layers{Catalog: cat, Overlay: overlay, Platform: d.o.Platform}
	if st.homeOK && st.user != nil {
		layers.Personal = st.user.AI.Harnesses.Overrides
		layers.Disabled = st.user.AI.Harnesses.Disabled
		plugins, pp := pluginHarnesses(st.home, st.user)
		layers.Plugins = plugins
		problems = append(problems, pp...)
	}
	defs, lp := Compose(layers)
	problems = append(problems, lp...)
	if len(o.IDs) > 0 {
		keep := defs[:0]
		for _, def := range defs {
			for _, id := range o.IDs {
				if def.ID == id {
					keep = append(keep, def)
					break
				}
			}
		}
		defs = keep
	}

	var dirs []string
	if d.o.WorkDirs != nil {
		dirs = d.o.WorkDirs()
	}
	res, err := newFinder(pathValue, RepositoryWorktrees(ctx, dirs...)).run(defs)
	if err != nil {
		return Report{}, fmt.Errorf("harness: detection: %w", err)
	}
	defs = dropSameProgram(defs, res)

	person := o.Requester == RequesterPerson
	probeAllowed := o.Probe && (person || st.autoProbe)
	report := Report{
		Registry:  RegistryInfo{Commit: cat.Source.Commit, CommittedAt: cat.Source.CommittedAt, Agents: cat.Source.Agents},
		AutoProbe: st.autoProbe,
		Problems:  problems,
		CheckedAt: now,
	}
	if o.Probe && !probeAllowed {
		report.ProbeRefused = CodeNeedsPerson
	}
	for _, s := range res.skipped {
		report.SkippedPath = append(report.SkippedPath, SkippedDir{Dir: Tilde(s.Dir), Reason: s.Reason})
	}
	for _, def := range defs {
		f, ok := res.found[def.ID]
		h, err := d.harness(ctx, st, def, f, ok, o, probeAllowed, now)
		if err != nil {
			return Report{}, err
		}
		if h.State == StateNotInstalled && !o.All {
			continue
		}
		report.Harnesses = append(report.Harnesses, h)
	}
	sort.SliceStable(report.Harnesses, func(i, j int) bool {
		a, b := report.Harnesses[i], report.Harnesses[j]
		if ga, gb := stateGroup(a), stateGroup(b); ga != gb {
			return ga < gb
		}
		return a.ID < b.ID
	})
	if report.Harnesses == nil {
		report.Harnesses = []Harness{}
	}
	return report, nil
}

// harness builds one Harness, probing it when allowed.
func (d *detector) harness(ctx context.Context, st settings, def Layered, f found, isFound bool, o DetectOptions, probeAllowed bool, now time.Time) (Harness, error) {
	dd := def.Definition
	h := Harness{
		ID: def.ID, Name: dd.Name, Kind: dd.Kind, Sources: append([]string(nil), def.Sources...),
		Command: dd.Command, Args: append([]string(nil), dd.Args...),
		Probe: Probe{Status: ProbeNone, Code: CodeNotProbed},
		Auth:  SignInStatus{Status: SignInUnknown, Code: CodeNoCheck},
	}
	if h.Name == "" {
		h.Name = def.ID
	}
	if h.Kind == "" {
		h.Kind = harnessdef.KindNative
	}
	if filepath.IsAbs(h.Command) {
		h.Command = filepath.Base(h.Command)
	}
	if e := def.Entry; e != nil {
		h.Description = e.Description
		h.RegistryVersion = e.Version
		if e.Quarantined {
			h.Quarantined, h.QuarantineReason = true, e.QuarantineReason
			h.Notes = append(h.Notes, NoteQuarantined)
		}
		if e.EnvIgnored {
			h.Notes = append(h.Notes, NoteRegistryEnvIgnored)
		}
	}
	if b := f.BaseFound; b != nil {
		h.Base = &Base{Name: b.Definition.Name, Command: b.Definition.Command, Path: Tilde(b.Path)}
	}
	found := isFound && f.Path != ""
	var rec *ProbeRecord
	if found {
		h.Path = Tilde(f.Path)
		rec = d.record(f, now)
		switch {
		case f.shim:
			h.Probe = Probe{Status: ProbeRefused, Code: CodeShim}
		case probeAllowed && d.shouldProbe(o, rec, now):
			r, err := d.probe(ctx, st, f, o, rec, now)
			if err != nil {
				return Harness{}, err
			}
			rec = &r
			h.Probe = Probe{Status: r.Status, Code: r.Code, At: timePtr(r.ProbedAt), Duration: r.DurationMS}
			if r.Status == ProbeNone {
				h.Probe = Probe{Status: ProbeNone, Code: CodeNotProbed}
			}
		case rec != nil && rec.Status != ProbeNone:
			h.Probe = Probe{Status: ProbeCached, Code: rec.Code, At: timePtr(rec.ProbedAt), Duration: rec.DurationMS}
		case o.Probe && !probeAllowed:
			h.Probe = Probe{Status: ProbeRefused, Code: CodeNeedsPerson}
		}
		if rec != nil {
			h.Version, h.VersionCode = rec.Version, rec.VersionCode
			h.ACP, h.SignIn = rec.ACP, rec.SignIn
		}
		h.Auth = d.auth(st, dd, f, rec, o, probeAllowed)
	} else if dd.StatusFrom != "" && !st.signInCheck {
		h.Auth = SignInStatus{Status: SignInUnknown, Code: CodeOff}
	}
	state, code := Resolve(ResolveInput{
		Kind: h.Kind, Found: found, BaseFound: f.BaseFound != nil, Record: rec,
		Version: h.Version, MinVersion: dd.MinVersion, Auth: h.Auth, ProbeCode: h.Probe.Code,
	})
	h.State, h.Code = state, code
	h.Fix = FixFor(FixInput{Harness: h, Definition: dd, Entry: def.Entry, Platform: d.o.Platform, ProbeTimeout: st.timeout, Windows: isWindows()})
	return h, nil
}

// record is the stored record for f's identity, if it is younger than
// DefaultRecordTTL.
func (d *detector) record(f found, now time.Time) *ProbeRecord {
	r, ok, err := d.o.Store.Get(f.ID, f.Identity)
	if err != nil || !ok || r.Identity != f.Identity || (!r.ProbedAt.IsZero() && now.Sub(r.ProbedAt) > DefaultRecordTTL) {
		return nil
	}
	return &r
}

// shouldProbe applies the cooldown and the daemon's changed-identity rule.
func (d *detector) shouldProbe(o DetectOptions, rec *ProbeRecord, now time.Time) bool {
	if rec == nil || rec.ProbedAt.IsZero() {
		return true
	}
	if now.Sub(rec.ProbedAt) < DefaultProbeCooldown {
		return false
	}
	return o.Requester != RequesterDaemon
}

// probe runs the version command, initialize and, when allowed, the sign-in
// check, and stores the record.
func (d *detector) probe(ctx context.Context, st settings, f found, o DetectOptions, prev *ProbeRecord, now time.Time) (ProbeRecord, error) {
	select {
	case probeSlots <- struct{}{}:
	case <-ctx.Done():
		return ProbeRecord{}, ctx.Err()
	}
	defer func() { <-probeSlots }()

	version, versionCode, err := runVersion(ctx, st.home, f)
	if err != nil {
		return ProbeRecord{}, err
	}
	r := d.o.Prober.Probe(ctx, f.Found, st.timeout)
	r.ID, r.Identity = f.ID, f.Identity
	r.Version, r.VersionCode = version, versionCode
	if r.ProbedAt.IsZero() {
		r.ProbedAt = now
	}
	r.ACP = sanitizeACP(r.ACP)
	if prev != nil {
		r.Auth, r.BaseIdentity = prev.Auth, prev.BaseIdentity
		if prev.AuthRequiredAt != nil && r.AuthRequiredAt == nil {
			r.AuthRequiredAt = prev.AuthRequiredAt
		}
	}
	if d.mayCheckSignIn(st, f, o) {
		baseIdentity := ""
		if f.Definition.StatusFrom == harnessdef.StatusFromBase && f.BaseFound != nil {
			baseIdentity = f.BaseFound.Identity
		}
		// A person's probe always checks. Otherwise the verdict is reused
		// until the base CLI's identity changes or it is a day old (a new
		// harness identity has no record, so it checks too), and never
		// sooner than DefaultStatusInterval.
		due := o.Requester == RequesterPerson || r.Auth == nil || r.Auth.CheckedAt == nil
		if !due {
			age := now.Sub(*r.Auth.CheckedAt)
			due = age >= DefaultStatusInterval && (r.BaseIdentity != baseIdentity || age > DefaultRecordTTL)
		}
		if due {
			if err := d.guardStatus(f); err != nil {
				return ProbeRecord{}, err
			}
			signInMu.Lock()
			v := d.o.SignIn.Check(ctx, f.Found, SignInRequest{
				Requester: o.Requester, AutoProbe: st.autoProbe, Enabled: st.signInCheck, Previous: r.Auth,
			})
			signInMu.Unlock()
			if v.CheckedAt == nil {
				v.CheckedAt = timePtr(d.o.Now())
			}
			r.Auth, r.BaseIdentity = &v, baseIdentity
		}
	}
	if r.Status != ProbeNone || r.Version != "" || r.Auth != nil {
		if err := d.o.Store.Put(r); err != nil {
			return r, nil // the cache is best effort; the pass still reports
		}
	}
	return r, nil
}

// guardStatus checks the file the status command would start.
func (d *detector) guardStatus(f found) error {
	p := f.Path
	if f.Definition.StatusFrom == harnessdef.StatusFromBase && f.BaseFound != nil {
		p = f.BaseFound.Path
	}
	return testGuard(p)
}

// mayCheckSignIn: a found harness with a status command, a checker, the
// toggle on, and for the base CLI a found base.
func (d *detector) mayCheckSignIn(st settings, f found, o DetectOptions) bool {
	def := f.Definition
	if d.o.SignIn == nil || def.StatusFrom == "" || !st.signInCheck {
		return false
	}
	if def.StatusFrom == harnessdef.StatusFromBase && f.BaseFound == nil {
		return false
	}
	return o.Requester == RequesterPerson || st.autoProbe
}

// auth is the sign-in verdict to show.
func (d *detector) auth(st settings, def harnessdef.Definition, f found, rec *ProbeRecord, o DetectOptions, probeAllowed bool) SignInStatus {
	switch {
	case def.StatusFrom == "":
		return SignInStatus{Status: SignInUnknown, Code: CodeNoCheck}
	case !st.signInCheck:
		return SignInStatus{Status: SignInUnknown, Code: CodeOff}
	case d.o.SignIn == nil:
		return SignInStatus{Status: SignInUnknown, Code: CodeNoCheck}
	case def.StatusFrom == harnessdef.StatusFromBase && f.BaseFound == nil:
		return SignInStatus{Status: SignInUnknown, Code: CodeBaseMissing}
	case rec != nil && rec.Auth != nil:
		return *rec.Auth
	case o.Probe && !probeAllowed, o.Requester != RequesterPerson && !st.autoProbe:
		return SignInStatus{Status: SignInUnknown, Code: CodeNeedsPerson}
	}
	return SignInStatus{Status: SignInUnknown, Code: CodeNotProbed}
}

// settings reads the personal values, or fails closed: with an unsafe or
// unreadable home, no personal or plugin layer is read and both toggles
// count as off.
func (d *detector) settings() (settings, []Problem) {
	st := settings{timeout: DefaultProbeTimeout}
	home := d.o.Home
	if home == "" {
		h, err := config.HomeDir()
		if err != nil {
			return st, []Problem{homeProblem("agentflow cannot find its home: " + err.Error() + ".")}
		}
		home = h
	}
	st.home = home
	if !filepath.IsAbs(home) {
		return st, []Problem{homeProblem("The agentflow home is not an absolute path.")}
	}
	if inside, why := config.InsideRepository(home); inside {
		return st, []Problem{homeProblem("The agentflow home " + why + ".")}
	}
	if homeUnsafe(home) {
		return st, []Problem{homeProblem("The agentflow home is not owned by you or is writable by others.")}
	}
	u := &config.User{}
	b, err := os.ReadFile(filepath.Join(home, "config.yml"))
	switch {
	case err == nil:
		if err := yaml.Unmarshal(b, u); err != nil {
			return st, []Problem{{Layer: SourcePersonal, Code: harnessdef.CodeRule, Message: "Your config.yml does not load, so agentflow ignores your harness settings: " + err.Error() + "."}}
		}
		if err := config.ValidateAI(u.AI); err != nil {
			return st, []Problem{{Layer: SourcePersonal, Code: harnessdef.CodeRule, Message: "Your harness settings are not valid, so agentflow ignores them: " + err.Error() + "."}}
		}
	case !errors.Is(err, os.ErrNotExist):
		return st, []Problem{homeProblem("agentflow cannot read your config.yml: " + err.Error() + ".")}
	}
	st.homeOK, st.user = true, u
	st.autoProbe = u.AI.Harnesses.AutoProbeOn()
	st.signInCheck = u.AI.Harnesses.SignInCheckOn()
	st.timeout = u.AI.Harnesses.EffectiveProbeTimeout()
	return st, nil
}

func homeProblem(msg string) Problem {
	return Problem{Layer: SourcePersonal, Code: ProblemHomeUnsafe, Message: msg + " agentflow ignores personal harness overrides and plugin harnesses, and probes nothing on its own."}
}

// pluginHarnesses are the harness entries of enabled declarative plugins.
func pluginHarnesses(home string, u *config.User) ([]PluginHarness, []Problem) {
	reg, err := manifest.ReadRegistry(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []Problem{{Layer: SourcePluginPrefix + "*", Code: harnessdef.CodeRule, Message: "The plugin registry cannot be read, so agentflow ignores plugin harnesses."}}
	}
	var out []PluginHarness
	var problems []Problem
	for _, p := range reg.Plugins {
		if !u.Plugins[p.ID].Enabled || len(p.Manifest.Extensions.Harness) == 0 {
			continue
		}
		if p.Manifest.Tier != manifest.TierDeclarative {
			problems = append(problems, Problem{Layer: SourcePluginPrefix + p.ID, Code: harnessdef.CodeRule, Message: "Only a declarative plugin may add harnesses."})
			continue
		}
		for _, e := range p.Manifest.Extensions.Harness {
			out = append(out, PluginHarness{Plugin: p.ID, ID: e.ID, Definition: e.Definition})
		}
	}
	return out, problems
}

// sanitizeACP clamps the agent's text in an initialize answer.
func sanitizeACP(a *ACPInfo) *ACPInfo {
	if a == nil {
		return nil
	}
	c := *a
	c.AgentName = SanitizeText(c.AgentName, MaxNameLen)
	c.AgentTitle = SanitizeText(c.AgentTitle, MaxNameLen)
	c.AgentVersion = SanitizeText(c.AgentVersion, maxVersionLen)
	return &c
}

// Tilde shows a path under the OS home as "~/...".
func Tilde(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	for _, h := range []string{home, resolved(home)} {
		if rel, err := filepath.Rel(h, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "~" + string(filepath.Separator) + rel
		}
	}
	return p
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// NewForWorkDir is New with production defaults for a command run in
// workDir: PATH entries inside any worktree of the repository containing
// workDir are skipped. It is what internal/app sets as
// app.NewHarnessDetector. Lane 6.2 replaces the in-memory Store and
// NoProber by wiring its own in Options.
func NewForWorkDir(workDir string) Detector {
	return New(Options{WorkDirs: func() []string { return []string{workDir} }})
}

// NoProber is the Prober until lane 6.2's probe is wired: it runs nothing
// and returns probe status none.
type NoProber struct{}

// Probe returns status none, code not_probed.
func (NoProber) Probe(context.Context, Found, time.Duration) ProbeRecord {
	return ProbeRecord{Status: ProbeNone, Code: CodeNotProbed}
}

// MemoryStore is a Store in memory, for tests and until lane 6.2's
// cache.json is wired. It is safe for concurrent use.
type MemoryStore struct {
	mu        sync.Mutex
	records   map[string]ProbeRecord
	published map[string]PublishedState
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]ProbeRecord{}}
}

func (m *MemoryStore) Get(id, identity string) (ProbeRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id+"\x00"+identity]
	return r, ok, nil
}

func (m *MemoryStore) Put(r ProbeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[r.ID+"\x00"+r.Identity] = r
	return nil
}

func (m *MemoryStore) MarkAuthRequired(id, identity string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := id + "\x00" + identity
	r, ok := m.records[key]
	if !ok {
		r = ProbeRecord{ID: id, Identity: identity, Status: ProbeNone, Code: CodeNotProbed}
	}
	r.AuthRequiredAt = &at
	m.records[key] = r
	return nil
}

func (m *MemoryStore) Published() (map[string]PublishedState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.published == nil {
		return nil, nil
	}
	out := make(map[string]PublishedState, len(m.published))
	for k, v := range m.published {
		out[k] = v
	}
	return out, nil
}

func (m *MemoryStore) SetPublished(p map[string]PublishedState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = make(map[string]PublishedState, len(p))
	for k, v := range p {
		m.published[k] = v
	}
	return nil
}

// dropSameProgram keeps one entry per installed program. Two catalog
// definitions can name the same command (the legacy "kimi" and "kimi-code"
// both install "kimi"); listing the one file twice would show a person one
// program as two harnesses. Found definitions whose file resolves to the
// same target form a group, and only its highest-ranked
// member stays: the one with the highest layer (personal, then a plugin,
// then the agentflow overlay, then the registry alone), then the lowest ID.
// A definition the person or a plugin shaped is never dropped: the person
// named it, or reviewed the plugin that did.
func dropSameProgram(defs []Layered, res finderResult) []Layered {
	type member struct {
		id   string
		rank int
	}
	groups := map[string][]member{}
	for _, d := range defs {
		f, ok := res.found[d.ID]
		if !ok || f.Path == "" || f.Target == "" {
			continue
		}
		groups[f.Target] = append(groups[f.Target], member{id: d.ID, rank: layerRank(d.Sources)})
	}
	drop := map[string]bool{}
	for _, ms := range groups {
		if len(ms) < 2 {
			continue
		}
		best := ms[0]
		for _, m := range ms[1:] {
			if m.rank > best.rank || (m.rank == best.rank && m.id < best.id) {
				best = m
			}
		}
		for _, m := range ms {
			if m.id != best.id && m.rank < layerRankPlugin {
				drop[m.id] = true
			}
		}
	}
	if len(drop) == 0 {
		return defs
	}
	keep := make([]Layered, 0, len(defs))
	for _, d := range defs {
		if !drop[d.ID] {
			keep = append(keep, d)
		}
	}
	return keep
}

const (
	layerRankRegistry = iota
	layerRankAgentflow
	layerRankPlugin
	layerRankPersonal
)

// layerRank is the highest layer in sources.
func layerRank(sources []string) int {
	r := layerRankRegistry
	for _, s := range sources {
		switch {
		case s == SourcePersonal:
			r = max(r, layerRankPersonal)
		case strings.HasPrefix(s, SourcePluginPrefix):
			r = max(r, layerRankPlugin)
		case s == SourceAgentflow:
			r = max(r, layerRankAgentflow)
		}
	}
	return r
}
