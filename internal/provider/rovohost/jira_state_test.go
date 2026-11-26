package rovohost

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

const stateSite = "https://fixture.atlassian.net"
const stateKey = "DEV-13"

type stateInvocation struct{ tool, session string }

type stateFixture struct {
	app                                                                         *app.App
	ctx                                                                         context.Context
	calls                                                                       []stateInvocation
	generation, account, project, key, status, category, assignee, transitionID string
	denied, unchanged, ambiguous                                                string
	duplicate                                                                   bool
	reassignOnMove                                                              bool
}

func newStateFixture(t *testing.T) *stateFixture {
	t.Helper()
	// Any accidental fallback reaches this local trap with fixture credentials.
	t.Setenv("JIRA_USER", "fixture")
	t.Setenv("JIRA_TOKEN", "fixture-only")
	previous := http.DefaultTransport
	http.DefaultTransport = inProcessHTTP{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected fallback request %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})}
	t.Cleanup(func() { http.DefaultTransport = previous })
	f := &stateFixture{generation: "first", account: "fixture-account", project: "DEV", key: stateKey, status: "Open", category: "new", transitionID: "29"}
	catalog := flatCatalog(t, "jira-state-flat-schemas.json")
	server := mcp.NewServer(&mcp.Implementation{Name: "state-fixture"}, nil)
	for tool, schema := range catalog {
		mcp.AddTool(server, &mcp.Tool{Name: tool, InputSchema: schema}, func(_ context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			f.calls = append(f.calls, stateInvocation{tool, req.Session.ID()})
			if f.denied == tool {
				return nil, nil, errors.New("fixture denied operation")
			}
			want := map[string]any{"cloudId": stateSite, "issueIdOrKey": stateKey}
			var data any
			switch tool {
			case issueTool:
				want["fields"], want["fieldsByKeys"], want["view"] = []any{"summary", "status", "project", "assignee"}, true, "full"
				var assignee any
				if f.assignee != "" {
					assignee = map[string]string{"accountId": f.assignee}
				}
				data = map[string]any{"key": f.key, "fields": map[string]any{"summary": "Fix kiosk", "status": map[string]any{"name": f.status, "statusCategory": map[string]string{"key": f.category}}, "project": map[string]string{"key": f.project}, "assignee": assignee}}
			case accountTool:
				delete(want, "issueIdOrKey")
				data = map[string]string{"accountId": f.account}
			case transitionsTool:
				to := map[string]any{"name": "Review", "statusCategory": map[string]string{"key": "indeterminate"}}
				options := []any{map[string]any{"id": f.transitionID, "name": "Request review", "to": to}}
				if f.duplicate {
					options = append(options, map[string]any{"id": "35", "to": to})
				}
				data = map[string]any{"transitions": options}
			case assignTool:
				want["fields"] = map[string]any{"assignee": map[string]any{"accountId": f.account}}
				if tool != f.unchanged {
					f.assignee = f.account
				}
				data = map[string]any{"ok": true}
			case moveTool:
				want["transitionId"] = f.transitionID
				if f.reassignOnMove {
					f.assignee = "other-account"
				}
				if tool != f.unchanged {
					f.status, f.category = "Review", "indeterminate"
				}
				data = map[string]any{"ok": true, "transitionId": json.Number(f.transitionID)}
			}
			if !reflect.DeepEqual(args, want) {
				t.Errorf("%s arguments = %#v, want %#v", tool, args, want)
			}
			if f.ambiguous == tool {
				return nil, map[string]any{"unexpected": "ambiguous dispatch"}, nil
			}
			return nil, map[string]any{"data": data}, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		if req.URL.String() != catalogEndpoint || req.Header.Get("Authorization") != "Bearer fixture-only" {
			t.Error("wrong Jira endpoint or grant")
		}
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	writer := newWriter(func(_ context.Context, profile string) (string, string, error) {
		if profile != "jira" {
			return "", "", errors.New("wrong enrollment")
		}
		return "fixture-only", f.generation, nil
	}, inProcessHTTP{h})
	t.Cleanup(writer.Close)
	f.ctx = native.WithWriteHost(t.Context(), writer)

	repo := &gitx.Repo{Dir: t.TempDir()}
	git := func(args ...string) {
		t.Helper()
		if out, err := repo.Git(t.Context(), args...); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", "-qb", "main")
	if err := os.WriteFile(filepath.Join(repo.Dir, "flow.yml"), []byte("projects:\n  DEV:\n    branch.created: Review\n    pr.opened: Review\n    order: [Open, Review, Done]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "flow.yml")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.org", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	home := t.TempDir()
	gate, err := policy.NewGate(home, config.Autonomy{Remote: config.Auto}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Repo{FlowMap: "flow.yml"}
	cfg.Jira.Site, cfg.Jira.Projects = stateSite, []string{"DEV"}
	f.app = &app.App{Cfg: cfg, Repo: repo, Home: home, Gate: gate}
	return f
}

func statePreview(t *testing.T, call func(string) (any, error)) string {
	t.Helper()
	out, err := call("")
	p, ok := out.(*app.Pending)
	if err != nil || !ok || p.Status != "needs_confirmation" || p.ConfirmToken == "" {
		t.Fatalf("preview = %+v, %v", out, err)
	}
	return p.ConfirmToken
}

func (f *stateFixture) transition(token string) (any, error) {
	return f.app.Transition(f.ctx, stateKey, "pr.opened", false, token)
}
func (f *stateFixture) start(token string) (any, error) {
	return f.app.Start(f.ctx, stateKey, false, token)
}
func (f *stateFixture) writes() []string {
	var writes []string
	for _, call := range f.calls {
		if call.tool == moveTool || call.tool == assignTool {
			writes = append(writes, call.tool)
		}
	}
	return writes
}

func (f *stateFixture) checkWriteLease(t *testing.T, tool string, want []string) {
	t.Helper()
	for _, call := range f.calls {
		if call.tool != tool {
			continue
		}
		var onLease []string
		for _, other := range f.calls {
			if other.session == call.session {
				onLease = append(onLease, other.tool)
			}
		}
		if len(onLease) < len(want) || !slices.Equal(onLease[len(onLease)-len(want):], want) {
			t.Errorf("%s lease reads/writes = %v, want suffix %v", tool, onLease, want)
		}
		return
	}
	t.Fatalf("missing %s dispatch", tool)
}

func TestDesktopJiraTransition(t *testing.T) {
	f := newStateFixture(t)
	if _, err := f.app.Transition(f.ctx, "OTHER-1", "pr.opened", false, ""); !errors.Is(err, native.ErrDenied) || len(f.calls) != 0 {
		t.Fatal("foreign project", err)
	}
	out, err := f.app.Transition(f.ctx, stateKey, "pr.opened", true, "")
	if err != nil || out.(app.TransitionResult).Applied || out.(app.TransitionResult).To != "Review" {
		t.Fatal(out, err)
	}
	token := statePreview(t, f.transition)
	f.transitionID = "33"
	if _, err := f.transition(token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("changed transition ID", err)
	}
	f.transitionID = "29"
	f.generation = "second"
	if _, err := f.transition(token); !errors.Is(err, native.ErrDenied) || len(f.writes()) != 0 {
		t.Fatal("changed grant", err, f.writes())
	}
	token = statePreview(t, f.transition)
	out, err = f.transition(token)
	if err != nil || !out.(app.TransitionResult).Applied {
		t.Fatal(out, err)
	}
	f.checkWriteLease(t, moveTool, []string{issueTool, transitionsTool, moveTool, issueTool})
	if _, err = f.transition(token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("replayed transition", err)
	}
	if _, err = f.transition(""); err != nil || !slices.Equal(f.writes(), []string{moveTool}) {
		t.Fatal("already at target", err, f.writes())
	}
}

func TestDesktopJiraStartApprovalBindsAccount(t *testing.T) {
	f := newStateFixture(t)
	token := statePreview(t, f.start)
	f.account = "new-account"
	if _, err := f.start(token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("changed current user", err)
	}
	f.account = "fixture-account"
	f.assignee = "another-account"
	if _, err := f.start(token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("changed assignee", err)
	}
	if len(f.writes()) != 0 || f.app.Repo.Branch(t.Context()) != "main" {
		t.Fatal("stale approval changed branch or Jira state")
	}
}

func TestDesktopJiraTerminalSkipsTransition(t *testing.T) {
	f := newStateFixture(t)
	f.status, f.category = "Done", "done"
	out, err := f.transition("")
	if err != nil || out.(app.TransitionResult).To != "" || len(f.calls) != 1 || f.calls[0].tool != issueTool {
		t.Fatal(out, err, f.calls)
	}
}

func TestDesktopJiraStateStopsBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*stateFixture)
	}{
		{"foreign issue", func(f *stateFixture) { f.key = "DEV-99" }},
		{"missing project", func(f *stateFixture) { f.project = "" }},
		{"foreign project", func(f *stateFixture) { f.project = "OTHER" }},
		{"ambiguous transitions", func(f *stateFixture) { f.duplicate = true }},
		{"unknown category", func(f *stateFixture) { f.category = "unknown" }},
		{"provider denied read", func(f *stateFixture) { f.denied = issueTool }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStateFixture(t)
			test.change(f)
			if _, err := f.transition(""); err == nil || errors.Is(err, native.ErrUnavailable) || len(f.writes()) != 0 {
				t.Fatal(err, f.writes())
			}
		})
	}
}

func TestDesktopJiraStartOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, unchanged, ambiguous, denied string
		alreadyAssigned, assigned, applied bool
		writes                             []string
	}{
		{name: "assigned and moved", assigned: true, applied: true, writes: []string{assignTool, moveTool}},
		{name: "already assigned", alreadyAssigned: true, assigned: true, applied: true, writes: []string{moveTool}},
		{name: "unverified assignment", unchanged: assignTool, writes: []string{assignTool}},
		{name: "ambiguous assignment", ambiguous: assignTool, writes: []string{assignTool}},
		{name: "denied assignment", denied: assignTool, writes: []string{assignTool}},
		{name: "unverified transition", unchanged: moveTool, assigned: true, writes: []string{assignTool, moveTool}},
		{name: "ambiguous transition", ambiguous: moveTool, assigned: true, writes: []string{assignTool, moveTool}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStateFixture(t)
			if tc.alreadyAssigned {
				f.assignee = f.account
			}
			token := statePreview(t, f.start)
			if len(f.writes()) != 0 || f.app.Repo.Branch(t.Context()) != "main" {
				t.Fatal("start preview mutated state")
			}
			f.unchanged, f.ambiguous, f.denied = tc.unchanged, tc.ambiguous, tc.denied
			out, err := f.start(token)
			result, ok := out.(app.StartResult)
			if !ok || result.Assigned != tc.assigned || (result.Transition != nil && result.Transition.Applied) != tc.applied ||
				result.Branch != "DEV-13-fix-kiosk" || f.app.Repo.Branch(t.Context()) != result.Branch || !slices.Equal(f.writes(), tc.writes) {
				t.Fatal(out, err, f.writes())
			}
			if !tc.applied {
				if !errors.Is(err, native.ErrWriteUncertain) || !strings.Contains(err.Error(), result.Branch) {
					t.Fatal("missing partial outcome", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.name == "assigned and moved" {
				f.checkWriteLease(t, assignTool, []string{accountTool, issueTool, assignTool, issueTool})
				f.checkWriteLease(t, moveTool, []string{issueTool, transitionsTool, moveTool, issueTool})
			}
			if _, err = f.start(token); !errors.Is(err, policy.ErrToken) || !slices.Equal(f.writes(), tc.writes) {
				t.Fatal("replayed start", err, f.writes())
			}
		})
	}
}

func TestDesktopJiraStartDetectsWorkflowReassignment(t *testing.T) {
	f := newStateFixture(t)
	f.reassignOnMove = true
	out, err := f.start(statePreview(t, f.start))
	result, ok := out.(app.StartResult)
	if !ok || err == nil || result.Assigned || result.Transition == nil || !result.Transition.Applied || !slices.Equal(f.writes(), []string{assignTool, moveTool}) {
		t.Fatal(out, err, f.writes())
	}
}

func TestDesktopJiraStateRejectsConflictingReceipts(t *testing.T) {
	s := session{issueKey: stateKey, stateTarget: "29"}
	for _, raw := range []string{`{"ok":false}`, `{"success":false}`, `{"key":"DEV-14"}`, `{"issueIdOrKey":"OTHER-13"}`, `{"issueKey":"DEV-14"}`, `{"transitionId":"35"}`, `{"transitionId":35}`, `{"transitionId":true}`, `{"errorMessages":["denied"]}`} {
		if err := s.checkStateResult(moveTool, []byte(raw)); err == nil {
			t.Errorf("accepted receipt %s", raw)
		}
	}
}

func TestDesktopJiraStateAllowlist(t *testing.T) {
	s := session{provider: "jira", site: stateSite, issueKey: stateKey, tools: flatCatalog(t, "jira-state-flat-schemas.json")}
	for _, pair := range []struct{ tool, arg, value string }{{moveTool, "transitionId", "29"}, {assignTool, "assignee", "account-1"}} {
		s.writeTool, s.stateTarget = pair.tool, pair.value
		call := native.Call{Provider: "jira", Server: "atlassian", Tool: pair.tool, Arguments: map[string]any{"cloudId": stateSite, "issueIdOrKey": stateKey, pair.arg: pair.value}}
		if err := s.Check(call); err != nil {
			t.Fatal(err)
		}
		for field, value := range map[string]any{"cloudId": "https://other.example", "issueIdOrKey": "DEV-14", pair.arg: "other", "fields": map[string]any{"reporter": "other"}, "transitionName": "Done", "sprintId": 1} {
			changed := call
			changed.Arguments = maps.Clone(call.Arguments)
			changed.Arguments[field] = value
			if err := s.Check(changed); !errors.Is(err, native.ErrDenied) {
				t.Errorf("accepted %s %s: %v", pair.tool, field, err)
			}
		}
		for _, tool := range []string{"executeWrite", "executeRead", commentTool, createTool, remoteLinkTool} {
			changed := call
			changed.Tool = tool
			if err := s.Check(changed); !errors.Is(err, native.ErrDenied) {
				t.Errorf("accepted %s on %s lease: %v", tool, pair.tool, err)
			}
		}
		delete(s.tools, pair.tool)
		if err := s.Check(call); !errors.Is(err, native.ErrDenied) {
			t.Errorf("missing enrolled tool %s: %v", pair.tool, err)
		}
	}
}
