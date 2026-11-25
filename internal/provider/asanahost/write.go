// Package asanahost dispatches a single mapped task write through a separately
// enrolled Devflow Asana V2 connection.
package asanahost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
)

const (
	protocolVersion = "2025-11-25"
	Origin          = "https://mcp.asana.com"
	Resource        = Origin + "/v2"
	Endpoint        = Resource + "/mcp"

	create     = "create_tasks"
	comment    = "add_comment"
	complete   = "update_tasks"
	getUser    = "get_user"
	getProject = "get_project"
	getTask    = "get_task"
)

// Binding contains only the nonsecret identity of an enrollment.
type Binding struct{ Generation, User, Workspace string }

// Enrollment supplies the selected grant. WithBoundToken must hold its lock
// across dispatch and reject a changed generation before invoking dispatch.
type Enrollment struct {
	BoundToken                       func(context.Context) (Binding, error)
	WithBoundToken                   func(context.Context, string, func(string) error) error
	ErrNotEnrolled, ErrLoginRequired error
	ErrGrantChanged, ErrDenied       error
}

// Scope is resolved by the App. EnableCompletion is for the transition flow
// that warns that completion affects every project containing the task.
type Scope struct {
	Project          string
	EnableCompletion bool
}

func NewWriter(enrollment Enrollment, scope Scope) *native.SessionWriter {
	return newWriter(enrollment, scope, nativeoauth.NewTransport())
}

func newWriter(e Enrollment, scope Scope, transport http.RoundTripper) *native.SessionWriter {
	return native.NewSessionWriter("devflow", func(ctx context.Context, call native.Call) (native.WriteSession, error) {
		if call.Provider != "asana" {
			return nil, native.ErrUnavailable
		}
		op, err := prepareOperation(scope, call)
		if err != nil {
			return nil, err
		}
		if e.BoundToken == nil || e.WithBoundToken == nil {
			return nil, native.ErrDenied
		}
		binding, err := e.BoundToken(ctx)
		if err != nil {
			return nil, e.failure(err, true)
		}
		if binding.Generation == "" || !gid.MatchString(binding.User) || !gid.MatchString(binding.Workspace) {
			return nil, native.ErrDenied
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "devflow", Version: "1"}, &mcp.ClientOptions{
			Capabilities: &mcp.ClientCapabilities{}, Logger: slog.New(slog.DiscardHandler),
			MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		})
		conn, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: Endpoint, MaxRetries: -1, DisableStandaloneSSE: true,
			HTTPClient: &http.Client{Transport: &boundTransport{e, binding.Generation, transport}, Timeout: time.Minute,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		}, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
		if err != nil {
			return nil, safeError(err)
		}
		s := &session{operation: op, binding: binding, conn: conn}
		if err := s.preflight(ctx); err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	})
}

type session struct {
	operation
	conn    *mcp.ClientSession
	binding Binding
	tools   map[string]toolSchema
	// First verified state stays fixed across approval. A create has no task
	// target until the mapped receipt has passed every check.
	before          *provider.Task
	createdID       string
	wroteCompletion bool
}

func (s *session) Origin() string { return Origin }
func (s *session) Close()         { _ = s.conn.Close() }

func (s *session) preflight(ctx context.Context) error {
	info := s.conn.InitializeResult()
	// Later SDK protocols cache tools/list. A write must perform fresh
	// on-wire discovery after the approval delay.
	if info.ProtocolVersion > protocolVersion || info.Capabilities == nil || info.Capabilities.Tools == nil {
		return native.ErrDenied
	}
	if err := s.loadCatalog(ctx); err != nil {
		return err
	}
	for _, check := range []struct {
		name string
		args map[string]any
		id   string
	}{
		{getUser, map[string]any{}, s.binding.User},
		{getProject, map[string]any{"project_id": s.ref.Scope}, s.ref.Scope},
	} {
		raw, err := s.invoke(ctx, check.name, check.args)
		if err != nil {
			return err
		}
		var obj struct {
			GID       string
			Workspace struct{ GID string }
		}
		if json.Unmarshal(raw, &obj) != nil || obj.GID != check.id ||
			(check.name == getProject && obj.Workspace.GID != s.binding.Workspace) {
			return native.ErrDenied
		}
	}
	if s.ref.ID != "" {
		_, task, err := s.readTask(ctx, s.ref)
		if err != nil {
			return err
		}
		if s.before != nil && !reflect.DeepEqual(*s.before, task) {
			return native.ErrDenied
		}
		s.before = &task
	}
	return nil
}

// Only the original mutation and its exact task read are public on the lease.
// Account and project lookups are internal, with arguments derived here.
func (s *session) Check(call native.Call) error {
	if call.Provider != "asana" || call.Server != "asana" {
		return native.ErrDenied
	}
	if reflect.DeepEqual(call, s.write.Call()) {
		return nil
	}
	id := s.ref.ID
	if id == "" {
		id = s.createdID
	}
	if id != "" && call.Tool == getTask && reflect.DeepEqual(call.Arguments, taskArgs(id)) {
		return nil
	}
	return native.ErrDenied
}

func (s *session) Call(ctx context.Context, call native.Call) (json.RawMessage, error) {
	if err := s.Check(call); err != nil {
		return nil, errors.Join(native.ErrWriteNotDispatched, err)
	}
	if call.Tool == getTask {
		ref := s.ref
		if ref.ID == "" {
			ref.ID = s.createdID
		}
		raw, _, err := s.readTask(ctx, ref)
		return raw, err
	}
	if err := s.preflight(ctx); err != nil {
		return nil, errors.Join(native.ErrWriteNotDispatched, err)
	}
	if call.Tool == complete && s.before.State.Terminal {
		return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied)
	}
	raw, err := s.invoke(ctx, call.Tool, s.write.Call().Arguments)
	if err != nil {
		return nil, err
	}
	result, err := s.write.Decode(raw)
	if err != nil {
		return nil, native.ErrDenied
	}
	if call.Tool != comment {
		// Batch tools can partially apply. Require explicit evidence of zero
		// failures before authorizing any read of a newly returned GID.
		var batch struct{ Failed []json.RawMessage }
		if json.Unmarshal(raw, &batch) != nil || batch.Failed == nil || len(batch.Failed) != 0 {
			return nil, native.ErrDenied
		}
		s.wroteCompletion = call.Tool == complete
		// A failed readback follows a dispatched mutation, even if the grant
		// changes before the read request itself is sent.
		if _, _, err := s.readTask(ctx, result.Ref); err != nil {
			return nil, errors.New("asana task write readback failed")
		}
	}
	if call.Tool == create {
		s.createdID = result.Ref.ID
	}
	return raw, nil
}

func (s *session) readTask(ctx context.Context, ref provider.Ref) (json.RawMessage, provider.Task, error) {
	var zero provider.Task
	read, err := nativemap.AsanaTask(ref)
	if err != nil {
		return nil, zero, native.ErrDenied
	}
	mapped := read.Call()
	var args map[string]any
	if json.Unmarshal(mapped.Arguments(), &args) != nil || mapped.Capability().Tool != getTask ||
		!reflect.DeepEqual(args, taskArgs(ref.ID)) {
		return nil, zero, native.ErrDenied
	}
	raw, err := s.invoke(ctx, getTask, args)
	if err != nil {
		return nil, zero, err
	}
	var fields struct {
		Notes       *string
		Workspace   *struct{ GID string }
		Memberships []struct{ Project struct{ GID string } }
	}
	if json.Unmarshal(raw, &fields) != nil || fields.Notes == nil ||
		(fields.Workspace != nil && fields.Workspace.GID != s.binding.Workspace) {
		return nil, zero, native.ErrDenied
	}
	task, err := read.Decode(raw)
	if err != nil {
		return nil, zero, native.ErrDenied
	}
	switch s.write.Call().Tool {
	case create:
		if len(fields.Memberships) != 1 || task.Title != s.title || *fields.Notes != s.notes || task.State.Terminal {
			return nil, zero, native.ErrDenied
		}
	case complete:
		if s.wroteCompletion && !task.State.Terminal {
			return nil, zero, native.ErrDenied
		}
	}
	return raw, task, nil
}

// A single successful JSON payload is required even without an output schema.
// Never pass MCP error text or follow-up prompts to the caller.
func (s *session) invoke(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	spec := s.tools[tool]
	if !spec.accepts(args) {
		return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied)
	}
	out, err := s.conn.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, safeError(err)
	}
	if out.IsError || out.NeedsInput() {
		return nil, native.ErrDenied
	}
	if spec.output != nil && (out.StructuredContent == nil || spec.output.Validate(out.StructuredContent) != nil) {
		return nil, native.ErrDenied
	}
	var raw []byte
	if out.StructuredContent != nil {
		raw, err = json.Marshal(out.StructuredContent)
	} else if len(out.Content) == 1 {
		if block, ok := out.Content[0].(*mcp.TextContent); ok {
			raw = []byte(block.Text)
		}
	}
	if err != nil || len(raw) > nativemap.MaxResultBytes {
		return nil, native.ErrDenied
	}
	return resultObject(raw, tool != create && tool != complete)
}
