// Package nativemap maps bounded agentflow operations to native provider tool
// arguments and projects successful JSON results into agentflow service objects.
package nativemap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nscaledev/agentflow/internal/provider"
)

type Kind string
type Operation string

const (
	Jira       Kind = "jira"
	Confluence Kind = "confluence"
	Asana      Kind = "asana"
	Slack      Kind = "slack"
	GitHub     Kind = "github"

	GetTask         Operation = "task.get"
	GetDocument     Operation = "document.get"
	ReadSlackThread Operation = "slack.thread.read"
	DeliverToSlack  Operation = "notification.deliver"
)

// Capability describes one mapped operation. Callers must verify the exact
// tool and inputs against the catalog of the selected provider session.
type Capability struct {
	Kind      Kind
	Operation Operation
	Tool      string
	Write     bool
}

var capabilities = [...]Capability{
	{Kind: Jira, Operation: GetTask, Tool: "getJiraIssue"},
	{Kind: Confluence, Operation: GetDocument, Tool: "getConfluenceContent"},
	{Kind: Asana, Operation: GetTask, Tool: "get_task"},
	{Kind: Slack, Operation: ReadSlackThread, Tool: "slack_read_thread"},
	{Kind: Slack, Operation: DeliverToSlack, Tool: "slack_send_message", Write: true},
	{Kind: GitHub, Operation: GetTask, Tool: "issue_read"},
}

// CapabilityFor reports applicability per operation. A catalog entry alone
// cannot grant access or establish that the session has a compatible schema.
func CapabilityFor(kind Kind, operation Operation) (Capability, error) {
	for _, capability := range capabilities {
		if capability.Kind == kind && capability.Operation == operation {
			return capability, nil
		}
	}
	return Capability{}, provider.Unsupported(string(kind), string(operation))
}

// Call carries a mapped tool name, provider instance and exact JSON arguments.
type Call struct {
	capability Capability
	instance   string
	args       json.RawMessage
}

func (c Call) Capability() Capability     { return c.capability }
func (c Call) Instance() string           { return c.instance }
func (c Call) Arguments() json.RawMessage { return bytes.Clone(c.args) }

func newCall(kind Kind, op Operation, instance string, args any) (Call, error) {
	if !validName(instance) {
		return Call{}, errors.New("native call requires a valid configured instance")
	}
	capability, err := CapabilityFor(kind, op)
	if err != nil {
		return Call{}, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return Call{}, fmt.Errorf("encode native tool arguments: %w", err)
	}
	return Call{capability: capability, instance: instance, args: raw}, nil
}

// Read binds a call and a typed decoder to the same requested reference.
type Read[T any] struct {
	call   Call
	decode func(json.RawMessage) (T, error)
}

func (r Read[T]) Call() Call { return r.call }

// Decode accepts structured JSON or the JSON bytes extracted from one
// successful text block. The caller handles tool errors and MCP envelopes.
func (r Read[T]) Decode(result json.RawMessage) (T, error) {
	if r.decode == nil {
		var zero T
		return zero, errors.New("native read was not prepared")
	}
	return r.decode(result)
}

// DecodeText handles one successful MCP text block. For Slack's formatted
// thread presentation, plain text travels as a JSON string.
func (r Read[T]) DecodeText(value string) (T, error) {
	if len(value) > MaxResultBytes {
		var zero T
		return zero, fmt.Errorf("%w: result exceeds byte limit", ErrInvalidResult)
	}
	raw := json.RawMessage(value)
	trimmed := strings.TrimSpace(value)
	if r.call.capability.Operation == ReadSlackThread && trimmed != "" &&
		!strings.ContainsRune("{[\"", rune(trimmed[0])) && !json.Valid(raw) {
		// Marshal a bounded string, which cannot fail.
		raw, _ = json.Marshal(value)
	}
	return r.Decode(raw)
}

func newRead[T any](kind Kind, op Operation, ref provider.Ref, args any, decode func(json.RawMessage) (T, error)) (Read[T], error) {
	call, err := newCall(kind, op, ref.Instance, args)
	if err != nil {
		return Read[T]{}, err
	}
	return Read[T]{call: call, decode: decode}, nil
}

const MaxResultBytes = 1 << 20

// ErrInvalidResult identifies missing identity or a response outside a mapped
// projection. Error messages omit provider-supplied data.
var ErrInvalidResult = errors.New("invalid native provider result")

func decodeObject(raw json.RawMessage, out any) error {
	if len(raw) > MaxResultBytes {
		return fmt.Errorf("%w: result exceeds byte limit", ErrInvalidResult)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !utf8.Valid(raw) || json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("%w: expected a compatible JSON object", ErrInvalidResult)
	}
	return nil
}

func httpsURL(value string) (*url.URL, bool) {
	u, err := url.Parse(value)
	return u, err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func httpsOrigin(value string) (string, error) {
	if len(value) > 2048 {
		return "", errors.New("native provider site exceeds the origin length limit")
	}
	u, ok := httpsURL(value)
	if !ok || (u.Path != "" && u.Path != "/") || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") {
		return "", errors.New("native provider site requires an https origin")
	}
	return "https://" + u.Host, nil
}

func validName(s string) bool {
	return len(s) <= 512 && utf8.ValidString(s) && strings.TrimSpace(s) == s && s != "" &&
		!strings.ContainsFunc(s, unicode.IsControl)
}
