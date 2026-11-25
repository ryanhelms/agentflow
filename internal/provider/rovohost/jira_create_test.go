package rovohost

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os/exec"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/nscaledev/agentflow/internal/provider/nativemap"
)

func TestDesktopJiraCreate(t *testing.T) {
	const site = "https://fixture.atlassian.net"
	const title = "Verify kiosk check-in"
	const description = "<book>\nConfirm link"
	tools := flatCatalog(t, "jira-create-flat-schema.json")
	reads, writes := 0, 0
	issueType, generation, receiptKey := "Task", "first-grant", "DEV-1"
	server := mcp.NewServer(&mcp.Implementation{Name: "jira-create-fixture"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: nativemap.JiraProjectIssueTypesTool, InputSchema: tools[nativemap.JiraProjectIssueTypesTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		if !maps.Equal(in, map[string]any{"cloudId": site, "projectIdOrKey": "DEV", "maxResults": float64(200)}) {
			return nil, nil, errors.New("wrong issue type lookup")
		}
		reads++
		return nil, map[string]any{"data": map[string]any{
			"startAt": 0, "total": 1, "issueTypes": []map[string]any{{"name": issueType, "subtask": false}},
		}}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: createTool, InputSchema: tools[createTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		writes++
		want := map[string]any{"cloudId": site, "projectKey": "DEV", "summary": title, "issueType": "Task",
			"description": "<p>&lt;book&gt;<br>Confirm link</p>", "contentFormat": "html", "labels": []any{"agentflow"}}
		if !reflect.DeepEqual(in, want) {
			t.Errorf("wrong create arguments: %+v", in)
		}
		return nil, map[string]any{"data": map[string]string{"key": receiptKey}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		if req.URL.String() != catalogEndpoint || req.Header.Get("Authorization") != "Bearer fixture-only" {
			t.Error("wrong endpoint or grant")
		}
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	writer := newWriter(func(_ context.Context, profile string) (string, string, error) {
		if profile != "jira" {
			return "", "", errors.New("wrong grant profile")
		}
		return "fixture-only", generation, nil
	}, inProcessHTTP{handler})
	t.Cleanup(writer.Close)
	ctx := native.WithWriteHost(t.Context(), writer)
	home, dir := t.TempDir(), t.TempDir()
	cmd := exec.CommandContext(t.Context(), "git", "init", "-qb", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize test repository: %v %s", err, out)
	}
	gate, err := policy.NewGate(home, config.Autonomy{Remote: config.Auto}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Repo{}
	cfg.Jira.Site, cfg.Jira.Projects = site, []string{"DEV"}
	a := &app.App{Repo: &gitx.Repo{Dir: dir}, Cfg: cfg, Gate: gate, Home: home}
	if _, err := a.CreateTicket(ctx, "OTHER", "Task", title, description, "", ""); !errors.Is(err, native.ErrDenied) || reads != 0 {
		t.Fatal("out-of-scope Jira project", err)
	}
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "DEV", "Task", title, description, "", token)
	}
	issueType = "Bug"
	if _, err := create(""); !errors.Is(err, native.ErrDenied) || writes != 0 {
		t.Fatal("unknown issue type was eligible for creation", err, writes)
	}
	issueType = "Task"
	preview := func() string {
		t.Helper()
		out, err := create("")
		p, ok := out.(*app.Pending)
		if err != nil || !ok || p.Status != "needs_confirmation" || p.ConfirmToken == "" {
			t.Fatalf("preview = %+v, err = %v", out, err)
		}
		return p.ConfirmToken
	}
	token := preview()
	if _, err := a.CreateTicket(ctx, "DEV", "Task", "Different text", description, "", token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("changed create payload accepted", err)
	}
	generation = "second-grant"
	if _, err := create(token); !errors.Is(err, native.ErrDenied) || writes != 0 {
		t.Fatal("approval survived grant change", err, writes)
	}
	token = preview()
	out, err := create(token)
	if err != nil || out.(map[string]string)["key"] != "DEV-1" || writes != 1 {
		t.Fatal(out, err, writes)
	}
	if _, err := create(token); !errors.Is(err, policy.ErrToken) || writes != 1 {
		t.Fatal("created a duplicate issue", err, writes)
	}
	token = preview()
	receiptKey = "OTHER-1"
	if _, err := create(token); !errors.Is(err, native.ErrWriteUncertain) || writes != 2 {
		t.Fatal("foreign create result verified", err, writes)
	}
	if _, err := create(token); !errors.Is(err, policy.ErrToken) || writes != 2 {
		t.Fatal("replayed create with uncertain result", err, writes)
	}
	server.RemoveTools(createTool)
	call, err := nativemap.JiraCreate(site, provider.TaskDraft{Scope: "DEV", Type: "Task", Title: "Fixture", Labels: []string{"agentflow"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Prepare(ctx, call); !errors.Is(err, native.ErrUnavailable) {
		t.Fatal("completed catalog omitted create", err)
	}

}

func TestDesktopJiraCreateAllowlist(t *testing.T) {
	const site = "https://fixture.atlassian.net"
	call, err := nativemap.JiraCreate(site, provider.TaskDraft{Scope: "DEV", Type: "Task", Title: "Title", Labels: []string{"agentflow"}})
	if err != nil {
		t.Fatal(err)
	}
	s := &session{provider: "jira", site: site, projectKey: "DEV", writeTool: createTool, tools: flatCatalog(t, "jira-create-flat-schema.json")}
	if err := s.Check(call); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"projectKey": "OTHER", "cloudId": "https://other.example", "parent": "DEV-12", "assignee": "elsewhere",
		"additional_fields": map[string]string{"project": "OTHER"}, "contentFormat": "markdown",
	} {
		changed := call
		changed.Arguments = maps.Clone(call.Arguments)
		changed.Arguments[key] = value
		if err := s.Check(changed); !errors.Is(err, native.ErrDenied) {
			t.Errorf("accepted %s: %v", key, err)
		}
	}
	read, err := nativemap.JiraProjectIssueTypes(site, "OTHER")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(read); !errors.Is(err, native.ErrDenied) {
		t.Fatal("allowed metadata read in another project", err)
	}
}
