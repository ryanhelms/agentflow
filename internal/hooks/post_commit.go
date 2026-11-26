package hooks

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

// PostCommit is the git post-commit hook (`agentflow hook post-commit`). It
// publishes commit.created for the new HEAD, with the subject parsed by the
// commit convention, and hook.ran. It prints nothing and never fails:
// git has already made the commit, and git and agents read hook output.
func PostCommit(ctx context.Context) error {
	start := time.Now()
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	created, ok := commitCreated(ctx, dir)
	outcome, reason := bus.HookPassed, ""
	if !ok {
		outcome, reason = bus.HookFailed, ReasonError
	}
	events := []notifyEvent{}
	if ok {
		events = append(events, notifyEvent{Type: bus.TypeCommitCreated, Hook: "post-commit", Payload: created})
	}
	events = append(events, hookRanEvent("post-commit", "git", start, outcome, reason))
	deliverEvents(ctx, dir, events...)
	return nil
}

// commitCreated describes HEAD of the worktree containing dir. The payload
// carries the subject only, never the body.
func commitCreated(ctx context.Context, dir string) (bus.CommitCreated, bool) {
	ctx, cancel := context.WithTimeout(ctx, NotifyBudget)
	defer cancel()
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return bus.CommitCreated{}, false
	}
	out, err := repo.Git(ctx, "log", "-1", "--format=%H%n%s")
	if err != nil {
		return bus.CommitCreated{}, false
	}
	oid, subject, _ := strings.Cut(strings.TrimRight(out, "\r\n"), "\n")
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return bus.CommitCreated{}, false
	}
	m := commit.Parse(subject)
	c := bus.CommitCreated{OID: oid, Subject: truncate(strings.TrimSpace(subject), bus.MaxPayloadText)}
	c.Branch = truncate(repo.Branch(ctx), bus.MaxPayloadText) // "" when detached
	if m.Conventional {
		c.Type, c.Breaking = m.Type, m.Breaking
		c.Scope = truncate(m.Scope, bus.MaxPayloadText)
		for _, k := range m.Keys {
			if len(c.Keys) == bus.MaxPayloadNames {
				break
			}
			c.Keys = append(c.Keys, truncate(k, bus.MaxPayloadText))
		}
	}
	return c, true
}
