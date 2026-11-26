package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/bus"
)

const (
	fixtureRepo  = bus.RepoID("0123456789abcdef")
	fixtureScope = "00112233445566778899aabbccddeeff"
)

// userFixture is a UserTranslator over temporary directories and a clock
// the test moves by hand.
type userFixture struct {
	t     *testing.T
	u     UserTarget
	tr    *UserTranslator
	clock time.Time
}

func newUserFixture(t *testing.T) *userFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u := UserTarget{Home: filepath.Join(root, ".agentflow"), UserHome: root}
	if err := os.MkdirAll(filepath.Join(u.Home, "approvals"), 0o700); err != nil {
		t.Fatal(err)
	}
	tr, err := NewUserTranslator(u)
	if err != nil {
		t.Fatal(err)
	}
	f := &userFixture{t: t, u: u, tr: tr, clock: time.Now().UTC().Truncate(time.Second)}
	tr.now = func() time.Time { return f.clock }
	return f
}

func opID(n int) string { return fmt.Sprintf("op_%032x", n) }

func (f *userFixture) path(scope, id string) string {
	return filepath.Join(f.u.Home, "approvals", scope+"-"+id+".json")
}

// record writes an approval record the way the journal does, with the
// fields the translator must never repeat.
func (f *userFixture) record(scope, id, state string, expires time.Time, extra map[string]any) string {
	f.t.Helper()
	rec := map[string]any{
		"operation_id": id, "state": state, "prompt_owner": "claude-code/3f2a9c04d1e57b68",
		"action": "pr.open", "class": "remote", "expires_at": expires.Format(time.RFC3339Nano),
		"issued_at": f.clock.Format(time.RFC3339Nano), "fingerprint": "FINGERPRINT-SECRET", "related": []string{"RELATED-KEY"},
		"proposal_nonce": "NONCE-SECRET", "source": ".agentflow/config.yml",
	}
	for k, v := range extra {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		f.t.Fatal(err)
	}
	p := f.path(scope, id)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *userFixture) translate(paths ...string) []bus.Event {
	f.t.Helper()
	b := Batch{Target: Target{Repo: bus.GlobalRepo}}
	for _, p := range paths {
		b.Changes = append(b.Changes, Change{Path: p, Kind: Modified, Time: f.clock})
	}
	evs, err := f.tr.Translate(context.Background(), b)
	if err != nil {
		f.t.Fatal(err)
	}
	return evs
}

func (f *userFixture) tick() []bus.Event {
	f.t.Helper()
	evs, err := f.tr.Tick(context.Background(), f.clock)
	if err != nil {
		f.t.Fatal(err)
	}
	return evs
}

func types(evs []bus.Event) []bus.Type {
	var out []bus.Type
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func decode[T any](t *testing.T, e bus.Event) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUserTranslatorApprovalLifecycle(t *testing.T) {
	f := newUserFixture(t)
	f.tr.Serve(fixtureScope, fixtureRepo)
	exp := f.clock.Add(5 * time.Minute)

	// A new pending record is a request, in its repository, with its public view only.
	p := f.record(fixtureScope, opID(1), "pending", exp, nil)
	evs := f.translate(p)
	if len(evs) != 1 || evs[0].Type != bus.TypeApprovalRequested {
		t.Fatalf("events = %v", types(evs))
	}
	e := evs[0]
	if e.Repo != fixtureRepo || e.Worktree != "" || e.Source != SourceApprovals || e.SchemaVersion != bus.SchemaVersion {
		t.Fatalf("envelope = %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	req := decode[bus.ApprovalRequested](t, e)
	if req.OperationID != opID(1) || req.Action != "pr.open" || req.Class != "remote" || req.Client != "claude-code" ||
		req.PolicySource != ".agentflow/config.yml" || !req.ExpiresAt.Equal(exp) || req.IssuedAt.IsZero() {
		t.Fatalf("payload = %+v", req)
	}
	for _, secret := range []string{"FINGERPRINT-SECRET", "RELATED-KEY", "NONCE-SECRET", "3f2a9c04d1e57b68", "fingerprint", "prompt_owner", f.u.Home} {
		if strings.Contains(string(e.Payload), secret) {
			t.Fatalf("payload leaks %q: %s", secret, e.Payload)
		}
	}
	if evs := f.translate(p); len(evs) != 0 {
		t.Fatalf("an unchanged record produced %v", types(evs))
	}

	// pending -> approved -> consumed: two resolutions, each with its previous state.
	f.record(fixtureScope, opID(1), "approved", exp, nil)
	evs = f.translate(p)
	if len(evs) != 1 || evs[0].Type != bus.TypeApprovalResolved {
		t.Fatalf("approved: %v", types(evs))
	}
	if r := decode[bus.ApprovalResolved](t, evs[0]); r.State != bus.StateApproved || r.Previous != bus.StatePending || r.OperationID != opID(1) {
		t.Fatalf("approved payload = %+v", r)
	}
	f.record(fixtureScope, opID(1), "consumed", exp, nil)
	evs = f.translate(p)
	if len(evs) != 1 || decode[bus.ApprovalResolved](t, evs[0]).Previous != bus.StateApproved {
		t.Fatalf("consumed: %v", types(evs))
	}
	if _, ok := f.tr.NextDeadline(); ok {
		t.Fatal("a finished record keeps a deadline")
	}

	// A change that skips a state (cancelled within one batch) still resolves.
	p2 := f.record(fixtureScope, opID(2), "pending", exp, nil)
	f.translate(p2)
	f.record(fixtureScope, opID(2), "cancelled", exp, nil)
	if evs := f.translate(p2); len(evs) != 1 || decode[bus.ApprovalResolved](t, evs[0]).State != bus.StateCancelled {
		t.Fatalf("cancelled: %v", types(evs))
	}

	// Finished records never start again, whatever the file says afterwards.
	f.record(fixtureScope, opID(2), "pending", exp, nil)
	if evs := f.translate(p2); len(evs) != 0 {
		t.Fatalf("a cancelled record reopened: %v", types(evs))
	}
}

func TestUserTranslatorIgnoresWhatIsNotAnApproval(t *testing.T) {
	f := newUserFixture(t)
	f.tr.Serve(fixtureScope, fixtureRepo)
	exp := f.clock.Add(5 * time.Minute)

	other := "ffffffffffffffffffffffffffffffff"
	if repo, ok := f.tr.RepoForScope(other); ok || repo != "" {
		t.Fatal("unserved scope routed")
	}
	if repo, ok := f.tr.RepoForScope(fixtureScope); !ok || repo != fixtureRepo {
		t.Fatalf("served scope = %q, %v", repo, ok)
	}
	// A record of a repository the daemon does not serve produces nothing.
	if evs := f.translate(f.record(other, opID(1), "pending", exp, nil)); len(evs) != 0 {
		t.Fatalf("unserved repository: %v", types(evs))
	}
	// Prepared operations (auto policy) and records first seen finished produce nothing.
	if evs := f.translate(f.record(fixtureScope, opID(2), "prepared", exp, nil)); len(evs) != 0 {
		t.Fatalf("prepared: %v", types(evs))
	}
	if evs := f.translate(f.record(fixtureScope, opID(2), "consumed", exp, nil)); len(evs) != 0 {
		t.Fatalf("prepared then consumed: %v", types(evs))
	}
	if evs := f.translate(f.record(fixtureScope, opID(3), "consumed", exp, nil)); len(evs) != 0 {
		t.Fatalf("first seen consumed: %v", types(evs))
	}
	// Files that are no record, a record naming another operation, a broken one.
	home := filepath.Join(f.u.Home, "approvals")
	for name, body := range map[string]string{
		"events.jsonl":                         "{}\n",
		"request-abc":                          opID(1),
		fixtureScope + "-" + opID(4) + ".json": "{",
		fixtureScope + "-" + opID(5) + ".json": `{"operation_id":"` + opID(6) + `","state":"pending"}`,
	} {
		p := filepath.Join(home, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if evs := f.translate(p); len(evs) != 0 {
			t.Fatalf("%s: %v", name, types(evs))
		}
	}
	if runtime.GOOS != "windows" {
		real := f.record(fixtureScope, opID(7), "cancelled", exp, nil)
		link := f.path(fixtureScope, opID(8))
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		f.record(fixtureScope, opID(9), "pending", exp, nil)
		if err := os.Remove(f.path(fixtureScope, opID(9))); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, f.path(fixtureScope, opID(9))); err != nil {
			t.Fatal(err)
		}
		if evs := f.translate(link, f.path(fixtureScope, opID(9))); len(evs) != 0 {
			t.Fatalf("symbolic link followed: %v", types(evs))
		}
	}
}

func TestUserTranslatorStartsSilently(t *testing.T) {
	f := newUserFixture(t)
	exp := f.clock.Add(5 * time.Minute)
	open := f.record(fixtureScope, opID(1), "pending", exp, nil)
	f.record(fixtureScope, opID(2), "approved", exp, nil)
	f.record(fixtureScope, opID(3), "consumed", exp, nil)
	stale := f.record(fixtureScope, opID(4), "pending", exp, nil)
	old := f.clock.Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	past := f.record(fixtureScope, opID(5), "pending", f.clock.Add(-time.Minute), nil)

	f.tr.Serve(fixtureScope, fixtureRepo)
	if evs := f.tick(); len(evs) != 0 {
		t.Fatalf("serving a repository reported %v", types(evs))
	}
	// The seeded open records keep their timers; the one past its expiry was seeded as expired.
	if d, ok := f.tr.NextDeadline(); !ok || !d.Equal(exp) {
		t.Fatalf("deadline = %v, %v", d, ok)
	}
	// So they follow their journal from here on.
	f.record(fixtureScope, opID(1), "approved", exp, nil)
	if evs := f.translate(open); len(evs) != 1 || decode[bus.ApprovalResolved](t, evs[0]).Previous != bus.StatePending {
		t.Fatalf("seeded pending record: %v", types(evs))
	}
	f.record(fixtureScope, opID(5), "expired", f.clock.Add(-time.Minute), nil)
	if evs := f.translate(past); len(evs) != 0 {
		t.Fatalf("expired record after seeding: %v", types(evs))
	}
	// A record too old to be open that is touched later is first seen finished.
	f.record(fixtureScope, opID(4), "expired", exp, nil)
	if evs := f.translate(stale); len(evs) != 0 {
		t.Fatalf("stale record: %v", types(evs))
	}
}

func TestUserTranslatorReportsExpiryNobodyRecorded(t *testing.T) {
	f := newUserFixture(t)
	f.tr.Serve(fixtureScope, fixtureRepo)
	exp := f.clock.Add(5 * time.Minute)
	p := f.record(fixtureScope, opID(1), "pending", exp, nil)
	f.translate(p)
	p2 := f.record(fixtureScope, opID(2), "pending", exp.Add(time.Minute), nil)
	f.translate(p2)
	f.record(fixtureScope, opID(2), "approved", exp.Add(time.Minute), nil)
	f.translate(p2)

	if d, ok := f.tr.NextDeadline(); !ok || !d.Equal(exp) {
		t.Fatalf("deadline = %v, %v", d, ok)
	}
	f.clock = exp.Add(-time.Second)
	if evs := f.tick(); len(evs) != 0 {
		t.Fatalf("early tick: %v", types(evs))
	}
	f.clock = exp
	evs := f.tick()
	if len(evs) != 1 || evs[0].Type != bus.TypeApprovalExpired {
		t.Fatalf("tick at expiry: %v", types(evs))
	}
	if x := decode[bus.ApprovalExpired](t, evs[0]); !x.Observed || x.Previous != bus.StatePending || x.OperationID != opID(1) || evs[0].Repo != fixtureRepo {
		t.Fatalf("expired payload = %+v", x)
	}
	if err := evs[0].Validate(); err != nil {
		t.Fatal(err)
	}
	// The journal's own expiry write, later, adds nothing.
	f.record(fixtureScope, opID(1), "expired", exp, nil)
	if evs := f.translate(p); len(evs) != 0 {
		t.Fatalf("journal expiry after observed expiry: %v", types(evs))
	}
	// An approved record expires too, with its previous state.
	f.clock = exp.Add(time.Minute)
	evs = f.tick()
	if len(evs) != 1 || decode[bus.ApprovalExpired](t, evs[0]).Previous != bus.StateApproved {
		t.Fatalf("approved expiry: %v", types(evs))
	}
	if _, ok := f.tr.NextDeadline(); ok {
		t.Fatal("deadline left after every record finished")
	}
	if evs := f.tick(); len(evs) != 0 {
		t.Fatalf("tick repeated: %v", types(evs))
	}
}

// The journal wrote "consumed" just before the expiry, but the change has
// not reached the translator when the timer fires: the timer reads the
// record first and reports what really happened.
func TestUserTranslatorTickReadsTheRecordFirst(t *testing.T) {
	f := newUserFixture(t)
	f.tr.Serve(fixtureScope, fixtureRepo)
	exp := f.clock.Add(time.Minute)
	f.translate(f.record(fixtureScope, opID(1), "pending", exp, nil))
	f.record(fixtureScope, opID(1), "consumed", exp, nil)
	f.clock = exp.Add(time.Second)
	evs := f.tick()
	if len(evs) != 1 || evs[0].Type != bus.TypeApprovalResolved || decode[bus.ApprovalResolved](t, evs[0]).State != bus.StateConsumed {
		t.Fatalf("tick = %v", types(evs))
	}

	// A record removed before its expiry is forgotten without a word.
	p := f.record(fixtureScope, opID(2), "pending", f.clock.Add(time.Minute), nil)
	f.translate(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(2 * time.Minute)
	if evs := f.tick(); len(evs) != 0 {
		t.Fatalf("removed record: %v", types(evs))
	}
	if _, ok := f.tr.NextDeadline(); ok {
		t.Fatal("deadline left for a removed record")
	}

	// A later expiry in the file moves the deadline instead of reporting.
	p = f.record(fixtureScope, opID(3), "pending", f.clock.Add(time.Minute), nil)
	f.translate(p)
	later := f.clock.Add(10 * time.Minute)
	f.record(fixtureScope, opID(3), "pending", later, nil)
	f.clock = f.clock.Add(time.Minute)
	if evs := f.tick(); len(evs) != 0 {
		t.Fatalf("extended record: %v", types(evs))
	}
	if d, ok := f.tr.NextDeadline(); !ok || !d.Equal(later) {
		t.Fatalf("deadline = %v, %v", d, ok)
	}
}

func TestUserTranslatorRescanFindsWhatWasMissed(t *testing.T) {
	f := newUserFixture(t)
	f.tr.Serve(fixtureScope, fixtureRepo)
	exp := f.clock.Add(5 * time.Minute)
	f.record(fixtureScope, opID(1), "pending", exp, nil)
	f.record(fixtureScope, opID(2), "consumed", exp, nil)
	// The watcher asks for a rescan by reporting the directory itself.
	evs := f.translate(filepath.Join(f.u.Home, "approvals"))
	if len(evs) != 1 || evs[0].Type != bus.TypeApprovalRequested || decode[bus.ApprovalRequested](t, evs[0]).OperationID != opID(1) {
		t.Fatalf("rescan = %v", types(evs))
	}
	if evs := f.translate(filepath.Join(f.u.Home, "approvals")); len(evs) != 0 {
		t.Fatalf("second rescan = %v", types(evs))
	}
}

func TestUserTranslatorCapsText(t *testing.T) {
	f := newUserFixture(t)
	f.tr.Serve(fixtureScope, fixtureRepo)
	long := strings.Repeat("é", 400)
	p := f.record(fixtureScope, opID(1), "pending", f.clock.Add(time.Minute), map[string]any{
		"action": long, "class": long, "prompt_owner": long + "/mac", "source": "/home/someone/.agentflow/config.yml",
	})
	evs := f.translate(p)
	if len(evs) != 1 {
		t.Fatalf("events = %v", types(evs))
	}
	req := decode[bus.ApprovalRequested](t, evs[0])
	for name, s := range map[string]string{"action": req.Action, "class": req.Class, "client": req.Client} {
		if len(s) > bus.MaxPayloadText || !utf8.ValidString(s) || s == "" {
			t.Errorf("%s = %d bytes, valid %v", name, len(s), utf8.ValidString(s))
		}
	}
	if req.PolicySource != "" {
		t.Errorf("policy source %q is an absolute path", req.PolicySource)
	}
	if got := clipText("ab\xffcd", 3); !utf8.ValidString(got) {
		t.Errorf("clipText produced %q", got)
	}
	if got := clipText("日本語", 4); got != "日" {
		t.Errorf("clipText cut inside a character: %q", got)
	}
}

func TestUserTranslatorPersonalConfigAndClientSettings(t *testing.T) {
	f := newUserFixture(t)
	write := func(path, content string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cfg := write(filepath.Join(f.u.Home, "config.yml"), "autonomy: {}\n")
	if err := f.tr.Track(context.Background(), Target{Repo: bus.GlobalRepo}); err != nil {
		t.Fatal(err)
	}

	// Personal layer: only what changed, relative to the home, in the global repository.
	write(cfg, "autonomy: {local: auto}\n")
	prov := write(filepath.Join(f.u.Home, "providers", "jira.yaml"), "kind: jira\n")
	write(filepath.Join(f.u.Home, "providers", "notes.txt"), "not a provider\n")
	evs := f.translate(cfg, prov)
	if len(evs) != 1 || evs[0].Type != bus.TypeConfigLayerChanged || evs[0].Repo != bus.GlobalRepo || evs[0].Worktree != "" {
		t.Fatalf("personal config = %v", types(evs))
	}
	if err := evs[0].Validate(); err != nil {
		t.Fatal(err)
	}
	if p := decode[bus.ConfigLayerChanged](t, evs[0]); p.Layer != bus.LayerPersonal || !slices.Equal(p.Paths, []string{"config.yml", "providers/jira.yaml"}) {
		t.Fatalf("payload = %+v", p)
	}
	if evs := f.translate(cfg); len(evs) != 0 {
		t.Fatalf("no-op reported: %v", types(evs))
	}
	if err := os.Remove(prov); err != nil {
		t.Fatal(err)
	}
	evs = f.translate(prov)
	if len(evs) != 1 || !slices.Equal(decode[bus.ConfigLayerChanged](t, evs[0]).Paths, []string{"providers/jira.yaml"}) {
		t.Fatalf("removed provider = %v", types(evs))
	}

	// Client settings: one event per client, naming files relative to the OS home.
	claude := write(filepath.Join(f.u.UserHome, ".claude", "settings.json"), `{"hooks":{}}`)
	codex := write(filepath.Join(f.u.UserHome, ".codex", "config.toml"), "model = 'x'\n")
	write(filepath.Join(f.u.UserHome, ".claude.json"), `{"account":"secret"}`)
	evs = f.translate(claude, codex)
	if len(evs) != 2 || evs[0].Type != bus.TypeClientSettingsChanged {
		t.Fatalf("client settings = %v", types(evs))
	}
	first, second := decode[bus.ClientSettingsChanged](t, evs[0]), decode[bus.ClientSettingsChanged](t, evs[1])
	if first.Client != "claude" || first.Scope != bus.SettingsUser || !slices.Equal(first.Paths, []string{".claude/settings.json"}) ||
		second.Client != "codex" || !slices.Equal(second.Paths, []string{".codex/config.toml"}) {
		t.Fatalf("payloads = %+v %+v", first, second)
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(e.Payload), "hooks") || strings.Contains(string(e.Payload), f.u.UserHome) {
			t.Fatalf("payload carries content or an absolute path: %s", e.Payload)
		}
	}
	write(claude, `{"hooks":{"x":1}}`)
	evs = f.translate(claude)
	if len(evs) != 1 || decode[bus.ClientSettingsChanged](t, evs[0]).Client != "claude" {
		t.Fatalf("edit = %v", types(evs))
	}
}

func TestCanonicalUserAndOwnedDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []UserTarget{{}, {Home: "rel", UserHome: root}, {Home: root, UserHome: ""}} {
		if _, err := CanonicalUser(bad); err == nil {
			t.Errorf("CanonicalUser(%+v) succeeded", bad)
		}
	}
	// A path that does not exist yet keeps its missing tail.
	got, err := CanonicalUser(UserTarget{Home: filepath.Join(root, "a", "b", ".agentflow"), UserHome: root})
	if err != nil || got.Home != filepath.Join(root, "a", "b", ".agentflow") {
		t.Fatalf("CanonicalUser = %+v, %v", got, err)
	}
	if exists, ok := OwnedDir(filepath.Join(root, "missing")); exists || !ok {
		t.Errorf("missing: exists=%v ok=%v", exists, ok)
	}
	if exists, ok := OwnedDir(root); !exists || !ok {
		t.Errorf("own directory: exists=%v ok=%v", exists, ok)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, ok := OwnedDir(file); !exists || ok {
		t.Errorf("file: exists=%v ok=%v", exists, ok)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		if exists, ok := OwnedDir(link); !exists || ok {
			t.Errorf("symbolic link: exists=%v ok=%v", exists, ok)
		}
		// A home reached through a link is resolved once, not refused.
		got, err := CanonicalUser(UserTarget{Home: filepath.Join(link, ".agentflow"), UserHome: link})
		if err != nil || got.UserHome != root || got.Home != filepath.Join(root, ".agentflow") {
			t.Fatalf("CanonicalUser through a link = %+v, %v", got, err)
		}
	}
}
