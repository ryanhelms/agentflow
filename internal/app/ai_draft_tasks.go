package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/asana"
	"github.com/ryanhelms/agentflow/internal/provider/githubissues"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// The read methods of the ticket and reply drafts (ADR "Phase 8
// features"). They return raw material and refusals; internal/ai/features
// turns it into the prompt's parts. Nothing here writes, posts or stages.

// TicketDraft is what a ticket draft reads besides the caller's text.
type TicketDraft struct {
	// Template is the template task create would use for the ref, or nil
	// when none applies.
	Template *tasktemplate.Template
}

// TicketDraftContext reads the task template of ref, or the configured
// default for a zero ref, from the worktree. A provider that cannot carry
// the template gets none. No provider is contacted.
func (a *App) TicketDraftContext(_ context.Context, ref tasktemplate.Ref) (TicketDraft, error) {
	var out TicketDraft
	if a.Cfg.Providers.TaskManagement.Provider == "none" {
		return out, &aisession.Refusal{Code: "no_task_provider", Message: "This repository has no task provider, so there is no ticket to draft.",
			Fix: "Choose a task provider (task_management.provider), then draft again."}
	}
	// task create takes templates only for the providers with task refs.
	if !a.UsesTaskRefs() {
		return out, nil
	}
	t, err := a.ResolveTaskTemplate(ref)
	if err != nil || t == nil {
		return out, err
	}
	fields := fileTaskFields
	switch {
	case a.usesIssues():
		fields = githubissues.SupportedFields()
	case a.usesAsana():
		fields = asana.SupportedFields()
	}
	if err := t.CheckSupport(fields); err != nil {
		if errors.Is(err, provider.ErrUnsupported) {
			return out, nil
		}
		return out, err
	}
	out.Template = t
	return out, nil
}

// ReplyDraft is what a reply draft reads.
type ReplyDraft struct {
	// Threads are the open threads to reply to, in the order GitHub lists
	// them (oldest first). Each holds its first comment.
	Threads []gh.Thread
	// Base is the commit the branch's changes start from, or "" when it
	// cannot be resolved. The diff is the feature's to read (ai.GitDiff).
	Base string
	// Commits are the branch's commits, a short ID and subject per line.
	Commits string
}

// replyDraftCommits is how many of the branch's commits a draft reads.
const replyDraftCommits = "30"

// ReplyDraftContext reads the open review threads of the branch's PR (one
// thread, or the first limit), where the branch's changes start and its
// commits. Without a base there are neither: the threads still go.
func (a *App) ReplyDraftContext(ctx context.Context, thread string, limit int) (ReplyDraft, error) {
	var out ReplyDraft
	res, err := a.ReviewThreads(ctx)
	if err != nil {
		return out, err
	}
	threads, _ := res["threads"].([]gh.Thread)
	if res["pr"] == nil || len(threads) == 0 {
		return out, &aisession.Refusal{Code: "no_threads", Message: "This branch has no open PR with an unresolved review thread.",
			Fix: "Check agentflow review; there is nothing to reply to."}
	}
	switch {
	case thread != "":
		i := slices.IndexFunc(threads, func(t gh.Thread) bool { return t.ID == thread })
		if i < 0 {
			return out, &aisession.Refusal{Code: "unknown_thread", Message: "That is not an open review thread of this branch's PR.",
				Fix: "Take the thread ID from agentflow review."}
		}
		threads = threads[i : i+1]
	case limit > 0 && len(threads) > limit:
		threads = threads[:limit]
	}
	out.Threads = threads
	if out.Base = a.replyDraftBase(ctx); out.Base == "" {
		return out, nil
	}
	// The format names no author.
	out.Commits, err = a.Repo.Git(ctx, "log", "--no-show-signature", "--no-decorate", "--no-color", "--no-notes",
		"--format=%h %s", "-n", replyDraftCommits, out.Base+"..HEAD", "--")
	return out, err
}

// replyDraftBase is where the branch's changes start: the merge base of
// HEAD and the base pr.prepare would use, taken from its remote-tracking
// ref when there is one, else the local branch. "" when it cannot be
// resolved. Nothing is fetched.
func (a *App) replyDraftBase(ctx context.Context) string {
	d, err := a.githubRemotes(ctx)
	if err != nil {
		return ""
	}
	name := a.publicationBase(ctx, d.Target.Name)
	for _, ref := range []string{"refs/remotes/" + d.Target.Name + "/" + name, "refs/heads/" + name} {
		if _, err := a.Repo.Git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			continue
		}
		out, err := a.Repo.Git(ctx, "merge-base", "HEAD", ref)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	return ""
}
