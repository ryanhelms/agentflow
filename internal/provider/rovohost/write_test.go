package rovohost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/provider/native"
)

type linkInput struct {
	CloudID string `json:"cloudId"`
	Key     string `json:"issueIdOrKey"`
	URL     string `json:"url"`
	Title   string `json:"title"`
}

type issueInput struct {
	CloudID string `json:"cloudId"`
	Key     string `json:"issueIdOrKey"`
}

func TestRovoWriterKeepsTheApprovedOAuthGrant(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "rovo-fixture"}, nil)
	var writes atomic.Int32
	mcp.AddTool(server, &mcp.Tool{Name: remoteLinkTool}, func(_ context.Context, _ *mcp.CallToolRequest, input linkInput) (*mcp.CallToolResult, map[string]bool, error) {
		if input.Key != "DEV-13" || input.URL != "https://docs.example.org/check" {
			t.Error("unexpected Rovo target")
		}
		writes.Add(1)
		return nil, map[string]bool{"success": true}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "getJiraIssue"}, func(_ context.Context, _ *mcp.CallToolRequest, input issueInput) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"data": map[string]string{"key": input.Key}}, nil
	})

	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		if req.Header.Get("Authorization") != "Bearer local-fixture-only" || req.URL.Query().Get("tools") != "all" {
			t.Error("missing scoped fixture authentication")
		}
		return server
	}, nil))
	defer endpoint.Close()
	host, err := url.Parse(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	generation := "fixture-generation"
	writer := newWriter(func(context.Context) (string, string, error) {
		return "local-fixture-only", generation, nil
	}, rewriteTo{host})
	defer writer.Close()

	call := native.Call{Provider: "jira", Server: "atlassian", Tool: remoteLinkTool, Arguments: map[string]any{
		"cloudId": "https://fixture.atlassian.net", "issueIdOrKey": "DEV-13", "url": "https://docs.example.org/check", "title": "Fixture",
	}}
	binding, err := writer.Prepare(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	read := native.Call{Provider: "jira", Server: "atlassian", Tool: "getJiraIssue", Arguments: map[string]any{
		"cloudId": "https://fixture.atlassian.net", "issueIdOrKey": "DEV-13",
	}}
	raw, err := writer.ReadBound(t.Context(), binding, read)
	var issue map[string]string
	if err != nil || json.Unmarshal(raw, &issue) != nil || issue["key"] != "DEV-13" || writes.Load() != 0 {
		t.Fatalf("preflight: %v, writes = %d", err, writes.Load())
	}
	generation = "changed-grant"
	if _, err := writer.Write(t.Context(), binding, call); !errors.Is(err, native.ErrDenied) || writes.Load() != 0 {
		t.Fatalf("account changed: %v, writes = %d", err, writes.Load())
	}
	binding, err = writer.Prepare(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(t.Context(), binding, call); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ReadBound(t.Context(), binding, read); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(t.Context(), binding, call); !errors.Is(err, native.ErrDenied) || writes.Load() != 1 {
		t.Fatalf("replay: %v, writes = %d", err, writes.Load())
	}
}

type rewriteTo struct{ endpoint *url.URL }

func (r rewriteTo) RoundTrip(req *http.Request) (*http.Response, error) {
	dest := *req.URL
	dest.Scheme, dest.Host = r.endpoint.Scheme, r.endpoint.Host
	req = req.Clone(req.Context())
	req.URL, req.Host = &dest, dest.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestRovoBearerNeverLeavesTheExactCatalogEndpoint(t *testing.T) {
	b := bearer{accessToken: "local-fixture-only"}
	for _, url := range []string{
		"https://other.example/v2/mcp?tools=all",
		origin + "/v2/mcp",
		origin + "/v2/mcp?tools=all&other=1",
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := b.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, native.ErrDenied) {
			t.Fatalf("%s: %v", url, err)
		}
	}
}
