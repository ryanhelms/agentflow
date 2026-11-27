package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ryanhelms/agentflow/internal/policy"
)

// PluginApproveAction is the autonomy action of plugin approve. It always
// asks (RequireConfirmation), whatever the autonomy level.
const PluginApproveAction = "plugin.approve"

// PluginApproveSummary is what plugin approve shows before it asks: the
// plugin, the command it asked to run, the command's safe description and
// when the request expires. It never holds the command's input.
type PluginApproveSummary struct {
	OperationID string    `json:"operation_id"`
	Decline     bool      `json:"decline,omitempty"`
	Plugin      string    `json:"plugin"`
	Command     string    `json:"command"`
	Subject     string    `json:"subject,omitempty"`
	Task        string    `json:"task,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	// Request is the confirmation summary the command's gate showed when
	// the plugin asked, in full: for notify the destinations and message,
	// for comment the ticket and body.
	Request json.RawMessage `json:"request"`
}

// PluginApproved is the result of plugin approve.
type PluginApproved struct {
	OperationID string    `json:"operation_id"`
	Plugin      string    `json:"plugin"`
	Command     string    `json:"command"`
	State       string    `json:"state"`
	ExpiresAt   time.Time `json:"expires_at"`
	Next        string    `json:"next"`
}

// PluginApprove approves, or with decline cancels, a write that plugin
// requested and that waits for a person (ADR, "Phase 5 approving a plugin's
// write"). It refuses any other approval. Without token it returns the
// needs_confirmation Pending with a PluginApproveSummary; the console's
// plugin host then runs an approved write by itself.
func (a *App) PluginApprove(_ context.Context, id, token string, decline bool) (any, error) {
	if a.Gate == nil {
		return nil, errors.New("plugin approve needs the approval journal: run it in the repository the plugin acted on")
	}
	req, err := a.Gate.PluginRequestOf(id)
	if err != nil {
		return nil, err
	}
	if req.State != "pending" {
		return nil, fmt.Errorf("%w: it is %s", policy.ErrNotPending, req.State)
	}
	verb := "approve"
	if decline {
		verb = "decline"
	}
	if !decline {
		if err := req.Approvable(); err != nil {
			return nil, err
		}
	}
	summary := PluginApproveSummary{OperationID: req.OperationID, Decline: decline, Plugin: req.Request.Plugin, Command: req.Request.Command,
		Subject: req.Request.Subject, Task: req.Request.Task, Branch: req.Request.Branch, ExpiresAt: req.ExpiresAt, Request: req.Request.Summary}
	cmd := "agentflow plugin approve " + shellQuote(id)
	if decline {
		cmd += " --decline"
	}
	pending, err := a.GateOperation(OperationGate{Class: policy.Local, Action: PluginApproveAction, Summary: summary, Token: token,
		RequireConfirmation: true, Commands: []string{cmd}, Bounds: []time.Time{req.ExpiresAt}})
	if err != nil {
		return nil, err
	}
	if pending != nil {
		return pending, nil
	}
	answer := a.Gate.ApprovePluginRequest
	next := "The console runs it once, now, and its result appears on the timeline. If the console restarted since the plugin asked, nothing runs."
	if decline {
		answer = a.Gate.DeclinePluginRequest
		next = "The write will not run."
	}
	ap, err := answer(id)
	if err != nil {
		return nil, fmt.Errorf("could not %s %s: %w", verb, id, err)
	}
	return PluginApproved{OperationID: ap.OperationID, Plugin: ap.Request.Plugin, Command: ap.Request.Command, State: ap.State,
		ExpiresAt: ap.ExpiresAt, Next: next}, nil
}
