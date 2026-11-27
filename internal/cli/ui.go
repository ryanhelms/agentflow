package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// The console's one-time sign-in link goes only to a person at a terminal:
// standard input is a terminal, app.AgentEnvironment() is empty and no AI
// session is set (the rule ai respond --allow uses). Anyone else never sees
// it: without --no-open the browser gets it directly, and --no-open is
// refused before anything starts or a link is minted. This closes the easy
// route only; a process running as the person can still read agentflow's
// private files.

// uiPerson reports whether the caller of agentflow ui is a person at a
// terminal. Tests replace it.
var uiPerson = func() bool { return app.AgentEnvironment() == "" && ops.StdinIsTerminal() }

// uiOpenBrowser opens the console's link. Tests replace it.
var uiOpenBrowser = webui.OpenBrowser

// uiLaunch starts or reuses the console and returns its link. Tests
// replace it.
var (
	uiLaunch    = webui.LaunchCLI
	uiServeOpen = webui.ServeForeground
	uiServeLink = webui.ServeAt
)

// errUILinkNotPerson refuses --no-open for anyone but a person at a terminal.
var errUILinkNotPerson = errors.New("agentflow ui --no-open prints a one-time sign-in link, which only a person at a terminal may see: run `agentflow ui` in your own terminal")

// A single daemon serves unrelated Git repositories. Git's per-invocation
// environment must never redirect its later operations into the first checkout.
func clearWorktreeGitEnv() {
	for _, key := range []string{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
		"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX",
		"GIT_SHALLOW_FILE", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_QUARANTINE_PATH",
	} {
		_ = os.Unsetenv(key)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			_ = os.Unsetenv(key)
		}
	}
}

func runUI(ctx context.Context, args []string) error {
	clearWorktreeGitEnv()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	// The console's startup plumbing: it reports readiness only, never a
	// link (the starter asks the daemon over the control request).
	if len(args) == 5 && args[0] == "--serve-child" {
		port, err := strconv.Atoi(args[4])
		if err != nil || port < 0 || port > 65535 {
			return errors.New("invalid console port")
		}
		return webui.ServeChild(ctx, webui.Destination{Screen: args[1], Role: args[2]}, args[3], port, os.Stdout)
	}

	if len(args) == 1 {
		switch args[0] {
		case "--status":
			running, port, err := webui.DaemonStatus(ctx)
			if err == nil {
				if running {
					fmt.Printf("agentflow UI daemon is running on loopback port %d\n", port)
				} else {
					fmt.Println("agentflow UI daemon is stopped")
				}
			}
			return err
		case "--stop":
			running, _, err := webui.StopDaemon(ctx)
			if err != nil {
				return err
			}
			if running {
				fmt.Println("Requested shutdown of the agentflow UI daemon")
			} else {
				fmt.Println("agentflow UI daemon is already stopped")
			}
			return nil
		}
	}

	f := flag.NewFlagSet("ui", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: agentflow ui [options] [directory] | --status | --stop")
		f.PrintDefaults()
	}
	port := f.Int("port", 0, "loopback port for the daemon (default: reuse its port, otherwise try 80, then an available port)")
	noOpen := f.Bool("no-open", false, "print the one-time launch URL without opening a browser (only a person at a terminal)")
	foreground := f.Bool("foreground", false, "keep a newly started daemon attached to this terminal")
	screen := f.String("screen", "", "open overview, providers, autonomy, plugins, harnesses, ai, configuration, or handoff (default: overview)")
	role := f.String("role", "", "select a role in the provider editor, such as task_management")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 1 || *port < 0 || *port > 65535 {
		return errors.New("usage: agentflow ui [--screen NAME] [--role ROLE] [--no-open] [--foreground] [--port N] [directory]")
	}
	dest, err := (webui.Destination{Screen: *screen, Role: *role}).Normalize("overview")
	if err != nil {
		return err
	}
	dir := "."
	if f.NArg() == 1 {
		dir = f.Arg(0)
	}
	person := uiPerson()
	if *noOpen && !person {
		return errUILinkNotPerson
	}
	ready := func(address string, _ bool) {
		if address == "" {
			fmt.Fprintln(os.Stdout, "Opened the requested screen in the existing agentflow tab.")
			return
		}
		if !person {
			// The link goes to the browser only, never to this output.
			if err := uiOpenBrowser(ctx, address); err != nil {
				fmt.Fprintln(os.Stderr, "agentflow could not open a browser. Run `agentflow ui` in your own terminal to get a sign-in link.")
				return
			}
			fmt.Fprintln(os.Stdout, "Opened the console in your browser.")
			return
		}
		fmt.Fprintln(os.Stdout, address)
		if !*noOpen {
			if err := uiOpenBrowser(ctx, address); err != nil {
				fmt.Fprintln(os.Stderr, "Open the printed URL in a local browser.")
			}
		}
	}
	if *foreground {
		serve := uiServeOpen
		if *noOpen {
			serve = uiServeLink
		}
		return linkFree(serve(ctx, dir, *port, dest, ready), person)
	}
	address, err := uiLaunch(ctx, dir, *port, dest, *noOpen)
	if err == nil {
		ready(address, false)
	}
	return linkFree(err, person)
}

// linkFree keeps a sign-in link out of an error shown to anyone but a
// person at a terminal.
func linkFree(err error, person bool) error {
	if err == nil || person || !strings.Contains(err.Error(), "launch=") {
		return err
	}
	return errors.New("the console could not start. Run `agentflow ui` in your own terminal")
}
