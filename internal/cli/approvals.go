package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/nscaledev/devflow/internal/app"
)

func runPolicy(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: devflow policy explain [ACTION] | status ID | approve ID --confirm-token TOKEN")
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("policy "+cmd, flag.ContinueOnError)
	fs.Bool("json", true, "print JSON")
	var id, token string
	if cmd == "approve" {
		fs.StringVar(&token, "confirm-token", "", "original review token, after explicit approval")
	}
	if len(rest) > 0 && len(rest[0]) > 0 && rest[0][0] != '-' {
		id, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return errors.New("unexpected policy arguments")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	switch cmd {
	case "explain":
		return writeResult(a.PolicyExplain(id))
	case "status":
		if id == "" {
			return errors.New("usage: devflow policy status ID")
		}
		return writeResult(a.ApprovalStatus(id))
	case "approve":
		if id == "" || token == "" {
			return errors.New("usage: devflow policy approve ID --confirm-token TOKEN")
		}
		return writeResult(a.ApproveOperation(id, token))
	default:
		return fmt.Errorf("unknown policy command %q", cmd)
	}
}

func runBranchWrite(ctx context.Context, cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	prepare := fs.Bool("prepare", false, "prepare the operation without applying it")
	yes := fs.Bool("yes", false, "approve the displayed operation")
	token := fs.String("confirm-token", "", "resume an exact approval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments to %s", cmd)
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if *prepare {
		if *yes || *token != "" {
			return errors.New("--prepare cannot be combined with --yes or --confirm-token")
		}
		if cmd == "pr mark-ready" {
			return writeResult(a.MarkReadyPrepare(ctx))
		}
		return writeResult(a.PushPrepare(ctx))
	}
	apply := a.Push
	if cmd == "pr mark-ready" {
		apply = a.MarkReady
	}
	if *token != "" {
		return writeResult(apply(ctx, *token))
	}
	return confirmLoop(*yes, func(t string) (any, error) { return apply(ctx, t) })
}

func writeResult(out any, err error) error {
	if err != nil {
		return err
	}
	return printJSON(out)
}

// An interactive prompt needs the reviewed payload and its policy context.
// Only the machine-facing prepare response returns the bearer credential.
func printApproval(p *app.Pending) error {
	if p.Policy != nil {
		fmt.Fprintf(os.Stderr, "Devflow %s: %s (%s)", p.Policy.Action, p.Policy.Level, p.Policy.Source)
		if p.Approval != nil {
			fmt.Fprintf(os.Stderr, "; request %s, owner %s, expires %s", p.Approval.OperationID, p.Approval.PromptOwner, p.Approval.ExpiresAt.Format("15:04:05 MST"))
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintln(os.Stderr, p.Reason)
	if p.Status == "awaiting_approval" {
		return printJSON(p)
	}
	return printJSON(p.Summary)
}
