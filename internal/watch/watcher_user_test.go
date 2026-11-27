package watch_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/watch/watchtest"
)

// userEnv is a user's home directory and agentflow home in a temporary
// directory, with HOME pointing at it.
type userEnv struct {
	target watch.UserTarget
}

func newUserEnv(t *testing.T) userEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	userHome := filepath.Join(root, "home")
	home := filepath.Join(userHome, ".agentflow")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("AGENTFLOW_HOME", home)
	return userEnv{target: watch.UserTarget{Home: home, UserHome: userHome}}
}

func (e userEnv) path(parts ...string) string {
	return filepath.Join(append([]string{e.target.Home}, parts...)...)
}

// startUser watches env with a real UserTranslator and returns both.
func startUser(t *testing.T, mode watch.Mode, env userEnv, opts ...func(*watch.Options)) (*harness, *watch.UserTranslator) {
	t.Helper()
	tr, err := watch.NewUserTranslator(env.target)
	if err != nil {
		t.Fatal(err)
	}
	o := watch.Options{Mode: mode}
	for _, f := range opts {
		f(&o)
	}
	h := startWith(t, o, func(w *watch.FileWatcher) {
		if err := w.AddUser(env.target, tr); err != nil {
			t.Fatal(err)
		}
	})
	return h, tr
}

func repoDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newGate(t *testing.T, env userEnv, client, repo string) *policy.Gate {
	t.Helper()
	g, err := policy.NewGate(env.target.Home, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, client)
	if err != nil {
		t.Fatal(err)
	}
	g.Configure(repo, nil, nil)
	return g
}

func approvalOf(typ bus.Type, repo bus.RepoID, id string) func(bus.Event) bool {
	return func(e bus.Event) bool {
		if e.Type != typ || e.Repo != repo {
			return false
		}
		var p struct {
			OperationID string `json:"operation_id"`
		}
		return json.Unmarshal(e.Payload, &p) == nil && p.OperationID == id
	}
}

func noApprovalEvents(t *testing.T, h *harness, d time.Duration) {
	t.Helper()
	for _, e := range h.quiet(d) {
		if strings.HasPrefix(string(e.Type), "approval.") {
			t.Fatalf("unexpected %s %s", e.Type, e.Payload)
		}
	}
}

// Real gates write the records, so the file names, states and ownership
// the translator reads are the ones production writes.
func TestApprovalsFollowTheJournal(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			env := newUserEnv(t)
			repo, elsewhere := repoDir(t), repoDir(t)
			gate := newGate(t, env, "claude-code", repo)

			// Open before the repository is served: read silently.
			early, err := gate.Check(policy.Remote, "push", "early", "", false)
			if err != nil || early.Approval == nil {
				t.Fatal(early, err)
			}

			h, tr := startUser(t, mode, env)
			tr.Serve(policy.ScopeKey(repo), testRepo)
			noApprovalEvents(t, h, 500*time.Millisecond)

			// A new request appears.
			d, err := gate.Check(policy.Remote, "pr.open", "payload", "", false)
			if err != nil || d.Approval == nil || d.Token == "" {
				t.Fatal(d, err)
			}
			id := d.Approval.OperationID
			e := h.wait(t, "approval requested", approvalOf(bus.TypeApprovalRequested, testRepo, id))
			req := payload[bus.ApprovalRequested](t, e)
			owner := d.Approval.PromptOwner
			if req.Action != "pr.open" || req.Class != "remote" || req.Client != "claude-code" || req.PolicySource == "" ||
				req.IssuedAt.IsZero() || !req.ExpiresAt.Equal(d.Approval.ExpiresAt) || e.Source != watch.SourceApprovals || e.Worktree != "" {
				t.Fatalf("requested = %+v %+v", e, req)
			}
			if mac := owner[strings.Index(owner, "/")+1:]; strings.Contains(string(e.Payload), mac) || strings.Contains(string(e.Payload), d.Token) {
				t.Fatalf("payload leaks the prompt owner's session or the bearer: %s", e.Payload)
			}

			// Approved, then used.
			if _, err := gate.Approve(id, d.Token); err != nil {
				t.Fatal(err)
			}
			e = h.wait(t, "approval approved", approvalOf(bus.TypeApprovalResolved, testRepo, id))
			if r := payload[bus.ApprovalResolved](t, e); r.State != bus.StateApproved || r.Previous != bus.StatePending {
				t.Fatalf("approved = %+v", r)
			}
			if out, err := gate.Check(policy.Remote, "pr.open", "payload", id, false); err != nil || out.Outcome != policy.Proceed {
				t.Fatal(out, err)
			}
			e = h.wait(t, "approval consumed", approvalOf(bus.TypeApprovalResolved, testRepo, id))
			if r := payload[bus.ApprovalResolved](t, e); r.State != bus.StateConsumed || r.Previous != bus.StateApproved {
				t.Fatalf("consumed = %+v", r)
			}

			// The record that was open before start is followed from its state then.
			if _, err := gate.Approve(early.Approval.OperationID, early.Token); err != nil {
				t.Fatal(err)
			}
			e = h.wait(t, "early approval approved", approvalOf(bus.TypeApprovalResolved, testRepo, early.Approval.OperationID))
			if r := payload[bus.ApprovalResolved](t, e); r.Previous != bus.StatePending {
				t.Fatalf("early = %+v", r)
			}

			// Cancelled.
			c, err := gate.Check(policy.Local, "commit", "cancel me", "", false)
			if err != nil {
				t.Fatal(err)
			}
			h.wait(t, "second request", approvalOf(bus.TypeApprovalRequested, testRepo, c.Approval.OperationID))
			if err := gate.Cancel(c.Approval.OperationID, c.Token); err != nil {
				t.Fatal(err)
			}
			e = h.wait(t, "cancelled", approvalOf(bus.TypeApprovalResolved, testRepo, c.Approval.OperationID))
			if r := payload[bus.ApprovalResolved](t, e); r.State != bus.StateCancelled {
				t.Fatalf("cancelled = %+v", r)
			}

			// Another repository's approvals never produce an event here.
			other := newGate(t, env, "codex", elsewhere)
			if _, err := other.Check(policy.Remote, "pr.open", "elsewhere", "", false); err != nil {
				t.Fatal(err)
			}
			noApprovalEvents(t, h, 600*time.Millisecond)
		})
	}
}

// A record that nobody touches expires on its own clock.
func TestApprovalExpiryIsObserved(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			env := newUserEnv(t)
			repo := repoDir(t)
			h, tr := startUser(t, mode, env)
			tr.Serve(policy.ScopeKey(repo), testRepo)

			id := fmt.Sprintf("op_%032x", 1)
			path := env.path("approvals", policy.ScopeKey(repo)+"-"+id+".json")
			write := func(state string, expires time.Time) {
				t.Helper()
				b, _ := json.Marshal(map[string]any{"operation_id": id, "state": state, "prompt_owner": "codex/0123456789abcdef",
					"action": "pr.open", "class": "remote", "issued_at": time.Now().UTC(), "expires_at": expires.UTC(),
					"fingerprint": "secret", "source": ".agentflow/config.yml"})
				writeFile(t, path, string(b))
			}
			expires := time.Now().Add(1200 * time.Millisecond)
			write("pending", expires)
			h.wait(t, "requested", approvalOf(bus.TypeApprovalRequested, testRepo, id))
			start := time.Now()
			e := h.wait(t, "observed expiry", approvalOf(bus.TypeApprovalExpired, testRepo, id))
			x := payload[bus.ApprovalExpired](t, e)
			if !x.Observed || x.Previous != bus.StatePending || x.Client != "codex" {
				t.Fatalf("expired = %+v", x)
			}
			if time.Now().Before(expires) || time.Since(start) > 4*time.Second {
				t.Fatalf("expiry reported at the wrong time: %v before it, %v after the request", time.Until(expires), time.Since(start))
			}

			// The journal's own write of "expired" adds nothing.
			write("expired", expires)
			noApprovalEvents(t, h, 600*time.Millisecond)

			// A record that finishes before its expiry never reports one.
			id = fmt.Sprintf("op_%032x", 2)
			path = env.path("approvals", policy.ScopeKey(repo)+"-"+id+".json")
			expires = time.Now().Add(1500 * time.Millisecond)
			write("pending", expires)
			h.wait(t, "second requested", approvalOf(bus.TypeApprovalRequested, testRepo, id))
			write("consumed", expires)
			h.wait(t, "second resolved", approvalOf(bus.TypeApprovalResolved, testRepo, id))
			noApprovalEvents(t, h, 1600*time.Millisecond)
		})
	}
}

func TestPersonalConfigAndClientSettingsInTheGlobalRepository(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			env := newUserEnv(t)
			h, _ := startUser(t, mode, env)
			isGlobal := func(typ bus.Type) func(bus.Event) bool {
				return func(e bus.Event) bool { return e.Type == typ && e.Repo == bus.GlobalRepo }
			}

			writeFile(t, env.path("config.yml"), "autonomy:\n  local: confirm\n")
			e := h.wait(t, "personal config", isGlobal(bus.TypeConfigLayerChanged))
			p := payload[bus.ConfigLayerChanged](t, e)
			if p.Layer != bus.LayerPersonal || !slices.Equal(p.Paths, []string{"config.yml"}) || e.Worktree != "" || e.Source != watch.SourceConfig {
				t.Fatalf("config event = %+v %+v", e, p)
			}
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}

			// ~/.agentflow/providers does not exist yet and is picked up when it appears.
			writeFile(t, env.path("providers", "jira.yml"), "kind: jira\n")
			e = h.wait(t, "personal provider", isGlobal(bus.TypeConfigLayerChanged))
			if p := payload[bus.ConfigLayerChanged](t, e); !slices.Equal(p.Paths, []string{"providers/jira.yml"}) {
				t.Fatalf("provider paths = %v", p.Paths)
			}

			// The plugin registry lives in ~/.agentflow/plugins, which does not
			// exist yet either: the host reloads on this event.
			writeFile(t, env.path("plugins", "installed.json"), `{"version":1,"plugins":[]}`)
			e = h.wait(t, "plugin registry", isGlobal(bus.TypeConfigLayerChanged))
			if p := payload[bus.ConfigLayerChanged](t, e); p.Layer != bus.LayerPersonal || !slices.Equal(p.Paths, []string{"plugins/installed.json"}) {
				t.Fatalf("registry paths = %+v", p)
			}

			// What else lives in the home does not signal anything.
			writeFile(t, env.path("plugins", "installed.json.lock"), "")
			writeFile(t, env.path("plugins", "ci-notify", "data", "plugin.log"), "line\n")
			writeFile(t, env.path("plugins", "deadletter.jsonl"), "{}\n")
			writeFile(t, env.path("audit.log"), "{}\n")
			writeFile(t, env.path("confirm.key"), "secret")
			writeFile(t, env.path("config.yml.lock"), "")
			writeFile(t, env.path("providers", "notes.txt"), "x")
			writeFile(t, env.path("config.yml"), "autonomy:\n  local: confirm\n") // same content
			for _, e := range h.quiet(700 * time.Millisecond) {
				t.Fatalf("unexpected %s %s", e.Type, e.Payload)
			}

			// Client settings in the OS home, one event per client.
			writeFile(t, filepath.Join(env.target.UserHome, ".claude", "settings.json"), `{"hooks":{}}`)
			e = h.wait(t, "claude settings", isGlobal(bus.TypeClientSettingsChanged))
			s := payload[bus.ClientSettingsChanged](t, e)
			if s.Client != "claude" || s.Scope != bus.SettingsUser || !slices.Equal(s.Paths, []string{".claude/settings.json"}) || e.Worktree != "" {
				t.Fatalf("settings event = %+v %+v", e, s)
			}
			if strings.Contains(string(e.Payload), "hooks") || strings.Contains(string(e.Payload), env.target.UserHome) {
				t.Fatalf("settings payload carries content or an absolute path: %s", e.Payload)
			}
			writeFile(t, filepath.Join(env.target.UserHome, ".codex", "config.toml"), "model = 'x'\n")
			e = h.wait(t, "codex settings", isGlobal(bus.TypeClientSettingsChanged))
			if s := payload[bus.ClientSettingsChanged](t, e); s.Client != "codex" {
				t.Fatalf("settings event = %+v", s)
			}
			writeFile(t, filepath.Join(env.target.UserHome, ".claude.json"), `{"projects":{}}`)
			writeFile(t, filepath.Join(env.target.UserHome, ".claude", "projects", "x.json"), `{}`)
			for _, e := range h.quiet(700 * time.Millisecond) {
				t.Fatalf("unexpected %s %s", e.Type, e.Payload)
			}
		})
	}
}

func TestRepositoryClientSettingsBelongToTheWorktree(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			h := start(t, watch.Options{Mode: mode}, target(t, root, "main"))
			settings := func(client string) func(bus.Event) bool {
				return func(e bus.Event) bool {
					return e.Type == bus.TypeClientSettingsChanged && e.Worktree == "main" && payload[bus.ClientSettingsChanged](t, e).Client == client
				}
			}
			settingsPath := func(path string) func(bus.Event) bool {
				return func(e bus.Event) bool {
					return e.Type == bus.TypeClientSettingsChanged && e.Worktree == "main" && slices.Contains(payload[bus.ClientSettingsChanged](t, e).Paths, path)
				}
			}

			writeFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"agentflow":{}}}`)
			e := h.wait(t, "mcp config", settingsPath(".mcp.json"))
			s := payload[bus.ClientSettingsChanged](t, e)
			if s.Scope != bus.SettingsRepo || !slices.Equal(s.Paths, []string{".mcp.json"}) || e.Repo != testRepo || e.Source != watch.SourceConfig {
				t.Fatalf("event = %+v %+v", e, s)
			}
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(e.Payload), "mcpServers") {
				t.Fatalf("payload carries content: %s", e.Payload)
			}

			// Directories that do not exist yet are picked up when they appear.
			writeFile(t, filepath.Join(root, ".vscode", "mcp.json"), `{}`)
			h.wait(t, "vscode", settings("vscode"))
			writeFile(t, filepath.Join(root, ".claude", "settings.json"), `{}`)
			e = h.wait(t, "claude settings", settingsPath(".claude/settings.json"))
			if s := payload[bus.ClientSettingsChanged](t, e); s.Client != "claude" || !slices.Equal(s.Paths, []string{".claude/settings.json"}) {
				t.Fatalf("claude settings = %+v", s)
			}
			writeFile(t, filepath.Join(root, ".claude", "settings.local.json"), `{}`)
			h.wait(t, "claude local settings", settingsPath(".claude/settings.local.json"))
			writeFile(t, filepath.Join(root, ".codex", "config.toml"), "x = 1\n")
			h.wait(t, "codex", settings("codex"))

			// Other files in those directories and in the root signal nothing, and neither does a rewrite.
			writeFile(t, filepath.Join(root, "README.md"), "hello\n")
			writeFile(t, filepath.Join(root, ".claude", "projects.json"), "{}")
			writeFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"agentflow":{}}}`)
			for _, e := range h.quiet(700 * time.Millisecond) {
				if e.Type == bus.TypeClientSettingsChanged {
					t.Fatalf("unexpected %s", e.Payload)
				}
			}
			writeFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{}}`)
			h.wait(t, "mcp edit", settingsPath(".mcp.json"))
		})
	}
}

func TestUserSourcesFallBackToPolling(t *testing.T) {
	env := newUserEnv(t)
	fake := watchtest.New(watch.ModeNotify)
	fake.Fail(env.target.Home)
	h, _ := startUser(t, "", env, func(o *watch.Options) {
		o.NewNotify = func() (watch.Source, error) { return fake, nil }
	})
	// The user group moved to polling as a whole, so it needs no notifications.
	writeFile(t, env.path("config.yml"), "autonomy: {}\n")
	h.wait(t, "polled personal config", func(e bus.Event) bool {
		return e.Type == bus.TypeConfigLayerChanged && e.Repo == bus.GlobalRepo
	})
	// An overflow makes the translator compare everything again, and nothing changed.
	fake.EmitError(watch.ErrOverflow)
	for _, e := range h.quiet(500 * time.Millisecond) {
		t.Fatalf("rescan reported %s %s", e.Type, e.Payload)
	}
}

// A client settings directory that links to a directory of another user
// (here root's /usr) is not watched, and the watcher says so. An agentflow
// home that is a link is refused too (OwnedDir).
func TestUserDirectoriesThatAreNotTheUsersAreNotWatched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows, and owners are not checked")
	}
	if os.Geteuid() == 0 {
		t.Skip("root owns the link target")
	}
	foreign := "/usr"
	if fi, err := os.Stat(foreign); err != nil || !fi.IsDir() {
		t.Skip("no /usr")
	}
	env := newUserEnv(t)
	if err := os.Symlink(foreign, filepath.Join(env.target.UserHome, ".claude")); err != nil {
		t.Fatal(err)
	}
	h, _ := startUser(t, watch.ModePoll, env)
	writeFile(t, env.path("config.yml"), "a: 1\n")
	e := h.wait(t, "personal config", func(e bus.Event) bool { return e.Type == bus.TypeConfigLayerChanged })
	if e.Repo != bus.GlobalRepo {
		t.Fatalf("event = %+v", e)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.ContainsFunc(h.errs, func(err error) bool {
		return strings.Contains(err.Error(), "not a directory owned by this user") && strings.Contains(err.Error(), ".claude")
	}) {
		t.Fatalf("the skipped directory was not reported: %v", h.errs)
	}
}

// ~/.claude kept in a dotfiles repository is a symbolic link to a directory
// the user owns. The real directory is watched, and its events name the
// file under the link's path, as for a real directory.
func TestLinkedClientSettingsDirectoryIsWatched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			env := newUserEnv(t)
			dotfiles := filepath.Join(repoDir(t), "dotfiles", "claude")
			if err := os.MkdirAll(dotfiles, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dotfiles, filepath.Join(env.target.UserHome, ".claude")); err != nil {
				t.Fatal(err)
			}
			h, _ := startUser(t, mode, env)
			writeFile(t, filepath.Join(dotfiles, "settings.json"), `{"hooks":{}}`)
			e := h.wait(t, "linked claude settings", func(e bus.Event) bool {
				return e.Type == bus.TypeClientSettingsChanged && e.Repo == bus.GlobalRepo
			})
			s := payload[bus.ClientSettingsChanged](t, e)
			if s.Client != "claude" || s.Scope != bus.SettingsUser || !slices.Equal(s.Paths, []string{".claude/settings.json"}) {
				t.Fatalf("settings event = %+v", s)
			}
			if strings.Contains(string(e.Payload), "dotfiles") {
				t.Fatalf("payload names the link target: %s", e.Payload)
			}
			h.mu.Lock()
			errs := slices.Clone(h.errs)
			h.mu.Unlock()
			for _, err := range errs {
				if strings.Contains(err.Error(), ".claude") {
					t.Fatalf("linked directory reported: %v", err)
				}
			}
		})
	}
}

func TestAddUserRejectsInvalidInput(t *testing.T) {
	env := newUserEnv(t)
	w, err := watch.New(watch.Options{Mode: watch.ModePoll})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := watch.NewUserTranslator(env.target)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddUser(watch.UserTarget{Home: "relative", UserHome: env.target.UserHome}, tr); err == nil {
		t.Error("relative home accepted")
	}
	if err := w.AddUser(env.target, nil); err == nil {
		t.Error("nil translator accepted")
	}
	if err := w.AddUser(env.target, tr); err != nil {
		t.Fatal(err)
	}
	if err := w.AddUser(env.target, tr); err == nil {
		t.Error("second user target accepted")
	}
	_ = w.Close()
	w2, err := watch.New(watch.Options{Mode: watch.ModePoll})
	if err != nil {
		t.Fatal(err)
	}
	_ = w2.Close()
	if err := w2.AddUser(env.target, tr); err == nil {
		t.Error("closed watcher accepted a user target")
	}
	if _, err := watch.NewUserTranslator(watch.UserTarget{}); err == nil {
		t.Error("empty target accepted")
	}
}

func TestConsoleHomeLock(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".agentflow")
	if c := watch.CheckHome(home, false); c.Checked || c.Warn {
		t.Fatalf("no daemon: %+v", c)
	}
	if ok, err := watch.ConsoleWatches(home); ok || err != nil {
		t.Fatalf("missing home: %v, %v", ok, err)
	}
	// A running daemon that does not hold this home triggers the warning.
	c := watch.CheckHome(home, true)
	if !c.Checked || !c.Warn || !strings.Contains(c.Detail, home) || !strings.Contains(c.Detail, "AGENTFLOW_HOME") {
		t.Fatalf("unwatched home: %+v", c)
	}
	release, held, err := watch.HoldConsoleHome(home)
	if err != nil || !held {
		t.Fatalf("HoldConsoleHome = %v, %v", held, err)
	}
	if ok, err := watch.ConsoleWatches(home); !ok || err != nil {
		t.Fatalf("held home: %v, %v", ok, err)
	}
	if c := watch.CheckHome(home, true); !c.Checked || c.Warn {
		t.Fatalf("watched home: %+v", c)
	}
	if _, again, err := watch.HoldConsoleHome(home); again || err != nil {
		t.Fatalf("second holder: %v, %v", again, err)
	}
	release()
	if ok, err := watch.ConsoleWatches(home); ok || err != nil {
		t.Fatalf("released home: %v, %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(home, watch.ConsoleLockName)); errors.Is(err, os.ErrNotExist) {
		t.Fatal("the lock file must stay so waiters contend on one inode")
	}
	// Dropping the lock lets the next daemon take it.
	if r, held, err := watch.HoldConsoleHome(home); err != nil || !held {
		t.Fatalf("take over: %v, %v", held, err)
	} else {
		r()
	}
}

func TestWorktreePathsWatchClientSettings(t *testing.T) {
	primary, _ := targets(t)
	dirs := map[string]bool{}
	for _, p := range watch.WorktreePaths(primary) {
		if p.Recursive || dirs[p.Dir] {
			t.Errorf("bad path %+v", p)
		}
		dirs[p.Dir] = true
	}
	for _, rel := range []string{"", ".claude", ".codex", ".cursor", ".gemini", ".grok", ".vscode", ".agentflow"} {
		if !dirs[filepath.Join(primary.Root, rel)] {
			t.Errorf("%q is not watched", rel)
		}
	}
	for _, f := range watch.RepoClientSettings {
		typ, ok := watch.Classify(primary, filepath.Join(primary.Root, filepath.FromSlash(f.Path)))
		if !ok || typ != bus.TypeClientSettingsChanged {
			t.Errorf("Classify(%s) = %q, %v", f.Path, typ, ok)
		}
	}
	for _, rel := range []string{"README.md", ".claude/projects.json", ".git/config", ".mcp.json/x", "sub/.mcp.json", ".vscode/settings.json"} {
		if typ, ok := watch.Classify(primary, filepath.Join(primary.Root, filepath.FromSlash(rel))); ok && typ == bus.TypeClientSettingsChanged {
			t.Errorf("Classify(%s) = %q", rel, typ)
		}
	}
}
