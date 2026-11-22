package cli

import (
	"context"
	"errors"
	"flag"
	"strings"

	"github.com/nscaledev/devflow/internal/app"
)

func runReleaseGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("release get TAG", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: devflow release get TAG")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	out, err := a.ReleaseGet(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(out)
}

func runReleasePublish(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: devflow release publish TAG --revision SHA [--title T] [--notes TEXT] [--dry-run] [--yes]")
	}
	in := app.ReleasePublishInput{Tag: args[0]}
	fs := flag.NewFlagSet("release publish TAG", flag.ContinueOnError)
	fs.StringVar(&in.Revision, "revision", "", "full commit SHA to publish")
	fs.StringVar(&in.Title, "title", "", "release title (defaults to the tag)")
	fs.StringVar(&in.Notes, "notes", "", "release notes")
	fs.BoolVar(&in.DryRun, "dry-run", false, "preview publication without contacting the provider")
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	flags := args[1:]
	if strings.HasPrefix(in.Tag, "-") {
		flags = args
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.HasPrefix(in.Tag, "-") {
		return errors.New("pass the release tag before its options")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	return confirmLoop(*yes, func(token string) (any, error) { return a.ReleasePublish(ctx, in, token) })
}
