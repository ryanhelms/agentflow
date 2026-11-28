package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/hooks"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
)

type commitContextIn struct {
	Paths []string `json:"paths,omitempty" jsonschema:"files to stage for this commit; empty uses what is already staged"`
}

type commitMessageIn struct {
	Message string `json:"message" jsonschema:"full commit message"`
}

type commitPrepareIn struct {
	Message string   `json:"message" jsonschema:"full conventional commit message"`
	Paths   []string `json:"paths,omitempty" jsonschema:"include only these paths or use the staged index"`
}

type commitIn struct {
	Message      string   `json:"message" jsonschema:"full commit message: type(KEY): summary, blank line, body"`
	Paths        []string `json:"paths,omitempty" jsonschema:"commit only these paths; prepare leaves the index unchanged"`
	ConfirmToken string   `json:"confirm_token,omitempty" jsonschema:"token from a needs_confirmation response, after the user agreed"`
}

// The commit family. The CLI command "commit" is a Verb that runs commit,
// commit.prepare, commit.plan or commit.context by its flags; commit.validate
// belongs to "lint-msg" and commit.preview to "preview", as before.
var (
	commitPlanSpec = command.Spec[deliveryNoIn, any]{
		Name:    "commit.plan",
		CLIPath: []string{"commit"},
		MCP: &command.MCP{Tool: "agentflow_commit_plan", Description: "Group the working tree into logical commits (code with tests per area, then docs, CI, build).",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run:    func(ctx context.Context, a *app.App, _ deliveryNoIn) (any, error) { return a.CommitPlan(ctx) },
	}

	// commit.context stages the paths it is given: a local write that no app
	// method audits, so the Invoker writes its line. With no paths it only
	// reads the staged index, as the interactive commit does, and counts as a
	// dry run: no events and no audit line.
	commitContextSpec = command.Spec[commitContextIn, app.CommitContext]{
		Name:    "commit.context",
		CLIPath: []string{"commit"},
		MCP: &command.MCP{Tool: "agentflow_commit_context", Description: "Stage paths and return what is needed to write the message: rules, task reference and context, suggested type, breaking paths, diff.",
			Annotations: command.LocalWrite},
		Policy: command.Policy{Class: policy.Local, Action: "commit_context", Gate: command.GateNone, AuditByInvoker: true},
		DryRun: func(in commitContextIn) bool { return len(in.Paths) == 0 },
		Describe: func(in commitContextIn) command.Detail {
			return command.Detail{Subject: fmt.Sprintf("stage %d paths", len(in.Paths))}
		},
		Run: func(ctx context.Context, a *app.App, in commitContextIn) (any, error) {
			return a.CommitContext(ctx, in.Paths)
		},
	}

	commitValidateSpec = command.Spec[commitMessageIn, any]{
		Name:    "commit.validate",
		CLIPath: []string{"lint-msg"},
		MCP: &command.MCP{Tool: "agentflow_commit_validate", Description: "Validate a commit message against the convention and suggest fixes.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in commitMessageIn) (any, error) {
			issues := a.Validate(ctx, in.Message)
			return map[string]any{"valid": len(issues) == 0, "issues": issues}, nil
		},
	}

	commitPreviewSpec = command.Spec[commitMessageIn, any]{
		Name:    "commit.preview",
		CLIPath: []string{"preview"},
		MCP: &command.MCP{Tool: "agentflow_commit_preview", Description: "Preview configured task transitions for a commit message and the next PR events.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in commitMessageIn) (any, error) {
			effects, warnings, err := a.Preview(ctx, in.Message)
			return map[string]any{"effects": effects, "warnings": warnings}, err
		},
	}

	// commit.prepare never returns a CommitResult: a prepared candidate is a
	// Pending, and a refused one is a status map ("invalid", "blocked").
	// The app method writes no audit line, so the Invoker writes one.
	commitPrepareSpec = command.Spec[commitPrepareIn, app.CommitResult]{
		Name:    "commit.prepare",
		CLIPath: []string{"commit"},
		MCP: &command.MCP{Tool: "agentflow_commit_prepare", Description: "Prepare an exact commit for approval without staging files or creating a commit.",
			Annotations: command.ReadOnly},
		Policy:   command.Policy{Class: policy.Local, Action: "commit", PrepareOnly: true, AuditByInvoker: true},
		Describe: func(in commitPrepareIn) command.Detail { return command.Detail{Subject: firstLine(in.Message)} },
		Run: func(ctx context.Context, a *app.App, in commitPrepareIn) (any, error) {
			return a.CommitPrepare(ctx, in.Message, in.Paths)
		},
	}

	commitSpec = command.Spec[commitIn, app.CommitResult]{
		Name:    "commit",
		CLIPath: []string{"commit"},
		MCP: &command.MCP{Tool: "agentflow_commit", Description: "Validate, secret-scan and commit. Returns needs_confirmation when the developer's autonomy is confirm.",
			Annotations: command.LocalWrite},
		Policy:   command.Policy{Class: policy.Local, Action: "commit"},
		Token:    func(in *commitIn) *string { return &in.ConfirmToken },
		Describe: func(in commitIn) command.Detail { return command.Detail{Subject: firstLine(in.Message)} },
		Run: func(ctx context.Context, a *app.App, in commitIn) (any, error) {
			return a.Commit(ctx, in.Message, in.Paths, in.ConfirmToken)
		},
		Links: func(o app.CommitResult) []bus.OpLink {
			return []bus.OpLink{{Kind: bus.OpLinkCommit, ID: o.SHA}}
		},
	}
)

func init() {
	command.Register(commitPlanSpec)
	command.Register(commitContextSpec)
	command.Register(commitValidateSpec)
	command.Register(commitPreviewSpec)
	command.Register(commitPrepareSpec)
	command.Register(commitSpec)
	command.RegisterVerb(command.Verb{
		Path:    []string{"commit"},
		Usage:   "  commit [-m MSG | --ai] [--path PATH] [--prepare | --confirm-token TOKEN] [--split] [--dry-run] [--yes]   guided commit using the configured workflow; --ai drafts the message first",
		Section: command.SectionWork,
		Order:   4300,
		Run:     runCommit,
	})
	// The legacy command reads every argument as a message, flags included,
	// so a Verb parses nothing.
	command.RegisterVerb(command.Verb{
		Path:    []string{"preview"},
		Usage:   "  preview [MSG]                  configured task effects of a message or of unpushed commits",
		Section: command.SectionWork,
		Order:   4400,
		Run:     runPreview,
	})
}

// runCommit is the guided commit: an exact commit from -m (and --path),
// its prepared candidate, the proposed groups, or a message built
// interactively. Every step is an operation run through the Invoker.
func runCommit(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	msg := fs.String("m", "", "commit message (otherwise built interactively)")
	prepare := fs.Bool("prepare", false, "prepare the exact commit without writing it")
	token := fs.String("confirm-token", "", "resume the exact reviewed change")
	var paths []string
	fs.Func("path", "include only this path (repeatable)", func(path string) error {
		if path == "" {
			return errors.New("--path requires a path")
		}
		paths = append(paths, path)
		return nil
	})
	split := fs.Bool("split", false, "show the proposed commit groups")
	dry := fs.Bool("dry-run", false, "validate and preview only")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	useAI := fs.Bool("ai", false, "draft the message with your harness first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *useAI {
		return commitWithAI(ctx, env, inv, fs, paths, *prepare)
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	origin := env.Origin(a)
	if *prepare || *token != "" || len(paths) > 0 {
		if *msg == "" || *split || *dry || (*prepare && (*token != "" || *yes)) {
			return errors.New("use -m MSG for an exact commit and select one of --prepare or --confirm-token")
		}
		if *prepare {
			return env.Print(command.Invoke(ctx, inv, a, commitPrepareSpec, commitPrepareIn{Message: *msg, Paths: paths}, origin))
		}
		if *token != "" {
			return env.Print(command.Invoke(ctx, inv, a, commitSpec, commitIn{Message: *msg, Paths: paths, ConfirmToken: *token}, origin))
		}
		return env.Confirm(*yes, func(tok string) (any, error) {
			return command.Invoke(ctx, inv, a, commitSpec, commitIn{Message: *msg, Paths: paths, ConfirmToken: tok}, origin)
		})
	}
	if *split {
		out, err := command.Invoke(ctx, inv, a, commitPlanSpec, deliveryNoIn{}, origin)
		if err != nil {
			return err
		}
		groups, _ := out.([]commit.Group)
		for i, g := range groups {
			fmt.Printf("%d) %s — %s\n", i+1, g.Type, g.Why)
			for _, f := range g.Files {
				fmt.Println("     ", f)
			}
		}
		fmt.Println("\nStage one group (git add …) and run agentflow commit for each.")
		return nil
	}
	if *msg == "" {
		out, err := command.Invoke(ctx, inv, a, commitContextSpec, commitContextIn{}, origin)
		if err != nil {
			return err
		}
		cc, _ := out.(*app.CommitContext)
		if cc == nil {
			return errors.New("commit context is empty")
		}
		var keys []string
		if !a.UsesTaskRefs() || a.Cfg.Commit.KeyRequired() {
			key := cc.Key
			if a.UsesTaskRefs() {
				key = ""
			}
			if k := askLine(fmt.Sprintf("Jira key [%s]: ", key)); k != "" {
				key = strings.ToUpper(k)
			}
			keys = []string{key}
		}
		typ := cc.SuggestedType
		if t := askLine(fmt.Sprintf("Type (%s) [%s]: ", strings.Join(a.Cfg.Commit.Types, "/"), typ)); t != "" {
			typ = t
		}
		summary := askLine("Summary (imperative, e.g. \"add quota API\"): ")
		body := askLine("Why (optional, one line): ")
		breaking := len(cc.Breaking) > 0 && strings.HasPrefix(strings.ToLower(askLine(fmt.Sprintf("Breaking change? %v [y/N]: ", cc.Breaking))), "y")
		verb := askLine("Verb (start/review/qa/done/block, optional): ")
		var verbs, trailers []string
		if verb != "" {
			verbs = []string{strings.TrimPrefix(verb, "#")}
		}
		if breaking {
			trailers = append(trailers, "BREAKING CHANGE: "+askLine("What breaks and how to migrate: "))
		}
		if cc.TaskRef != "" {
			trailers = append(trailers, provider.TaskTrailer+cc.TaskRef)
		}
		*msg = commit.Format(typ, keys, breaking, summary, body, verbs, trailers)
	}
	checked, err := command.Invoke(ctx, inv, a, commitValidateSpec, commitMessageIn{Message: *msg}, origin)
	if err != nil {
		return err
	}
	if verdict, _ := checked.(map[string]any); verdict != nil {
		if issues, _ := verdict["issues"].([]commit.Issue); len(issues) > 0 {
			for _, i := range issues {
				fmt.Println("✖", i.Message)
				if i.Fix != "" {
					fmt.Println("  try:", i.Fix)
				}
			}
			return &hooks.ExitError{Code: 1, Msg: "invalid message"}
		}
	}
	// A failed preview leaves the effects empty, as the legacy command did.
	previewed, _ := command.Invoke(ctx, inv, a, commitPreviewSpec, commitMessageIn{Message: *msg}, origin)
	preview, _ := previewed.(map[string]any)
	effects, _ := preview["effects"].([]app.Effect)
	warnings, _ := preview["warnings"].([]string)
	for _, e := range effects {
		line := fmt.Sprintf("  %s %s", e.Key, e.When)
		if e.To != "" {
			line += fmt.Sprintf(": %s → %s", e.From, e.To)
		}
		if e.Note != "" {
			line += " (" + e.Note + ")"
		}
		fmt.Println(line)
	}
	for _, w := range warnings {
		fmt.Println("  !", w)
	}
	if *dry {
		fmt.Println("✓ valid")
		return nil
	}
	m := *msg
	return env.Confirm(*yes, func(tok string) (any, error) {
		return command.Invoke(ctx, inv, a, commitSpec, commitIn{Message: m, ConfirmToken: tok}, origin)
	})
}

// runPreview prints the configured task effects of each message given, or of
// each unpushed commit.
func runPreview(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	msgs := args
	if len(msgs) == 0 {
		if msgs, err = a.Repo.Unpushed(ctx); err != nil {
			return err
		}
	}
	if len(msgs) == 0 {
		fmt.Println("no unpushed commits")
		return nil
	}
	origin := env.Origin(a)
	for _, m := range msgs {
		out, err := command.Invoke(ctx, inv, a, commitPreviewSpec, commitMessageIn{Message: m}, origin)
		if err != nil {
			return err
		}
		preview, _ := out.(map[string]any)
		effects, _ := preview["effects"].([]app.Effect)
		warnings, _ := preview["warnings"].([]string)
		fmt.Println(strings.SplitN(m, "\n", 2)[0])
		for _, e := range effects {
			fmt.Printf("  %s %s: %s → %s %s\n", e.Key, e.When, e.From, e.To, e.Note)
		}
		for _, w := range warnings {
			fmt.Println("  !", w)
		}
	}
	return nil
}
