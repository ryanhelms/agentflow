package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/hooks"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type prReadyIn struct {
	Run bool `json:"run_checks,omitempty" jsonschema:"also run the repo's test and lint commands"`
}

type prOpenIn struct {
	app.PROpenInput
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// The pr family. "pr" is a Verb that runs pr.ready, pr.prepare or pr.open by
// its flags, and "pr mark-ready" one that runs pr.mark-ready.prepare or
// pr.mark-ready. The prepare operations write no audit line in their app
// methods, so the Invoker writes one; pr.open and pr.mark-ready audit
// themselves.
var (
	prReadySpec = command.Spec[prReadyIn, any]{
		Name:    "pr.ready",
		CLIPath: []string{"pr"},
		MCP: &command.MCP{Tool: "agentflow_pr_ready", Description: "Readiness check before a PR: branch, commit conventions, clean tree, secrets, acceptance criteria, and configured task effects. Optionally run tests and lint.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run:    func(ctx context.Context, a *app.App, in prReadyIn) (any, error) { return a.PRReady(ctx, in.Run) },
	}

	prPrepareSpec = command.Spec[app.PROpenInput, appliedMap]{
		Name:    "pr.prepare",
		CLIPath: []string{"pr"},
		MCP: &command.MCP{Tool: "agentflow_pr_prepare", Description: "Prepare a branch push and pull request for approval without publishing.",
			Annotations: command.ReadOnly},
		Policy:   command.Policy{Class: policy.Remote, Action: "pr_open", Related: []string{"push"}, PrepareOnly: true, AuditByInvoker: true},
		Describe: func(in app.PROpenInput) command.Detail { return command.Detail{Subject: firstLine(in.Title)} },
		Run:      func(ctx context.Context, a *app.App, in app.PROpenInput) (any, error) { return a.PRPrepare(ctx, in) },
	}

	prOpenSpec = command.Spec[prOpenIn, appliedMap]{
		Name:    "pr.open",
		CLIPath: []string{"pr"},
		MCP: &command.MCP{Tool: "agentflow_pr_open", Description: "Push the branch and open a PR with a convention title, templated body, acceptance criteria and reviewers.",
			Annotations: command.RemoteWrite},
		Policy:   command.Policy{Class: policy.Remote, Action: "pr_open", Related: []string{"push"}},
		Token:    func(in *prOpenIn) *string { return &in.ConfirmToken },
		Describe: func(in prOpenIn) command.Detail { return command.Detail{Subject: firstLine(in.Title)} },
		Run: func(ctx context.Context, a *app.App, in prOpenIn) (any, error) {
			return appliedStatus("opened")(a.PROpen(ctx, in.PROpenInput, in.ConfirmToken))
		},
		Links: pullRequestLinks,
	}

	prMarkReadyPrepareSpec = command.Spec[deliveryNoIn, appliedMap]{
		Name:    "pr.mark-ready.prepare",
		CLIPath: []string{"pr", "mark-ready"},
		MCP: &command.MCP{Tool: "agentflow_pr_mark_ready_prepare", Description: "Prepare to mark the current draft pull request ready for review.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Remote, Action: "pr_ready_for_review", PrepareOnly: true, AuditByInvoker: true},
		Run:    func(ctx context.Context, a *app.App, _ deliveryNoIn) (any, error) { return a.MarkReadyPrepare(ctx) },
	}

	// A pull request that is already ready counts as done: the operation's
	// goal holds, and there is nothing to refuse.
	prMarkReadySpec = command.Spec[branchTokenIn, appliedMap]{
		Name:    "pr.mark-ready",
		CLIPath: []string{"pr", "mark-ready"},
		MCP: &command.MCP{Tool: "agentflow_pr_mark_ready", Description: "Mark the current draft pull request ready for review at its reviewed head.",
			Annotations: command.RemoteWrite},
		Policy: command.Policy{Class: policy.Remote, Action: "pr_ready_for_review"},
		Token:  func(in *branchTokenIn) *string { return &in.ConfirmToken },
		Run: func(ctx context.Context, a *app.App, in branchTokenIn) (any, error) {
			return appliedStatus("ready_for_review", "already_ready")(a.MarkReady(ctx, in.ConfirmToken))
		},
		Links: pullRequestLinks,
	}
)

func init() {
	command.Register(prReadySpec)
	command.Register(prPrepareSpec)
	command.Register(prOpenSpec)
	command.Register(prMarkReadyPrepareSpec)
	command.Register(prMarkReadySpec)
	command.RegisterVerb(branchWriteVerb([]string{"pr", "mark-ready"},
		"  pr mark-ready [--prepare | --confirm-token TOKEN] [--yes]   mark the current draft pull request ready for review", 4200,
		prMarkReadyPrepareSpec, prMarkReadySpec))
	command.RegisterVerb(command.Verb{
		Path:    []string{"pr"},
		Usage:   "  pr [--check] [--run] [--title T | --ai] [--draft] [--prepare | --confirm-token TOKEN] [--yes]   readiness check, then open the PR; --ai drafts the title and body",
		Section: command.SectionWork,
		Order:   4700,
		Run:     runPR,
	})
}

// pullRequestLinks links an opened or promoted pull request.
func pullRequestLinks(o appliedMap) []bus.OpLink {
	var id string
	switch n := o["number"].(type) {
	case int:
		id = strconv.Itoa(n)
	case int64:
		id = strconv.FormatInt(n, 10)
	}
	if id == "" || id == "0" {
		return nil
	}
	link := bus.OpLink{Kind: bus.OpLinkPull, ID: id}
	if u, _ := o["url"].(string); strings.HasPrefix(u, "https://") {
		link.URL = u
	}
	return []bus.OpLink{link}
}

// runPR checks readiness and opens the pull request, or only prepares or
// applies the exact publication, as its flags say.
func runPR(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	prepare := fs.Bool("prepare", false, "prepare the exact publication without publishing it")
	token := fs.String("confirm-token", "", "resume the exact reviewed publication")
	body := fs.String("body", "", "pull request body")
	var labels, reviewers []string
	fs.Func("label", "label to apply (repeatable)", func(v string) error { labels = append(labels, v); return nil })
	fs.Func("reviewer", "reviewer to request (repeatable)", func(v string) error { reviewers = append(reviewers, v); return nil })
	check := fs.Bool("check", false, "readiness check only")
	runCmds := fs.Bool("run", false, "also run test and lint commands")
	title := fs.String("title", "", "PR title")
	draft := fs.Bool("draft", false, "open as draft")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	useAI := fs.Bool("ai", false, "draft the title and body with your harness first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *useAI {
		return prWithAI(ctx, env, inv, fs, reviewers, labels)
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	origin := env.Origin(a)
	in := app.PROpenInput{Title: *title, Body: *body, Reviewers: reviewers, Labels: labels}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "draft" {
			in.Draft = draft
		}
	})
	if *prepare || *token != "" {
		if *check || *runCmds || (*prepare && (*token != "" || *yes)) {
			return errors.New("use one of --prepare, --confirm-token or --check")
		}
		if *prepare {
			return env.Print(command.Invoke(ctx, inv, a, prPrepareSpec, in, origin))
		}
		return env.Print(command.Invoke(ctx, inv, a, prOpenSpec, prOpenIn{PROpenInput: in, ConfirmToken: *token}, origin))
	}
	out, err := command.Invoke(ctx, inv, a, prReadySpec, prReadyIn{Run: *runCmds}, origin)
	if err != nil {
		return err
	}
	r, _ := out.(*app.Readiness)
	if r == nil {
		return errors.New("readiness check returned nothing")
	}
	for _, c := range r.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✖"
		}
		fmt.Printf("%s %-22s %s\n", mark, c.Name, c.Detail)
	}
	if *check {
		if !r.Ready {
			return &hooks.ExitError{Code: 1, Msg: "not ready"}
		}
		return nil
	}
	return env.Confirm(*yes, func(tok string) (any, error) {
		return command.Invoke(ctx, inv, a, prOpenSpec, prOpenIn{PROpenInput: in, ConfirmToken: tok}, origin)
	})
}
