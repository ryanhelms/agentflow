package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/worktree"
)

func runFinish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("finish", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be removed")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: agentflow finish [BRANCH] [--dry-run] [--yes]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	var last any
	err = confirmLoop(*yes, func(token string) (any, error) {
		out, err := a.Finish(ctx, fs.Arg(0), *dryRun, token)
		last = out
		return out, err
	})
	var refused *worktree.RefusedError
	if errors.As(err, &refused) {
		_ = printJSON(refused.Outcome)
	}
	if res, ok := last.(app.FinishResult); ok && err == nil && res.State == worktree.StateFinished {
		fmt.Fprintf(os.Stderr, "Primary checkout: %s\n", res.Primary)
	}
	return err
}

func runSweep(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	allRepos := fs.Bool("all-repos", false, "every repository under worktree.root and in worktree.checkouts")
	dryRun := fs.Bool("dry-run", false, "report only; finish nothing")
	stale := fs.Int("stale-days", 7, "report worktrees with no commits for longer than this")
	asJSON := fs.Bool("json", false, "print JSON")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: agentflow sweep [--all-repos] [--dry-run] [--stale-days N] [--json] [--yes]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	req := app.SweepRequest{AllRepos: *allRepos, DryRun: *dryRun, StaleDays: *stale}
	var res *worktree.SweepResult
	_, done, err := confirmed(*yes, func(token string) (any, error) {
		r, pending, err := a.Sweep(ctx, req, token)
		res = r
		if pending != nil {
			return pending, err
		}
		return r, err
	})
	if done || res == nil {
		return err
	}
	if *asJSON {
		if perr := printJSON(res); perr != nil {
			return perr
		}
	} else {
		printSweep(os.Stdout, res)
	}
	return err
}

func printSweep(w io.Writer, res *worktree.SweepResult) {
	if len(res.Worktrees) == 0 {
		fmt.Fprintln(w, "No linked worktrees.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tBRANCH\tSTATE\tDETAIL\tPATH")
	for _, o := range res.Worktrees {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", o.Repo, o.Branch, o.State, o.Detail, o.Path)
	}
	_ = tw.Flush()
}

func runPromote(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	from := fs.String("from", "", "integration branch (default: origin's default branch, or develop)")
	to := fs.String("to", "main", "release branch")
	dryRun := fs.Bool("dry-run", false, "show the PR without opening it")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	return confirmLoop(*yes, func(token string) (any, error) {
		return a.Promote(ctx, *from, *to, *dryRun, token, nil)
	})
}

func runWorktree(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "go-work" {
		return errors.New("usage: agentflow worktree go-work")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	out, err := a.WorktreeGoWork(ctx)
	if err != nil {
		return err
	}
	return printJSON(out)
}
