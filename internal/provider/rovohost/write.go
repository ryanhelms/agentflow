// Package rovohost connects Devflow to a separately enrolled Atlassian Rovo
// MCP grant. It does not inspect the calling application's credential store.
package rovohost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/rovooauth"
)

const (
	origin           = "https://mcp.atlassian.com"
	catalogEndpoint  = rovooauth.Resource + "?tools=all"
	remoteLinkTool   = "createJiraIssueRemoteIssueLink"
	updateTool       = "updateConfluenceContent"
	pageTool         = "getConfluenceContent"
	spaceTool        = "getConfluenceSpace"
	versionTool      = "getConfluenceContentVersion"
	maxToolPages     = 24
	maxResponseBytes = 8 << 20
)

type tokenSource func(context.Context, string) (accessToken, generation string, err error)

func NewWriter() *native.SessionWriter {
	return newWriter(rovooauth.BoundToken, rovooauth.NewTransport())
}

func newWriter(token tokenSource, transport http.RoundTripper) *native.SessionWriter {
	return native.NewSessionWriter("devflow", func(ctx context.Context, call native.Call) (native.WriteSession, error) {
		if call.Server != "atlassian" {
			return nil, native.ErrUnavailable
		}
		switch {
		case call.Provider == "jira" && call.Tool == remoteLinkTool,
			call.Provider == "confluence" && call.Tool == updateTool:
			return connect(ctx, call, token, transport)
		}
		return nil, native.ErrUnavailable
	})
}

type session struct {
	provider         string
	site             string
	pageID           string
	verifiedSnapshot string
	client           *mcp.ClientSession
	bearer           *bearer
	tools            map[string]*jsonschema.Schema
	token            tokenSource
	generation       string
}

func connect(ctx context.Context, call native.Call, token tokenSource, transport http.RoundTripper) (*session, error) {
	accessToken, generation, err := token(ctx, call.Provider)
	if errors.Is(err, rovooauth.ErrNotEnrolled) {
		return nil, native.ErrUnavailable
	}
	if err != nil {
		return nil, enrollmentError(call.Provider, err)
	}
	if accessToken == "" || generation == "" {
		return nil, fmt.Errorf("%w: enroll Devflow through Atlassian OAuth", native.ErrSignIn)
	}
	wire := &bearer{transport: transport}
	wire.token.Store(&accessToken)
	client := mcp.NewClient(&mcp.Implementation{Name: "devflow", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		Logger:       slog.New(slog.DiscardHandler),
	})
	connected, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: catalogEndpoint, MaxRetries: -1, DisableStandaloneSSE: true,
		HTTPClient: &http.Client{
			Transport:     wire,
			Timeout:       time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil)
	if err != nil {
		return nil, transportError(call.Provider, err)
	}
	tools, err := listTools(ctx, connected, call.Provider)
	if err != nil {
		_ = connected.Close()
		return nil, err
	}
	site, _ := call.Arguments["cloudId"].(string)
	pageID, _ := call.Arguments["contentId"].(string)
	return &session{provider: call.Provider, site: site, pageID: pageID, client: connected, bearer: wire, tools: tools, token: token, generation: generation}, nil
}

func listTools(ctx context.Context, client *mcp.ClientSession, provider string) (map[string]*jsonschema.Schema, error) {
	tools := make(map[string]*jsonschema.Schema)
	seenCursors := make(map[string]bool)
	params := &mcp.ListToolsParams{}
	for range maxToolPages {
		page, err := client.ListTools(ctx, params)
		if err != nil {
			return nil, transportError(provider, err)
		}
		for _, tool := range page.Tools {
			if !relevantTool(provider, tool.Name) {
				continue
			}
			if _, exists := tools[tool.Name]; exists {
				return nil, errors.New("atlassian MCP returned an ambiguous tool catalog")
			}
			schema, err := decodeCatalogSchema(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			tools[tool.Name] = schema
		}
		if page.NextCursor == "" {
			return tools, nil
		}
		if seenCursors[page.NextCursor] {
			break
		}
		seenCursors[page.NextCursor] = true
		params.Cursor = page.NextCursor
	}
	return nil, errors.New("atlassian MCP returned an incomplete tool catalog")
}

func decodeCatalogSchema(input any) (*jsonschema.Schema, error) {
	raw, err := json.Marshal(input)
	var schema *jsonschema.Schema
	if err != nil || json.Unmarshal(raw, &schema) != nil || schema == nil {
		return nil, errors.New("atlassian MCP returned a malformed input schema")
	}
	return schema, nil
}

func relevantTool(provider, name string) bool {
	if provider == "confluence" {
		return name == updateTool || name == pageTool || name == spaceTool || name == versionTool
	}
	switch name {
	case remoteLinkTool, "getJiraIssue", "listJiraIssueRemoteIssueLinks", "executeRead":
		return true
	default:
		return false
	}
}

func (s *session) Origin() string { return origin }

func (s *session) Check(call native.Call) error {
	_, err := s.wireCall(call)
	return err
}

// Check and dispatch use the same translation. The original arguments remain
// part of the approval digest, including the canonical page-only constraint.
func (s *session) wireCall(call native.Call) (native.Call, error) {
	if call.Provider != s.provider || call.Server != "atlassian" {
		return native.Call{}, native.ErrDenied
	}
	if s.provider == "confluence" {
		return s.confluenceCall(call)
	}
	if !relevantTool(s.provider, call.Tool) || call.Tool == "executeRead" && call.Arguments["name"] != "listJiraIssueRemoteIssueLinks" {
		return native.Call{}, native.ErrDenied
	}
	return call, native.ValidateInputSchema(s.tools[call.Tool], call.Arguments)
}

func (s *session) Call(ctx context.Context, call native.Call) (json.RawMessage, error) {
	wire, err := s.wireCall(call)
	if err != nil {
		return nil, errors.Join(native.ErrWriteNotDispatched, err)
	}
	if wire.Tool == pageTool {
		s.verifiedSnapshot = ""
	}
	if wire.Tool == updateTool && wire.Arguments["snapshotToken"] != s.verifiedSnapshot {
		return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied, errors.New("snapshot was not verified on this connection"))
	}
	raw, err := s.invoke(ctx, wire.Tool, wire.Arguments)
	if err != nil || s.provider != "confluence" {
		return raw, err
	}
	switch wire.Tool {
	case pageTool:
		return s.pageWithSpace(ctx, raw)
	case versionTool:
		return s.historyResult(raw)
	default:
		return raw, nil
	}
}

func (s *session) invoke(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	accessToken, generation, err := s.token(ctx, s.provider)
	if err != nil {
		return nil, errors.Join(native.ErrWriteNotDispatched, enrollmentError(s.provider, err))
	}
	if s.generation != generation {
		return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied, errors.New("the enrolled Atlassian connection changed"))
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(native.ErrWriteNotDispatched, err)
	}
	s.bearer.token.Store(&accessToken)
	if args == nil {
		args = map[string]any{}
	}
	out, err := s.client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, transportError(s.provider, err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	payload, err := native.Payload(raw)
	if err != nil {
		return nil, err
	}
	// Rovo's flat catalog wraps successful results in a data object.
	// Keep any other envelope intact so provider errors remain visible.
	var envelope map[string]json.RawMessage
	if json.Unmarshal(payload, &envelope) == nil && len(envelope) == 1 && len(envelope["data"]) > 0 {
		return envelope["data"], nil
	}
	if s.provider == "confluence" && tool != updateTool {
		return nil, fmt.Errorf("%w: missing Rovo Confluence data envelope", native.ErrDenied)
	}
	return raw, nil
}

func (s *session) Close() { _ = s.client.Close() }

type bearer struct {
	token     atomic.Pointer[string]
	transport http.RoundTripper
}

func (b *bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != catalogEndpoint || req.Host != "" && req.Host != req.URL.Host {
		return nil, native.ErrDenied
	}
	token := b.token.Load()
	if token == nil || *token == "" {
		return nil, native.ErrSignIn
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+*token)
	resp, err := b.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		_ = resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	default:
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.LimitReader(resp.Body, maxResponseBytes), resp.Body}
		return resp, nil
	}
}

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("atlassian MCP returned HTTP %d", e) }

func enrollmentError(provider string, err error) error {
	if errors.Is(err, rovooauth.ErrLoginRequired) || errors.Is(err, rovooauth.ErrNotEnrolled) {
		return fmt.Errorf("%w: run devflow setup login --provider %s --client devflow", native.ErrSignIn, provider)
	}
	return err
}

func transportError(provider string, err error) error {
	var status statusError
	switch {
	case errors.As(err, &status) && status == http.StatusUnauthorized:
		return fmt.Errorf("%w: run devflow setup login --provider %s --client devflow", native.ErrSignIn, provider)
	case errors.As(err, &status) && status == http.StatusForbidden, errors.Is(err, native.ErrDenied):
		return native.ErrDenied
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return errors.New("atlassian MCP connection or tool call failed")
	}
}
