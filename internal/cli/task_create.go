package cli

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/tasktemplate"
	"golang.org/x/term"
)

func runTask(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "link" {
		return runTaskLink(ctx, args[1:])
	}
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: devflow task create [flags] | devflow task link KEY --url URL [--title T] [--yes]")
	}
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	project := fs.String("project", "", "project or scope")
	typ := fs.String("type", "", "provider task type")
	title := fs.String("summary", "", "task title")
	description := fs.String("description", "", "task description")
	link := fs.String("link-to", "", "related task or supported knowledge reference")
	var pin tasktemplate.Ref
	fs.StringVar(&pin.Name, "template", "", "exact catalog template name")
	fs.StringVar(&pin.Revision, "template-revision", "", "exact catalog revision")
	yes := fs.Bool("yes", false, "approve creation under the configured policy")
	dry := fs.Bool("dry-run", false, "print the resolved create preview")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments after task create flags")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		ctx = app.WithJiraWriteCLI(ctx)
	}
	create := func(token string) (any, error) {
		return a.CreateTicketWithTemplate(ctx, *project, *typ, *title, *description, *link, pin, token)
	}
	out, err := create("")
	if err != nil {
		return err
	}
	p, ok := out.(*app.Pending)
	if !ok {
		return errors.New("expected task create preview")
	}
	if *dry {
		return printJSON(p.Summary)
	}
	if p.Status == "suggest" {
		if err := printJSON(p.Summary); err != nil {
			return err
		}
		return errors.New(p.Reason)
	}
	if p.Status != "needs_confirmation" || p.ConfirmToken == "" {
		return errors.New("missing task creation confirmation")
	}
	if !*yes {
		if err := printJSON(p.Summary); err != nil {
			return err
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("pass --yes to approve noninteractive task creation")
		}
		if !strings.EqualFold(ask("Create this task? [y/N] "), "y") {
			return errors.New("cancelled")
		}
	}
	out, err = create(p.ConfirmToken)
	if err != nil {
		return err
	}
	return printJSON(out)
}

func runTaskLink(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: devflow task link KEY --url URL [--title T] [--yes]")
	}
	fs := flag.NewFlagSet("task link", flag.ContinueOnError)
	url := fs.String("url", "", "external web link")
	title := fs.String("title", "", "link title, defaults to the URL")
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *url == "" {
		return errors.New("usage: devflow task link KEY --url URL [--title T] [--yes]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	return confirmLoop(*yes, func(token string) (any, error) {
		return a.LinkJiraRemote(ctx, args[0], *url, *title, token)
	})
}
