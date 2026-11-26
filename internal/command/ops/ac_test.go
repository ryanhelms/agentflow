package ops_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

// taskAuditLines returns the lines of a's audit.log.
func taskAuditLines(t *testing.T, a *app.App) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(a.Home, "audit.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []map[string]any
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		out = append(out, line)
	}
	return out
}

func TestACListPublishesNothing(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	key := r.createTask(t, "Review quotas", "- [ ] Run tests\n- [ ] Update docs")
	em, inv := recording()
	env := &commandtest.Env{App: r.app}
	var err error
	text := taskStdout(t, func() { err = taskRun(t, env, inv, "ac", key) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "· 0/2 acceptance criteria\n  [ ] Run tests\n  [ ] Update docs\n") {
		t.Errorf("text checklist %q", text)
	}
	env = &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "ac", "list", "--json", key); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct{ Text string }
	}
	if err := json.Unmarshal(env.Out.Bytes(), &list); err != nil || len(list.Items) != 2 {
		t.Fatalf("JSON checklist %q, %v", env.Out.String(), err)
	}
	if got := em.Types(); len(got) != 0 {
		t.Errorf("a read published %v", got)
	}
}

func TestACUpdatePublishesApplyAndRefusal(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	key := r.createTask(t, "Review quotas", "- [ ] Run tests")
	auditBefore := len(taskAuditLines(t, r.app))
	em, inv := recording()
	env := &commandtest.Env{App: r.app}

	// Ticking is a local write the policy does not gate: it is requested and
	// applied at once, and writes no audit line of its own.
	if err := taskRun(t, env, inv, "ac", "done", "Run", "tests", "--key", key, "--evidence", "abc123"); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpApplied}) {
		t.Fatalf("done events %v", got)
	}
	// The item is the criterion's text: it is in no event.
	payloads := taskPayloads(t, em)
	if p := payloads[0]; p.Operation != "ac.update" || p.Action != "comment" || p.Subject != "tick an acceptance criterion" || p.Task != key {
		t.Errorf("requested payload %+v", p)
	}
	for _, ev := range em.Events() {
		if strings.Contains(string(ev.Payload), "Run tests") || strings.Contains(string(ev.Payload), "abc123") {
			t.Errorf("payload carries the item or evidence: %s", ev.Payload)
		}
	}
	if !strings.Contains(env.Out.String(), `"done": true`) || !strings.Contains(env.Out.String(), `"evidence": "abc123"`) {
		t.Errorf("output %q", env.Out.String())
	}
	if lines := taskAuditLines(t, r.app)[auditBefore:]; len(lines) != 0 {
		t.Errorf("a local tick wrote audit lines: %v", lines)
	}

	// `ac undo` is the same operation with done false.
	em, inv = recording()
	env = &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "ac", "undo", "Run tests", "--key", key); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpApplied}) {
		t.Fatalf("undo events %v", got)
	}
	if p := taskPayloads(t, em)[0]; p.Subject != "untick an acceptance criterion" {
		t.Errorf("undo subject %q", p.Subject)
	}
	if strings.Contains(env.Out.String(), `"done": true`) {
		t.Errorf("undo left the item done: %q", env.Out.String())
	}

	// A criterion that is not on the task is refused.
	em, inv = recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "ac", "done", "no such criterion", "--key", key); err == nil {
		t.Fatal("ticked a criterion that does not exist")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", got)
	}

	// With --post the progress comment is gated by the provider, and a file
	// task cannot take comments.
	em, inv = recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "ac", "done", "Run tests", "--key", key, "--post", "--yes"); err == nil {
		t.Fatal("a file task took a progress comment")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("post events %v", got)
	}
}

func TestACUpdatePostWaitsForTheCommentApproval(t *testing.T) {
	f := newFixture(t)
	taskNoTerminal(t)
	// The fixture's Jira also serves the issue, whose description lists the
	// criterion.
	const issue = `{"key":"PLAT-7","fields":{"summary":"Quota","description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"- [ ] Run tests"}]}]},"status":{"name":"Open"},"issuetype":{"name":"Task"}}}`
	offline := http.DefaultTransport
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/PLAT-7" {
			rec := httptest.NewRecorder()
			_, _ = rec.WriteString(issue)
			return rec.Result(), nil
		}
		return offline.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = offline })

	em, inv := recording()
	env := &commandtest.Env{App: f.app}
	if err := taskRun(t, env, inv, "ac", "done", "Run tests", "--key", "PLAT-7", "--post", "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 {
		t.Fatalf("posts %d", f.posts)
	}
	// The progress comment is gated like any comment: the call waits for
	// approval once and is then applied. The comment's own audit line stays
	// the only one.
	payloads := checkConfirmed(t, em, "ac.update", "Run tests")
	if p := payloads[0]; p.Action != "comment" || p.Task != "PLAT-7" {
		t.Errorf("requested payload %+v", p)
	}
	if lines := f.auditLines(t); len(lines) != 1 || lines[0]["action"] != "comment" {
		t.Errorf("audit lines %v, want the comment's one", lines)
	}
}

func TestACCommandLineErrors(t *testing.T) {
	_, inv := recording()
	const usage = "usage: agentflow ac [list] [KEY] [--json] | agentflow ac done|undo ITEM [--key KEY] [--evidence E] [--post] [--yes]"
	for _, args := range [][]string{
		{"ac", "done"},
		{"ac", "undo"},
		{"ac", "list", "A", "B"},
		{"ac", "A", "B"},
	} {
		err := taskRun(t, &commandtest.Env{}, inv, args...)
		if err == nil || err.Error() != usage {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
}
