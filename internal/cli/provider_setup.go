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
	switch op {
	case "providers":
		return runProviderList(ctx, args)
	case "login":
		return runProviderLogin(ctx, args)
	case "logout":
		return runProviderLogout(ctx, args)
	}
	fs := flag.NewFlagSet("setup "+op, flag.ContinueOnError)
	client := fs.String("client", "", "project adapter, including desktop clients without a CLI: "+strings.Join(setup.SetupClients, ", "))
	asJSON := fs.Bool("json", false, "print the typed setup plan as JSON")
	companion := fs.String("companion", "", "scope setup to one user-declared provider MCP companion")
	var yes, installGH, installMCP bool
	if op == "apply" {
		fs.BoolVar(&yes, "yes", false, "approve the selected local setup action")
		fs.BoolVar(&installMCP, "install", false, "install only the pinned --companion ID")
		fs.BoolVar(&installGH, "install-gh", false, "install the pinned gh companion only")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: devflow setup [plan|verify|apply] [--companion ID] [--client NAME] [--json]")
	}
	var plan *setup.ProviderSetupPlan
	switch op {
	case "plan", "verify":
		var err error
		plan, err = setup.PlanSetup(ctx, ".", *client, *companion)
		if err != nil {
			return err
		}
	case "apply":
		if *client == "" && !installGH && !installMCP {
			return errors.New("setup apply requires --client NAME, --install-gh or --companion ID --install")
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		action := app.SetupAction{Client: *client, Companion: *companion, InstallGH: installGH, InstallMCP: installMCP}
		result, err := a.ApplySetup(ctx, action, "")
		if err != nil {
			return err
		}
		if pending, ok := result.(*app.Pending); ok {
			if pending.Status == "suggest" {
				return fmt.Errorf("setup apply requires local write permission: %s", pending.Reason)
			}
			if !yes {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return errors.New("review devflow setup plan, then pass --yes to approve the selected setup action")
				}
				if err := printJSON(pending.Summary); err != nil {
					return err
				}
				if !strings.EqualFold(ask("Apply this setup action? [y/N] "), "y") {
					return errors.New("cancelled")
				}
			}
			result, err = a.ApplySetup(ctx, action, pending.ConfirmToken)
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
		return errors.New("usage: devflow setup [plan|verify|apply] [--companion ID] [--client NAME] [--json]")
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
		for _, tools := range plan.Tooling {
			fmt.Printf("%s  preferred MCP: %s (%s)\n", tools.Provider, tools.MCP.Name, tools.MCP.Endpoint)
			fmt.Printf("  Setup: %s\n", tools.MCP.Guide)
			if tools.CLI != nil {
				fmt.Printf("  CLI fallback: %s (installed: %t) · %s\n", tools.CLI.Command, tools.CLI.Installed, tools.CLI.Guide)
			}
		}
		if i := plan.InstallGH; i != nil {
			a := i.Artifact
			fmt.Printf("Pinned gh %d.%d.%d from %s (SHA256 %s) under %s\n", a.Version.Major, a.Version.Minor, a.Version.Patch, a.Source, a.SHA256, i.Home)
		}
		for _, s := range plan.Companions {
			if s.Endpoint != "" {
				fmt.Printf("%s  %s\n", s.ID, s.Endpoint)
			}
			if s.Command != "" {
				fmt.Printf("%s  %s\n", s.ID, s.Command)
			}
			if s.AuthEnv != "" {
				fmt.Printf("%s uses environment variable %s\n", s.ID, s.AuthEnv)
			}
			if s.Install != nil {
				a := s.Install.Artifact
				fmt.Printf("Pinned %s %d.%d.%d from %s (SHA256 %s) under %s\n", s.ID, a.Version.Major, a.Version.Minor, a.Version.Patch, a.Source, a.SHA256, s.Install.Home)
			}
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

func runProviderList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup providers", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the provider list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: devflow setup providers [--json]")
	}
	list, err := setup.ListProviders(ctx, ".")
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(list)
	}
	if !list.ConfigFound {
		fmt.Printf("No .devflow/config.yml in %s; showing defaults.\n", list.Root)
	}
	for _, r := range list.Roles {
		fmt.Println(r.Role)
		for _, o := range r.Options {
			mark := " "
			if o.Selected {
				mark = "*"
			}
			var tags []string
			if o.Default {
				tags = append(tags, "default")
			}
			if o.Readiness != setup.ReadinessNone {
				tags = append(tags, o.Readiness+" readiness")
			} else {
				tags = append(tags, "disables the role")
			}
			fmt.Printf("  %s %-14s %s\n", mark, o.ID, strings.Join(tags, ", "))
		}
	}
	fmt.Println(`
* selected
built-in           devflow checks the provider directly
client             verify with the client MCP or a declared companion (Jira and Confluence also support signed-in Teamwork Graph CLI)
client-or-headless use client verification, or run headless with ASANA_ACCESS_TOKEN for a direct Asana check
companion          setup checks a declared MCP companion`)
	return nil
}

func runProviderLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup login", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to authenticate")
	client := fs.String("client", "", "MCP client, such as claude-desktop or codex")
	server := fs.String("server", "", "existing MCP server name if different from the provider default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *provider == "" || fs.NArg() != 0 {
		return errors.New("usage: devflow setup login --provider NAME [--client NAME] [--server NAME]")
	}
	return setup.LoginProvider(ctx, ".", *provider, *client, *server, os.Stdin, os.Stdout, os.Stderr)
}

func runProviderLogout(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup logout", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to disconnect")
	client := fs.String("client", "", "the client that owns the enrollment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *provider != "jira" || *client != "devflow" || fs.NArg() != 0 {
		return errors.New("usage: devflow setup logout --provider jira --client devflow")
	}
	return setup.DisconnectRovo(ctx)
}
