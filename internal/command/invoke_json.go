package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
)

// JSONCommand is a registered command that takes its input as JSON: the
// plugin host's command.invoke and declarative invoke action (ADR, "Phase 5
// command invocation"). Every Spec implements it; look one up in a Registry
// and assert it.
type JSONCommand interface {
	Command
	// InvokeJSON decodes input strictly into the Spec's In, sets resume as
	// the confirm token, then binds and runs the call through inv.
	//
	// The decoding refuses unknown fields, trailing data, anything but an
	// object, and an input that carries a confirm token
	// (ErrConfirmTokenInput): the only way to resume is resume, an approval
	// ID, which the gate honours only after a person approved it.
	InvokeJSON(ctx context.Context, inv Invoker, a *app.App, input json.RawMessage, resume string, o Origin) JSONResult
}

// JSONResult is how an InvokeJSON call ended.
type JSONResult struct {
	// OperationID is the call's operation ID (the approval's when the gate
	// issued or redeemed one). Empty when the input was refused.
	OperationID string
	// Value and Err are Run's result, unchanged.
	Value any
	Err   error
	// Outcome classifies them, as the outcome event does.
	Outcome Outcome
	// Detail is the Spec's Describe of the input: safe to store and show.
	Detail Detail
}

// Input refusals of InvokeJSON. The call never became an operation, so no
// event was published.
var (
	ErrConfirmTokenInput = errors.New("command input must not carry a confirm token")
	ErrNoResume          = errors.New("command takes no confirmation to resume")
	ErrJSONInput         = errors.New("invalid command input")
)

// InvokeJSON implements JSONCommand.
func (s Spec[In, Out]) InvokeJSON(ctx context.Context, inv Invoker, a *app.App, input json.RawMessage, resume string, o Origin) JSONResult {
	in, err := s.decodeJSON(input)
	if err != nil {
		return JSONResult{Err: err, Outcome: Outcome{Kind: OutcomeFailed, Code: CodeError}}
	}
	if resume != "" {
		if s.Token == nil {
			return JSONResult{Err: ErrNoResume, Outcome: Outcome{Kind: OutcomeFailed, Code: CodeError}}
		}
		*s.Token(&in) = resume
	}
	c := Bind(a, s, in, o, "", time.Now())
	v, err := inv.Invoke(ctx, c)
	out := c.Classify(v, err)
	return JSONResult{OperationID: out.OperationID(c), Value: v, Err: err, Outcome: out, Detail: c.Detail}
}

// decodeJSON decodes input into In strictly.
func (s Spec[In, Out]) decodeJSON(input json.RawMessage) (In, error) {
	var in In
	raw := bytes.TrimSpace(input)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return in, fmt.Errorf("%w: not a JSON object", ErrJSONInput)
	}
	for k := range keys {
		// encoding/json matches field names case-insensitively.
		if strings.EqualFold(k, "confirm_token") {
			return in, ErrConfirmTokenInput
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("%w: %s", ErrJSONInput, decodeProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return in, fmt.Errorf("%w: trailing data", ErrJSONInput)
	}
	if s.Token != nil && *s.Token(&in) != "" {
		return in, ErrConfirmTokenInput
	}
	return in, nil
}

// decodeProblem names what was wrong without echoing input values.
func decodeProblem(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return "wrong type for " + typeErr.Field
	}
	if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
		return "unknown field " + strings.TrimPrefix(msg, "json: unknown field ")
	}
	return "malformed object"
}
