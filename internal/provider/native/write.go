package native

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrWriteUncertain indicates that a native write may have been dispatched.
// It cannot select another transport or automatically retry.
var ErrWriteUncertain = errors.New("native provider write outcome is uncertain")

// WriteBinding pins the server and exact operation to a prepared provider
// session. Include it in the Devflow approval summary before dispatching.
type WriteBinding struct {
	Provider   string `json:"provider"`
	Owner      string `json:"connection_owner,omitempty"`
	Server     string `json:"server"`
	Tool       string `json:"tool"`
	Origin     string `json:"origin"`
	Connection string `json:"connection"`
	Payload    string `json:"payload_sha256,omitempty"`
}

// WriteHost owns the authenticated connection and must enforce bindings on
// every call. Prepare is read-only. Write consumes a binding at dispatch.
type WriteHost interface {
	Prepare(context.Context, Call) (WriteBinding, error)
	Write(context.Context, WriteBinding, Call) (json.RawMessage, error)
	ReadBound(context.Context, WriteBinding, Call) (json.RawMessage, error)
}

type writeHostKey struct{}

func WithWriteHost(ctx context.Context, host WriteHost) context.Context {
	return context.WithValue(ctx, writeHostKey{}, host)
}

func PrepareWrite(ctx context.Context, call Call) (WriteBinding, error) {
	host, ok := ctx.Value(writeHostKey{}).(WriteHost)
	if !ok {
		return WriteBinding{}, ErrUnavailable
	}
	return host.Prepare(ctx, call)
}

func Write(ctx context.Context, binding WriteBinding, call Call) (json.RawMessage, error) {
	host, ok := ctx.Value(writeHostKey{}).(WriteHost)
	if !ok {
		return nil, ErrDenied
	}
	return host.Write(ctx, binding, call)
}

// ReadBound verifies scope or current state using the same connection as a
// pending write. Read-only client continuations never satisfy this method.
func ReadBound(ctx context.Context, binding WriteBinding, call Call) (json.RawMessage, error) {
	host, ok := ctx.Value(writeHostKey{}).(WriteHost)
	if !ok {
		return nil, ErrDenied
	}
	return host.ReadBound(ctx, binding, call)
}

// RebindWrite prepares the final payload on the same authenticated connection.
// This is used before approval when a trusted scope read supplies a revision
// or a provider-specific ID needed in the write arguments.
func RebindWrite(ctx context.Context, binding WriteBinding, call Call) (WriteBinding, error) {
	host, ok := ctx.Value(writeHostKey{}).(interface {
		Rebind(context.Context, WriteBinding, Call) (WriteBinding, error)
	})
	if !ok {
		return WriteBinding{}, ErrUnavailable
	}
	return host.Rebind(ctx, binding, call)
}

// ReadForWrite performs a trusted discovery read before preparing the final
// tool call. Callers recheck write preconditions using ReadBound.
func ReadForWrite(ctx context.Context, call Call) (json.RawMessage, error) {
	host, ok := ctx.Value(writeHostKey{}).(interface {
		ReadForWrite(context.Context, Call) (json.RawMessage, error)
	})
	if !ok {
		return nil, ErrUnavailable
	}
	return host.ReadForWrite(ctx, call)
}
