package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// A plugin's write waits for a person (ADR, "Phase 5 approving a plugin's
// write"). Its approval record carries a PluginRequest: a safe summary of
// what will run, written by the host when the gate first holds the call.
// The person approves it with agentflow plugin approve at a terminal
// (ApprovePluginRequest), and the plugin host then resumes the call itself.
//
// ApprovePluginRequest is the one way to approve a record without its
// bearer token. That is sound only because the person at the terminal holds
// the per-user secret the token is derived from: any process that runs as
// the user could compute the token. The command's own checks (no agent
// session, a terminal, an explicit y) are what keep that act a person's.

// PluginRequest describes a plugin's held write for the person who answers
// it: the command, its Detail (subject, task, branch) and Summary, the same
// confirmation summary the gate shows a person for that command (for notify
// the destinations and the message, for comment the ticket and the body).
// Every string is cleaned of control and bidi characters, and any value
// under a key that names a secret is redacted. A record is mode 0600.
type PluginRequest struct {
	Plugin  string          `json:"plugin"`
	Command string          `json:"command"`
	Subject string          `json:"subject,omitempty"`
	Task    string          `json:"task,omitempty"`
	Branch  string          `json:"branch,omitempty"`
	Summary json.RawMessage `json:"summary,omitempty"`
	// Omitted says why Summary is missing, for example that it was larger
	// than MaxRequestSummary. A request without a Summary cannot be approved.
	Omitted string `json:"omitted,omitempty"`
}

// MaxRequestSummary bounds a stored Summary. A larger one is not stored, so
// the request cannot be approved: never a cut summary.
const MaxRequestSummary = 256 << 10

// ErrNoRequestSummary: the record does not say in full what would run.
var ErrNoRequestSummary = errors.New("this request does not store the full summary of what it would run, so it cannot be approved: decline it with --decline, or let it expire")

// maxRequestText caps each PluginRequest field.
const maxRequestText = 256

// clean returns r with every field cut to one safe line.
func (r PluginRequest) clean() PluginRequest {
	out := PluginRequest{Plugin: safeLine(r.Plugin), Command: safeLine(r.Command), Subject: safeLine(r.Subject),
		Task: safeLine(r.Task), Branch: safeLine(r.Branch), Omitted: safeLine(r.Omitted)}
	if len(r.Summary) > 0 {
		var v any
		if err := json.Unmarshal(r.Summary, &v); err != nil {
			out.Omitted = "the summary was not valid JSON"
			return out
		}
		raw, err := json.Marshal(cleanValue("", v))
		switch {
		case err != nil:
			out.Omitted = "the summary could not be encoded"
		case len(raw) > MaxRequestSummary:
			out.Omitted = fmt.Sprintf("the summary is %d bytes, more than the %d this record keeps", len(raw), MaxRequestSummary)
		default:
			out.Summary = raw
		}
	}
	return out
}

// secretKey reports whether a JSON key names a secret.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, w := range []string{"token", "secret", "password", "passwd", "credential", "authorization", "api_key", "apikey", "private_key", "webhook", "cookie"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// cleanValue returns v with every string cleaned (safeText) and every value
// under a secret key replaced by "(redacted)".
func cleanValue(key string, v any) any {
	if key != "" && secretKey(key) {
		return "(redacted)"
	}
	switch t := v.(type) {
	case string:
		return safeText(t)
	case []any:
		for i := range t {
			t[i] = cleanValue("", t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[safeText(k)] = cleanValue(k, val)
		}
		return out
	}
	return v
}

// safeText is s without control characters other than newline and tab, and
// without bidi formatting characters. It keeps every line.
func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || bidi(r) {
			return -1
		}
		return r
	}, s)
}

func bidi(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}

// safeLine is s's first line without control or bidi formatting
// characters, at most maxRequestText bytes of valid UTF-8.
func safeLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || bidi(r) {
			return -1
		}
		return r
	}, s)
	if len(s) > maxRequestText {
		s = s[:maxRequestText]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// PluginApproval is what plugin approve shows: the record's public view and
// its request. Request is empty, apart from Plugin, when the plugin host
// did not describe the record.
type PluginApproval struct {
	OperationID string        `json:"operation_id"`
	State       string        `json:"state"`
	RequestedBy string        `json:"requested_by"`
	Action      string        `json:"action"`
	Request     PluginRequest `json:"request"`
	ExpiresAt   time.Time     `json:"expires_at"`
}

// Approvable reports why a person may not approve p, or nil. Only a record
// the plugin host described (DescribePluginRequest), for a write a plugin
// may invoke (manifest.InvocableWrites) whose gate action is that command,
// with its full Summary, can be approved. Any of them may be declined.
func (p PluginApproval) Approvable() error {
	r := p.Request
	switch {
	case r.Command == "":
		return ErrNoHostRequest
	case !slices.Contains(manifest.InvocableWrites, r.Command) || p.Action != r.Command:
		return ErrNotPluginWrite
	case len(r.Summary) == 0 && r.Omitted != "":
		return fmt.Errorf("%w (%s)", ErrNoRequestSummary, r.Omitted)
	case len(r.Summary) == 0:
		return ErrNoRequestSummary
	}
	return nil
}

// Refusals of plugin approve.
var (
	ErrNotPluginRequest = errors.New("this approval was not requested by a plugin: answer it where it was asked, or with agentflow policy approve")
	ErrNoPluginRequest  = errors.New("no approval with this ID in this repository: run plugin approve in the repository the plugin acted on")
	ErrNotPending       = errors.New("this approval is not waiting for a person")
	ErrNoHostRequest    = errors.New("the plugin host did not describe this request, so it cannot be approved: decline it with --decline, or let it expire")
	ErrNotPluginWrite   = errors.New("this request is not a write a plugin may invoke, so it cannot be approved: decline it with --decline, or let it expire")
)

// DescribePluginRequest stores req on record id, which this gate's plugin
// client must have requested. It writes only once: a later call, or a record
// another client requested, changes nothing.
func (g *Gate) DescribePluginRequest(id string, req PluginRequest) error {
	if !strings.HasPrefix(g.Client, PluginClientPrefix) {
		return ErrNotRequester
	}
	return g.locked(func() error {
		r, err := g.load(id)
		if err != nil {
			return err
		}
		if r.RequestedBy != g.Client {
			return ErrNotRequester
		}
		if r.Request != nil {
			return nil
		}
		req = req.clean()
		req.Plugin = strings.TrimPrefix(g.Client, PluginClientPrefix)
		r.Request = &req
		return g.persist(&r)
	})
}

// PluginRequestOf returns record id when a plugin requested it. Like Status
// it first refreshes the record, which may mark it expired (or cancelled,
// for an abandoned prompt); it changes nothing else.
func (g *Gate) PluginRequestOf(id string) (PluginApproval, error) {
	var r approvalRecord
	err := g.locked(func() error {
		var err error
		if r, err = g.loadPluginRecord(id); err != nil {
			return err
		}
		return g.refresh(&r)
	})
	if err != nil {
		return PluginApproval{}, err
	}
	return pluginApproval(r), nil
}

// ApprovePluginRequest approves record id, which a plugin requested and no
// one answered yet. It never applies to a record another client asked for,
// and a plugin client may not call it.
func (g *Gate) ApprovePluginRequest(id string) (PluginApproval, error) {
	return g.answerPluginRequest(id, approved)
}

// DeclinePluginRequest cancels record id under the same rules: the plugin's
// write will never run.
func (g *Gate) DeclinePluginRequest(id string) (PluginApproval, error) {
	return g.answerPluginRequest(id, cancelled)
}

func (g *Gate) answerPluginRequest(id, state string) (PluginApproval, error) {
	if strings.HasPrefix(g.Client, PluginClientPrefix) {
		return PluginApproval{}, ErrNotRequester
	}
	var r approvalRecord
	err := g.locked(func() error {
		var err error
		if r, err = g.loadPluginRecord(id); err != nil {
			return err
		}
		if err = g.refresh(&r); err != nil {
			return err
		}
		if err = stateError(r.State); err != nil {
			return err
		}
		if r.State != pending {
			return ErrNotPending
		}
		if state == approved {
			if err := pluginApproval(r).Approvable(); err != nil {
				return err
			}
		}
		if rule := g.Explain(r.Class, r.Action, r.Related, r.Forced); state == approved && rule.Level == config.Suggest {
			return ErrChanged
		}
		return g.change(&r, state)
	})
	if err != nil {
		return PluginApproval{}, err
	}
	return pluginApproval(r), nil
}

func (g *Gate) loadPluginRecord(id string) (approvalRecord, error) {
	if _, err := parseID(id); err != nil {
		return approvalRecord{}, fmt.Errorf("%q is not an approval ID: it looks like op_ and 32 hex digits", id)
	}
	r, err := g.load(id)
	if errors.Is(err, ErrToken) {
		return r, ErrNoPluginRequest
	}
	if err != nil {
		return r, err
	}
	if !strings.HasPrefix(r.RequestedBy, PluginClientPrefix) {
		return r, ErrNotPluginRequest
	}
	return r, nil
}

func pluginApproval(r approvalRecord) PluginApproval {
	out := PluginApproval{OperationID: r.OperationID, State: r.State, RequestedBy: safeLine(r.RequestedBy), Action: safeLine(r.Action),
		ExpiresAt: r.ExpiresAt.UTC()}
	if r.Request != nil {
		out.Request = r.Request.clean()
	}
	// The plugin is the one that asked, whatever a request says.
	out.Request.Plugin = safeLine(strings.TrimPrefix(r.RequestedBy, PluginClientPrefix))
	return out
}
