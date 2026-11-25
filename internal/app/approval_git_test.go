package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/policy"
)

func TestCommitPrepareBindsSelectedContentWithoutStaging(t *testing.T) {
	ctx := t.Context()
	a, err := Open(ctx, newRepo(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	const msg = "fix(PLAT-1): bound quota"
	path := filepath.Join(a.Repo.Dir, "new.txt")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("original")
	paths := []string{"new.txt"}
	v, err := a.CommitPrepare(ctx, msg, paths)
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	if staged, err := a.Repo.StagedFiles(ctx); err != nil || len(staged) != 0 {
		t.Fatalf("preview staged %v, %v", staged, err)
	}
	write("changed contents under the same name")
	if _, err = a.Commit(ctx, msg, paths, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted changed content: %v", err)
	}
	if staged, err := a.Repo.StagedFiles(ctx); err != nil || len(staged) != 0 {
		t.Fatalf("failed confirmation staged %v, %v", staged, err)
	}
	write("original")
	if _, err = a.Commit(ctx, msg, paths, token); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Repo.Git(ctx, "show", "HEAD:new.txt"); err != nil || got != "original" {
		t.Fatalf("committed %q, %v", got, err)
	}
	a.Gate.Autonomy.Local = config.Auto
	write("second")
	v, err = a.CommitPrepare(ctx, msg, paths)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := v.(*Pending)
	if !ok || p.Status != "prepared" || p.ConfirmToken == "" {
		t.Fatalf("auto prepare: %#v", v)
	}
	if staged, err := a.Repo.StagedFiles(ctx); err != nil || len(staged) != 0 {
		t.Fatalf("auto preview staged %v, %v", staged, err)
	}
}

func TestReadyForReviewBindsRemoteDraft(t *testing.T) {
	ctx := t.Context()
	sha := ""
	draft := true
	base := "main"
	changes := 0
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/team/app/pulls":
			fmt.Fprintf(w, `[{"node_id":"PR_1","number":1,"state":"open","draft":%v,"base":{"ref":%q},"head":{"ref":"PLAT-1-quota","sha":%q},"title":"Reviewed"}]`, draft, base, sha)
		case "POST /graphql":
			changes++
			fmt.Fprintf(w, `{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_1","isDraft":false,"headRefOid":%q}}}}`, sha)
			draft = false
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/team/app")
	out, err := a.Repo.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(out)
	a.Gate.Autonomy.Remote = config.Auto
	pre, err := a.MarkReadyPrepare(ctx)
	if err != nil || pre.(*Pending).Status != "prepared" || changes != 0 {
		t.Fatalf("auto ready preparation wrote or failed: %v %v", pre, err)
	}
	a.Gate.Autonomy.Remote = config.Confirm
	v, err := a.MarkReady(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	base = "release"
	if _, err = a.MarkReady(ctx, token); !errors.Is(err, policy.ErrToken) || changes != 0 {
		t.Fatalf("accepted changed destination: %v", err)
	}
	base = "main"
	if _, err = a.MarkReady(ctx, token); err != nil || changes != 1 {
		t.Fatalf("failed promotion: %v", err)
	}
	if _, err = a.MarkReady(ctx, ""); err != nil || changes != 1 {
		t.Fatalf("repeated mutation: %v", err)
	}
}

func TestPushBindsFeatureBranchAndDestination(t *testing.T) {
	ctx := t.Context()
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "tasks.example" {
			fmt.Fprint(w, fixtureIssue)
			return
		}
		t.Fatalf("unexpected request %s", r.URL)
	})
	mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/team/app")
	v, err := a.Push(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	mustGit(t, a.Repo, "remote", "set-url", "origin", "https://github.com/other/project")
	if _, err = a.Push(ctx, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted different push destination: %v", err)
	}
	mustGit(t, a.Repo, "switch", "main")
	if _, err = a.Push(ctx, ""); err == nil {
		t.Fatal("accepted protected branch")
	}
}

func TestPreparedPushPublishesOnlyApprovedSHA(t *testing.T) {
	ctx := t.Context()
	opened := 0
	approvedSHA := ""
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "api.github.com" && r.Method == "POST" && r.URL.Path == "/repos/team/app/pulls" {
			var request struct {
				Title, Body, Head, Base string
				Draft                   bool
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Title != "feat(PLAT-1): publish approved quota" || request.Body != "Reviewed body" || request.Head != "PLAT-1-quota" || request.Base != "main" || !request.Draft {
				t.Fatalf("unexpected PR: %+v", request)
			}
			opened++
			fmt.Fprintf(w, `{"number":7,"html_url":"https://github.com/team/app/pull/7","draft":true,"head":{"sha":%q}}`, approvedSHA)
			return
		}
		if r.URL.Host == "tasks.example" {
			fmt.Fprint(w, fixtureIssue)
			return
		}
		t.Fatalf("unexpected request %s", r.URL)
	})
	remote := t.TempDir()
	mustGit(t, a.Repo, "init", "--bare", remote)
	mustGit(t, a.Repo, "remote", "add", "origin", "git@github.com:team/app.git")
	const bridge = `#!/bin/sh
case "$*" in
	*git-receive-pack*) exec git-receive-pack "$FIXTURE_BARE" ;;
	*git-upload-pack*) exec git-upload-pack "$FIXTURE_BARE" ;;
	*) exit 1 ;;
esac
`
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(bridge), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", bin)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	t.Setenv("FIXTURE_BARE", remote)
	// The push hook receives the selected remote name and the frozen transport.
	hook := filepath.Join(a.Repo.Dir, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n[ \"$1\" = origin ] && [ \"$2\" = git@github.com:team/app.git ]\n"), 0700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, a.Repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/review/*")
	a.Gate.Autonomy.Remote = config.Auto
	v, err := a.PushPrepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := v.(*Pending)
	if !ok || p.Status != "prepared" || p.ConfirmToken == "" {
		t.Fatalf("unexpected prepare: %v", v)
	}
	if refs, err := a.Repo.Git(ctx, "--git-dir="+remote, "for-each-ref", "--format=%(refname)"); err != nil || refs != "" {
		t.Fatalf("wrote before permission: %v %s", err, refs)
	}
	v, err = a.Push(ctx, p.ConfirmToken)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := a.Repo.Git(ctx, "--git-dir="+remote, "rev-parse", "refs/heads/PLAT-1-quota")
	if err != nil || strings.TrimSpace(ref) != v.(map[string]any)["head_sha"] {
		t.Fatalf("pushed wrong revision: %s %v", ref, err)
	}
	up, err := a.Repo.Git(ctx, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil || strings.TrimSpace(up) != "origin/review/PLAT-1-quota" {
		t.Fatalf("upstream %s %v", up, err)
	}

	approvedSHA = strings.TrimSpace(ref)
	draft := true
	in := PROpenInput{Title: "feat(PLAT-1): publish approved quota", Body: "Reviewed body", Draft: &draft}
	a.User.Autonomy.Actions = map[string]config.Level{"push": config.Confirm, "pr_open_draft": config.Auto}
	v, err = a.PRPrepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	if opened != 0 {
		t.Fatal("draft preparation posted to GitHub")
	}
	changed := in
	changed.Body = "Changed body"
	if _, err = a.PROpen(ctx, changed, token); !errors.Is(err, policy.ErrToken) || opened != 0 {
		t.Fatalf("published changed body: %v", err)
	}
	v, err = a.PROpen(ctx, in, token)
	if err != nil || v.(map[string]any)["number"] != 7 || opened != 1 {
		t.Fatalf("opening approved draft: %v %v", v, err)
	}
}
