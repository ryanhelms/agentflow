package policy

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/config"
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

// PluginRequest is the safe summary of a plugin's held write. It never holds
// the command's input: only the command name and its Detail (subject, task
// and branch, which a Spec's Describe keeps free of message bodies and
// secrets), with control and bidi characters removed.
type PluginRequest struct {
	Plugin  string `json:"plugin"`
	Command string `json:"command"`
	Subject string `json:"subject,omitempty"`
	Task    string `json:"task,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

// maxRequestText caps each PluginRequest field.
const maxRequestText = 256

// clean returns r with every field cut to one safe line.
func (r PluginRequest) clean() PluginRequest {
	return PluginRequest{Plugin: safeLine(r.Plugin), Command: safeLine(r.Command), Subject: safeLine(r.Subject),
		Task: safeLine(r.Task), Branch: safeLine(r.Branch)}
}

// safeLine is s's first line without control or bidi formatting
// characters, at most maxRequestText bytes of valid UTF-8.
func safeLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x061C || r == 0x200E || r == 0x200F ||
			r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 {
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
// its request.
type PluginApproval struct {
	OperationID string        `json:"operation_id"`
	State       string        `json:"state"`
	RequestedBy string        `json:"requested_by"`
	Request     PluginRequest `json:"request"`
	ExpiresAt   time.Time     `json:"expires_at"`
}

// Refusals of plugin approve.
var (
	ErrNotPluginRequest = errors.New("this approval was not requested by a plugin: answer it where it was asked, or with agentflow policy approve")
	ErrNoPluginRequest  = errors.New("no approval with this ID in this repository: run plugin approve in the repository the plugin acted on")
	ErrNotPending       = errors.New("this approval is not waiting for a person")
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

// PluginRequestOf returns record id when a plugin requested it, after
// refreshing its expiry. It reads only.
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
	out := PluginApproval{OperationID: r.OperationID, State: r.State, RequestedBy: safeLine(r.RequestedBy), ExpiresAt: r.ExpiresAt.UTC()}
	if r.Request != nil {
		out.Request = r.Request.clean()
	} else {
		// A record written before requests were described: name the plugin
		// and the action only.
		out.Request = PluginRequest{Plugin: safeLine(strings.TrimPrefix(r.RequestedBy, PluginClientPrefix)), Command: safeLine(r.Action)}
	}
	return out
}
