package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

func fileTaskApp(t *testing.T, settings string) *App {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".devflow"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte("commit: {require_key: false}\ntask_management:\n"+settings), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFileSystemTaskLifecycle(t *testing.T) {
	ctx := t.Context()
	a := fileTaskApp(t, "  provider: filesystem\n  settings: {scope: 'eng/platform'}\n")
	s := a.Cfg.Providers.TaskManagement.Settings.(*config.FileSystemTaskSettings)
	root := filepath.Join(a.Repo.Dir, s.Root)
	if items, err := a.MyWork(ctx); err != nil || len(items) != 0 {
		t.Fatalf("new store work=%v, err=%v", items, err)
	}
	a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Suggest, config.Auto
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "", "", "Review quotas", "- [ ] Run tests", "", token)
	}
	v, err := create("")
	if err != nil || v.(*Pending).Status != "suggest" {
		t.Fatalf("local policy: %v, %v", v, err)
	}
	a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Confirm, config.Suggest
	v, err = create("")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created directory: %v", err)
	}
	s.Root = t.TempDir()
	if _, err = create(tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted different root: %v", err)
	}
	s.Root = root
	v, err = create(tok)
	if err != nil {
		t.Fatal(err)
	}
	key := v.(map[string]string)["key"]
	ref, err := provider.ParseRef(key)
	if err != nil || ref.Scope != "eng/platform" || ref.Instance != "filesystem" {
		t.Fatalf("ref=%+v, err=%v", ref, err)
	}
	files, err := filepath.Glob(filepath.Join(root, "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v, err=%v", files, err)
	}
	renamed := filepath.Join(root, "quota.md")
	if err = os.Rename(files[0], renamed); err != nil {
		t.Fatal(err)
	}
	work, err := a.MyWork(ctx)
	if err != nil || len(work) != 1 || work[0] != (WorkItem{Key: key, Summary: "Review quotas", Status: "Open", Type: "Task"}) {
		t.Fatalf("work=%+v err=%v", work, err)
	}
	if a.CurrentKey(ctx) != "" {
		t.Fatal("used Jira key from branch")
	}
	mustGit(t, a.Repo, "commit", "--allow-empty", "-qm", "fix: handle quota (#42)", "-m", provider.TaskTrailer+key)
	tc, err := a.Context(ctx, "")
	if err != nil || tc.Key != key || tc.Criteria == nil || tc.Summary != work[0].Summary || tc.FlowNext["#done"] != "closed" {
		t.Fatalf("context=%+v err=%v", tc, err)
	}
	keys, err := a.Keys(ctx, "main..HEAD")
	if err != nil || !slices.Equal(keys, []string{key}) {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	prBody, err := a.taskPRBody(ctx, "Custom body", key)
	if err != nil || !strings.HasSuffix(prBody, provider.TaskTrailer+key+"\n") {
		t.Fatalf("body=%q err=%v", prBody, err)
	}
	if _, err = a.Context(ctx, "github-issues/org%2Frepo/1"); err == nil {
		t.Fatal("accepted foreign instance")
	}
	v, err = a.Transition(ctx, "", "#done", true, "")
	if err != nil || v.(TransitionResult).To != "closed" {
		t.Fatalf("plan=%v err=%v", v, err)
	}
	v, err = a.Transition(ctx, key, "#done", false, "")
	if err != nil {
		t.Fatal(err)
	}
	tok = confirmation(t, v)
	f, err := os.OpenFile(renamed, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\nUpdated after preview"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Transition(ctx, key, "#done", false, tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted stale revision: %v", err)
	}
	a.Gate.Autonomy.Local = config.Auto
	v, err = a.Transition(ctx, key, "#done", false, "")
	if err != nil || !v.(TransitionResult).Applied {
		t.Fatalf("transition=%v err=%v", v, err)
	}
	work, err = a.MyWork(ctx)
	if err != nil || len(work) != 0 {
		t.Fatalf("closed work=%v err=%v", work, err)
	}
}

func TestRepositoryTaskPublication(t *testing.T) {
	ctx := t.Context()
	a := fileTaskApp(t, "  provider: repository\n  settings: {directory: planning/tasks}\n")
	s := a.Cfg.Providers.TaskManagement.Settings.(*config.RepositoryTaskSettings)
	bare := filepath.Join(t.TempDir(), "tasks.git")
	mustGit(t, a.Repo, "clone", "--bare", "--local", a.Repo.Dir, bare)
	remote := &gitx.Repo{Dir: bare}
	mustGit(t, remote, "branch", "board", "main")
	mustGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/board")
	mustGit(t, a.Repo, "remote", "add", "origin", bare)
	refs := func() string {
		t.Helper()
		v, err := remote.Git(ctx, "show-ref")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := refs()
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "", "", "Write runbook", "Details", "", token)
	}
	if _, err := create(""); err == nil || !strings.Contains(err.Error(), "hosted GitHub origin") || refs() != before {
		t.Fatalf("PR publication=%v", err)
	}
	s.Publication = "push"
	a.Gate.Autonomy.Remote = config.Suggest
	v, err := create("")
	if err != nil || v.(*Pending).Status != "suggest" || refs() != before {
		t.Fatalf("suggest=%v err=%v", v, err)
	}
	a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Suggest, config.Auto
	v, err = create("")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	summary := v.(*Pending).Summary.(map[string]any)
	if summary["base"] != "board" || summary["target"] != "board" || summary["origin"] != bare || summary["directory"] != "planning/tasks" || refs() != before {
		t.Fatalf("preview=%v", summary)
	}
	mustGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	if _, err = create(tok); err == nil || !strings.Contains(err.Error(), "protected") || refs() != before {
		t.Fatalf("protected default: %v", err)
	}
	mustGit(t, remote, "branch", "other", "main")
	mustGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/other")
	if _, err = create(tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted changed default: %v", err)
	}
	mustGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/board")
	s.Directory = "elsewhere"
	if _, err = create(tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted changed directory: %v", err)
	}
	s.Directory = "planning/tasks"
	// Even an explicit path can be redirected by Git's global pushInsteadOf.
	// Exercise the rules actually loaded by the private publication checkout.
	refsBeforeRewrite := refs()
	testHome := t.TempDir()
	t.Setenv("HOME", testHome)
	s.Repository = &bare
	mustGit(t, a.Repo, "config", "--file", filepath.Join(testHome, ".gitconfig"), "url."+filepath.Join(t.TempDir(), "different.git")+".pushInsteadOf", bare)
	if _, err = create(tok); err == nil || !strings.Contains(err.Error(), "push destination") || refs() != refsBeforeRewrite {
		t.Fatalf("accepted rewritten push URL: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	v, err = create(tok)
	if err != nil {
		t.Fatal(err)
	}
	key := v.(map[string]string)["key"]
	if !strings.HasPrefix(key, "repository/default/") {
		t.Fatalf("key=%q", key)
	}
	changed, err := remote.Git(ctx, "diff", "--name-only", "main..board")
	if err != nil || !strings.HasPrefix(changed, "planning/tasks/task-") {
		t.Fatalf("changed=%s, err=%v", changed, err)
	}
	// Explicit directory and named remote must round-trip the same stored ref.
	s.Repository = &bare
	tc, err := a.Context(ctx, key)
	if err != nil || tc.Key != key || tc.Summary != "Write runbook" || tc.Status != "Open" {
		t.Fatalf("context=%+v err=%v", tc, err)
	}
	mustGit(t, a.Repo, "remote", "add", "board-remote", bare)
	named := "board-remote"
	s.Repository = &named
	work, err := a.MyWork(ctx)
	if err != nil || len(work) != 1 || work[0].Key != key {
		t.Fatalf("work=%v err=%v", work, err)
	}
	before = refs()
	v, err = a.Transition(ctx, key, "#done", false, "")
	if err != nil || refs() != before {
		t.Fatalf("preview=%v err=%v", v, err)
	}
	v, err = a.Transition(ctx, key, "#done", false, confirmation(t, v))
	if err != nil || !v.(TransitionResult).Applied {
		t.Fatalf("transition=%v err=%v", v, err)
	}
	before = refs()
	v, err = a.Transition(ctx, key, "#done", false, "")
	if err != nil || v.(TransitionResult).Applied || refs() != before {
		t.Fatalf("replay=%v err=%v", v, err)
	}
	tc, err = a.Context(ctx, key)
	if err != nil || tc.Status != "Closed" {
		t.Fatalf("context=%+v err=%v", tc, err)
	}
}

func TestFileTaskCreateRetryAndLinks(t *testing.T) {
	ctx := t.Context()
	a := fileTaskApp(t, "  provider: filesystem\n")
	root := t.TempDir()
	a.Cfg.Providers.TaskManagement.Settings.(*config.FileSystemTaskSettings).Root = root
	link := provider.Ref{Instance: "filesystem", Scope: "default", ID: "parent/#2"}
	mustGit(t, a.Repo, "config", "--local", "branch."+a.Repo.Branch(ctx)+".devflowTaskRef", link.String())
	t.Setenv("DEVFLOW_OPERATION_ID", "test-create-event")
	create := func() string {
		t.Helper()
		call := func(token string) (any, error) {
			return a.CreateTicket(ctx, "other/team", "Bug", "Fix task", "Body", "", token)
		}
		v, err := call("")
		if err != nil {
			t.Fatal(err)
		}
		v, err = call(confirmation(t, v))
		if err != nil {
			t.Fatal(err)
		}
		return v.(map[string]string)["key"]
	}
	key := create()
	// Load a fresh app/gate, as a subsequent automation run would.
	var err error
	a, err = Open(ctx, a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Providers.TaskManagement.Settings.(*config.FileSystemTaskSettings).Root = root
	if got := create(); got != key {
		t.Fatalf("retry created %q, want %q", got, key)
	}
	ref, err := provider.ParseRef(key)
	if err != nil {
		t.Fatal(err)
	}
	task, err := a.getTask(ctx, ref)
	if err != nil || task.Type != "Bug" || ref.Scope != "other/team" || len(task.Links) != 1 || task.Links[0].Ref == nil || *task.Links[0].Ref != link {
		t.Fatalf("task=%+v, err=%v", task, err)
	}
	if work, err := a.MyWork(ctx); err != nil || len(work) != 0 {
		t.Fatalf("work outside default scope: %v, err=%v", work, err)
	}
}

func TestRepositoryTaskReview(t *testing.T) {
	a, bare := repositoryFixture(t, "task_management")
	ctx := t.Context()
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("DEVFLOW_OPERATION_ID", "")
	t.Setenv("GITHUB_RUN_ID", "")
	docsGit(t, bare, "branch", "board", "main")
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/board")
	before := docsGit(t, bare, "show-ref")

	prs := map[string]string{}
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = docsPRTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "api.github.com" || r.URL.Path != "/repos/docs-team/handbook/pulls" {
			t.Fatalf("wrong publication endpoint %s", r.URL)
		}
		switch r.Method {
		case http.MethodGet:
			head := strings.TrimPrefix(r.URL.Query().Get("head"), "docs-team:")
			if r.URL.Query().Get("base") != "board" {
				t.Fatalf("PR lookup on wrong base %s", r.URL)
			}
			if url, ok := prs[head]; ok {
				fmt.Fprintf(w, `[{"html_url":%q,"state":"open","base":{"ref":"board"},"head":{"ref":%q,"repo":{"full_name":"docs-team/handbook"}}}]`, url, head)
			} else {
				fmt.Fprint(w, "[]")
			}
		case http.MethodPost:
			var in struct{ Head, Base, Body string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Base != "board" || !strings.Contains(in.Body, "task repository/default/") || prs[in.Head] != "" {
				t.Fatalf("unexpected publication %+v, err=%v", in, err)
			}
			prs[in.Head] = fmt.Sprintf("https://github.com/docs-team/handbook/pull/%d", len(prs)+1)
			fmt.Fprintf(w, `{"html_url":%q}`, prs[in.Head])
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})}

	create := func(tok string) (any, error) {
		return a.CreateTicket(ctx, "", "", "Review quotas", "Details", "", tok)
	}
	a.Gate.Autonomy.Remote = config.Auto
	v, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	preview := v.(*Pending).Summary.(map[string]any)
	head := preview["target"].(string)
	if preview["base"] != "board" || !strings.HasPrefix(head, "devflow/publication/") || docsGit(t, bare, "show-ref") != before || len(prs) != 0 {
		t.Fatalf("unexpected preview %+v, PRs %v", preview, prs)
	}
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	if _, err := create(token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted changed base: %v", err)
	}
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/board")
	// A separate process can redeem the interactive preview.
	a, err = Open(ctx, a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	v, err = create(token)
	if err != nil {
		t.Fatal(err)
	}
	out := v.(map[string]string)
	key := out["key"]
	if out["status"] != string(provider.PendingReview) || out["branch"] != head || out["url"] == "" || out["url"] != prs[head] || !strings.HasPrefix(key, "repository/default/") {
		t.Fatalf("create response %+v", out)
	}
	if _, err := create(token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("reused approval: %v", err)
	}
	refs := docsGit(t, bare, "show-ref")
	if work, err := a.MyWork(ctx); err != nil || len(work) != 0 {
		t.Fatalf("unmerged create visible: %v, %v", work, err)
	}
	// Retry using the same operation ID and fresh approval.
	t.Setenv("DEVFLOW_OPERATION_ID", preview["operation_id"].(string))
	a, err = Open(ctx, a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	confirm := func(call func(string) (any, error)) any {
		t.Helper()
		preview, err := call("")
		if err != nil {
			t.Fatal(err)
		}
		result, err := call(confirmation(t, preview))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	retry := confirm(create).(map[string]string)
	if retry["branch"] != head || retry["key"] != key || retry["url"] != out["url"] || len(prs) != 1 || docsGit(t, bare, "show-ref") != refs {
		t.Fatalf("create replay %+v", retry)
	}

	// Advance the fixture base as if the task creation PR was accepted.
	docsGit(t, bare, "update-ref", "refs/heads/board", docsGit(t, bare, "rev-parse", head))
	refs = docsGit(t, bare, "show-ref")
	a.Gate.Autonomy.Remote, a.Gate.Autonomy.Local = config.Suggest, config.Confirm
	start := func(token string) (any, error) { return a.Start(ctx, key, true, token) }
	started := confirm(start).(StartResult)
	if started.Assigned || started.Transition != nil || started.Key != key || started.Worktree == "" || !strings.HasPrefix(started.Branch, "task-") {
		t.Fatalf("local start %+v", started)
	}
	a.Gate.Autonomy.Local = config.Auto
	v, err = start("")
	if err != nil {
		t.Fatal(err)
	}
	resumed := v.(StartResult)
	if resumed != started || docsGit(t, bare, "show-ref") != refs || len(prs) != 1 {
		t.Fatalf("resume %+v", resumed)
	}

	a.Gate.Autonomy.Remote = config.Auto
	closeTask := func(tok string) (any, error) { return a.Transition(ctx, key, "#done", false, tok) }
	closed := confirm(closeTask).(TransitionResult)
	if closed.Applied || closed.Status != provider.PendingReview || closed.Key != key || closed.Branch == head || closed.URL == "" || closed.URL != prs[closed.Branch] || len(prs) != 2 {
		t.Fatalf("transition response %+v", closed)
	}
	if work, err := a.MyWork(ctx); err != nil || len(work) != 1 || work[0].Key != key || work[0].Status != "Open" {
		t.Fatalf("pending close changed base: %v, %v", work, err)
	}
	// A retry from another client reconciles the same review.
	a, err = Open(ctx, a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	refs = docsGit(t, bare, "show-ref")
	if retry := confirm(closeTask).(TransitionResult); retry != closed || len(prs) != 2 || docsGit(t, bare, "show-ref") != refs {
		t.Fatalf("transition replay %+v", retry)
	}
}
