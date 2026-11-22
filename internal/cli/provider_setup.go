package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/hooks"
	"github.com/nscaledev/devflow/internal/setup"
)

func runProviderSetup(ctx context.Context, args []string) error {
	op := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		op, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("setup "+op, flag.ContinueOnError)
	client := fs.String("client", "", "project adapter, including desktop clients without a CLI: "+strings.Join(setup.Clients, ", "))
	asJSON := fs.Bool("json", false, "print the typed setup plan as JSON")
	var yes bool
	if op == "apply" {
		fs.BoolVar(&yes, "yes", false, "approve registering the named project adapter")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: devflow setup [plan|verify|apply] [--client NAME] [--json]")
	}
	var plan *setup.ProviderSetupPlan
	switch op {
	case "plan", "verify":
		var err error
		plan, err = setup.PlanProviderSetup(ctx, ".", *client)
		if err != nil {
			return err
		}
	case "apply":
		if *client == "" {
			return errors.New("setup apply requires --client NAME")
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		result, err := a.ApplyProviderSetup(ctx, *client, "")
		if err != nil {
			return err
		}
		if pending, ok := result.(*app.Pending); ok {
			if pending.Status == "suggest" {
				return fmt.Errorf("project registration requires local write permission: %s", pending.Reason)
			}
			if !yes {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return errors.New("review devflow setup plan, then pass --yes to approve noninteractive registration")
				}
				if err := printJSON(pending.Summary); err != nil {
					return err
				}
				if !strings.EqualFold(ask("Register this project adapter? [y/N] "), "y") {
					return errors.New("cancelled")
				}
			}
			result, err = a.ApplyProviderSetup(ctx, *client, pending.ConfirmToken)
			if err != nil {
				return err
			}
		}
		var ok bool
		plan, ok = result.(*setup.ProviderSetupPlan)
		if !ok {
			return errors.New("setup changed during confirmation. Review the current plan")
		}
	default:
		return errors.New("usage: devflow setup [plan|verify|apply] [--client NAME] [--json]")
	}
	if *asJSON {
		if err := printJSON(plan); err != nil {
			return err
		}
	} else {
		fmt.Printf("Provider setup in %s (%s/%s)\n", plan.Root, plan.Platform.OS, plan.Platform.Arch)
		for _, r := range plan.Requirements {
			state := "ready"
			if !r.Ready {
				state = "needs attention"
			}
			fmt.Printf("%s  %s (%s)  %s\n", state, r.Role, r.Provider, r.Detail)
		}
		if r := plan.Registration; r != nil {
			fmt.Printf("%s  %s  %s\n", r.State, r.Path, r.Detail)
		}
		fmt.Println("Ready:", plan.Ready)
	}
	if op != "plan" && !plan.Ready {
		return &hooks.ExitError{Code: 1}
	}
	return nil
}
