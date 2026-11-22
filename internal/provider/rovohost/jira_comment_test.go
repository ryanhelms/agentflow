package rovohost

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
)

func TestDesktopJiraComment(t *testing.T) {
	const site = "https://fixture.atlassian.net"
	const key = "DEV-13"
	const note = "<ready>\n**review**"
	const html = "<p>&lt;ready&gt;<br>**review**<br><br>(via devflow)</p>"
	tools := flatCatalog(t, "jira-comment-flat-schema.json")
	reads, writes := 0, 0
	project, generation := "DEV", "fixture-grant"
	ambiguous := false
	server := mcp.NewServer(&mcp.Implementation{Name: "jira-fixture"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "getJiraIssue", InputSchema: tools["getJiraIssue"]}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		if in["cloudId"] != site || in["issueIdOrKey"] != key {
			return nil, nil, errors.New("wrong issue read")
		}
		reads++
		return nil, map[string]any{"data": map[string]any{"key": key, "fields": map[string]any{"project": map[string]string{"key": project}}}}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: commentTool, InputSchema: tools[commentTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		writes++
		if !maps.Equal(in, map[string]any{"cloudId": site, "issueIdOrKey": key, "commentBody": html, "contentFormat": "html"}) {
			t.Error("wrong comment arguments")
		}
		if ambiguous {
			return nil, map[string]any{"data": map[string]bool{"success": true}}, nil
		}
		// Synthetic output projection. The vendor catalog fixture describes the
		// HTML echo, but does not include an output schema.
		return nil, map[string]any{"data": map[string]any{"commentId": "101", "body": html, "appliedContentFormat": "html", "issueKey": key}}, nil
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
	home := t.TempDir()
	gate, err := policy.NewGate(home, config.Autonomy{Remote: config.Auto}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Repo{}
	cfg.Jira.Site, cfg.Jira.Projects = site, []string{"DEV"}
	a := &app.App{Cfg: cfg, Gate: gate, Home: home}
	if _, err := a.Comment(ctx, "OTHER-1", note, ""); !errors.Is(err, native.ErrDenied) || reads != 0 {
		t.Fatal("out-of-scope Jira project", err)
	}
	preview := func() string {
		t.Helper()
		out, err := a.Comment(ctx, key, note, "")
		p, ok := out.(*app.Pending)
		if err != nil || !ok || p.Status != "needs_confirmation" || p.ConfirmToken == "" {
			t.Fatalf("preview = %+v, err = %v", out, err)
		}
		return p.ConfirmToken
	}
	token := preview()
	project = "OTHER"
	if _, err := a.Comment(ctx, key, note, token); !errors.Is(err, provider.ErrConflict) {
		t.Fatal("foreign issue project", err)
	}
	project = "DEV"
	if _, err := a.Comment(ctx, key, "changed", token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("modified comment accepted", err)
	}
	// A stale Desktop approval must not select the fallback, even under auto.
	if _, err := a.Comment(t.Context(), key, note, token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("approval changed route", err)
	}
	generation = "re-enrolled"
	if _, err := a.Comment(ctx, key, note, token); !errors.Is(err, native.ErrDenied) || writes != 0 {
		t.Fatal("approval survived grant change", err, writes)
	}
	token = preview()
	out, err := a.Comment(ctx, key, note, token)
	if err != nil || out.(map[string]any)["comment_id"] != "101" || writes != 1 {
		t.Fatal(out, err, writes)
	}
	if _, err := a.Comment(ctx, key, note, token); !errors.Is(err, policy.ErrToken) || writes != 1 {
		t.Fatal("replayed successful write", err, writes)
	}
	ambiguous = true
	token = preview()
	out, err = a.Comment(ctx, key, note, token)
	if !errors.Is(err, native.ErrWriteUncertain) || out.(map[string]any)["commented"] != false || writes != 2 {
		t.Fatal("ambiguous receipt", out, err, writes)
	}
	if _, err := a.Comment(ctx, key, note, token); !errors.Is(err, policy.ErrToken) {
		t.Fatal("replayed uncertain write", err)
	}
	preview()
	if writes != 2 {
		t.Fatal("automatically reposted an uncertain comment")
	}
	receipts := filepath.Join(home, "task-write-receipts")
	claims, err := filepath.Glob(filepath.Join(receipts, "*.claim"))
	if err != nil || len(claims) != 2 {
		t.Fatal("successful and uncertain attempts must both retain their claims", claims, err)
	}
	outcomes, err := filepath.Glob(filepath.Join(receipts, "*.outcome"))
	if err != nil || len(outcomes) != 1 {
		t.Fatal("only the verified comment should have a durable outcome", outcomes, err)
	}
	audit, err := os.ReadFile(filepath.Join(home, "audit.log"))
	if err != nil || !strings.Contains(string(audit), `"action":"comment"`) || !strings.Contains(string(audit), "uncertain") || strings.Contains(string(audit), "review**") {
		t.Fatal("missing comment audit or disclosed content", err)
	}
}

func TestDesktopJiraCommentAllowlist(t *testing.T) {
	const site = "https://fixture.atlassian.net"
	ref := provider.Ref{Instance: "jira", Scope: "DEV", ID: "DEV-13"}
	call, err := nativemap.JiraComment(site, ref, "Review")
	if err != nil {
		t.Fatal(err)
	}
	s := &session{provider: "jira", site: site, issueKey: ref.ID, writeTool: commentTool, tools: flatCatalog(t, "jira-comment-flat-schema.json")}
	if err := s.Check(call); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{"commentId": "42", "jsmCommentType": "internalNote", "contentFormat": "markdown", "issueIdOrKey": "DEV-14", "cloudId": "https://other.example"} {
		changed := call
		changed.Arguments = maps.Clone(call.Arguments)
		changed.Arguments[field] = value
		if err := s.Check(changed); !errors.Is(err, native.ErrDenied) {
			t.Errorf("accepted %s, err = %v", field, err)
		}
	}
	for _, tool := range []string{"executeWrite", remoteLinkTool, "editJiraIssue", "transitionJiraIssue"} {
		changed := call
		changed.Tool = tool
		if err := s.Check(changed); !errors.Is(err, native.ErrDenied) {
			t.Errorf("accepted %s, err = %v", tool, err)
		}
	}
	s.tools[commentTool].Required = append(s.tools[commentTool].Required, "commentId")
	if err := s.Check(call); !errors.Is(err, native.ErrDenied) {
		t.Fatal("incompatible create schema", err)
	}
	delete(s.tools, commentTool)
	if err := s.Check(call); !errors.Is(err, native.ErrDenied) {
		t.Fatal("missing enrolled tool", err)
	}
}
