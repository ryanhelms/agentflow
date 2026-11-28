package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type reviewReplyIn struct {
	ThreadID     string `json:"thread_id"`
	Body         string `json:"body"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

const reviewUsage = "usage: agentflow review [threads] [--json] | agentflow review reply THREAD_ID BODY [--yes]"

var (
	reviewThreadsSpec = command.Spec[deliveryNoIn, any]{
		Name:    "review.threads",
		CLIPath: []string{"review"},
		MCP: &command.MCP{Tool: "agentflow_review_threads", Description: "Unresolved review threads on this branch's PR.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run:    func(ctx context.Context, a *app.App, _ deliveryNoIn) (any, error) { return a.ReviewThreads(ctx) },
	}

	// The reply's body stays out of the event: the subject says only what
	// the operation does.
	reviewReplySpec = command.Spec[reviewReplyIn, map[string]bool]{
		Name:    "review.reply",
		CLIPath: []string{"review", "reply"},
		CLI: &command.CLI[reviewReplyIn]{
			// Declared here to keep the legacy command's help text.
			Flags: func(fs *flag.FlagSet, _ *reviewReplyIn) { fs.Bool("yes", false, "accept the confirmation prompt") },
			Args: func(in *reviewReplyIn, args []string) error {
				if len(args) != 2 {
					return errors.New("usage: agentflow review reply THREAD_ID BODY [--yes]")
				}
				in.ThreadID, in.Body = args[0], args[1]
				return nil
			},
			Interspersed: true,
			Confirm:      true,
			Usage:        "  review reply THREAD_ID BODY [--yes]   reply on a review thread\n  review reply --ai [THREAD_ID]  draft replies to open threads; asks before each one",
			Section:      command.SectionWork,
			Order:        4900,
		},
		MCP: &command.MCP{Tool: "agentflow_review_reply", Description: "Reply on a review thread, e.g. with the commit that addressed it.",
			Annotations: command.RemoteWrite},
		Policy:   command.Policy{Class: policy.Remote, Action: "review_reply"},
		Token:    func(in *reviewReplyIn) *string { return &in.ConfirmToken },
		Describe: func(reviewReplyIn) command.Detail { return command.Detail{Subject: "reply on a review thread"} },
		Run: func(ctx context.Context, a *app.App, in reviewReplyIn) (any, error) {
			return a.ReviewReply(ctx, in.ThreadID, in.Body, in.ConfirmToken)
		},
	}
)

func init() {
	command.Register(reviewThreadsSpec)
	command.Register(reviewReplySpec)
	// A Verb, not a Spec's CLI: the flag --json only changes how the result
	// prints, and the optional word "threads" comes before the flags.
	command.RegisterVerb(command.Verb{
		Path:    []string{"review"},
		Usage:   "  review [threads] [--json]      unresolved review threads on this branch's PR",
		Section: command.SectionWork,
		Order:   4800,
		Run:     runReviewThreads,
	})
}

// runReviewThreads prints the unresolved review threads of the branch's PR.
func runReviewThreads(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	if len(args) > 0 && args[0] == "threads" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print unresolved review threads as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(reviewUsage)
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	out, err := command.Invoke(ctx, inv, a, reviewThreadsSpec, deliveryNoIn{}, env.Origin(a))
	if err != nil {
		return err
	}
	if *asJSON {
		return env.Print(out, nil)
	}
	result, _ := out.(map[string]any)
	threads, _ := result["threads"].([]gh.Thread)
	if result["pr"] == nil {
		fmt.Println("no open PR for this branch")
		return nil
	}
	fmt.Printf("%v · %d unresolved\n", result["pr"], len(threads))
	for _, t := range threads {
		fmt.Printf("\n%s:%d  @%s  thread %s\n%s\n", t.Path, t.Line, t.Author, t.ID, indentBody(t.Body))
	}
	return nil
}
