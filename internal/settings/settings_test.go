package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/configconsole"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/settings"
)

// fakePublisher records publications instead of calling GitHub.
type fakePublisher struct {
	published []string
	state     string
	fail      error
}

func (f *fakePublisher) Plan(_ context.Context, _ *gitx.Repo, _, _, _, title string) (configconsole.PublicationPlan, error) {
	return configconsole.PublicationPlan{Destination: "acme/widgets", Head: "abc", Title: title, CI: "none", Baseline: map[string]string{}}, nil
}

func (f *fakePublisher) Publish(_ context.Context, _ *gitx.Repo, _ configconsole.PublicationPlan, _, _, content string, draft bool, _ string) (settings.Publication, error) {
	if f.fail != nil {
		return settings.Publication{}, f.fail
	}
	f.published = append(f.published, content)
	mode := settings.RepoDirect
	if draft {
		mode = settings.RepoDraft
	}
	return settings.Publication{Mode: mode, URL: "https://github.com/acme/widgets/pull/7", Number: 7, Branch: "agentflow-config", State: "draft"}, nil
}

func (f *fakePublisher) State(context.Context, *gitx.Repo, settings.Publication) (string, error) {
	return f.state, nil
}

type fixture struct {
	dir, home string
	app       *app.App
	svc       *settings.Service
	pub       *fakePublisher
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

const repoConfig = "# repository settings\ncommit:\n  types: [feat, fix, chore]\npolicy:\n  max_autonomy:\n    local: auto\n"

func newFixture(t *testing.T, branch string) *fixture {
	t.Helper()
	goldentest.Isolate(t)
	f := &fixture{dir: t.TempDir(), home: t.TempDir()}
	t.Setenv("AGENTFLOW_HOME", f.home)
	t.Setenv("HOME", t.TempDir())
	git(t, f.dir, "init", "-q", "-b", "main")
	git(t, f.dir, "config", "user.email", "dev@example.com")
	git(t, f.dir, "config", "user.name", "Dev")
	if err := os.MkdirAll(filepath.Join(f.dir, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(repoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "add", ".")
	git(t, f.dir, "commit", "-q", "-m", "chore: init")
	if branch != "main" {
		git(t, f.dir, "switch", "-q", "-c", branch)
	}
	f.reopen(t)
	return f
}

func (f *fixture) reopen(t *testing.T) {
	t.Helper()
	a, err := app.Open(context.Background(), f.dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	f.app = a
	if f.pub == nil {
		f.pub = &fakePublisher{state: "open"}
	}
	f.svc = settings.New(a)
	f.svc.Publisher = f.pub
}

func set(id string, v any) settings.Edit {
	b, _ := json.Marshal(v)
	return settings.Edit{ID: id, Op: settings.OpSet, Value: b}
}

func codes(pv settings.Preview) []string {
	var out []string
	for _, d := range pv.Diagnostics {
		out = append(out, string(d.Severity)+":"+d.Code)
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// save runs the two-step confirmation: the first call must ask.
func (f *fixture) save(t *testing.T, cs settings.ChangeSet) settings.SaveResult {
	t.Helper()
	out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := out.(*app.Pending)
	if !ok || p.Status != "needs_confirmation" {
		t.Fatalf("first save returned %#v, want needs_confirmation", out)
	}
	out, err = f.svc.Save(t.Context(), cs, p.ConfirmToken, f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(settings.SaveResult)
	if !ok {
		t.Fatalf("save returned %#v", out)
	}
	if id, _ := policy.OperationIDOf(p.ConfirmToken); res.Operation != id {
		t.Errorf("operation %q, want the approval's %q", res.Operation, id)
	}
	return res
}

func (f *fixture) undo(t *testing.T, req settings.UndoRequest) any {
	t.Helper()
	out, err := f.svc.Undo(t.Context(), req, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := out.(*app.Pending)
	if !ok {
		return out
	}
	out, err = f.svc.Undo(t.Context(), req, p.ConfirmToken, f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) personal(t *testing.T) string {
	b, _ := os.ReadFile(filepath.Join(f.home, "config.yml"))
	return string(b)
}

func TestPreviewListsLooseningAndNeverPersonalYAML(t *testing.T) {
	f := newFixture(t, "task-1")
	if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte("# my secret notes\natlassian:\n  email: me@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{set("personal:autonomy.local", "auto")}})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Status != settings.StatusOK || len(pv.Changes) != 1 || !pv.Changes[0].Loosens || len(pv.Confirm) != 1 {
		t.Fatalf("preview %+v", pv)
	}
	if !has(codes(pv), "info:loosens") {
		t.Errorf("diagnostics %v", codes(pv))
	}
	raw, _ := json.Marshal(pv)
	if strings.Contains(string(raw), "secret notes") || strings.Contains(string(raw), "me@example.com") {
		t.Errorf("the preview carries personal YAML: %s", raw)
	}
	if len(pv.Files) != 1 || pv.Files[0].Diff != "" || pv.Files[0].Path != settings.PersonalFile {
		t.Errorf("files %+v", pv.Files)
	}
}

func TestCheckChainCodes(t *testing.T) {
	f := newFixture(t, "task-1")
	for _, tc := range []struct {
		name string
		edit settings.Edit
		want string
	}{
		{"unknown", set("repo:nothing.here", 1), "error:unknown"},
		{"layer", set("repo:autonomy.local", "auto"), "error:layer"},
		{"type", set("repo:commit.subject_max", "long"), "error:type"},
		{"enum", set("personal:autonomy.local", "always"), "error:enum"},
		{"syntax", settings.Edit{ID: "nolayer", Op: settings.OpSet, Value: json.RawMessage(`1`)}, "error:syntax"},
		{"action", set("personal:autonomy.actions", map[string]string{"no_such_action": "auto"}), "error:reference"},
		{"secret", set("repo:commit.types", []string{"ghp_" + strings.Repeat("a", 36)}), "error:credential"},
		{"window range", set("personal:approval_window", "2h"), "error:config"},
		{"unchanged", set("repo:policy.max_autonomy.local", "auto"), "info:unchanged"},
		{"inactive", set("repo:task_management.settings.project_key", "X"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{tc.edit}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && !has(codes(pv), tc.want) {
				t.Errorf("diagnostics %v, want %s", codes(pv), tc.want)
			}
		})
	}
	pv, _ := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60), set("repo:commit", map[string]any{})}})
	if !has(codes(pv), "error:syntax") {
		t.Errorf("overlapping edits: %v", codes(pv))
	}
}

func TestSaveAlwaysConfirmsWritesAndJournals(t *testing.T) {
	f := newFixture(t, "task-1")
	// Even under auto for the action, a save asks.
	if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte("autonomy:\n  local: auto\n  actions:\n    console.settings: auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	res := f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:approval_window", "10m")}})
	if res.Status != settings.StatusSaved || !res.Undo {
		t.Fatalf("result %+v", res)
	}
	if got := f.personal(t); !strings.Contains(got, "10m") || !strings.Contains(got, "console.settings: auto") {
		t.Errorf("personal file:\n%s", got)
	}
	info, _ := os.Stat(filepath.Join(f.home, "config.yml"))
	jinfo, _ := os.Stat(f.svc.JournalPath(t.Context()))
	dinfo, _ := os.Stat(filepath.Dir(f.svc.JournalPath(t.Context())))
	if info.Mode().Perm() != 0o600 || jinfo == nil || jinfo.Mode().Perm() != 0o600 || dinfo.Mode().Perm() != 0o700 {
		t.Errorf("modes: personal %v, journal %v, dir %v", info.Mode(), jinfo, dinfo)
	}
	rec, err := f.svc.Journal(t.Context())
	if err != nil || rec == nil || rec.Operation != res.Operation || len(rec.Edits) != 1 || string(rec.Edits[0].Before) != "null" {
		t.Fatalf("journal %+v %v", rec, err)
	}
}

func TestSaveRefusesAfterOutsideEdit(t *testing.T) {
	f := newFixture(t, "task-1")
	cs := settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60)}, Repo: settings.RepoTarget{Mode: settings.RepoWorktree}}
	out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	p := out.(*app.Pending)
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(repoConfig+"flow_map: x@y:z.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Save(t.Context(), cs, p.ConfirmToken, f.app.GateOperation)
	if policy.ErrorCode(err) != "operation_changed" {
		t.Fatalf("err %v, want operation_changed", err)
	}
	// expect that no longer holds conflicts.
	cs.Edits[0].Expect = json.RawMessage(`72`)
	pv, _ := f.svc.Preview(t.Context(), cs)
	if pv.Status != settings.StatusConflict {
		t.Errorf("status %s %v", pv.Status, codes(pv))
	}
}

func TestWorktreeModeWritesFileAndRefusesProtected(t *testing.T) {
	f := newFixture(t, "main")
	cs := settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60)}, Repo: settings.RepoTarget{Mode: settings.RepoWorktree}}
	pv, _ := f.svc.Preview(t.Context(), cs)
	if !has(codes(pv), "error:protected") {
		t.Fatalf("main: %v", codes(pv))
	}
	git(t, f.dir, "switch", "-q", "-c", "task-2")
	res := f.save(t, cs)
	b, _ := os.ReadFile(filepath.Join(f.dir, ".agentflow", "config.yml"))
	if !strings.Contains(string(b), "subject_max: 60") || !strings.HasPrefix(string(b), "# repository settings") || res.Publication == nil || res.Publication.Mode != settings.RepoWorktree {
		t.Errorf("file:\n%s\nresult %+v", b, res)
	}
}

func TestDraftPublishesWithoutTouchingWorktree(t *testing.T) {
	f := newFixture(t, "task-1")
	cs := settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60), set("personal:autonomy.remote", "suggest")}}
	pv, _ := f.svc.Preview(t.Context(), cs)
	if pv.Repo == nil || pv.Repo.Mode != settings.RepoDraft || pv.Repo.Destination != "acme/widgets" || pv.Repo.Title != settings.DefaultTitle || len(pv.Files) != 2 || pv.Files[0].Diff == "" {
		t.Fatalf("preview %+v", pv)
	}
	res := f.save(t, cs)
	b, _ := os.ReadFile(filepath.Join(f.dir, ".agentflow", "config.yml"))
	if string(b) != repoConfig || len(f.pub.published) != 1 || res.Publication.URL == "" || !strings.Contains(f.personal(t), "suggest") {
		t.Fatalf("worktree file changed or nothing published: %+v", res)
	}
	// While the pull request is open, its fields cannot be undone.
	out := f.undo(t, settings.UndoRequest{})
	pv, ok := out.(settings.Preview)
	if !ok || pv.Status != settings.StatusInvalid || !has(codes(pv), "error:published") {
		t.Fatalf("undo %#v", out)
	}
}

func TestPublicationFailureWritesNothing(t *testing.T) {
	f := newFixture(t, "task-1")
	f.pub.fail = errors.New("offline")
	cs := settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60), set("personal:autonomy.remote", "suggest")}}
	out, _ := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
	_, err := f.svc.Save(t.Context(), cs, out.(*app.Pending).ConfirmToken, f.app.GateOperation)
	if err == nil || f.personal(t) != "" {
		t.Fatalf("err %v, personal %q", err, f.personal(t))
	}
}

func TestUndoRedoAndConflicts(t *testing.T) {
	f := newFixture(t, "task-1")
	cs := settings.ChangeSet{Edits: []settings.Edit{set("personal:autonomy.remote", "suggest"), set("personal:approval_window", "10m")}}
	first := f.save(t, cs)
	undone, ok := f.undo(t, settings.UndoRequest{}).(settings.SaveResult)
	if !ok || strings.Contains(f.personal(t), "suggest") || strings.Contains(f.personal(t), "10m") {
		t.Fatalf("undo %+v, file %q", undone, f.personal(t))
	}
	rec, _ := f.svc.Journal(t.Context())
	if rec.UndoOf != first.Operation {
		t.Errorf("undo_of %q, want %q", rec.UndoOf, first.Operation)
	}
	if _, ok := f.undo(t, settings.UndoRequest{}).(settings.SaveResult); !ok || !strings.Contains(f.personal(t), "suggest") {
		t.Fatalf("redo did not restore: %q", f.personal(t))
	}
	// An outside change to one field conflicts.
	edited := strings.Replace(f.personal(t), "10m", "20m", 1)
	if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if pv, ok := f.undo(t, settings.UndoRequest{}).(settings.Preview); !ok || pv.Status != settings.StatusConflict {
		t.Fatalf("undo over an outside edit: %#v", pv)
	}
	if _, ok := f.undo(t, settings.UndoRequest{SkipConflicts: true}).(settings.SaveResult); !ok {
		t.Fatal("skip_conflicts did not save")
	}
	if got := f.personal(t); strings.Contains(got, "suggest") || !strings.Contains(got, "20m") {
		t.Errorf("skip_conflicts file %q", got)
	}
}

func TestStateAndFields(t *testing.T) {
	f := newFixture(t, "task-1")
	st, err := f.svc.State(t.Context())
	if err != nil || st.Branch != "task-1" || st.Protected || st.Revisions.Repo == "" || st.CatalogRevision == "" {
		t.Fatalf("state %+v %v", st, err)
	}
	var found bool
	for _, v := range st.Values {
		if v.ID == "repo:policy.max_autonomy.local" {
			found = v.Source == settings.SourceRepo && string(v.Layer.JSON) == `"auto"`
		}
	}
	if !found {
		t.Errorf("values miss the repository ceiling")
	}
	view, err := f.svc.Fields(t.Context(), settings.FieldsRequest{Modified: true})
	if err != nil || len(view.Values) != 2 {
		t.Fatalf("modified fields %+v %v", view.Values, err)
	}
	view, err = f.svc.Fields(t.Context(), settings.FieldsRequest{Group: "autonomy"})
	if err != nil || len(view.Fields) == 0 {
		t.Fatalf("fields %+v %v", view, err)
	}
	for _, fd := range view.Fields {
		if fd.Meta.Group != "autonomy" {
			t.Errorf("group filter let %s through", fd.ID)
		}
	}
}

func TestSensitiveValuesAreRedacted(t *testing.T) {
	f := newFixture(t, "task-1")
	fields := f.svc.Catalog.Fields()
	for i := range fields {
		if fields[i].ID == "repo:flow_map" {
			fields[i].Meta.Sensitive = true
		}
	}
	f.svc.Catalog = consolecfg.NewCatalog(fields)
	cs := settings.ChangeSet{Edits: []settings.Edit{set("repo:flow_map", "acme/private@v1:flow.yml")}, Repo: settings.RepoTarget{Mode: settings.RepoWorktree}}
	pv, err := f.svc.Preview(t.Context(), cs)
	if err != nil || len(pv.Changes) != 1 {
		t.Fatalf("%v %+v", err, pv)
	}
	if pv.Files[0].Diff != "" {
		t.Errorf("the diff shows a sensitive value")
	}
	if c := pv.Changes[0]; !c.After.Redacted || c.After.JSON != nil || c.After.Digest == "" {
		t.Errorf("change %+v", c)
	}
	out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(out.(*app.Pending).Summary); strings.Contains(string(raw), "private@v1") {
		t.Errorf("the approval summary carries a sensitive value: %s", raw)
	}
	// Once the file sets it, editing another field must not show it as a
	// context line of the diff, in the preview or the approval summary.
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte("flow_map: acme/private@v1:secret.yml\n"+repoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	other := settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60)}, Repo: settings.RepoTarget{Mode: settings.RepoWorktree}}
	pv, err = f.svc.Preview(t.Context(), other)
	if err != nil || len(pv.Changes) != 1 {
		t.Fatalf("%v %+v", err, pv)
	}
	out, err = f.svc.Save(t.Context(), other, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"preview": pv, "summary": out.(*app.Pending).Summary} {
		if raw, _ := json.Marshal(v); strings.Contains(string(raw), "secret.yml") {
			t.Errorf("the %s shows a sensitive value through the diff: %s", name, raw)
		}
	}
	st, _ := f.svc.State(t.Context())
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "ryanhelms/agentflow@v0") {
		t.Errorf("state carries a sensitive default: %s", raw)
	}
}

// A personal approval change answers to console.autonomy as well: suggest
// there, personal or as the repository ceiling, wins over console.settings.
func TestApprovalChangesAnswerToConsoleAutonomy(t *testing.T) {
	for name, tc := range map[string]struct{ personal, repo string }{
		"personal suggest":   {personal: "autonomy:\n  actions:\n    console.autonomy: suggest\n", repo: repoConfig},
		"repository ceiling": {repo: repoConfig + "    actions:\n      console.autonomy: suggest\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "task-1")
			if tc.personal != "" {
				if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte(tc.personal), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(tc.repo), 0o644); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			before := f.personal(t)
			for _, cs := range []settings.ChangeSet{
				{Edits: []settings.Edit{set("personal:autonomy.local", "auto")}},
				{Edits: []settings.Edit{set("personal:approval_window", "10m")}},
				{Edits: []settings.Edit{set("personal:autonomy.local", "auto"), set("repo:commit.subject_max", 60)}},
			} {
				out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
				if err != nil {
					t.Fatal(err)
				}
				p, ok := out.(*app.Pending)
				if !ok || p.Status != "suggest" || p.ConfirmToken != "" {
					t.Fatalf("save %v returned %#v, want suggest", cs.Edits, out)
				}
			}
			if f.personal(t) != before || len(f.pub.published) != 0 {
				t.Errorf("a suggest-only save wrote something")
			}
			// Other personal fields still save under console.settings.
			res := f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:atlassian.email", "me@example.com")}})
			if res.Status != settings.StatusSaved {
				t.Errorf("unrelated field: %+v", res)
			}
		})
	}
}

// last_save.undoable follows the pull request: false while it is open, true
// once the cache says it merged, and the journal keeps the newer state.
func TestUndoableAfterMerge(t *testing.T) {
	f := newFixture(t, "task-1")
	f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60)}})
	rec, _ := f.svc.Journal(t.Context())
	if f.svc.Summary(t.Context(), rec).Undoable {
		t.Fatal("undoable while the pull request is open")
	}
	// The merge reaches the worktree.
	b, _ := os.ReadFile(filepath.Join(f.dir, ".agentflow", "config.yml"))
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(strings.Replace(string(b), "commit:\n", "commit:\n  subject_max: 60\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	f.svc.PullState = func(n int) (string, bool) { return "merged", n == 7 }
	sum := f.svc.Summary(t.Context(), rec)
	if !sum.Undoable || sum.Publication.State != "merged" {
		t.Fatalf("summary after merge %+v", sum)
	}
	if rec, _ := f.svc.Journal(t.Context()); rec.Publication.State != "merged" {
		t.Errorf("journal state %q, want merged", rec.Publication.State)
	}
	f.svc.PullState = nil
	f.pub.state = "merged"
	if pv, err := f.svc.PreviewUndo(t.Context(), settings.UndoRequest{}); err != nil || pv.Status != settings.StatusOK {
		t.Fatalf("undo preview %s %v %v", pv.Status, codes(pv), err)
	}
}

// A save whose journal cannot be written must not leave the previous
// record to be undone over it.
func TestJournalFailureDropsTheStaleRecord(t *testing.T) {
	f := newFixture(t, "task-1")
	f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:autonomy.remote", "suggest")}})
	restore := settings.FailJournalWrites(errors.New("disk full"))
	second := f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:approval_window", "10m")}})
	restore()
	if second.Status != settings.StatusSaved || second.Undo {
		t.Fatalf("second save %+v", second)
	}
	if rec, err := f.svc.Journal(t.Context()); err != nil || rec != nil {
		t.Fatalf("journal %+v %v, want none", rec, err)
	}
	if _, err := f.svc.Undo(t.Context(), settings.UndoRequest{}, "", f.app.GateOperation); !errors.Is(err, settings.ErrNothingToUndo) {
		t.Errorf("undo: %v", err)
	}
	// A journal directory that cannot be written refuses the save up front.
	dir := filepath.Dir(f.svc.JournalPath(t.Context()))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	before := f.personal(t)
	cs := settings.ChangeSet{Edits: []settings.Edit{set("personal:approval_window", "20m")}}
	out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Save(t.Context(), cs, out.(*app.Pending).ConfirmToken, f.app.GateOperation); err == nil || f.personal(t) != before {
		t.Errorf("save with an unwritable journal: %v, file %q", err, f.personal(t))
	}
}

// A save whose repository part was published but whose personal write
// failed is a PartialResult, audited as failed with code partial under the
// class it was gated with.
func TestPartialSaveIsAFailure(t *testing.T) {
	f := newFixture(t, "task-1")
	var audits []policy.OperationAudit
	f.svc.Audit = func(o policy.OperationAudit) { audits = append(audits, o) }
	cs := settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60), set("personal:autonomy.remote", "suggest")}}
	out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
	if err != nil {
		t.Fatal(err)
	}
	restore := settings.FailPersonalWrites(errors.New("read-only"))
	out, err = f.svc.Save(t.Context(), cs, out.(*app.Pending).ConfirmToken, f.app.GateOperation)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := out.(settings.PartialResult)
	if !ok || r.Status != settings.StatusPartial || len(f.pub.published) != 1 || f.personal(t) != "" {
		t.Fatalf("result %#v", out)
	}
	if raw, _ := json.Marshal(r); !strings.Contains(string(raw), `"status":"partial"`) || strings.Contains(string(raw), "SaveResult") {
		t.Errorf("JSON %s", raw)
	}
	if len(audits) != 2 || audits[0].Outcome != "awaiting_approval" || audits[1].Outcome != "failed" || audits[1].Code != "partial" ||
		audits[1].Class != policy.Remote || audits[1].Action != "console.publish" || audits[1].OperationID != audits[0].OperationID {
		t.Errorf("audits %+v", audits)
	}
	// The journal records only the published repository edit.
	if rec, _ := f.svc.Journal(t.Context()); rec == nil || len(rec.Edits) != 1 || rec.Edits[0].ID != "repo:commit.subject_max" {
		t.Errorf("journal %+v", rec)
	}
}

// A symlinked personal file is followed to a regular file the user owns,
// and saves write through the link. A link the editor cannot use refuses
// personal edits only: repository edits and the page still work.
func TestPersonalSymlink(t *testing.T) {
	f := newFixture(t, "task-1")
	dotfiles := filepath.Join(t.TempDir(), "agentflow.yml")
	if err := os.WriteFile(dotfiles, []byte("approval_window: 5m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, "config.yml")
	if err := os.Symlink(dotfiles, link); err != nil {
		t.Fatal(err)
	}
	st, err := f.svc.State(t.Context())
	if err != nil || st.Revisions.Personal == settings.RevisionAbsent {
		t.Fatalf("state through a link: %+v %v", st.Revisions, err)
	}
	f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:autonomy.remote", "suggest")}})
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the save replaced the link: %v", err)
	}
	if b, _ := os.ReadFile(dotfiles); !strings.Contains(string(b), "suggest") || !strings.Contains(string(b), "5m") {
		t.Errorf("target file %q", b)
	}

	// A dangling link: personal edits fail, everything else works.
	if err := os.Remove(dotfiles); err != nil {
		t.Fatal(err)
	}
	st, err = f.svc.State(t.Context())
	if err != nil || len(st.Problems) == 0 || st.Revisions.Repo == "" {
		t.Fatalf("state with a dangling link: %+v %v", st, err)
	}
	if _, err := f.svc.Fields(t.Context(), settings.FieldsRequest{}); err != nil {
		t.Fatalf("fields: %v", err)
	}
	pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{set("personal:autonomy.remote", "auto")}})
	if err != nil || pv.Status != settings.StatusInvalid || !has(codes(pv), "error:config") {
		t.Fatalf("personal edit through a dangling link: %s %v %v", pv.Status, codes(pv), err)
	}
	res := f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("repo:commit.subject_max", 60)}, Repo: settings.RepoTarget{Mode: settings.RepoWorktree}})
	if res.Status != settings.StatusSaved {
		t.Errorf("repository save: %+v", res)
	}
	// A link to a directory is refused the same way.
	if err := os.Mkdir(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	if pv, _ := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{set("personal:autonomy.remote", "auto")}}); pv.Status != settings.StatusInvalid {
		t.Errorf("link to a directory: %s", pv.Status)
	}
}
