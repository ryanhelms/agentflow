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
	// Hunks are the branch's changes to the threads' files, one entry per
	// file with changes.
	Hunks []ReplyHunks
	// Commits are the branch's commits, a short ID and subject per line.
	Commits string
}

// ReplyHunks is one file's section of the branch diff.
type ReplyHunks struct{ Path, Diff string }

// replyDraftCommits is how many of the branch's commits a draft reads.
const replyDraftCommits = "30"

// ReplyDraftContext reads the open review threads of the branch's PR (one
// thread, or the first limit), and the branch's changes to their files.
// Without a base there are no changes to read: the threads still go.
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
	base := a.replyDraftBase(ctx)
	if base == "" {
		return out, nil
	}
	// One diff of the whole branch: a reviewer's path never reaches git as a
	// pathspec. The a/ b/ prefixes and unquoted names are what the draft's
	// secret-file filter reads, and a wide diff.context would send whole
	// files, so the person's git settings decide neither.
	diff, err := a.Repo.Git(ctx, "-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "--no-textconv",
		"--no-renames", "--submodule=short", "--unified=3", "--inter-hunk-context=0",
		"--src-prefix=a/", "--dst-prefix=b/", base, "HEAD", "--")
	if err != nil {
		return out, err
	}
	out.Hunks = replyHunks(diff, threads)
	// The format names no author.
	out.Commits, err = a.Repo.Git(ctx, "log", "--no-show-signature", "--no-decorate", "--no-color", "--no-notes",
		"--format=%h %s", "-n", replyDraftCommits, base+"..HEAD", "--")
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

// replyHunks picks from the branch diff the section of each thread's file,
// matching the exact unquoted header. A file whose name git quotes has no
// section here, so its changes are not sent.
func replyHunks(diff string, threads []gh.Thread) []ReplyHunks {
	byHeader := map[string]string{}
	for _, s := range strings.Split("\n"+diff, "\ndiff --git ")[1:] {
		header, _, _ := strings.Cut(s, "\n")
		byHeader[header] = "diff --git " + strings.TrimSuffix(s, "\n") + "\n"
	}
	var out []ReplyHunks
	for _, t := range threads {
		header := "a/" + t.Path + " b/" + t.Path
		if s, ok := byHeader[header]; ok && t.Path != "" {
			out = append(out, ReplyHunks{Path: t.Path, Diff: s})
			delete(byHeader, header) // one entry per file
		}
	}
	return out
}
