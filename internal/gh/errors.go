package gh

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var (
	// ErrCLIUnavailable means the configured GitHub CLI executable is missing.
	ErrCLIUnavailable = errors.New("github cli executable unavailable")
	// ErrAuthentication means GitHub requires a valid login.
	ErrAuthentication = errors.New("github authentication required")
	// ErrPermission means GitHub refused the operation under the current login.
	ErrPermission = errors.New("github permission denied")
	// ErrRateLimited means GitHub applied a primary or secondary rate limit.
	ErrRateLimited = errors.New("github rate limit exceeded")
	// ErrWriteOutcomeUnknown means a CLI request may have applied a write.
	ErrWriteOutcomeUnknown = errors.New("github write outcome unknown")
)

// APIError is a GitHub HTTP failure. The response body is limited to 400 bytes.
type APIError struct {
	Method string
	Path   string
	Status int
	Header http.Header
	Body   string

	outcomeUnknown bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAuthentication:
		return e.Status == http.StatusUnauthorized
	case ErrPermission:
		return e.Status == http.StatusForbidden && !e.rateLimited()
	case ErrRateLimited:
		return e.rateLimited()
	case ErrWriteOutcomeUnknown:
		return e.outcomeUnknown
	default:
		return false
	}
}

func (e *APIError) rateLimited() bool {
	return e.Status == http.StatusTooManyRequests || (e.Status == http.StatusForbidden &&
		(e.Header.Get("X-RateLimit-Remaining") == "0" || e.Header.Get("Retry-After") != "" ||
			strings.Contains(strings.ToLower(e.Body), "rate limit")))
}

func decodeResponse(method, path string, status int, headers http.Header, data []byte, out any) error {
	if status == http.StatusNotModified {
		return ErrNotModified
	}
	if status >= 300 {
		return &APIError{Method: method, Path: path, Status: status, Header: headers, Body: string(data[:min(400, len(data))])}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func mayWrite(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func cliOutcomeError(method string, err error) error {
	if err != nil && mayWrite(method) {
		return fmt.Errorf("%w: %w", ErrWriteOutcomeUnknown, err)
	}
	return err
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors"`
}

type graphQLError struct {
	Message    string `json:"message"`
	Type       string `json:"type"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

func (e *graphQLError) Error() string {
	return "github graphql: " + e.Message
}

func (e *graphQLError) Is(target error) bool {
	code := e.Type
	if code == "" {
		code = e.Extensions.Code
	}
	switch code {
	case "UNAUTHENTICATED", "UNAUTHORIZED":
		return target == ErrAuthentication
	case "FORBIDDEN", "INSUFFICIENT_SCOPES":
		return target == ErrPermission
	case "RATE_LIMITED":
		return target == ErrRateLimited
	default:
		return false
	}
}
