package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// ActionPolicy reports the decision for each possible route. The write itself
// resolves its actual provider, destination and mandatory review requirements.
type ActionPolicy struct {
	Action string                    `json:"action"`
	Rules  []config.AutonomyDecision `json:"rules"`
	config.AutonomyAction
	Includes []string `json:"includes,omitempty"`
}

func ExplainPolicy(user *config.User, repo *config.Repo, action string) ([]ActionPolicy, error) {
	return explainPolicy(user, repo, user, repo, action)
}

func explainPolicy(user *config.User, repo *config.Repo, publicationUser *config.User, publicationRepo *config.Repo, action string) ([]ActionPolicy, error) {
	names := config.AutonomyActions()
	if action != "" {
		if !config.ValidAutonomyAction(action) {
			return nil, fmt.Errorf("unknown approval action %q", action)
		}
		names = []string{action}
	}
	var result []ActionPolicy
	for _, name := range names {
		spec, _ := config.DescribeAutonomyAction(name)
		item := ActionPolicy{Action: name, AutonomyAction: spec}
		actionUser, actionRepo := user, repo
		if name == "pr_open" || name == "pr_open_draft" {
			item.Includes = []string{"push"}
			actionUser, actionRepo = publicationUser, publicationRepo
		}
		for _, class := range spec.Classes {
			item.Rules = append(item.Rules, config.ResolveOperationAutonomy(actionUser, actionRepo, class, name, item.Includes, spec.Mandatory)...)
		}
		result = append(result, item)
	}
	return result, nil
}

func (a *App) PolicyExplain(ctx context.Context, action string) ([]ActionPolicy, error) {
	if action != "" && action != "pr_open" && action != "pr_open_draft" {
		return ExplainPolicy(a.User, a.Cfg, action)
	}
	local, err := a.currentPublicationConfig()
	if err != nil {
		return nil, err
	}
	if !a.Cfg.Policy.AllowAutoPRPublication && !local.Policy.AllowAutoPRPublication {
		return ExplainPolicy(a.User, a.Cfg, action)
	}
	var target, base string
	if remotes, err := a.githubRemotes(ctx); err == nil {
		target = remotes.Target.Name
		base = a.publicationBase(ctx, target)
	}
	reviewed := a.reviewedPRPolicy(ctx, target, base, local)
	publicationUser, err := config.LoadUser()
	if err != nil {
		return nil, errors.New("cannot read personal approval settings")
	}
	return explainPolicy(a.User, a.Cfg, publicationUser, reviewed, action)
}

func (a *App) gateWithActions(class policy.Class, primary string, related []string, summary any, token string, force bool, commands []string, bounds ...time.Time) (*Pending, error) {
	return a.checkApproval(class, primary, related, summary, token, force, commands, false, earliestApprovalExpiry(bounds...))
}

func (a *App) prepareWithActions(class policy.Class, primary string, related []string, summary any, commands []string) (*Pending, error) {
	return a.checkApproval(class, primary, related, summary, "", false, commands, true, time.Time{})
}

func (a *App) checkApproval(class policy.Class, action string, related []string, summary any, token string, force bool, commands []string, prepareOnly bool, deadline time.Time) (*Pending, error) {
	d, err := a.Gate.CheckActionsUntil(class, action, related, summary, token, force, prepareOnly, deadline)
	if err != nil {
		return nil, err
	}
	return PendingDecision(a.Gate, d, summary, commands), nil
}

// PendingDecision preserves foreground prompt ownership and cancellation for
// operations that resolve the current policy immediately before a write.
func PendingDecision(gate *policy.Gate, d policy.Decision, summary any, commands []string) *Pending {
	if d.Outcome == policy.Proceed {
		return nil
	}
	p := &Pending{Status: string(d.Outcome), ConfirmToken: d.Token, Reason: d.Reason, Summary: summary, Approval: d.Approval, Policy: d.Policy}
	if d.Outcome == policy.NeedsConfirmation {
		p.gate = gate
	}
	if d.Outcome == policy.SuggestOnly {
		p.Commands = commands
	}
	return p
}

// OpenPrompt keeps a foreground prompt alive until the CLI applies or
// declines it. Other clients can release the claim if this process is killed.
func (p *Pending) OpenPrompt() (func(), error) {
	if p.gate == nil || p.Approval == nil || p.ConfirmToken == "" {
		return nil, fmt.Errorf("missing confirmation owner")
	}
	return p.gate.OpenPrompt(p.Approval.OperationID, p.ConfirmToken)
}

// Cancel lets the prompt owner release its request when the CLI cannot
// display a preview or the user declines it. A status response has no bearer.
func (p *Pending) Cancel() error {
	if p.gate == nil || p.Approval == nil || p.ConfirmToken == "" {
		return nil
	}
	return p.gate.Cancel(p.Approval.OperationID, p.ConfirmToken)
}

func (a *App) ApprovalStatus(id string) (policy.Approval, error) {
	return a.Gate.Status(id)
}

func (a *App) ApproveOperation(id, token string) (policy.Approval, error) {
	return a.Gate.Approve(id, token)
}
