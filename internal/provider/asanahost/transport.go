package asanahost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
)

type boundTransport struct {
	enrollment Enrollment
	generation string
	transport  http.RoundTripper
}

func (b *boundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != Endpoint || (req.Host != "" && req.Host != req.URL.Host) ||
		(req.Method != http.MethodPost && req.Method != http.MethodDelete) {
		return nil, errors.Join(native.ErrWriteNotDispatched, native.ErrDenied)
	}
	var response *http.Response
	var wireErr error
	attempted := false
	err := b.enrollment.WithBoundToken(req.Context(), b.generation, func(token string) error {
		if !nativeoauth.Opaque(token) || attempted {
			return native.ErrDenied
		}
		bound := req.Clone(req.Context())
		bound.Header.Set("Authorization", "Bearer "+token)
		bound.GetBody = nil // A POST body cannot be replayed on a new connection.
		attempted = true
		response, wireErr = b.request(bound) //nolint:bodyclose // request closes the wire body and returns a bounded in-memory copy to the SDK.
		return wireErr
	})
	if !attempted {
		return nil, errors.Join(native.ErrWriteNotDispatched, b.enrollment.failure(err, false))
	}
	if wireErr != nil {
		return nil, wireErr
	}
	if err != nil {
		_ = response.Body.Close()
		return nil, errors.New("asana MCP dispatch could not be confirmed")
	}
	return response, nil
}

func (b *boundTransport) request(req *http.Request) (*http.Response, error) {
	response, err := b.transport.RoundTrip(req)
	if err != nil {
		return nil, errors.New("asana MCP request failed")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	// Asana also returns 401 for workspace-disabled apps. Treat it as terminal.
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, native.ErrDenied
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
	default:
		return nil, errors.New("asana MCP request rejected")
	}
	// Streamable HTTP permits a JSON response or a finite POST event stream.
	// A standalone or resumed GET is never allowed on this connection.
	if req.Method == http.MethodPost && response.StatusCode == http.StatusOK {
		media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if media != "application/json" && media != "text/event-stream" {
			return nil, errors.New("invalid asana MCP response type")
		}
	}
	const maxBody = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, errors.New("invalid asana MCP response body")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.Request = nil // Keep the authenticated request private to this transport.
	return response, nil
}

func (e Enrollment) failure(err error, selecting bool) error {
	switch {
	case err == nil, errors.Is(err, e.ErrDenied), errors.Is(err, e.ErrGrantChanged):
		return native.ErrDenied
	case errors.Is(err, e.ErrLoginRequired):
		return native.ErrSignIn
	case errors.Is(err, e.ErrNotEnrolled):
		if selecting {
			return native.ErrUnavailable
		}
		return native.ErrSignIn
	default:
		return native.ErrDenied
	}
}

// SDK, keychain and HTTP errors may contain provider data. Only return owned
// error classes. Unavailability is reserved for selection before a grant exists.
func safeError(err error) error {
	var result error
	switch {
	case errors.Is(err, native.ErrSignIn):
		result = native.ErrSignIn
	case errors.Is(err, native.ErrDenied):
		result = native.ErrDenied
	case errors.Is(err, context.Canceled):
		result = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		result = context.DeadlineExceeded
	default:
		result = errors.New("asana MCP connection or tool call failed")
	}
	if errors.Is(err, native.ErrWriteNotDispatched) {
		return errors.Join(native.ErrWriteNotDispatched, result)
	}
	return result
}
