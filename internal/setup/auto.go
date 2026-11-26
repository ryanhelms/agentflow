package setup

import (
	"context"
	"strings"

	"github.com/ryanhelms/agentflow/internal/gitx"
)

// Automatic init modes (~/.agentflow/config.yml init.auto). The AI-client plugins' session
// hook applies them in repos that have no .agentflow/config.yml yet.
const (
	AutoInit = "auto" // set the repo up and tell the developer what was written
	AutoAsk  = "ask"  // only suggest `agentflow init`
	AutoOff  = "off"  // say nothing
)

// AutoInitDecision returns what the session hook should do for repo: AutoOff when the
// clone opted out (git config agentflow.autoinit false, set by `agentflow remove`) or its
// GitHub owner is not in orgs, otherwise mode.
func AutoInitDecision(ctx context.Context, repo *gitx.Repo, mode string, orgs []string) string {
	if mode == AutoOff {
		return AutoOff
	}
	if v, _ := repo.Git(ctx, "config", "--get", "--type=bool", AutoInitKey); strings.TrimSpace(v) == "false" {
		return AutoOff
	}
	owner, _, _ := strings.Cut(repo.RemoteRepo(ctx), "/")
	for _, o := range orgs {
		if owner != "" && strings.EqualFold(o, owner) {
			return mode
		}
	}
	return AutoOff
}
