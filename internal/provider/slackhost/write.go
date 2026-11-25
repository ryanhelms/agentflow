// Package slackhost dispatches mapped Slack operations over a separately
// enrolled Devflow connection. The app owns destination policy and receipts.
package slackhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
	"github.com/nscaledev/devflow/internal/provider/slackoauth"
)

const (
	origin         = slackoauth.Resource
	endpoint       = slackoauth.Endpoint
	send           = "slack_send_message"
	profile        = "slack_read_user_profile"
	channels       = "slack_list_user_channels"
	channelHistory = "slack_read_channel"
	thread         = "slack_read_thread"
)

// WithBoundToken compares the grant generation and keeps the lock during dispatch.
type Enrollment struct {
	BoundIdentity                    func(context.Context) (slackoauth.GrantIdentity, error)
	WithBoundToken                   func(context.Context, string, func(string) error) error
	ErrNotEnrolled, ErrLoginRequired error
}

func NewWriter() *native.SessionWriter {
	return newWriter(Enrollment{
		BoundIdentity: slackoauth.BoundIdentity, WithBoundToken: slackoauth.WithBoundToken,
		ErrNotEnrolled: slackoauth.ErrNotEnrolled, ErrLoginRequired: slackoauth.ErrLoginRequired,
	}, nativeoauth.NewTransport())
}

func newWriter(enrollment Enrollment, transport http.RoundTripper) *native.SessionWriter {
	return native.NewSessionWriter("devflow", func(ctx context.Context, call native.Call) (native.WriteSession, error) {
		if call.Provider != "slack" {
			return nil, native.ErrUnavailable
		}
		if call.Server != "slack" || call.Tool != send {
			return nil, native.ErrDenied
		}
		channel, _ := call.Arguments["channel_id"].(string)
		message, _ := call.Arguments["message"].(string)
		if len(call.Arguments) != 2 || !nativemap.ValidSlackSend(channel, message) {
			return nil, native.ErrDenied
		}
		if enrollment.BoundIdentity == nil || enrollment.WithBoundToken == nil {
			return nil, native.ErrDenied
		}
		grant, err := enrollment.BoundIdentity(ctx)
		if err != nil {
			if errors.Is(err, enrollment.ErrNotEnrolled) {
				return nil, native.ErrUnavailable
			}
			return nil, enrollment.failure(err)
		}
		if grant.Generation == "" || grant.Team == "" || grant.User == "" {
			return nil, native.ErrSignIn
		}
		if call.Scope != grant.Team && call.Scope != grant.Team+"/"+grant.User {
			return nil, fmt.Errorf("%w: slack enrollment differs from the configured workspace or actor", native.ErrDenied)
		}
		wire := &boundTransport{enrollment: enrollment, generation: grant.Generation, transport: transport}
		client := mcp.NewClient(&mcp.Implementation{Name: "devflow", Version: "1"}, &mcp.ClientOptions{
			Capabilities: &mcp.ClientCapabilities{}, Logger: slog.New(slog.DiscardHandler),
			MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		})
		conn, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: endpoint, MaxRetries: -1, DisableStandaloneSSE: true,
			HTTPClient: &http.Client{Transport: wire, Timeout: time.Minute,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if err != nil {
			return nil, safeError(err)
		}
		s := &session{conn: conn, channel: channel, message: message, scope: call.Scope, team: grant.Team}
		caps := conn.InitializeResult().Capabilities
		if caps == nil || caps.Tools == nil {
			s.Close()
			return nil, native.ErrDenied
		}
		s.tools, err = s.catalog(ctx)
		if err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	})
}

type toolSchema struct {
	input  *jsonschema.Schema
	output *jsonschema.Resolved
}

type session struct {
	conn             *mcp.ClientSession
	channel, message string
	scope, team      string
	tools            map[string]toolSchema
}

func (s *session) Origin() string { return origin }
func (s *session) Close()         { _ = s.conn.Close() }

func (s *session) catalog(ctx context.Context) (map[string]toolSchema, error) {
	tools := make(map[string]toolSchema)
	params := &mcp.ListToolsParams{}
	seen := map[string]bool{}
	for range 24 {
		page, err := s.conn.ListTools(ctx, params)
		if err != nil {
			return nil, safeError(err)
		}
		for _, tool := range page.Tools {
			if tool == nil {
				return nil, native.ErrDenied
			}
			switch tool.Name {
			case send, profile, channels, channelHistory, thread:
			default:
				continue
			}
			if _, exists := tools[tool.Name]; exists {
				return nil, fmt.Errorf("%w: duplicate Slack tool", native.ErrDenied)
			}
			input, err := objectSchema(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			spec := toolSchema{input: input}
			if tool.OutputSchema != nil {
				output, err := objectSchema(tool.OutputSchema)
				if err != nil {
					return nil, err
				}
				spec.output, err = output.Resolve(nil)
				if err != nil {
					return nil, native.ErrDenied
				}
			}
			tools[tool.Name] = spec
		}
		if page.NextCursor == "" {
			if tools[send].input == nil || tools[profile].input == nil || tools[channels].input == nil {
				return nil, fmt.Errorf("%w: missing Slack send or scope verification tool", native.ErrDenied)
			}
			return tools, nil
		}
		if seen[page.NextCursor] {
			break
		}
		seen[page.NextCursor] = true
		params.Cursor = page.NextCursor
	}
	return nil, fmt.Errorf("%w: incomplete Slack tool catalog", native.ErrDenied)
}

func objectSchema(value any) (*jsonschema.Schema, error) {
	raw, err := json.Marshal(value)
	var schema jsonschema.Schema
	if err != nil || json.Unmarshal(raw, &schema) != nil || schema.Type != "object" {
		return nil, fmt.Errorf("%w: incompatible Slack tool schema", native.ErrDenied)
	}
	return &schema, nil
}

func (s *session) Call(ctx context.Context, call native.Call) (json.RawMessage, error) {
	if call.Tool == send {
		// Approval may have stayed open while the provider's catalog changed.
		tools, err := s.catalog(ctx)
		if err != nil {
			return nil, errors.Join(native.ErrWriteNotDispatched, err)
		}
		// The approved workspace and actor were verified with these exact
		// read contracts. A changed scope schema needs a fresh preview.
		if !reflect.DeepEqual(tools[profile].input, s.tools[profile].input) ||
			!reflect.DeepEqual(tools[channels].input, s.tools[channels].input) {
			return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied)
		}
		s.tools = tools
	}
	if err := s.Check(call); err != nil {
		return nil, errors.Join(native.ErrWriteNotDispatched, err)
	}
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	out, err := s.conn.CallTool(ctx, &mcp.CallToolParams{Name: call.Tool, Arguments: args})
	if err != nil {
		return nil, safeError(err)
	}
	if out.IsError || out.NeedsInput() {
		return nil, native.ErrDenied
	}
	if schema := s.tools[call.Tool].output; schema != nil && (out.StructuredContent == nil || schema.Validate(out.StructuredContent) != nil) {
		return nil, fmt.Errorf("%w: invalid Slack tool output", native.ErrDenied)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, errors.New("invalid slack MCP result")
	}
	if call.Tool == send {
		payload, err := native.Payload(raw)
		if err != nil {
			return nil, err
		}
		ack, err := nativemap.DecodeSlackMessageReceipt(s.channel, payload)
		if err != nil {
			return nil, err
		}
		return json.Marshal(ack)
	}
	return raw, nil
}
