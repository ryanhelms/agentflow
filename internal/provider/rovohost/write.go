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
	maxToolPages     = 24
	maxResponseBytes = 8 << 20
)

type tokenSource func(context.Context) (accessToken, generation string, err error)

func NewWriter() *native.SessionWriter {
	return newWriter(rovooauth.BoundToken, rovooauth.NewTransport())
}

func newWriter(token tokenSource, transport http.RoundTripper) *native.SessionWriter {
	return native.NewSessionWriter("devflow", func(ctx context.Context, call native.Call) (native.WriteSession, error) {
		if call.Provider != "jira" || call.Server != "atlassian" || call.Tool != remoteLinkTool {
			return nil, native.ErrUnavailable
		}
		return connect(ctx, token, transport)
	})
}

type session struct {
	client     *mcp.ClientSession
	tools      map[string]*jsonschema.Schema
	token      tokenSource
	generation string
}

func connect(ctx context.Context, token tokenSource, transport http.RoundTripper) (*session, error) {
	accessToken, generation, err := token(ctx)
	if errors.Is(err, rovooauth.ErrNotEnrolled) {
		return nil, native.ErrUnavailable
	}
	if err != nil {
		return nil, enrollmentError(err)
	}
	if accessToken == "" || generation == "" {
		return nil, fmt.Errorf("%w: enroll Devflow through Atlassian OAuth", native.ErrSignIn)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "devflow", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		Logger:       slog.New(slog.DiscardHandler),
	})
	connected, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: catalogEndpoint, MaxRetries: -1, DisableStandaloneSSE: true,
		HTTPClient: &http.Client{
			Transport:     bearer{accessToken: accessToken, transport: transport},
			Timeout:       time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil)
	if err != nil {
		return nil, transportError(err)
	}
	tools, err := listTools(ctx, connected)
	if err != nil {
		_ = connected.Close()
		return nil, err
	}
	return &session{client: connected, tools: tools, token: token, generation: generation}, nil
}

func listTools(ctx context.Context, client *mcp.ClientSession) (map[string]*jsonschema.Schema, error) {
	tools := make(map[string]*jsonschema.Schema)
	seenCursors := make(map[string]bool)
	params := &mcp.ListToolsParams{}
	for range maxToolPages {
		page, err := client.ListTools(ctx, params)
		if err != nil {
			return nil, transportError(err)
		}
		for _, tool := range page.Tools {
			if !relevantTool(tool.Name) {
				continue
			}
			if _, exists := tools[tool.Name]; exists {
				return nil, errors.New("atlassian MCP returned an ambiguous tool catalog")
			}
			var schema *jsonschema.Schema
			if raw, err := json.Marshal(tool.InputSchema); err == nil {
				_ = json.Unmarshal(raw, &schema)
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

func relevantTool(name string) bool {
	switch name {
	case remoteLinkTool, "getJiraIssue", "listJiraIssueRemoteIssueLinks", "executeRead":
		return true
	default:
		return false
	}
}

func (s *session) Origin() string { return origin }

func (s *session) Check(call native.Call) error {
	if call.Provider != "jira" || call.Server != "atlassian" || !relevantTool(call.Tool) {
		return native.ErrUnavailable
	}
	if call.Tool == "executeRead" && call.Arguments["name"] != "listJiraIssueRemoteIssueLinks" {
		return native.ErrDenied
	}
	return native.ValidateInputSchema(s.tools[call.Tool], call.Arguments)
}

func (s *session) Call(ctx context.Context, call native.Call) (json.RawMessage, error) {
	_, generation, err := s.token(ctx)
	if err != nil {
		return nil, enrollmentError(err)
	}
	if s.generation != generation {
		return nil, fmt.Errorf("%w: the enrolled Atlassian connection changed", native.ErrDenied)
	}
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	out, err := s.client.CallTool(ctx, &mcp.CallToolParams{Name: call.Tool, Arguments: args})
	if err != nil {
		return nil, transportError(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	payload, err := native.Payload(raw)
	if err != nil {
		return nil, err
	}
	// Rovo's flat catalog wraps successful Jira results in a data object.
	// Keep any other envelope intact so provider errors remain visible.
	var envelope map[string]json.RawMessage
	if json.Unmarshal(payload, &envelope) == nil && len(envelope) == 1 && len(envelope["data"]) > 0 {
		return envelope["data"], nil
	}
	return raw, nil
}

func (s *session) Close() { _ = s.client.Close() }

type bearer struct {
	accessToken string
	transport   http.RoundTripper
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != catalogEndpoint || req.Host != "" && req.Host != req.URL.Host {
		return nil, native.ErrDenied
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.accessToken)
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

func enrollmentError(err error) error {
	if errors.Is(err, rovooauth.ErrLoginRequired) || errors.Is(err, rovooauth.ErrNotEnrolled) {
		return fmt.Errorf("%w: run devflow setup login --provider jira --client devflow", native.ErrSignIn)
	}
	return err
}

func transportError(err error) error {
	var status statusError
	switch {
	case errors.As(err, &status) && status == http.StatusUnauthorized:
		return fmt.Errorf("%w: run devflow setup login --provider jira --client devflow", native.ErrSignIn)
	case errors.As(err, &status) && status == http.StatusForbidden, errors.Is(err, native.ErrDenied):
		return native.ErrDenied
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return errors.New("atlassian MCP connection or tool call failed")
	}
}
