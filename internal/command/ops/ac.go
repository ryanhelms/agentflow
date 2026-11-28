package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/contextx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type acUpdateIn struct {
	Key          string `json:"key,omitempty"`
	Item         string `json:"item" jsonschema:"text (or a unique part) of the acceptance criterion"`
	Done         bool   `json:"done"`
	Evidence     string `json:"evidence,omitempty" jsonschema:"commit SHA or test name that proves it"`
	Post         bool   `json:"post,omitempty" jsonschema:"also post a progress comment to the ticket"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

const acUsage = "usage: agentflow ac [list] [KEY] [--json] | agentflow ac done|undo ITEM [--key KEY] [--evidence E] [--post] [--yes]"

// The two operations are typed values because the verbs below reach them
// through command.Invoke. A verb owns their CLI path: `ac` lists, `ac done`
// and `ac undo` tick and untick.
var (
	acChecklist = command.Spec[taskKeyIn, any]{
		Name:    "ac.checklist",
		CLIPath: []string{"ac"},
		MCP: &command.MCP{Tool: "agentflow_ac_checklist", Description: "Acceptance criteria for the ticket as a checklist with local done state.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in taskKeyIn) (any, error) {
			return a.Checklist(ctx, in.Key)
		},
	}

	acUpdate = command.Spec[acUpdateIn, any]{
		Name:    "ac.update",
		CLIPath: []string{"ac", "done"},
		MCP: &command.MCP{Tool: "agentflow_ac_update", Description: "Tick or untick an acceptance criterion with evidence; optionally post progress to the ticket.",
			Annotations: command.LocalWrite},
		// Ticking is a local write the policy does not gate. With post, the
		// app method posts the progress through Comment, which gates the
		// "comment" action and audits it: so the action is "comment", and
		// the Invoker adds no audit line of its own (AuditByInvoker is
		// unset) to keep one line per call.
		Policy: command.Policy{Class: policy.Remote, Action: "comment"},
		Token:  func(in *acUpdateIn) *string { return &in.ConfirmToken },
		Describe: func(in acUpdateIn) command.Detail {
			// The item is the criterion's text, so only the task is named.
			verb := "untick"
			if in.Done {
				verb = "tick"
			}
			return command.Detail{Subject: verb + " an acceptance criterion", Task: in.Key}
		},
		Run: func(ctx context.Context, a *app.App, in acUpdateIn) (any, error) {
			return a.UpdateChecklist(ctx, in.Key, in.Item, in.Done, in.Evidence, in.Post, in.ConfirmToken)
		},
	}
)

func init() {
	command.Register(acChecklist)
	command.Register(acUpdate)
	command.RegisterVerb(command.Verb{
		Path:    []string{"ac"},
		Usage:   "  ac [list] [KEY] [--json]       acceptance criteria checklist with local done state\n  ac --ai [KEY]                  draft criteria checks against the branch; asks before each tick",
		Section: command.SectionWork,
		Order:   3000,
		Run:     runAC,
	})
	command.RegisterVerb(command.Verb{
		Path: []string{"ac", "done"},
		Usage: "  ac done|undo ITEM [--key KEY] [--evidence E] [--post] [--yes]\n" +
			"                                 tick or untick a criterion; --post comments progress",
		Section: command.SectionWork,
		Order:   3100,
		Run: func(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
			return runACUpdate(ctx, env, inv, "done", args)
		},
	})
}

// runAC is `agentflow ac`: list the checklist, or tick or untick an item.
func runAC(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	sub := "list"
	if len(args) > 0 {
		switch args[0] {
		case "list", "done", "undo":
			sub, args = args[0], args[1:]
		}
	}
	if sub != "list" {
		return runACUpdate(ctx, env, inv, sub, args)
	}
	if wantsAI(args) {
		return acWithAI(ctx, env, inv, args)
	}
	fs := flag.NewFlagSet("ac list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the checklist as JSON")
	pos, err := taskParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New(acUsage)
	}
	in := taskKeyIn{}
	if len(pos) == 1 {
		in.Key = pos[0]
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	out, err := command.Invoke(ctx, inv, a, acChecklist, in, env.Origin(a))
	if err != nil {
		return err
	}
	if *asJSON {
		return env.Print(out, nil)
	}
	cl, _ := out.(*contextx.Checklist)
	if cl == nil {
		return errors.New("expected an acceptance criteria checklist")
	}
	d, n := cl.Progress()
	fmt.Fprintf(os.Stdout, "%s · %d/%d acceptance criteria\n", cl.Key, d, n)
	for _, i := range cl.Items {
		mark := "[ ]"
		if i.Done {
			mark = "[x]"
		}
		line := "  " + mark + " " + i.Text
		if i.Evidence != "" {
			line += "  (" + i.Evidence + ")"
		}
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}

// runACUpdate is `agentflow ac done` and `agentflow ac undo`: tick or untick
// the item the arguments name, through the confirmation flow.
func runACUpdate(ctx context.Context, env command.CLIEnv, inv command.Invoker, sub string, args []string) error {
	fs := flag.NewFlagSet("ac "+sub, flag.ContinueOnError)
	key := fs.String("key", "", "task key; defaults to the branch task")
	evidence := fs.String("evidence", "", "commit SHA or test name that proves it")
	post := fs.Bool("post", false, "also post a progress comment to the task")
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	pos, err := taskParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	item := strings.Join(pos, " ")
	if item == "" {
		return errors.New(acUsage)
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	in := acUpdateIn{Key: *key, Item: item, Done: sub == "done", Evidence: *evidence, Post: *post}
	return env.Confirm(*yes, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return command.Invoke(ctx, inv, a, acUpdate, next, env.Origin(a))
	})
}
