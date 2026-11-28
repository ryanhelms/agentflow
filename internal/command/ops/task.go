package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

type startIn struct {
	Key          string `json:"key" jsonschema:"task key or qualified reference to start"`
	Worktree     bool   `json:"worktree,omitempty" jsonschema:"create a git worktree instead of switching branch (automatic when there are uncommitted changes)"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"token from a needs_confirmation response, after the user agreed"`

	yes bool // --yes, command line only
}

type taskCreateIn struct {
	Template         string `json:"template,omitempty" jsonschema:"exact catalog template name; pair with template_revision"`
	TemplateRevision string `json:"template_revision,omitempty"`
	Project          string `json:"project,omitempty" jsonschema:"project or scope; may come from the template or provider"`
	Type             string `json:"type,omitempty" jsonschema:"task type, when supported"`
	Summary          string `json:"summary,omitempty" jsonschema:"task title; may come from a template"`
	Description      string `json:"description,omitempty"`
	LinkTo           string `json:"link_to,omitempty" jsonschema:"related task key or supported knowledge reference; defaults to the branch task"`
	ConfirmToken     string `json:"confirm_token,omitempty"`

	yes, dryRun, ai bool // --yes, --dry-run and --ai, command line only
}

type taskLinkIn struct {
	Key          string `json:"key,omitempty" jsonschema:"Jira issue key, or use the current branch's issue"`
	URL          string `json:"url" jsonschema:"Absolute HTTP or HTTPS URL to add to Jira Web links"`
	Title        string `json:"title,omitempty" jsonschema:"Web link title, defaults to the URL"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"Single-use Agentflow confirmation token"`
}

const taskLinkUsage = "usage: agentflow task link KEY --url URL [--title T] [--yes]"

func init() {
	command.Register(command.Spec[startIn, any]{
		Name:    "start",
		CLIPath: []string{"start"},
		CLI: &command.CLI[startIn]{
			// --yes is declared here for the custom runner and to keep the
			// legacy command's help text.
			Flags: func(fs *flag.FlagSet, in *startIn) {
				fs.BoolVar(&in.Worktree, "worktree", false, "create a worktree")
				fs.BoolVar(&in.yes, "yes", false, "skip the confirmation prompt")
			},
			// Like the legacy command: the first positional argument is the
			// key and any further ones are ignored.
			Args: func(in *startIn, args []string) error {
				if len(args) > 0 {
					in.Key = args[0]
				}
				return nil
			},
			Interspersed: true,
			Run:          runStart,
			Usage: "  start KEY [--worktree]         branch or worktree for KEY; apply supported start effects.\n" +
				"                                 Worktrees go to <worktree.root>/<repo>/<branch> (default ~/.worktrees)\n" +
				"                                 with a git-excluded .agentflow/worktree.yml and, if configured, go.work",
			Section: command.SectionWork,
			Order:   2300,
		},
		MCP: &command.MCP{Tool: "agentflow_start", Description: "Start work on a task in a branch or worktree. Assignment and start transitions depend on provider support and configuration.",
			Annotations: command.RemoteWrite, WriteHost: command.WriteHostTasks},
		Policy: command.Policy{Class: policy.Remote, Action: "start"},
		Token:  func(in *startIn) *string { return &in.ConfirmToken },
		Describe: func(in startIn) command.Detail {
			return command.Detail{Subject: "start " + in.Key, Task: in.Key}
		},
		Links: func(out any) []bus.OpLink {
			var res app.StartResult
			switch v := out.(type) {
			case app.StartResult:
				res = v
			case *app.StartResult:
				if v == nil {
					return nil
				}
				res = *v
			default:
				return nil
			}
			links := []bus.OpLink{{Kind: bus.OpLinkTask, ID: res.Key}}
			if res.Branch != "" {
				links = append(links, bus.OpLink{Kind: bus.OpLinkBranch, ID: res.Branch})
			}
			return links
		},
		Run: func(ctx context.Context, a *app.App, in startIn) (any, error) {
			return a.Start(ctx, in.Key, in.Worktree, in.ConfirmToken)
		},
	})

	command.Register(command.Spec[taskCreateIn, any]{
		Name:    "task.create",
		CLIPath: []string{"task", "create"},
		CLI: &command.CLI[taskCreateIn]{
			Flags: func(fs *flag.FlagSet, in *taskCreateIn) {
				fs.StringVar(&in.Project, "project", "", "project or scope")
				fs.StringVar(&in.Type, "type", "", "provider task type")
				fs.StringVar(&in.Summary, "summary", "", "task title")
				fs.StringVar(&in.Description, "description", "", "task description")
				fs.StringVar(&in.LinkTo, "link-to", "", "related task or supported knowledge reference")
				fs.StringVar(&in.Template, "template", "", "exact catalog template name")
				fs.StringVar(&in.TemplateRevision, "template-revision", "", "exact catalog revision")
				fs.BoolVar(&in.yes, "yes", false, "approve creation under the configured policy")
				fs.BoolVar(&in.dryRun, "dry-run", false, "print the resolved create preview")
				fs.BoolVar(&in.ai, "ai", false, "draft the summary and description from --description, then ask")
			},
			Args: func(_ *taskCreateIn, args []string) error {
				if len(args) != 0 {
					return errors.New("unexpected arguments after task create flags")
				}
				return nil
			},
			Run: runTaskCreateWithAI,
			Usage: "  task create [--summary S] [--project P] [--type T] [--description D] [--link-to REF]\n" +
				"              [--template NAME --template-revision REV] [--dry-run] [--yes]\n" +
				"  task create --ai --description TEXT [the flags above, except --summary and --yes]\n" +
				"                                 draft the ticket from your description first",
			Section: command.SectionWork,
			Order:   2100,
		},
		MCP: &command.MCP{Tool: "agentflow_ticket_create", Description: "Create a task with confirmation and an optional related task. Repository publication can return a pending review PR.",
			Annotations: command.RemoteWrite, WriteHost: command.WriteHostTasks},
		Policy: command.Policy{Class: policy.Remote, Action: "ticket_create"},
		Token:  func(in *taskCreateIn) *string { return &in.ConfirmToken },
		// A preview that --dry-run asks for changes nothing and is no operation.
		DryRun: func(in taskCreateIn) bool { return in.dryRun },
		Describe: func(in taskCreateIn) command.Detail {
			return command.Detail{Subject: in.Summary}
		},
		Links: func(out any) []bus.OpLink {
			if created, ok := out.(map[string]string); ok && created["key"] != "" {
				return []bus.OpLink{{Kind: bus.OpLinkTask, ID: created["key"]}}
			}
			return nil
		},
		Run: func(ctx context.Context, a *app.App, in taskCreateIn) (any, error) {
			if taskInteractiveClient(a) {
				ctx = app.WithJiraWriteCLI(ctx)
			}
			return a.CreateTicketWithTemplate(ctx, in.Project, in.Type, in.Summary, in.Description, in.LinkTo, tasktemplate.Ref{Name: in.Template, Revision: in.TemplateRevision}, in.ConfirmToken)
		},
	})

	command.Register(command.Spec[taskLinkIn, any]{
		Name:    "task.link",
		CLIPath: []string{"task", "link"},
		CLI: &command.CLI[taskLinkIn]{
			Flags: func(fs *flag.FlagSet, in *taskLinkIn) {
				fs.StringVar(&in.URL, "url", "", "external web link")
				fs.StringVar(&in.Title, "title", "", "link title, defaults to the URL")
				// Declared here to keep the legacy command's help text.
				fs.Bool("yes", false, "accept the confirmation prompt")
			},
			Args: func(in *taskLinkIn, args []string) error {
				if len(args) != 1 || in.URL == "" {
					return errors.New(taskLinkUsage)
				}
				in.Key = args[0]
				return nil
			},
			Interspersed: true,
			Confirm:      true,
			Usage:        "  task link KEY --url URL [--title T] [--yes]   add a web link to a Jira issue",
			Section:      command.SectionWork,
			Order:        2200,
		},
		MCP: &command.MCP{Tool: "agentflow_jira_remote_link", Description: "Add a Web link to an existing Jira issue through a connected Atlassian MCP or signed-in Teamwork Graph CLI.",
			Annotations: command.RemoteWrite, WriteHost: command.WriteHostTasks},
		Policy: command.Policy{Class: policy.Remote, Action: "jira_remote_link"},
		Token:  func(in *taskLinkIn) *string { return &in.ConfirmToken },
		Describe: func(in taskLinkIn) command.Detail {
			// The URL can carry a secret in its query, so only the task is named.
			return command.Detail{Subject: "add a web link to " + in.Key, Task: in.Key}
		},
		Run: func(ctx context.Context, a *app.App, in taskLinkIn) (any, error) {
			return a.LinkJiraRemote(ctx, in.Key, in.URL, in.Title, in.ConfirmToken)
		},
	})
}

// runStart is `agentflow start`: it picks a task from the work list when no
// key is given, runs the operation through the confirmation flow and tells
// where to start the agent when a worktree was made.
func runStart(ctx context.Context, call command.CLICall[startIn], in startIn) error {
	a := call.App
	if in.Key == "" {
		items, err := a.MyWork(ctx)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return errors.New("no open tasks found")
		}
		for i, it := range items {
			fmt.Printf("%2d) %-12s %-14s %s\n", i+1, it.Key, it.Status, it.Summary)
		}
		n := taskAsk("Start which ticket? ")
		var idx int
		if _, err := fmt.Sscanf(n, "%d", &idx); err != nil || idx < 1 || idx > len(items) {
			return errors.New("no ticket chosen")
		}
		in.Key = items[idx-1].Key
	}
	var started *app.StartResult
	err := call.Env.Confirm(in.yes, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		out, err := call.Invoke(ctx, next)
		switch r := out.(type) {
		case app.StartResult:
			started = &r
		case *app.StartResult:
			started = r
		}
		return out, err
	})
	if started != nil && started.Worktree != "" && started.Next != "" {
		fmt.Fprintf(os.Stderr, "Worktree: %s\n%s\n", started.Worktree, started.Next)
	}
	return err
}

// runTaskCreate is `agentflow task create`: a preview first, then the
// approval a person or --yes gives, then the creation.
func runTaskCreate(ctx context.Context, call command.CLICall[taskCreateIn], in taskCreateIn) error {
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	call.App.Gate.PreviewOnly = in.dryRun || (!in.yes && !interactive)
	if interactive {
		ctx = app.WithJiraWriteCLI(ctx)
	}
	create := func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return call.Invoke(ctx, next)
	}
	out, err := create("")
	if err != nil {
		return err
	}
	p, ok := out.(*app.Pending)
	if !ok {
		return errors.New("expected task create preview")
	}
	if in.dryRun {
		return call.Env.Print(p.Summary, nil)
	}
	if p.Status == "suggest" {
		if err := call.Env.Print(p.Summary, nil); err != nil {
			return err
		}
		return errors.New(p.Reason)
	}
	if p.Status == "prepared" && !in.yes && !interactive {
		if err := taskPrintApproval(call.Env, p); err != nil {
			return err
		}
		return errors.New("pass --yes to approve noninteractive task creation")
	}
	if p.Status == "awaiting_approval" {
		return taskPrintApproval(call.Env, p)
	}
	if p.Status != "needs_confirmation" || p.ConfirmToken == "" {
		return errors.New("missing task creation confirmation")
	}
	if !in.yes {
		release, err := p.OpenPrompt()
		if err != nil {
			return err
		}
		defer release()
		if err := taskPrintApproval(call.Env, p); err != nil {
			return errors.Join(err, p.Cancel())
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("pass --yes to approve noninteractive task creation")
		}
		if !strings.EqualFold(taskAsk("Create this task? [y/N] "), "y") {
			return errors.Join(errors.New("cancelled"), p.Cancel())
		}
	}
	return call.Env.Print(create(p.ConfirmToken))
}
