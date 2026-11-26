package codexhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

type writeSession struct {
	host   *host
	thread string
	server string
	target *serverStatus
	cancel context.CancelFunc
}

func NewWriter(dir string) *native.SessionWriter {
	return native.NewSessionWriter("codex", func(ctx context.Context, call native.Call) (native.WriteSession, error) {
		return openWriteSession(ctx, dir, call)
	})
}

func openWriteSession(ctx context.Context, dir string, call native.Call) (native.WriteSession, error) {
	if os.Getenv(activeEnvKey) != "" || strings.EqualFold(call.Server, "agentflow") {
		return nil, native.ErrDenied
	}
	ctx, cancel := context.WithCancel(ctx)
	h, err := startHost(ctx, dir)
	if err != nil {
		cancel()
		return nil, writeTransportError(call.Provider, err)
	}
	session := &writeSession{host: h, server: call.Server, cancel: cancel}
	ready := false
	defer func() {
		if !ready {
			session.Close()
		}
	}()
	session.thread, err = h.startThread(dir)
	if err != nil {
		return nil, writeTransportError(call.Provider, err)
	}
	session.target, err = h.findTool(session.thread, call.Server, call.Tool)
	// A signed-out server can advertise an empty catalog.
	if call.Provider == "jira" && call.Tool == "createJiraIssue" && session.target != nil &&
		session.target.RuntimeStatus != "disabled" && session.target.AuthStatus == "notLoggedIn" {
		return nil, native.ErrSignIn
	}
	if err != nil {
		return nil, writeTransportError(call.Provider, err)
	}
	ready = true
	return session, nil
}

func (s *writeSession) Origin() string { return s.target.HTTPOrigin }

func (s *writeSession) Check(call native.Call) error {
	switch s.target.AuthStatus {
	case "oAuth":
	case "notLoggedIn":
		return fmt.Errorf("%s: %w; start the MCP client's OAuth flow", call.Provider, native.ErrSignIn)
	default:
		return fmt.Errorf("%w: the host did not verify an OAuth session", native.ErrUnavailable)
	}
	if err := s.target.checkTool(call.Tool); err != nil {
		return writeTransportError(call.Provider, err)
	}
	var spec struct {
		InputSchema *jsonschema.Schema `json:"inputSchema"`
	}
	if err := json.Unmarshal(s.target.Tools[call.Tool], &spec); err != nil {
		return fmt.Errorf("%w: the provider tool supplied an invalid input schema", native.ErrDenied)
	}
	return native.ValidateInputSchema(spec.InputSchema, call.Arguments)
}

func (s *writeSession) Call(ctx context.Context, call native.Call) (json.RawMessage, error) {
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	raw, err := s.host.callTool(s.thread, s.server, call.Tool, call.Arguments, s.target)
	return raw, writeTransportError(call.Provider, err)
}

func (s *writeSession) Close() {
	s.host.close()
	s.cancel()
}

func writeTransportError(provider string, err error) error {
	var hostErr *Error
	if !errors.As(err, &hostErr) {
		return err
	}
	switch hostErr.Kind {
	case Authentication:
		return fmt.Errorf("%s: %w; start the MCP client's OAuth flow", provider, native.ErrSignIn)
	case Permission:
		return fmt.Errorf("%s: %w", provider, native.ErrDenied)
	case Unavailable:
		return native.ErrUnavailable
	default:
		return err
	}
}
