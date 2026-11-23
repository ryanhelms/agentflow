package consolepublish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/gh"
)

// A synthetic GitHub API. No listener, credentials, repository or external
// process is used. Losing a successful response simulates a retry after a write.
type forge struct {
	t                                                         *testing.T
	change                                                    proposal
	defaultSHA, defaultOID, head                              string
	pr, draft, armed, merged, clean                           bool
	loseCommit, losePR, extraFile                             bool
	commits, creates, merges                                  int
	protected, rules, actions, rulesUnavailable, driftAfterPR bool
	message                                                   string
	tree                                                      []treeEntry
}

func newForge(t *testing.T) (*forge, *gh.Client) {
	t.Helper()
	f := &forge{t: t, change: proposal{Path: ".devflow/config.yml", Original: "old", Content: "new", Title: "chore: update settings"}, defaultSHA: "base", defaultOID: blobID("old")}
	old := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = old })
	return f, gh.New("")
}

func (f *forge) result() map[string]any {
	state := "open"
	repo := map[string]string{"full_name": "org/repo"}
	m := map[string]any{"node_id": "node", "number": 5, "title": f.change.Title, "html_url": "https://github.com/org/repo/pull/5", "draft": f.draft,
		"base": map[string]any{"ref": "main", "repo": repo}, "head": map[string]any{"ref": f.change.branch("main"), "sha": f.head, "repo": repo}, "mergeable": f.clean}
	if f.merged {
		state = "closed"
		m["merged_at"] = "2026-01-01T00:00:00Z"
		m["merge_commit_sha"] = "landed"
	}
	m["state"] = state
	if f.clean {
		m["mergeable_state"] = "clean"
	} else {
		m["mergeable_state"] = "blocked"
	}
	if f.armed {
		m["auto_merge"] = map[string]bool{"enabled": true}
	}
	return m
}

func (f *forge) RoundTrip(req *http.Request) (*http.Response, error) {
	p := strings.TrimPrefix(req.URL.Path, "/repos/org/repo")
	branch := f.change.branch("main")
	code := 200
	var out any = map[string]any{}
	var args map[string]any
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if len(b) > 0 {
			if err = json.Unmarshal(b, &args); err != nil {
				return nil, err
			}
		}
	}
	switch {
	case req.Method == "GET" && p == "":
		out = repository{Default: "main", Squash: true, Auto: true}
	case req.Method == "GET" && strings.HasPrefix(p, "/branches/"):
		out = map[string]any{"name": strings.TrimPrefix(p, "/branches/"), "protected": f.protected}
	case req.Method == "GET" && strings.HasPrefix(p, "/rules/branches/"):
		out = []any{}
		if f.rules {
			out = []any{map[string]string{"type": "required_status_checks"}}
		}
		if f.rulesUnavailable {
			code = 403
		}
	case req.Method == "GET" && p == "/actions/workflows":
		active := []any{}
		if f.actions {
			active = append(active, map[string]string{"state": "active"})
		}
		out = map[string]any{"workflows": active}
	case req.Method == "GET" && p == "/git/ref/heads/main":
		out = map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": f.defaultSHA}}
	case req.Method == "GET" && p == "/git/ref/heads/"+branch:
		if f.head == "" {
			code = 404
		} else {
			out = map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"sha": f.head}}
		}
	case req.Method == "GET" && strings.HasPrefix(p, "/git/trees/"):
		out = map[string]any{"tree": append([]treeEntry{{Path: f.change.Path, Mode: "100644", SHA: f.defaultOID}}, f.tree...)}
	case req.Method == "POST" && p == "/git/refs":
		if args["ref"] != "refs/heads/"+branch || args["sha"] != f.defaultSHA {
			f.t.Fatalf("unexpected ref mutation: %#v", args)
		}
		f.head = f.defaultSHA
	case req.Method == "POST" && p == "/graphql":
		q := args["query"].(string)
		vars := args["variables"].(map[string]any)
		input, _ := vars["input"].(map[string]any)
		switch {
		case strings.Contains(q, "createCommitOnBranch"):
			if input["expectedHeadOid"] != f.head || input["message"].(map[string]any)["headline"] != f.change.Title {
				f.t.Fatalf("commit not pinned to proposal: %#v", input)
			}
			files := input["fileChanges"].(map[string]any)["additions"].([]any)
			if len(files) != 1 || files[0].(map[string]any)["path"] != f.change.Path {
				f.t.Fatalf("unexpected files: %v", files)
			}
			f.message = input["message"].(map[string]any)["body"].(string)
			f.commits++
			f.head = "proposed"
			if f.loseCommit {
				code = 502
			} else {
				out = map[string]any{"data": map[string]any{"createCommitOnBranch": map[string]any{"commit": map[string]string{"oid": f.head}}}}
			}
		case strings.Contains(q, "markPullRequestReadyForReview"):
			if vars["id"] != "node" || !f.draft {
				f.t.Fatalf("unexpected draft transition: %v", vars)
			}
			f.draft = false
			out = map[string]any{"data": map[string]any{"markPullRequestReadyForReview": map[string]any{"pullRequest": map[string]bool{"isDraft": false}}}}
		case strings.Contains(q, "enablePullRequestAutoMerge"):
			if input["expectedHeadOid"] != f.head || input["pullRequestId"] != "node" || input["mergeMethod"] != "SQUASH" {
				f.t.Fatalf("unbound merge: %#v", input)
			}
			f.armed = true
			out = map[string]any{"data": map[string]any{"enablePullRequestAutoMerge": map[string]any{"pullRequest": map[string]any{"autoMergeRequest": map[string]string{"enabledAt": "now"}}}}}
		default:
			f.t.Fatalf("unexpected mutation: %v", q)
		}
	case req.Method == "GET" && strings.HasPrefix(p, "/compare/"):
		files := []map[string]string{{"filename": f.change.Path, "status": "modified", "sha": blobID(f.change.Content)}}
		if f.extraFile {
			files = append(files, map[string]string{"filename": "other"})
		}
		out = map[string]any{"ahead_by": 1, "files": files, "commits": []map[string]any{{"commit": map[string]any{"message": strings.TrimSpace(f.change.Title + "\n\n" + f.message)}}}}
	case req.Method == "GET" && p == "/pulls":
		if req.URL.Query().Get("head") != "org:"+branch || req.URL.Query().Get("base") != "main" {
			f.t.Fatalf("unscoped pull query: %v", req.URL)
		}
		if !f.pr {
			out = []any{}
		} else {
			out = []any{f.result()}
		}
	case req.Method == "POST" && p == "/pulls":
		f.creates++
		f.pr = true
		f.draft = args["draft"].(bool)
		if f.driftAfterPR {
			f.defaultSHA = "other"
			f.defaultOID = blobID("changed configuration")
		}
		if f.losePR {
			code = 502
		} else {
			out = f.result()
		}
	case req.Method == "GET" && p == "/pulls/5":
		out = f.result()
	case req.Method == "PUT" && p == "/pulls/5/merge":
		if args["sha"] != f.head || args["merge_method"] != "squash" {
			f.t.Fatalf("unbound merge %#v", args)
		}
		if args["commit_message"] != f.message {
			f.t.Fatalf("squash message dropped CI or task reference: %#v", args)
		}
		f.merges++
		f.merged = true
		f.defaultSHA = "landed"
		f.defaultOID = blobID(f.change.Content)
		out = map[string]any{"merged": true, "sha": "landed"}
	default:
		f.t.Fatalf("unexpected GitHub request %s %s", req.Method, p)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: code, Status: fmt.Sprint(code), Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b))), Request: req}, nil
}

func TestPublishReconcilesUnknownWritesAndRetries(t *testing.T) {
	f, c := newForge(t)
	f.loseCommit, f.losePR = true, true
	deps := map[string]string{f.change.Path: blobID(f.change.Original)}
	for i := 0; i < 2; i++ {
		result, err := publish(context.Background(), c, "org/repo", f.change, deps, false)
		if err != nil || result.State != "auto_merge_requested" || result.URL == "" {
			t.Fatalf("attempt %d: %+v, %v", i, result, err)
		}
	}
	if f.creates != 1 || f.commits != 1 || f.merges != 0 {
		t.Fatalf("writes %d %d %d", f.creates, f.commits, f.merges)
	}
	f.merged = true
	f.defaultSHA = "landed"
	f.defaultOID = blobID(f.change.Content)
	result, err := publish(context.Background(), c, "org/repo", f.change, deps, false)
	if err != nil || result.State != "already_applied" || result.URL == "" || result.Revision != "landed" {
		t.Fatalf("merged replay: %+v, %v", result, err)
	}
}

func TestPublishRejectsStaleOrForeignChanges(t *testing.T) {
	for _, kind := range []string{"edited file", "inherited config", "extra definition", "branch collision"} {
		t.Run(kind, func(t *testing.T) {
			f, c := newForge(t)
			deps := map[string]string{f.change.Path: blobID(f.change.Original)}
			switch kind {
			case "edited file":
				f.defaultOID = blobID("a change by another developer")
			case "inherited config":
				deps["flow-map.yml"] = blobID("local")
				f.tree = []treeEntry{{Path: "flow-map.yml", Mode: "100644", SHA: blobID("remote")}}
			case "extra definition":
				f.tree = []treeEntry{{Path: ".devflow/providers/extra.yml", Mode: "100644", SHA: blobID("extra")}}
			case "branch collision":
				f.head = "occupied"
				f.extraFile = true
			}
			_, err := publish(context.Background(), c, "org/repo", f.change, deps, false)
			if err == nil || f.creates != 0 || f.commits != 0 || f.armed {
				t.Fatalf("unsafe publish: %v %+v", err, f)
			}
		})
	}
}

func TestPublishDraftOrImmediatelyEligible(t *testing.T) {
	for _, draft := range []bool{false, true} {
		t.Run(fmt.Sprint(draft), func(t *testing.T) {
			f, c := newForge(t)
			f.clean = true
			res, err := publish(context.Background(), c, "org/repo", f.change, map[string]string{f.change.Path: blobID(f.change.Original)}, draft)
			if err != nil {
				t.Fatal(err)
			}
			if draft && (res.State != "draft" || f.merged || f.armed) {
				t.Fatalf("draft was applied: %+v", res)
			}
			if !draft && (res.State != "merged" || res.Revision != "landed" || f.merges != 1) {
				t.Fatalf("merge failed: %+v", res)
			}
		})
	}
}

func TestPublicationDetectsActionSkipSupport(t *testing.T) {
	for _, kind := range []string{"active", "protected", "rules", "denied", "no actions"} {
		t.Run(kind, func(t *testing.T) {
			f, c := newForge(t)
			f.clean = true
			f.actions = kind != "no actions"
			f.protected = kind == "protected"
			f.rules = kind == "rules"
			f.rulesUnavailable = kind == "denied"
			got, err := publish(context.Background(), c, "org/repo", f.change, map[string]string{f.change.Path: blobID(f.change.Original)}, false)
			if err != nil || got.CI == "" || strings.Contains(f.message, "[skip actions]") != (kind == "active") {
				t.Fatalf("CI result %+v message=%q err=%v", got, f.message, err)
			}
		})
	}
}

func TestPublicationRechecksDefaultBeforeMerge(t *testing.T) {
	f, c := newForge(t)
	f.clean = true
	f.driftAfterPR = true
	got, err := publish(context.Background(), c, "org/repo", f.change, map[string]string{f.change.Path: blobID(f.change.Original)}, false)
	if err != nil || got.State != "open" || got.URL == "" || got.Warning == "" || f.merged || f.armed {
		t.Fatalf("merged against changed requirements: %+v, %v", got, err)
	}
}

func TestPublishToApprovedRepositoryLeavesWorktreeAndIndexUntouched(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "fixture-only")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture %v: %s: %v", args, b, err)
		}
		return string(b)
	}
	git("init", "--quiet", "--initial-branch=main")
	git("remote", "add", "origin", "https://github.com/org/repo.git")
	f, _ := newForge(t)
	f.change.Original = "devflow: 1\ncommit: {require_key: false}\n"
	f.change.Content = f.change.Original + "# team setting\n"
	f.defaultOID = blobID(f.change.Original)
	full := filepath.Join(root, f.change.Path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(f.change.Original), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", f.change.Path)
	before := git("status", "--porcelain=v1")
	ctx := context.Background()
	repo, err := Destination(ctx, root)
	if err != nil || repo != "org/repo" {
		t.Fatalf("destination %q: %v", repo, err)
	}
	snapshot, err := consolecfg.View(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := SourceBaseline(root, snapshot.Files, f.change.Path, f.change.Original)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PublishTo(ctx, root, repo, f.change.Path, f.change.Original, f.change.Content, f.change.Title, true, nil)
	if err == nil || !strings.Contains(err.Error(), "configuration inputs changed") || f.pr || f.commits != 0 {
		t.Fatalf("published with a different baseline: %v", err)
	}
	_, err = PublishTo(ctx, root, "org/other", f.change.Path, f.change.Original, f.change.Content, f.change.Title, true, baseline)
	if err == nil || f.pr || f.commits != 0 {
		t.Fatalf("unexpected wrong-target publish: %v", err)
	}
	got, err := PublishTo(ctx, root, repo, f.change.Path, f.change.Original, f.change.Content, f.change.Title, true, baseline)
	if err != nil || got.State != "draft" {
		t.Fatalf("draft: %+v, %v", got, err)
	}
	b, err := os.ReadFile(full)
	if err != nil || string(b) != f.change.Original || git("status", "--porcelain=v1") != before {
		t.Fatal("publication changed working files or index")
	}
}

func TestPublishingExistingDraftMarksSamePRReady(t *testing.T) {
	f, c := newForge(t)
	deps := map[string]string{f.change.Path: blobID(f.change.Original)}
	draft, err := publish(context.Background(), c, "org/repo", f.change, deps, true)
	if err != nil || draft.State != "draft" {
		t.Fatalf("draft: %+v, %v", draft, err)
	}
	ready, err := publish(context.Background(), c, "org/repo", f.change, deps, false)
	if err != nil || ready.State != "auto_merge_requested" || ready.URL != draft.URL || f.draft || f.creates != 1 || f.commits != 1 {
		t.Fatalf("ready: %+v, %v", ready, err)
	}
}
