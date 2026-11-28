package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/deliver"
)

// --ai on task create and review reply (ADR "Phase 8 commands"): for a
// person at a terminal, ai.draft and then the command's own path with the
// drafted text. Nothing is applied without that person's yes, and no flag
// answers for them. Names carry a "tasks" prefix so other lanes' files in
// this package do not collide with them.

var (
	// tasksDraftInvoker runs ai.draft as the command line runs it: its
	// events, audit line and registered stages. The command's runner has
	// an Invoker only for its own operation.
	tasksDraftInvoker = func() command.Invoker {
		return command.NewInvoker(command.Options{Emitters: deliver.Source(deliver.Options{}), Auditor: command.AuditLog{}})
	}
	// tasksDraftAsk reads the answer to a question (tests replace it).
	tasksDraftAsk = askLine
	// tasksCreate is task create's own path (tests replace it).
	tasksCreate = runTaskCreate
)

func tasksYes(prompt string) bool {
	return strings.HasPrefix(strings.ToLower(tasksDraftAsk(prompt)), "y")
}

// tasksForPerson refuses --ai to anyone but a person at a terminal, before
// anything is read or sent. It returns the person's context.
func tasksForPerson(ctx context.Context, instead string) (context.Context, error) {
	ctx = cliAIRequester(ctx)
	if isPerson(ctx) {
		return ctx, nil
	}
	return ctx, aiRefused(&aisession.Refusal{Code: ai.CodeNeedsPerson, Message: "--ai is for you at your own terminal.", Fix: instead})
}

func tasksUsage(msg, fix string) error {
	return aiRefused(&aisession.Refusal{Code: ai.CodeUsage, Message: msg, Fix: fix})
}

// tasksDraft runs ai.draft for the person: the draft's own confirmation,
// the session in this process, and the result printed. An unusable draft
// is an error. A nil draft with no error means nothing ran (the policy only
// suggests, or the draft waits for an approval).
func tasksDraft(ctx context.Context, env command.CLIEnv, a *app.App, in aiDraftIn) (*ai.Draft, error) {
	inv, origin := tasksDraftInvoker(), env.Origin(a)
	in.foreground = true
	out, err := confirmDraft(false, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return command.Invoke(ctx, inv, a, aiDraftSpec, next, origin)
	})
	if err != nil || out == nil {
		return nil, err
	}
	var d ai.Draft
	switch v := out.(type) {
	case ai.Draft:
		d = v
	case AIDraftRunning:
		if !v.foreground {
			return nil, env.Print(v, nil)
		}
		if d, err = hostDraft(ctx, a, v.Session); err != nil {
			return nil, err
		}
	default:
		return nil, env.Print(out, nil)
	}
	if err := printDraftResult(os.Stdout, d); err != nil {
		return nil, err
	}
	return &d, nil
}

// runTaskCreateWithAI is task create: with --ai the summary and the
// description are drafted from --description first.
func runTaskCreateWithAI(ctx context.Context, call command.CLICall[taskCreateIn], in taskCreateIn) error {
	if !in.ai {
		return runTaskCreate(ctx, call, in)
	}
	person, err := tasksForPerson(ctx, "Run agentflow ai draft ticket --text TEXT (or agentflow_ai_draft), then pass the candidate's summary and description to agentflow task create.")
	if err != nil {
		return err
	}
	if in.yes || in.Summary != "" {
		return tasksUsage("--ai takes no --yes and no --summary: it drafts the summary and asks you.",
			"Remove them. --description is the text the draft starts from.")
	}
	d, err := tasksDraft(person, call.Env, call.App, aiDraftIn{Feature: string(ai.FeatureTicket), Text: in.Description,
		Template: in.Template, TemplateRevision: in.TemplateRevision, Project: in.Project, Type: in.Type, LinkTo: in.LinkTo})
	if err != nil || d == nil {
		return err
	}
	for _, c := range d.Candidates {
		if c.Operation != "task.create" || len(c.Issues) > 0 {
			continue
		}
		var drafted taskCreateIn
		if err := json.Unmarshal(c.Input, &drafted); err != nil {
			return err
		}
		if !tasksYes("Use this draft? [y/N]: ") {
			return errors.New("cancelled: no task was created")
		}
		// Only the drafted text comes from the draft; every other field is
		// the person's own flag.
		in.ai, in.Summary, in.Description = false, drafted.Summary, drafted.Description
		return tasksCreate(ctx, call, in)
	}
	return errors.New("the draft has no task to create")
}

// tasksReplyAIArgs is review reply --ai's arguments: at most one thread.
func tasksReplyAIArgs(in *reviewReplyIn, args []string) error {
	if len(args) > 1 {
		return tasksUsage("--ai drafts the reply, so it takes no BODY.", "Run agentflow review reply --ai [THREAD_ID].")
	}
	if len(args) == 1 {
		in.ThreadID = args[0]
	}
	return nil
}

// runReviewReply is review reply: with --ai the replies are drafted first;
// without it, the confirmation flow of every write.
func runReviewReply(ctx context.Context, call command.CLICall[reviewReplyIn], in reviewReplyIn) error {
	if !in.ai {
		return tasksSendReply(ctx, call, in, in.yes)
	}
	person, err := tasksForPerson(ctx, "Run agentflow ai draft reply (or agentflow_ai_draft), then pass a candidate's thread ID and body to agentflow review reply.")
	if err != nil {
		return err
	}
	if in.yes {
		return tasksUsage("--ai takes no --yes: it asks before it sends each reply.", "Remove --yes.")
	}
	d, err := tasksDraft(person, call.Env, call.App, aiDraftIn{Feature: string(ai.FeatureReply), Thread: in.ThreadID})
	if err != nil || d == nil {
		return err
	}
	for _, c := range d.Candidates {
		if c.Operation != "review.reply" || len(c.Issues) > 0 || c.Target == nil {
			continue
		}
		var drafted reviewReplyIn
		if err := json.Unmarshal(c.Input, &drafted); err != nil {
			return err
		}
		fmt.Printf("\nReply to %s (the model's words):\n%s\n", visible(c.Target.Label), prefixLines(drafted.Body, "  | "))
		if !tasksYes("Send this reply? [y/N]: ") {
			fmt.Println("Not sent.")
			continue
		}
		if err := tasksSendReply(ctx, call, reviewReplyIn{ThreadID: drafted.ThreadID, Body: drafted.Body}, false); err != nil {
			return err
		}
	}
	return nil
}

// tasksSendReply runs review.reply through its confirmation.
func tasksSendReply(ctx context.Context, call command.CLICall[reviewReplyIn], in reviewReplyIn, yes bool) error {
	return call.Env.Confirm(yes, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return call.Invoke(ctx, next)
	})
}
