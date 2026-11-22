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
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/rovooauth"
)

const (
	origin           = "https://mcp.atlassian.com"
	catalogEndpoint  = rovooauth.Resource + "?tools=all"
	commentTool      = "addOrEditJiraIssueComment"
	issueTool        = "getJiraIssue"
	accountTool      = "getJiraCurrentUser"
	transitionsTool  = "listJiraIssueTransitions"
	moveTool         = "transitionJiraIssue"
	assignTool       = "editJiraIssue"
	createTool       = "createJiraIssue"
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
		case call.Provider == "jira" && (call.Tool == remoteLinkTool || call.Tool == commentTool || call.Tool == createTool || jiraStateTool(call.Tool)),
			call.Provider == "confluence" && call.Tool == updateTool:
			return connect(ctx, call, token, transport)
		}
		return nil, native.ErrUnavailable
	})
}

type session struct {
	provider         string
	site             string
	writeTool        string
	issueKey         string
	projectKey       string
	stateTarget      string // Approved account or transition ID.
	pageID           string
	verifiedSnapshot string
	client           *mcp.ClientSession
	bearer           *bearer
	tools            map[string]*jsonschema.Schema
	token            tokenSource
	generation       string
	verifiedAccount  string
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
	tools, err := listTools(ctx, connected, call)
	if err != nil {
		_ = connected.Close()
		return nil, err
	}
	site, _ := call.Arguments["cloudId"].(string)
	pageID, _ := call.Arguments["contentId"].(string)
	issueKey, _ := call.Arguments["issueIdOrKey"].(string)
	projectKey, _ := call.Arguments["projectKey"].(string)
	target, _ := call.Arguments["assignee"].(string)
	if call.Tool == moveTool {
		target, _ = call.Arguments["transitionId"].(string)
	}
	return &session{
		stateTarget: target,
		provider:    call.Provider, site: site, writeTool: call.Tool, issueKey: issueKey, projectKey: projectKey, pageID: pageID,
		client: connected, bearer: wire, tools: tools, token: token, generation: generation,
	}, nil
}

func listTools(ctx context.Context, client *mcp.ClientSession, call native.Call) (map[string]*jsonschema.Schema, error) {
	tools := make(map[string]*jsonschema.Schema)
	seenCursors := make(map[string]bool)
	params := &mcp.ListToolsParams{}
	for range maxToolPages {
		page, err := client.ListTools(ctx, params)
		if err != nil {
			return nil, transportError(call.Provider, err)
		}
		for _, tool := range page.Tools {
			if !relevantTool(call.Tool, tool.Name) {
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

func relevantTool(writeTool, name string) bool {
	switch writeTool {
	case issueTool, accountTool, transitionsTool:
		return name == writeTool
	case assignTool:
		return name == assignTool || name == issueTool || name == accountTool
	case moveTool:
		return name == moveTool || name == issueTool || name == transitionsTool || name == accountTool
	case commentTool:
		return name == commentTool || name == "getJiraIssue"
	case createTool:
		return name == createTool || name == nativemap.JiraProjectIssueTypesTool
	case updateTool:
		return name == updateTool || name == pageTool || name == spaceTool || name == versionTool
	case remoteLinkTool:
		return name == remoteLinkTool || name == "getJiraIssue" || name == "listJiraIssueRemoteIssueLinks" || name == "executeRead"
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
	if !relevantTool(s.writeTool, call.Tool) {
		return native.Call{}, native.ErrDenied
	}
	if jiraStateTool(s.writeTool) {
		return s.jiraStateCall(call)
	}
	if s.writeTool == createTool {
		if err := s.checkCreateCall(call); err != nil {
			return native.Call{}, err
		}
		return call, s.checkSchema(call.Tool, call.Arguments)
	}
	if s.writeTool == commentTool {
		if s.site == "" || s.issueKey == "" ||
			call.Arguments["cloudId"] != s.site || call.Arguments["issueIdOrKey"] != s.issueKey {
			return native.Call{}, native.ErrDenied
		}
		if call.Tool == commentTool {
			body, ok := call.Arguments["commentBody"].(string)
			if len(call.Arguments) != 4 || !ok || body == "" || call.Arguments["contentFormat"] != "html" {
				return native.Call{}, native.ErrDenied
			}
		}
		return call, s.checkSchema(call.Tool, call.Arguments)
	}
	if call.Tool == "executeRead" && call.Arguments["name"] != "listJiraIssueRemoteIssueLinks" {
		return native.Call{}, native.ErrDenied
	}
	return call, native.ValidateInputSchema(s.tools[call.Tool], call.Arguments)
}

func (s *session) checkCreateCall(call native.Call) error {
	if s.site == "" || s.projectKey == "" || call.Arguments["cloudId"] != s.site {
		return native.ErrDenied
	}
	if call.Tool == nativemap.JiraProjectIssueTypesTool {
		if len(call.Arguments) != 3 || call.Arguments["projectIdOrKey"] != s.projectKey || call.Arguments["maxResults"] != 200 {
			return native.ErrDenied
		}
		return nil
	}
	if call.Arguments["projectKey"] != s.projectKey {
		return native.ErrDenied
	}
	_, hasDescription := call.Arguments["description"]
	format, hasFormat := call.Arguments["contentFormat"]
	if hasDescription != hasFormat || hasFormat && format != "html" {
		return native.ErrDenied
	}
	for arg := range call.Arguments {
		if !slices.Contains([]string{"cloudId", "projectKey", "summary", "issueType", "labels", "description", "contentFormat"}, arg) {
			return native.ErrDenied
		}
	}
	return nil
}

func (s *session) checkSchema(name string, args map[string]any) error {
	schema := s.tools[name]
	for field := range args {
		// An open schema can accept and silently ignore unknown safety fields.
		if schema == nil || schema.Properties[field] == nil {
			return fmt.Errorf("%w: Rovo did not advertise %s.%s", native.ErrDenied, name, field)
		}
	}
	if err := native.ValidateInputSchema(schema, args); err != nil {
		return fmt.Errorf("%w: incompatible Rovo schema for %s", native.ErrDenied, name)
	}
	return nil
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
	if wire.Tool == assignTool && s.verifiedAccount != s.stateTarget {
		return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied, errors.New("self-assignment requires the current account on this connection"))
	}
	raw, err := s.invoke(ctx, wire.Tool, wire.Arguments)
	if wire.Tool == accountTool {
		s.verifiedAccount = ""
		if err == nil {
			s.verifiedAccount, err = nativemap.DecodeJiraCurrentUser(raw)
		}
	}
	if err == nil && jiraStateTool(s.writeTool) && wire.Tool == issueTool {
		err = s.checkStateIssue(raw)
	}
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
		if jiraStateTool(s.writeTool) {
			if err := s.checkStateResult(tool, envelope["data"]); err != nil {
				return nil, err
			}
		}
		return envelope["data"], nil
	}
	if jiraStateTool(s.writeTool) {
		return nil, fmt.Errorf("%w: missing Rovo Jira state data envelope", native.ErrDenied)
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
