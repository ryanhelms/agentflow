// Package native routes read operations through the calling client's existing
// provider connection and then through an available vendor CLI.
package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrUnavailable = errors.New("provider MCP capability unavailable")
	ErrSignIn      = errors.New("provider sign-in required")
	ErrDenied      = errors.New("provider access denied")
)

type Call struct {
	Provider  string         `json:"provider"`
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// Host reads through one provider connection selected and authenticated by the
// client. Implementations report ErrUnavailable only before invoking a tool.
type Host interface {
	Read(context.Context, Call) (json.RawMessage, error)
}

type hostKey struct{}

func WithHost(ctx context.Context, host Host) context.Context {
	return context.WithValue(ctx, hostKey{}, host)
}

func Read[T any](ctx context.Context, call Call, decode func(json.RawMessage) (T, error), fallback func() (T, error)) (T, error) {
	host, _ := ctx.Value(hostKey{}).(Host)
	if host != nil {
		payload, err := host.Read(ctx, call)
		if err == nil {
			return decode(payload)
		}
		var zero T
		if !errors.Is(err, ErrUnavailable) {
			return zero, err
		}
	}
	return fallback()
}

// Request tells a client how to complete a read using a sibling provider MCP.
// The ID binds the supplied read result to its provider, tool, and arguments.
type Request struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Call
}

func (r *Request) Error() string {
	return "read requires the client provider connection"
}

type Reply struct {
	ID     string `json:"id"`
	Status string `json:"status" jsonschema:"ok, unavailable, sign_in_required, access_denied, or failed"`
	Result any    `json:"result,omitempty" jsonschema:"The provider MCP JSON result without any credentials."`
}

// Client delegates read-only operations to the existing host session without
// attempting to access its credentials or assuming that a sibling is connected.
type Client struct {
	Replies []Reply
	Direct  Host
}

func (c Client) Read(ctx context.Context, call Call) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	id := hex.EncodeToString(digest[:])
	for _, reply := range c.Replies {
		if reply.ID != id {
			continue
		}
		switch reply.Status {
		case "ok":
			raw, err := json.Marshal(reply.Result)
			if err != nil || reply.Result == nil || len(raw) > 1<<20 {
				return nil, errors.New("invalid provider MCP response")
			}
			return raw, nil
		case "unavailable":
			return nil, ErrUnavailable
		case "sign_in_required":
			return nil, fmt.Errorf("%s: %w; start the client's provider OAuth flow", call.Provider, ErrSignIn)
		case "access_denied":
			return nil, fmt.Errorf("%s: %w", call.Provider, ErrDenied)
		default:
			return nil, fmt.Errorf("%s: provider MCP read failed", call.Provider)
		}
	}
	if c.Direct != nil {
		raw, err := c.Direct.Read(ctx, call)
		if !errors.Is(err, ErrUnavailable) {
			return raw, err
		}
	}
	return nil, &Request{Status: "native_read_required", ID: id, Call: call}
}

// Decode accepts structured MCP output and the single JSON text result used by
// providers that return a content array.
func Decode(raw json.RawMessage, target any) error {
	payload, err := Payload(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

// Payload removes a successful MCP result envelope when one is present.
func Payload(raw json.RawMessage) (json.RawMessage, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return raw, nil
	}
	var envelope struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	switch {
	case envelope.IsError:
		return nil, errors.New("provider MCP reported a tool error")
	case len(envelope.StructuredContent) > 0:
		raw = envelope.StructuredContent
	case len(envelope.Content) == 1 && envelope.Content[0].Type == "text":
		raw = json.RawMessage(envelope.Content[0].Text)
	}
	return raw, nil
}
