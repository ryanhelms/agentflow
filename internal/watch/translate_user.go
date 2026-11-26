package watch

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Limits of the user-level translator.
const (
	// maxOpenAge is how old an approval record file may be and still be
	// open. The longest approval window is one hour (config.MaxApprovalWindow);
	// an older file that still says "pending" is an expiry nobody wrote yet,
	// so the first read skips it.
	maxOpenAge = 2 * time.Hour
	// approvalSkew allows for a record issued just before Serve recorded
	// its time, by another process on the same host.
	approvalSkew = time.Second
	// forgetAbove and forgetAfter bound memory: above that many records, the
	// ones that finished more than forgetAfter ago are forgotten.
	forgetAbove = 2048
	forgetAfter = 24 * time.Hour
	// maxApprovalScan bounds one directory scan.
	maxApprovalScan = 20000
)

// UserTranslator is the Message Translator for the user-level sources
// (UserTarget): the personal configuration layer, the client settings files
// in the OS home and the approval records. It also implements
// ApprovalRouter, Tracker and Timed.
//
//   - config.layer.changed {layer: personal} and client.settings.changed
//     {scope: user} come from content hashes, as ConfigTranslator does for a
//     worktree: only files that were added, removed or changed are named.
//   - approval.* come from the record files. A repository's records are
//     read silently when Serve starts to serve it, and from then on every
//     state change goes through ApprovalTransitions. A record of a
//     repository that is not served produces nothing.
//   - A record that is still open when its expiry passes produces
//     approval.expired with Observed set, from Tick, because the journal
//     writes "expired" only when a process next touches the record.
//
// Nothing it emits carries a path outside a relative file name, file
// contents, a fingerprint or a session MAC.
type UserTranslator struct {
	u            UserTarget
	approvalsDir string
	now          func() time.Time

	mu       sync.Mutex
	wake     func()
	personal map[string][sha256.Size]byte
	settings map[string][sha256.Size]byte
	scopes   map[string]bus.RepoID
	served   map[string]time.Time // when Serve first served each scope key
	records  map[string]*approvalEntry
}

var (
	_ Translator     = (*UserTranslator)(nil)
	_ Tracker        = (*UserTranslator)(nil)
	_ Timed          = (*UserTranslator)(nil)
	_ ApprovalRouter = (*UserTranslator)(nil)
)

// approvalEntry is what the translator remembers about one record file.
type approvalEntry struct {
	repo    bus.RepoID
	state   bus.ApprovalState
	view    bus.Approval
	expires time.Time
	// check is when Tick next looks at an open record: its expiry.
	check time.Time
	seen  time.Time
}

// NewUserTranslator returns a translator for u. u must be absolute; its
// paths are canonicalized the same way FileWatcher.AddUser does, so the
// paths of the Changes it receives compare equal.
func NewUserTranslator(u UserTarget) (*UserTranslator, error) {
	u, err := CanonicalUser(u)
	if err != nil {
		return nil, err
	}
	return &UserTranslator{u: u, approvalsDir: policy.ApprovalsDir(u.Home), now: time.Now,
		personal: map[string][sha256.Size]byte{}, settings: map[string][sha256.Size]byte{},
		scopes: map[string]bus.RepoID{}, served: map[string]time.Time{}, records: map[string]*approvalEntry{}}, nil
}

// Target returns the canonical UserTarget the translator reads.
func (t *UserTranslator) Target() UserTarget { return t.u }

// SetWake implements Timed.
func (t *UserTranslator) SetWake(wake func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.wake = wake
}

// RepoForScope implements ApprovalRouter over the repositories passed to
// Serve.
func (t *UserTranslator) RepoForScope(scopeKey string) (bus.RepoID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	repo, ok := t.scopes[scopeKey]
	return repo, ok
}

// Serve starts to translate the approval records of the repository whose
// policy.ScopeKey is scopeKey into events in repo. Records that exist now
// are read silently, in the state they have: a pending approval that was
// open before the daemon served the repository shows in the approvals
// projection, which reads the records, but does not produce a late
// approval.requested. Serve may be called again for the same repository.
func (t *UserTranslator) Serve(scopeKey string, repo bus.RepoID) {
	t.mu.Lock()
	t.scopes[scopeKey] = repo
	if _, ok := t.served[scopeKey]; !ok {
		t.served[scopeKey] = t.now()
	}
	t.scanLocked(t.now(), true, scopeKey)
	wake := t.wake
	t.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// Track implements Tracker: it records the baseline of the personal
// configuration and the client settings files, so the first change after
// start is reported against it. The Target is only the placeholder the
// watcher batches user-level changes under.
func (t *UserTranslator) Track(context.Context, Target) error {
	personal := personalHashes(t.u.Home)
	settings := settingsHashes(t.u.UserHome, UserClientSettings)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.personal, t.settings = personal, settings
	return nil
}

// Untrack implements Tracker. A UserTranslator keeps its state.
func (t *UserTranslator) Untrack(Target) {}

// Translate implements Translator.
func (t *UserTranslator) Translate(_ context.Context, b Batch) ([]bus.Event, error) {
	var personal, settings, rescan bool
	var names []string
	seen := map[string]bool{}
	for _, ch := range b.Changes {
		if ch.Path == t.approvalsDir {
			rescan = true // the directory appeared, or the watcher asks for a rescan
			continue
		}
		signal, _, ok := ClassifyUser(t.u, ch.Path)
		if !ok {
			continue
		}
		switch signal {
		case SignalPersonalConfig:
			personal = true
		case SignalClientSettings:
			settings = true
		case SignalApproval:
			if name := filepath.Base(ch.Path); !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	at := batchTime(b)
	var out []bus.Event
	var errs []error
	t.mu.Lock()
	if personal {
		evs, err := t.personalEventsLocked(at)
		out, errs = append(out, evs...), appendErr(errs, err)
	}
	if settings {
		evs, err := t.settingsEventsLocked(at)
		out, errs = append(out, evs...), appendErr(errs, err)
	}
	for _, name := range names {
		evs, err := t.applyLocked(name, at, false)
		out, errs = append(out, evs...), appendErr(errs, err)
	}
	if rescan {
		evs, es := t.scanLocked(at, false, "")
		out, errs = append(out, evs...), append(errs, es...)
	}
	t.pruneLocked(t.now())
	t.mu.Unlock()
	return out, errors.Join(errs...)
}

// NextDeadline implements Timed: the earliest expiry of an open record.
func (t *UserTranslator) NextDeadline() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var next time.Time
	for _, e := range t.records {
		if isOpen(e.state) && !e.check.IsZero() && (next.IsZero() || e.check.Before(next)) {
			next = e.check
		}
	}
	return next, !next.IsZero()
}

// Tick implements Timed. For each open record whose expiry passed it reads
// the record again first, so an approval that was applied just before its
// expiry is reported as resolved, not expired, and only then reports the
// expiry nobody has recorded (Observed).
func (t *UserTranslator) Tick(_ context.Context, now time.Time) ([]bus.Event, error) {
	t.mu.Lock()
	var due []string
	for name, e := range t.records {
		if isOpen(e.state) && !e.check.IsZero() && !now.Before(e.check) {
			due = append(due, name)
		}
	}
	sort.Strings(due)
	var out []bus.Event
	var errs []error
	for _, name := range due {
		if _, err := os.Lstat(filepath.Join(t.approvalsDir, name)); errors.Is(err, fs.ErrNotExist) {
			delete(t.records, name) // the record was removed: nothing to report
			continue
		}
		evs, err := t.applyLocked(name, now, false)
		out, errs = append(out, evs...), appendErr(errs, err)
		e := t.records[name]
		if e == nil || !isOpen(e.state) || !ObservedExpiry(e.state, e.expires, now) {
			continue // finished, or the read moved the expiry to a later time
		}
		ev, err := t.approvalEvent(bus.TypeApprovalExpired, e.repo, now, e.view, bus.StateExpired, e.state, true)
		out, errs = append(out, ev...), appendErr(errs, err)
		e.state, e.check, e.seen = bus.StateExpired, time.Time{}, now
	}
	t.pruneLocked(now)
	t.mu.Unlock()
	return out, errors.Join(errs...)
}

// applyLocked reads the record called name and, unless seed is set,
// returns the event its state change produces. Seeding records the state
// and returns nothing.
func (t *UserTranslator) applyLocked(name string, at time.Time, seed bool) ([]bus.Event, error) {
	scope, _, ok := ParseApprovalRecord(name)
	if !ok {
		return nil, nil
	}
	repo, served := t.scopes[scope]
	if !served {
		return nil, nil // another repository's record, or one of no repository we serve
	}
	path := filepath.Join(t.approvalsDir, name)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || !ownedByCurrentUser(fi) {
		return nil, nil // removed, replaced by a link, or not the user's own file
	}
	rec, err := policy.ReadJournalRecord(path)
	if err != nil {
		return nil, nil // unreadable or not a record: the next change repairs it
	}
	now := t.now()
	current := bus.ApprovalState(rec.State)
	if seed && ObservedExpiry(current, rec.ExpiresAt, now) {
		current = bus.StateExpired
	}
	e := t.records[name]
	var previous bus.ApprovalState
	if e == nil {
		e = &approvalEntry{}
		t.records[name] = e
	} else {
		previous = e.state
	}
	if isFinal(previous) {
		return nil, nil // a finished record never starts again
	}
	e.repo, e.state, e.view, e.expires, e.seen = repo, current, ApprovalPayload(rec), rec.ExpiresAt, now
	e.check = time.Time{}
	if isOpen(current) {
		e.check = rec.ExpiresAt
	}
	if seed {
		return nil, nil
	}
	// A record created and answered between two looks at the directory
	// is first seen in its final state: report the request, then the
	// answer (ApprovalTransitions).
	var out []bus.Event
	from := previous
	// Only a record issued since the repository was served can have been
	// created and answered unseen; an older one finished before.
	asked := rec.PromptOwner != "" && !rec.IssuedAt.Before(t.served[scope].Add(-approvalSkew))
	for _, typ := range ApprovalTransitions(previous, current, asked) {
		state := current
		if typ == bus.TypeApprovalRequested {
			state = bus.StatePending
		}
		evs, err := t.approvalEvent(typ, repo, at, e.view, state, from, false)
		if err != nil {
			return out, err
		}
		out = append(out, evs...)
		from = state
	}
	return out, nil
}

// scanLocked applies every record of the served repositories (only
// scopeKey when it is not empty) that can still change: the ones it knows
// to be open, and unknown ones recent enough to be open. With seed set it
// records them silently.
func (t *UserTranslator) scanLocked(at time.Time, seed bool, scopeKey string) ([]bus.Event, []error) {
	list, err := os.ReadDir(t.approvalsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{err}
	}
	if len(list) > maxApprovalScan {
		list = list[:maxApprovalScan]
	}
	now := t.now()
	var out []bus.Event
	var errs []error
	for _, d := range list {
		scope, _, ok := ParseApprovalRecord(d.Name())
		if !ok || (scopeKey != "" && scope != scopeKey) {
			continue
		}
		if _, served := t.scopes[scope]; !served {
			continue
		}
		if known := t.records[d.Name()]; known != nil {
			if isFinal(known.state) {
				continue
			}
		} else if info, err := d.Info(); err != nil || now.Sub(info.ModTime()) > maxOpenAge {
			continue
		}
		evs, err := t.applyLocked(d.Name(), at, seed)
		out, errs = append(out, evs...), appendErr(errs, err)
	}
	return out, errs
}

func (t *UserTranslator) pruneLocked(now time.Time) {
	if len(t.records) <= forgetAbove {
		return
	}
	for name, e := range t.records {
		if isFinal(e.state) && now.Sub(e.seen) > forgetAfter {
			delete(t.records, name)
		}
	}
}

func (t *UserTranslator) personalEventsLocked(at time.Time) ([]bus.Event, error) {
	now := personalHashes(t.u.Home)
	var paths []string
	for p, sum := range now {
		if prev, ok := t.personal[p]; !ok || prev != sum {
			paths = append(paths, p)
		}
	}
	for p := range t.personal {
		if _, ok := now[p]; !ok {
			paths = append(paths, p)
		}
	}
	t.personal = now
	if len(paths) == 0 {
		return nil, nil
	}
	sort.Strings(paths)
	e, err := newEvent(bus.TypeConfigLayerChanged, SourceConfig, bus.GlobalRepo, "", at,
		bus.ConfigLayerChanged{Layer: bus.LayerPersonal, Paths: capNames(paths)})
	if err != nil {
		return nil, err
	}
	return []bus.Event{e}, nil
}

func (t *UserTranslator) settingsEventsLocked(at time.Time) ([]bus.Event, error) {
	now := settingsHashes(t.u.UserHome, UserClientSettings)
	old := t.settings
	t.settings = now
	return settingsEvents(old, now, UserClientSettings, bus.SettingsUser, bus.GlobalRepo, "", at)
}

func (t *UserTranslator) approvalEvent(typ bus.Type, repo bus.RepoID, at time.Time, view bus.Approval,
	current, previous bus.ApprovalState, observed bool) ([]bus.Event, error) {
	var payload any
	switch typ {
	case bus.TypeApprovalRequested:
		payload = bus.ApprovalRequested{Approval: view}
	case bus.TypeApprovalResolved:
		payload = bus.ApprovalResolved{Approval: view, State: current, Previous: previous}
	default:
		payload = bus.ApprovalExpired{Approval: view, Previous: previous, Observed: observed}
	}
	e, err := newEvent(typ, SourceApprovals, repo, "", at, payload)
	if err != nil {
		return nil, err
	}
	return []bus.Event{e}, nil
}

// personalHashes hashes ~/.agentflow/config.yml and providers/*.y{a,}ml,
// keyed by their slash-separated path relative to home.
func personalHashes(home string) map[string][sha256.Size]byte {
	out := map[string][sha256.Size]byte{}
	if sum, ok := hashSettingsFile(filepath.Join(home, "config.yml")); ok {
		out["config.yml"] = sum
	}
	list, _ := os.ReadDir(filepath.Join(home, "providers"))
	for _, e := range list {
		if e.IsDir() || !yamlName(e.Name()) {
			continue
		}
		if sum, ok := hashSettingsFile(filepath.Join(home, "providers", e.Name())); ok {
			out["providers/"+e.Name()] = sum
		}
	}
	return out
}

// ApprovalPayload is the public part of an approval record that events and
// the approvals projection carry: the operation, its class and policy
// source, the client that owns the prompt (the part of the owner before the
// "/") and the issue and expiry times. It never carries the fingerprint,
// the related keys or the owner's session MAC, which ReadJournalRecord does
// not read. Text is capped at bus.MaxPayloadText bytes, and a policy source
// that is an absolute path is dropped.
func ApprovalPayload(r policy.JournalRecord) bus.Approval {
	client, _, _ := strings.Cut(r.PromptOwner, "/")
	source := r.Source
	if filepath.IsAbs(source) || strings.HasPrefix(source, "/") || strings.HasPrefix(source, `\`) || (len(source) > 1 && source[1] == ':') {
		source = ""
	}
	return bus.Approval{
		OperationID:  r.OperationID,
		Action:       clipText(r.Action, bus.MaxPayloadText),
		Class:        clipText(string(r.Class), bus.MaxPayloadText),
		Client:       clipText(client, bus.MaxPayloadText),
		PolicySource: clipText(source, bus.MaxPayloadText),
		IssuedAt:     r.IssuedAt.UTC(),
		ExpiresAt:    r.ExpiresAt.UTC(),
	}
}

// clipText cuts s to at most n bytes, never inside a character.
func clipText(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isOpen(s bus.ApprovalState) bool { return s == bus.StatePending || s == bus.StateApproved }

func isFinal(s bus.ApprovalState) bool {
	return s == bus.StateConsumed || s == bus.StateCancelled || s == bus.StateExpired
}

func appendErr(errs []error, err error) []error {
	if err != nil {
		return append(errs, err)
	}
	return errs
}
