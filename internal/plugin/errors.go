package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// codeError is an error with a fixed code (bus.PluginCode*, a Refuse* code
// or a dead letter reason). Fatal codes move the plugin to failed instead
// of backoff.
type codeError struct {
	err   error
	code  string
	fatal bool
}

func (e *codeError) Error() string { return e.code + ": " + e.err.Error() }
func (e *codeError) Unwrap() error { return e.err }

func coded(err error, code string, fatal bool) error {
	if err == nil {
		err = errors.New(code)
	}
	return &codeError{err: err, code: code, fatal: fatal}
}

// Code is the fixed code of err: its own, timeout for a deadline, or
// rpc_error. Never the error's text, which may come from a plugin.
func Code(err error) string {
	var ce *codeError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ce):
		return ce.code
	case errors.Is(err, context.DeadlineExceeded):
		return bus.PluginCodeTimeout
	default:
		return ReasonRPCError
	}
}

func fatal(err error) bool {
	var ce *codeError
	return errors.As(err, &ce) && ce.fatal
}

// Dead letter reasons besides the bus.PluginCode* values.
const (
	ReasonAction   = "action"    // a declarative action failed
	ReasonRPCError = "rpc_error" // the plugin answered event.deliver with an error
)

// Refusal is a host refusal of a plugin request: a Refuse* code. The
// process tier sends it as jsonrpc CodeForbidden (CodeRateLimited for
// RefuseRateLimited) with the code as data.
type Refusal struct{ Code string }

func (r *Refusal) Error() string { return "plugin request refused: " + r.Code }

func refuse(code string) error { return &Refusal{Code: code} }

// DeniedError is a permission rule's refusal of an operation (ADR, "Phase 5
// permission rules"). Message is the rule's display text, clamped.
type DeniedError struct {
	Plugin  string
	Rule    string
	Message string
}

// DeniedCode is DeniedError's error code.
const DeniedCode = "plugin_denied"

func (e *DeniedError) Error() string {
	return fmt.Sprintf("denied by plugin %s (rule %s): %s", e.Plugin, e.Rule, e.Message)
}

// Code returns DeniedCode.
func (e *DeniedError) Code() string { return DeniedCode }

// oneLine is SafeText: plugin text cut to one safe line.
func oneLine(s string, n int) string { return SafeText(s, n) }

// SafeText makes plugin text safe to display: its first line, without
// control characters or bidirectional formatting characters (which can
// reorder what a person reads), cut to at most n bytes of valid UTF-8.
func SafeText(s string, n int) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || bidiFormat(r) {
			return -1
		}
		return r
	}, s)
	return clip(s, n)
}

// bidiFormat reports whether r is a bidirectional formatting character.
func bidiFormat(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F,
		r >= 0x202A && r <= 0x202E,
		r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
