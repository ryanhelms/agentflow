package ops_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/command/deliver"
	_ "github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/webui"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fixture is a repository whose task provider is an offline Jira that
// accepts comments on PLAT-7, opened as the CLI opens it.
type fixture struct {
	dir   string
	app   *app.App
	posts int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	goldentest.Isolate(t)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("JIRA_USER", "dev@example.invalid")
	t.Setenv("JIRA_TOKEN", "fixture")
	t.Setenv("GH_TOKEN", "fixture")
	f := &fixture{dir: t.TempDir()}
	old := http.DefaultTransport
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && r.URL.String() == "https://fixture.invalid/rest/api/3/issue/PLAT-7/comment" {
			f.posts++
			return httptest.NewRecorder().Result(), nil
		}
		return nil, errors.New("offline fixture")
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "dev@example.com"},
		{"config", "user.name", "Dev"},
		{"commit", "-q", "--allow-empty", "-m", "chore: init"},
		{"switch", "-q", "-c", "PLAT-7-quota"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte("jira: {site: https://fixture.invalid}\ntask_management: {provider: jira}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background(), f.dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	f.app = a
	return f
}

// auditLines returns the lines of the App's audit.log.
func (f *fixture) auditLines(t *testing.T) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(f.app.Home, "audit.log"))
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

func (f *fixture) commonDir(t *testing.T) string {
	t.Helper()
	id, err := webui.IdentifyWorktree(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	return id.CommonDir
}

func lookup(t *testing.T, name string) command.Command {
	t.Helper()
	c, ok := command.Default.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return c
}

func recording() (*commandtest.Emitter, command.Invoker) {
	em := &commandtest.Emitter{}
	return em, command.NewInvoker(command.Options{Emitters: em.Source(), Auditor: command.AuditLog{}})
}

func TestEODPublishesConfirmApplyAndRefusal(t *testing.T) {
	f := newFixture(t)
	em, inv := recording()
	env := &commandtest.Env{App: f.app, Answer: true}
	if err := lookup(t, "eod.post").RunCLI(t.Context(), env, inv, []string{"PLAT-7=fixed the quota", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 || !strings.Contains(env.Out.String(), `"PLAT-7": true`) {
		t.Fatalf("posts %d, output %q", f.posts, env.Out.String())
	}
	want := []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied}
	evs := em.Events()
	if !slices.Equal(em.Types(), want) {
		t.Fatalf("events %v, want %v", em.Types(), want)
	}
	approval := evs[1].CorrelationID
	if evs[1].CausationID != evs[0].ID || evs[2].CorrelationID != approval || evs[3].CorrelationID != approval || evs[3].CausationID != evs[2].ID {
		t.Fatalf("events not correlated to the approval %s: %+v", approval, evs)
	}
	p, _ := commandtest.Payload(evs[0])
	if p.Operation != "eod.post" || p.Action != "eod" || p.Class != "remote" || p.Subject != "end-of-day notes on 1 tasks" || p.Client != "cli" {
		t.Errorf("requested payload %+v", p)
	}
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), "fixed the quota") || strings.Contains(string(ev.Payload), "cf_") {
			t.Errorf("payload carries the note or the token: %s", ev.Payload)
		}
	}

	// A forged token is refused by the gate: op.failed with its code only.
	em, inv = recording()
	in := map[string]any{"notes": map[string]string{"PLAT-7": "x"}, "confirm_token": "cf_forged"}
	if out, isErr := invokeTool(t, f.app, "eod.post", in, inv); !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", got)
	}
	if p, _ := commandtest.Payload(em.Events()[1]); p.Code == "" || p.Code == command.CodeUnexpectedResult {
		t.Errorf("refusal code %q", p.Code)
	}

	// A "no" at the prompt shows only through approval.resolved: the CLI
	// publishes no op.failed for it (decision 8).
	em, inv = recording()
	env = &commandtest.Env{App: f.app}
	if err := lookup(t, "eod.post").RunCLI(t.Context(), env, inv, []string{"PLAT-7=later"}); err == nil {
		t.Fatal("declined prompt succeeded")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval}) {
		t.Fatalf("declined events %v", got)
	}
}

// invokeTool calls a registered command's MCP tool, added through AddMCP to
// an in-memory server whose calls run on a with an MCP Origin. It returns
// the tool result's text and whether the result is an error.
func invokeTool(t *testing.T, a *app.App, name string, in map[string]any, inv command.Invoker) (string, bool) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	wrap := func(ctx context.Context, _ command.Meta, call func(context.Context, *app.App, command.Origin) (any, error)) *mcp.CallToolResult {
		out, err := call(ctx, a, command.Origin{Source: "mcp:test", Client: "test"})
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
	}
	c := lookup(t, name)
	c.AddMCP(srv, wrap, inv)
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(t.Context(), st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: c.Meta().Tool, Arguments: in})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestApprovalApproveAuditsOnceAndJoinsTheOperation(t *testing.T) {
	f := newFixture(t)
	// A shared approval to approve: the confirmation eod asks for.
	pending, err := f.app.PostEOD(t.Context(), map[string]string{"PLAT-7": "note"}, "")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := pending.(*app.Pending)
	if !ok || p.Approval == nil {
		t.Fatalf("eod did not ask for confirmation: %#v", pending)
	}
	before := len(f.auditLines(t))
	em, inv := recording()
	env := &commandtest.Env{App: f.app}
	if err := lookup(t, "approval.approve").RunCLI(t.Context(), env, inv, []string{p.Approval.OperationID, "--confirm-token", p.ConfirmToken}); err != nil {
		t.Fatal(err)
	}
	lines := f.auditLines(t)[before:]
	if len(lines) != 1 {
		t.Fatalf("approval.approve wrote %d audit lines, want 1: %v", len(lines), lines)
	}
	if l := lines[0]; l["operation"] != "approval.approve" || l["operation_id"] != p.Approval.OperationID || l["result"] != "applied" || l["action"] != "approval_approve" {
		t.Errorf("audit line %v", l)
	}
	evs := em.Events()
	if !slices.Equal(em.Types(), []bus.Type{bus.TypeOpRequested, bus.TypeOpApplied}) {
		t.Fatalf("events %v", em.Types())
	}
	for _, ev := range evs {
		if ev.CorrelationID != p.Approval.OperationID {
			t.Errorf("%s correlation %q, want the approved operation %q", ev.Type, ev.CorrelationID, p.Approval.OperationID)
		}
		if strings.Contains(string(ev.Payload), p.ConfirmToken) {
			t.Errorf("payload carries the token: %s", ev.Payload)
		}
	}
}

func TestEODAuditLineCountUnchanged(t *testing.T) {
	// Before: the app method alone, through the confirmation flow.
	f := newFixture(t)
	pending, err := f.app.PostEOD(t.Context(), map[string]string{"PLAT-7": "note"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.PostEOD(t.Context(), map[string]string{"PLAT-7": "note"}, pending.(*app.Pending).ConfirmToken); err != nil {
		t.Fatal(err)
	}
	direct := len(f.auditLines(t))
	// After: the same operation through the registry and the real Auditor.
	g := newFixture(t)
	_, inv := recording()
	if err := lookup(t, "eod.post").RunCLI(t.Context(), &commandtest.Env{App: g.app}, inv, []string{"PLAT-7=note", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if got := len(g.auditLines(t)); got != direct || direct != 1 {
		t.Fatalf("audit lines: %d through the Invoker, %d direct; want 1 each", got, direct)
	}
}

func TestOperationWithoutStoreCreatesNothing(t *testing.T) {
	f := newFixture(t)
	var warned strings.Builder
	inv := command.NewInvoker(command.Options{Emitters: deliver.Source(deliver.Options{Warn: &warned}), Auditor: command.AuditLog{}})
	env := &commandtest.Env{App: f.app}
	if err := lookup(t, "eod.post").RunCLI(t.Context(), env, inv, []string{"PLAT-7=note", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := lookup(t, "policy.explain").RunCLI(t.Context(), env, inv, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.commonDir(t), "agentflow")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an operation created the agentflow directory under .git: %v", err)
	}
	if warned.Len() != 0 {
		t.Errorf("warned without a store: %q", warned.String())
	}
}

func TestOpEventsReachTheRepositoryStore(t *testing.T) {
	f := newFixture(t)
	common := f.commonDir(t)
	// The daemon creates the store; here a short-lived owner stands in.
	s, err := store.Open(t.Context(), common, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	repo := s.Repo()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var warned strings.Builder
	inv := command.NewInvoker(command.Options{Emitters: deliver.Source(deliver.Options{Warn: &warned}), Auditor: command.AuditLog{}})
	if err := lookup(t, "eod.post").RunCLI(t.Context(), &commandtest.Env{App: f.app}, inv, []string{"PLAT-7=note", "--yes"}); err != nil {
		t.Fatal(err)
	}
	r, err := store.Open(t.Context(), common, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var evs []bus.Event
	for ev, err := range r.ReadFrom(t.Context(), bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	var types []bus.Type
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	want := []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied}
	if !slices.Equal(types, want) {
		t.Fatalf("stored events %v, want %v (warning %q)", types, want, warned.String())
	}
	id, err := webui.IdentifyWorktree(t.Context(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	approval := evs[1].CorrelationID
	for i, ev := range evs {
		if ev.Repo != repo || ev.Worktree != id.Worktree || ev.Source != "cli" {
			t.Errorf("event %d: repo %q worktree %q source %q", i, ev.Repo, ev.Worktree, ev.Source)
		}
		if code := notifysock.Check(bus.Event{ID: ev.ID, Type: ev.Type, Source: ev.Source, Repo: ev.Repo, Worktree: ev.Worktree,
			CorrelationID: ev.CorrelationID, CausationID: ev.CausationID, Time: ev.Time, SchemaVersion: ev.SchemaVersion, Payload: ev.Payload}); code != "" {
			t.Errorf("event %d would be refused by the socket: %s", i, code)
		}
		if i > 0 && ev.CorrelationID != approval {
			t.Errorf("event %d correlation %q, want %q", i, ev.CorrelationID, approval)
		}
	}
	if evs[1].CausationID != evs[0].ID || evs[3].CausationID != evs[2].ID {
		t.Errorf("outcome events do not name their request as cause: %+v", evs)
	}
}
