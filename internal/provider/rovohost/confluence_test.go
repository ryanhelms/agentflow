package rovohost

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
)

func flatCatalog(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("testdata/confluence-flat-schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var tools map[string]*jsonschema.Schema
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestConfluenceDesktopPublish(t *testing.T) {
	const site = "https://fixture.atlassian.net"
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	tools := flatCatalog(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "confluence-fixture"}, nil)
	version, writes, historyReads, spaceReads := 7, 0, 0, 0
	generation := "fixture-generation"
	rotateAfterPage := false
	spaceID, spaceBase, spaceURL := "100", site+"/wiki", site+"/wiki/spaces/ENG"
	pageID, pageType, pageStatus := "42", "page", "current"
	space, snapshot, message := "ENG", "fixture-snapshot", ""
	historyMessage := ""
	type pageInput struct {
		CloudID string `json:"cloudId"`
		ID      string `json:"content_id"`
		Detail  string `json:"detail"`
		Format  string `json:"content_format"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: pageTool, InputSchema: tools[pageTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in pageInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.CloudID != site || in.ID != "42" || in.Detail != "full" || in.Format != "html" {
			return nil, nil, errors.New("wrong page read")
		}
		if rotateAfterPage {
			generation = "rotated-between-bound-reads"
		}
		return nil, map[string]any{"data": map[string]any{"id": pageID, "type": pageType, "title": "Runbook", "status": pageStatus,
			"spaceId": "100", "body": map[string]string{"format": "html", "value": "<p>example</p>"}, "detail": "full",
			"metadata": map[string]any{"version": map[string]any{"number": version, "message": message}}, "snapshotToken": snapshot}}, nil
	})
	type spaceInput struct {
		CloudID string `json:"cloudId"`
		ID      string `json:"spaceIdOrKey"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: spaceTool, InputSchema: tools[spaceTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in spaceInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.CloudID != site || in.ID != "100" {
			return nil, nil, errors.New("space lookup did not use the page's numeric space ID")
		}
		spaceReads++
		return nil, map[string]any{"data": map[string]string{"id": spaceID, "key": space, "baseUrl": spaceBase, "webUrl": spaceURL}}, nil
	})
	type updateInput struct {
		CloudID string `json:"cloudId"`
		ID      string `json:"contentId"`

		Title    string `json:"title"`
		Snapshot string `json:"snapshotToken"`
		Message  string `json:"versionMessage"`
		Body     struct {
			Format string `json:"format"`
			Value  string `json:"value"`
		} `json:"body"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: updateTool, InputSchema: tools[updateTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.CloudID != site || in.ID != "42" || in.Title != "Runbook" ||
			in.Snapshot != snapshot || !strings.HasPrefix(in.Message, "devflow docs publish ") || in.Body.Format != "html" || in.Body.Value != "<p>&lt;example&gt;</p>" {
			return nil, nil, errors.New("wrong replacement")
		}
		writes++
		version++
		message, historyMessage = in.Message, in.Message
		return nil, map[string]any{"data": map[string]string{"contentId": "42", "operationPerformed": "UPDATED"}}, nil
	})
	type versionInput struct {
		CloudID string `json:"cloudId"`
		ID      string `json:"contentId"`
		Number  int    `json:"versionNumber"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: versionTool, InputSchema: tools[versionTool]}, func(_ context.Context, _ *mcp.CallToolRequest, in versionInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.CloudID != site || in.ID != "42" || in.Number != 8 {
			return nil, nil, errors.New("wrong historical read")
		}
		historyReads++
		return nil, map[string]any{"data": map[string]any{"contentType": "page", "ok": true, "version": map[string]any{"number": 8, "message": historyMessage}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if r.URL.String() != catalogEndpoint || r.Header.Get("Authorization") != "Bearer fixture-only" {
			t.Error("wrong MCP audience or bearer")
		}
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	writer := newWriter(func(_ context.Context, profile string) (string, string, error) {
		if profile != "confluence" {
			return "", "", errors.New("wrong enrollment")
		}
		return "fixture-only", generation, nil
	}, inProcessHTTP{handler})
	t.Cleanup(writer.Close)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".devflow"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte("knowledge_base: {provider: confluence, settings: {site: "+site+", publish_spaces: [ENG]}}"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := policy.NewGate(t.TempDir(), config.Autonomy{Remote: config.Confirm}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Cfg: cfg, Gate: gate}
	in := app.DocsPublishInput{Ref: provider.Ref{Instance: "confluence", Scope: "ENG", ID: "42"}, Title: "Runbook", Body: "<example>", ExpectedRevision: "7", OperationID: "fixture-update"}
	ctx := native.WithWriteHost(t.Context(), writer)
	in.Ref.Scope, space = "OTHER", "OTHER"
	if _, err := a.DocsPublish(ctx, in, ""); err == nil {
		t.Fatal("space outside configured publication list accepted")
	}
	in.Ref.Scope = "ENG"
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrNotFound) {
		t.Fatal("foreign page accepted", err)
	}
	space = "ENG"
	for _, tc := range []struct {
		field       *string
		bad         string
		spaceLookup bool
	}{
		{&pageID, "43", false}, {&pageType, "live_doc", false}, {&pageStatus, "draft", false},
		{&snapshot, "", false}, {&spaceID, "101", true}, {&spaceBase, "https://other.example/wiki", true},
		{&spaceURL, "https://other.example/wiki/spaces/ENG", true}, {&space, "", true},
	} {
		original, reads := *tc.field, spaceReads
		*tc.field = tc.bad
		if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, nativemap.ErrInvalidResult) ||
			(spaceReads > reads) != tc.spaceLookup || writes != 0 {
			t.Fatalf("invalid page or space accepted: %q, %v", tc.bad, err)
		}
		*tc.field = original
	}
	rotateAfterPage = true
	reads := spaceReads
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, native.ErrDenied) || reads != spaceReads || writes != 0 {
		t.Fatal("space lookup followed a changed grant", err)
	}
	rotateAfterPage = false
	preview, err := a.DocsPublish(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	token := preview.(*app.Pending).ConfirmToken
	snapshot = "new-snapshot"
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) || writes != 0 {
		t.Fatal("approval followed changed snapshot", err)
	}
	snapshot = "fixture-snapshot"
	generation = "new-consent"
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, native.ErrDenied) || writes != 0 {
		t.Fatal("approval followed a changed grant", err)
	}
	preview, err = a.DocsPublish(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	token = preview.(*app.Pending).ConfirmToken
	out, err := a.DocsPublish(ctx, in, token)
	if err != nil || out.(provider.WriteResult).Status != provider.Applied || writes != 1 || historyReads != 0 {
		t.Fatal("unverified update", out, err, writes, historyReads)
	}
	version++ // An unrelated edit follows the confirmed update.
	message = "later edit"
	gate.Autonomy.Remote = config.Auto
	out, err = a.DocsPublish(ctx, in, "")
	result, ok := out.(provider.WriteResult)
	if err != nil || !ok || result.Status != provider.AlreadyApplied || result.Revision != "8" || writes != 1 || historyReads != 1 {
		t.Fatal("historical replay", out, err, writes, historyReads)
	}
	historyMessage = "different operation"
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrConflict) || writes != 1 {
		t.Fatal("replayed a different version message", err)
	}
}

func TestConfluenceCatalogAndTargetRequired(t *testing.T) {
	s := &session{provider: "confluence", site: "https://fixture.atlassian.net", pageID: "42", tools: flatCatalog(t)}
	args := map[string]any{"cloudId": s.site, "contentId": s.pageID, "contentType": "page", "title": "Runbook",
		"body": map[string]any{"format": "html", "value": "<p>replacement</p>"}, "snapshotToken": "s", "versionMessage": "receipt"}
	call := native.Call{Provider: "confluence", Server: "atlassian", Tool: updateTool, Arguments: args}
	wire, err := s.wireCall(call)
	if err != nil || wire.Arguments["contentType"] != nil || args["contentType"] != "page" || len(wire.Arguments) != 6 {
		t.Fatal("canonical and flat updates", wire, err)
	}
	if _, err := s.Call(t.Context(), call); !errors.Is(err, native.ErrWriteNotDispatched) || !errors.Is(err, native.ErrDenied) {
		t.Fatal("dispatched without a verified snapshot", err)
	}
	for _, field := range []string{"title", "body", "snapshotToken", "versionMessage"} {
		prop := s.tools[updateTool].Properties[field]
		delete(s.tools[updateTool].Properties, field)
		if err := s.Check(call); !errors.Is(err, native.ErrDenied) {
			t.Fatal("undeclared update parameter", field, err)
		}
		s.tools[updateTool].Properties[field] = prop
	}
	for key, bad := range map[string]any{"cloudId": "https://other.example", "contentId": "43", "contentType": "blogpost",
		"versionMessage": "", "snapshotToken": "", "body": map[string]any{"format": "markdown", "value": "body"}, "dryRun": true} {
		call.Arguments = maps.Clone(args)
		call.Arguments[key] = bad
		if err := s.Check(call); !errors.Is(err, native.ErrDenied) {
			t.Fatal("changed canonical field accepted", key, err)
		}
	}
	call.Arguments = args
	delete(s.tools, versionTool)
	if err := s.Check(call); !errors.Is(err, native.ErrDenied) {
		t.Fatal("missing history accepted", err)
	}
}

func TestConfluenceRejectsMissingRevisionEvidence(t *testing.T) {
	s := &session{provider: "confluence", site: "https://fixture.atlassian.net", pageID: "42"}
	for _, version := range []string{`{}`, `{"number":7}`, `{"number":0,"message":""}`, `{"number":7,"message":null}`} {
		raw := json.RawMessage(`{"id":"42","type":"page","status":"current","spaceId":"100","snapshotToken":"s","metadata":{"version":` + version + `}}`)
		if _, err := s.pageWithSpace(t.Context(), raw); !errors.Is(err, nativemap.ErrInvalidResult) {
			t.Fatalf("incomplete version %s: %v", version, err)
		}
	}
	for _, raw := range []string{`{"ok":false,"contentType":"page"}`, `{"ok":true,"contentType":"blogpost"}`, `{"ok":true,"contentType":"page","id":"43"}`} {
		if _, err := s.historyResult(json.RawMessage(raw)); !errors.Is(err, nativemap.ErrInvalidResult) {
			t.Fatal("invalid historical page proof", err)
		}
	}
}
