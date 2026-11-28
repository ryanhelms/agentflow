package features_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/app"
)

func replyContext() ai.Context {
	return ai.Context{Feature: ai.FeatureReply, Targets: []ai.Target{
		{Index: 1, Kind: "thread", ID: "PRRT_one", Label: "src/a.go:3, alice"},
		{Index: 2, Kind: "thread", ID: "PRRT_two", Label: "src/b.go:9, bob"},
	}, Fixed: json.RawMessage(`{}`)}
}

func TestReplyCandidates(t *testing.T) {
	ticketFeature(t, ai.FeatureReply)
	d := ticketFinish(t, ai.FeatureReply, replyContext(),
		`{"replies": [{"thread": 2, "body": "- fixed", "thread_id": "PRRT_other"}, {"thread": 1, "body": "Thanks @alice, see https://example.com"}]}`)
	if d.State != ai.DraftReady || len(d.Candidates) != 2 || len(d.Issues) != 0 {
		t.Fatalf("draft %+v", d)
	}
	c := d.Candidates[0]
	if c.Operation != "review.reply" || c.Tool != "agentflow_review_reply" || c.Target == nil || c.Target.ID != "PRRT_two" {
		t.Fatalf("candidate %+v", c)
	}
	if got := ticketInput(t, c); len(got) != 2 || got["thread_id"] != "PRRT_two" || got["body"] != "- fixed" {
		t.Errorf("input %v", got)
	}
	// "--" comes before the positional words, so a body that starts with
	// "-" is not read as a flag.
	if !slices.Equal(c.CLI, []string{"agentflow", "review", "reply", "--", "PRRT_two", "- fixed"}) {
		t.Errorf("cli %v", c.CLI)
	}
	second := d.Candidates[1]
	if !slices.Equal(second.CLI, []string{"agentflow", "review", "reply", "--", "PRRT_one", "Thanks @alice, see https://example.com"}) {
		t.Errorf("cli %v", second.CLI)
	}
	if !slices.Equal(ticketCodes(second.Warnings), []string{ai.IssueLink, ai.IssueMention}) {
		t.Errorf("warnings %+v", second.Warnings)
	}
}

func TestReplyIssues(t *testing.T) {
	// An index out of range or given twice makes no candidate.
	d := ticketFinish(t, ai.FeatureReply, replyContext(),
		`{"replies": [{"thread": 3, "body": "x"}, {"thread": 0, "body": "x"}, {"thread": 1, "body": "first"}, {"thread": 1, "body": "again"}]}`)
	if d.State != ai.DraftReady || len(d.Candidates) != 1 || ticketInput(t, d.Candidates[0])["body"] != "first" {
		t.Fatalf("draft %+v", d)
	}
	if !slices.Equal(ticketCodes(d.Issues), []string{"duplicate_target", "unknown_target"}) {
		t.Errorf("issues %+v", d.Issues)
	}
	// Issues are agentflow's own words: not even a number the model made up.
	d = ticketFinish(t, ai.FeatureReply, replyContext(), `{"replies": [{"thread": 987654321, "body": "x"}]}`)
	if !slices.Equal(ticketCodes(d.Issues), []string{"unknown_target"}) || strings.Contains(d.Issues[0].Message, "987654321") {
		t.Errorf("issues %+v", d.Issues)
	}
	d = ticketFinish(t, ai.FeatureReply, replyContext(), `{"replies": [{"thread": 1, "body": "  "}]}`)
	if d.State != ai.DraftInvalid || !slices.Equal(ticketCodes(d.Candidates[0].Issues), []string{"missing_field"}) {
		t.Errorf("empty body: %+v", d)
	}
	d = ticketFinish(t, ai.FeatureReply, replyContext(), `{"replies": []}`)
	if d.State != ai.DraftInvalid || !slices.Equal(ticketCodes(d.Issues), []string{"no_reply"}) {
		t.Errorf("no reply: %+v", d)
	}
	d = ticketFinish(t, ai.FeatureReply, replyContext(), acptest.DraftAnswers[acptest.DraftValid]["reply"])
	if d.State != ai.DraftReady {
		t.Errorf("the fake's valid answer: %+v", d)
	}
	// The fake's invalid answer also holds a token shape.
	d = ticketFinish(t, ai.FeatureReply, replyContext(), acptest.DraftAnswers[acptest.DraftInvalid]["reply"])
	if d.State != ai.DraftInvalid || !slices.Equal(ticketCodes(d.Issues), []string{ai.IssueSecretInDraft, "unknown_target"}) {
		t.Errorf("the fake's invalid answer: %+v", d)
	}
}

type replyTransport struct{ http.Handler }

func (h replyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

// replyRepo is a branch with a commit over main, an origin on GitHub.com
// (never contacted: the transport answers) and these threads on its PR.
func replyRepo(t *testing.T, threads ...map[string]any) *app.App {
	t.Helper()
	a := ticketApp(t, "  provider: none\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = a.Repo.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "chore: init")
	git("switch", "-q", "-c", "topic")
	if err := os.WriteFile(a.Repo.Dir+"/a.go", []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.go")
	git("commit", "-q", "-m", "feat: add a")
	git("remote", "add", "origin", "git@github.com:team/app.git")
	var nodes []string
	for _, th := range threads {
		b, _ := json.Marshal(th)
		nodes = append(nodes, string(b))
	}
	t.Setenv("GH_TOKEN", "fixture")
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = replyTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/team/app/pulls":
			fmt.Fprint(w, `[{"number":7,"html_url":"https://github.com/team/app/pull/7","head":{"ref":"topic","sha":"abc"},"base":{"ref":"main"}}]`)
		case "/graphql":
			if body, _ := io.ReadAll(r.Body); strings.Contains(string(body), "mutation") {
				t.Errorf("a draft wrote to GitHub: %s", body)
			}
			fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[%s]}}}}}`, strings.Join(nodes, ","))
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	})}
	return a
}

func replyThread(id, path, author, body string, line int, outdated bool) map[string]any {
	return map[string]any{"id": id, "isResolved": false, "isOutdated": outdated, "path": path, "line": line,
		"comments": map[string]any{"nodes": []any{map[string]any{"body": body, "author": map[string]any{"login": author}}}}}
}

func TestReplyGather(t *testing.T) {
	f := ticketFeature(t, ai.FeatureReply)
	threads := []map[string]any{
		replyThread("PRRT_one", "a.go", "alice", "Why?\r\nSay more.", 3, true),
		replyThread("PRRT_two", "a.go", "bob", "And this?", 0, false),
		replyThread("PRRT_three", "gone.go", "carol", "Old.", 1, false),
	}
	for i := range 9 {
		threads = append(threads, replyThread(fmt.Sprintf("PRRT_%d", i), "gone.go", "dan", "More.", 1, false))
	}
	a := replyRepo(t, threads...)
	c, err := f.Gather(context.Background(), a, ai.Request{Feature: ai.FeatureReply})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Targets) != ai.MaxThreads || c.Targets[0].ID != "PRRT_one" || c.Targets[0].Index != 1 || c.Targets[0].Kind != "thread" ||
		c.Targets[0].Label != "a.go:3, alice" || c.Targets[1].Label != "a.go, bob" || c.Targets[2].ID != "PRRT_three" {
		t.Fatalf("targets %+v", c.Targets)
	}
	byKind := map[ai.PartKind][]ai.Part{}
	for _, p := range c.Parts {
		byKind[p.Kind] = append(byKind[p.Kind], p)
	}
	if len(byKind[ai.PartThread]) != ai.MaxThreads || len(byKind[ai.PartDiff]) != 1 || len(byKind[ai.PartCommits]) != 1 {
		t.Fatalf("parts %+v", c.Parts)
	}
	first := byKind[ai.PartThread][0]
	want := "thread: 1\npath: a.go\nline: 3\nauthor: alice\noutdated: yes\ncomment:\nWhy?\nSay more."
	if first.Text != want || first.Source != "github" || first.Label != "review thread 1" {
		t.Errorf("thread part %q %q %q", first.Label, first.Source, first.Text)
	}
	diff := byKind[ai.PartDiff][0]
	if !strings.HasPrefix(diff.Text, "diff --git a/a.go b/a.go\n") || diff.Label != "branch changes to the file of thread 1, 2" || diff.Source != "git" {
		t.Errorf("diff part %q %q", diff.Label, diff.Text)
	}
	if !strings.HasSuffix(strings.TrimSpace(byKind[ai.PartCommits][0].Text), "feat: add a") {
		t.Errorf("commits %q", byKind[ai.PartCommits][0].Text)
	}

	// The caps of the feature table.
	caps, ok := f.(ai.Caps)
	if !ok {
		t.Fatal("reply states no caps")
	}
	for kind, want := range map[ai.PartKind][2]int{ai.PartThread: {4 << 10, 0}, ai.PartDiff: {8 << 10, 8 << 10}, ai.PartCommits: {0, 0}} {
		if total, file := caps.PartCap(ai.Part{Kind: kind}); total != want[0] || file != want[1] {
			t.Errorf("%s: %d %d", kind, total, file)
		}
	}

	// One thread.
	c, err = f.Gather(context.Background(), a, ai.Request{Feature: ai.FeatureReply, Thread: "PRRT_three"})
	if err != nil || len(c.Targets) != 1 || c.Targets[0].ID != "PRRT_three" || c.Targets[0].Index != 1 {
		t.Errorf("one thread: %+v %v", c.Targets, err)
	}
	if _, err = f.Gather(context.Background(), a, ai.Request{Feature: ai.FeatureReply, Thread: "PRRT_nope"}); ticketRefusal(t, err) != "unknown_thread" {
		t.Errorf("unknown: %v", err)
	}
	if _, err = f.Gather(context.Background(), replyRepo(t), ai.Request{Feature: ai.FeatureReply}); ticketRefusal(t, err) != "no_threads" {
		t.Errorf("none: %v", err)
	}
}

// The diff parts hold the branch's changes to the threads' files only, with
// short context, in the form the framework's filter reads, whatever the
// person's git settings say: a secret file's content never gets through.
func TestReplyGatherDiff(t *testing.T) {
	threads := []map[string]any{
		replyThread("T1", "long.txt", "alice", "a", 20, false),
		replyThread("T2", ".env", "alice", "b", 1, false),
		replyThread("T3", "café/.env", "alice", "c", 1, false),
		replyThread("T4", "café/a b.go", "alice", "d", 1, false),
		// A reviewer's path never reaches git: magic matches nothing.
		replyThread("T5", ":(glob)**", "alice", "e", 1, false),
		replyThread("T6", "my notes.go", "alice", "f", 1, false),
	}
	a := replyRepo(t, threads...)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = a.Repo.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	put := func(name, body string) {
		t.Helper()
		p := filepath.Join(a.Repo.Dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", "-f", "--", name)
	}
	rows := func(changed string) string {
		var b strings.Builder
		for i := 1; i <= 40; i++ {
			if i == 20 && changed != "" {
				fmt.Fprintln(&b, changed)
				continue
			}
			fmt.Fprintf(&b, "row-%02d\n", i)
		}
		return b.String()
	}
	// long.txt is on main before the branch's change to it.
	git("switch", "-q", "main")
	put("long.txt", rows(""))
	git("commit", "-q", "-m", "chore: long file")
	git("switch", "-q", "topic")
	git("merge", "-q", "--no-edit", "main")
	put("long.txt", rows("changed"))
	put(".env", "DB_HOST=plain-value\n")
	put("café/.env", "DB_HOST=other-plain-value\n")
	put("café/a b.go", "package cafe\n")
	put("my notes.go", "package notes\n")
	put("not-reviewed.go", "package other\n")
	git("commit", "-q", "-m", "feat: change files")
	for _, kv := range [][2]string{{"diff.noprefix", "true"}, {"diff.mnemonicprefix", "true"}, {"diff.context", "1000"},
		{"diff.interHunkContext", "1000"}, {"diff.external", "false"}, {"color.diff", "always"}} {
		git("config", kv[0], kv[1])
	}

	c, err := ticketFeature(t, ai.FeatureReply).Gather(context.Background(), a, ai.Request{Feature: ai.FeatureReply})
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	var all strings.Builder
	for _, p := range c.Parts {
		if p.Kind != ai.PartDiff {
			continue
		}
		labels = append(labels, p.Label)
		all.WriteString(p.Text)
		if strings.Count(p.Text, "diff --git ") != 1 {
			t.Errorf("more than one file in a part:\n%s", p.Text)
		}
	}
	want := []string{"branch changes to the file of thread 1", "branch changes to the file of thread 2",
		"branch changes to the file of thread 3", "branch changes to the file of thread 4", "branch changes to the file of thread 6"}
	if !slices.Equal(labels, want) {
		t.Fatalf("diff parts %v", labels)
	}
	diff := all.String()
	// A context line starts its own line; a hunk header may name a row.
	if !strings.Contains(diff, "\n row-17\n") || !strings.Contains(diff, "\n+changed\n") ||
		strings.Contains(diff, "\n row-16\n") || strings.Contains(diff, "\n row-24\n") {
		t.Errorf("not three lines of context:\n%s", diff)
	}
	if !strings.Contains(diff, "+package cafe") || strings.Contains(diff, "package other") {
		t.Errorf("files:\n%s", diff)
	}

	parts, err := ai.Filter(c.Parts, ai.FilterOptions{Root: a.Repo.Dir})
	if err != nil {
		t.Fatal(err)
	}
	var sent strings.Builder
	for _, p := range parts {
		sent.WriteString(p.Text)
	}
	if strings.Contains(sent.String(), "plain-value") || !strings.Contains(sent.String(), "+changed") ||
		!strings.Contains(sent.String(), "+package cafe") {
		t.Errorf("after the filter:\n%s", sent.String())
	}
}
