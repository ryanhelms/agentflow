package app

import (
	"context"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/config"
)

// currentPublicationConfig picks up local edits in long-lived MCP sessions.
// Removing a previously opted-in file revokes the exception and retains its
// prior ceilings for this session.
func (a *App) currentPublicationConfig() (*config.Repo, error) {
	local, found, err := config.LoadRepoCore(a.Repo.Dir)
	if !found && err == nil && a.Cfg.Policy.AllowAutoPRPublication {
		previous := *a.Cfg
		previous.Policy.AllowAutoPRPublication = false
		return &previous, nil
	}
	return local, err
}

// reviewedPRPolicy limits the exception to settings both the checkout and the
// target's current default branch permit. Reading a local tracking ref alone
// would miss a revocation after the last fetch.
func (a *App) reviewedPRPolicy(ctx context.Context, target, base string, local *config.Repo) *config.Repo {
	cfg := *local
	cfg.Policy.AllowAutoPRPublication = false
	if !local.Policy.AllowAutoPRPublication || target == "" || base == "" {
		return &cfg
	}
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := a.Repo.Git(lookup, "ls-remote", "--symref", "--", target, "HEAD")
	if err != nil {
		return &cfg
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "ref: refs/heads/"+base+"\tHEAD" {
		return &cfg
	}
	sha, ok := strings.CutSuffix(lines[1], "\tHEAD")
	if !ok || (len(sha) != 40 && len(sha) != 64) {
		return &cfg
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return &cfg
	}
	content, err := a.Repo.Git(lookup, "--no-replace-objects", "cat-file", "blob", sha+":.agentflow/config.yml")
	if err != nil {
		return &cfg
	}
	reviewed, _, err := config.ParseRepoCore([]byte(content))
	if err != nil {
		return &cfg
	}
	cfg.Policy.MaxAutonomy.Remote = config.Min(cfg.Policy.MaxAutonomy.Remote, reviewed.Policy.MaxAutonomy.Remote)
	caps := maps.Clone(cfg.Policy.MaxAutonomy.Actions)
	for _, action := range []string{"pr_open", "pr_open_draft", "push"} {
		if level := reviewed.Policy.MaxAutonomy.Actions[action]; level != "" {
			if caps == nil {
				caps = make(map[string]config.Level)
			}
			caps[action] = config.Min(caps[action], level)
		}
	}
	cfg.Policy.MaxAutonomy.Actions = caps
	if window := reviewed.Policy.MaxApprovalWindow; window != nil && (cfg.Policy.MaxApprovalWindow == nil || *window < *cfg.Policy.MaxApprovalWindow) {
		cfg.Policy.MaxApprovalWindow = window
	}
	cfg.Policy.ProtectedBranches = slices.Concat(cfg.Policy.ProtectedBranches, reviewed.Policy.ProtectedBranches)
	cfg.Policy.AllowAutoPRPublication = reviewed.Policy.AllowAutoPRPublication
	return &cfg
}
