package codexhost

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Kind classifies a host transport error for the caller.
type Kind string

const (
	Unavailable    Kind = "unavailable"
	Authentication Kind = "authentication"
	Permission     Kind = "permission"
	Failure        Kind = "failure"
)

// Error describes a failed operation without exposing arbitrary host output.
type Error struct {
	Kind      Kind
	Operation string
	RPCCode   int
	detail    string
	cause     error
}

func (e *Error) Error() string {
	if e.RPCCode != 0 {
		return fmt.Sprintf("codex host %s: %s: %s (rpc %d)", e.Operation, e.Kind, e.detail, e.RPCCode)
	}
	return fmt.Sprintf("codex host %s: %s: %s", e.Operation, e.Kind, e.detail)
}

func (e *Error) Unwrap() error { return e.cause }

func hostError(kind Kind, operation, detail string) *Error {
	return &Error{Kind: kind, Operation: operation, detail: detail}
}

func classifyError(code int, message string) Kind {
	message = strings.ToLower(message)
	switch {
	case code == 401 || containsAny(message, "oauth app access restrictions", "unauthorized", "authentication required", "authentication failed",
		"reauthentication", "not authenticated", "not logged in", "login required", "status: 401", "401 client error"):
		return Authentication
	case code == 403 || containsAny(message, "forbidden", "permission denied", "access denied",
		"approval required", "policy denied", "status: 403", "403 client error"):
		return Permission
	case code == -32601:
		return Unavailable
	default:
		return Failure
	}
}

func containsAny(s string, matches ...string) bool {
	for _, match := range matches {
		if strings.Contains(s, match) {
			return true
		}
	}
	return false
}

func toolErrorKind(content []json.RawMessage) Kind {
	for _, item := range content {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &block) == nil && block.Type == "text" {
			if kind := classifyError(0, block.Text); kind != Failure {
				return kind
			}
		}
	}
	return Failure
}
