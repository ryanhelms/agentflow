// Package cli implements the agentflow command line.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/auth"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/deliver"
	_ "github.com/ryanhelms/agentflow/internal/command/ops" // registers the operations
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/hooks"
	"github.com/ryanhelms/agentflow/internal/mcpserver"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/codexhost"
	"github.com/ryanhelms/agentflow/internal/provider/desktophost"
	"github.com/ryanhelms/agentflow/internal/provider/native"
	"github.com/ryanhelms/agentflow/internal/provider/slackhost"
	"github.com/ryanhelms/agentflow/internal/setup"
	"github.com/ryanhelms/agentflow/internal/setup/initproviders"
	"github.com/ryanhelms/agentflow/internal/update"
	"github.com/ryanhelms/agentflow/internal/version"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// usage is the usage text: the header, then every section's blocks, from the
// command registry and from legacyUsage for commands not migrated yet.
// TestUsageGolden pins it byte for byte.
var usage = renderUsage()

// renderUsage renders the usage text from the registry's blocks and the
// legacy blocks whose path the registry does not bind.
func renderUsage() string {
	registered := command.Default.Usage()
	bound := map[string]bool{}
	for _, b := range registered {
		bound[b.Path] = true
	}
	blocks := append([]command.UsageBlock(nil), registered...)
	for _, b := range legacyUsage {
		if !bound[b.Path] {
			blocks = append(blocks, b)
		}
	}
	command.SortUsage(blocks)
	var out strings.Builder
	out.WriteString(usageHeader)
	var section command.Section
	for _, b := range blocks {
		if b.Section != section {
			section = b.Section
			out.WriteString("\n" + string(section) + "\n")
		}
		out.WriteString(b.Text + "\n")
	}
	return out.String()
}

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	ctx := context.Background()
	if len(args) > 0 && args[0] == "--native-client" {
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: agentflow --native-client NAME COMMAND")
			return 1
		}
		command := args[2]
		if command == "task" && len(args) > 3 && args[3] == "create" {
			command = "task create"
		}
		writer, err := nativeWriter(args[1], command)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentflow:", err)
			return 1
		}
		defer writer.Close()
		ctx = native.WithWriteHost(ctx, writer)
		if command == "comment" || command == "task create" {
			ctx = app.WithJiraWriteCLI(ctx)
		}
		args = args[2:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	if cmd == "doctor" && len(rest) > 0 && rest[0] == "--providers" {
		cmd, rest = "setup", append([]string{"verify"}, rest[1:]...)
	}
	if !quietCommand(cmd) && (cmd != "init" || !initInspection(rest)) {
		maybeUpdate(ctx)
	}
	err := run(ctx, cmd, rest)
	var ee *hooks.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.Code
	case errors.Is(err, flag.ErrHelp):
		return 0
	default:
		fmt.Fprintln(os.Stderr, "agentflow:", err)
		return 1
	}
}

func nativeWriter(client, command string) (*native.SessionWriter, error) {
	switch client {
	case "codex":
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return codexhost.NewWriter(dir), nil
	case "claude-desktop":
		switch command {
		case "comment", "start", "transition", "task create":
			return desktophost.NewWriter(), nil
		case "notify":
			return slackhost.NewWriter(), nil
		default:
			return nil, errors.New("usage: agentflow --native-client claude-desktop COMMAND (comment, start, transition, task create or notify)")
		}
	default:
		return nil, fmt.Errorf("unsupported native client %q", client)
	}
}

// quietCommand: commands that must stay fast and print nothing extra.
func quietCommand(cmd string) bool {
	switch cmd {
	case "config", "policy", "hook", "lint-msg", "keys", "mcp", "ui", "events", "notify", "setup", "secops", "update", "version", "help":
		return true
	}
	return false
}

// invoker runs the registered operations of CLI commands. The MCP server has
// its own, with the same options.
var invoker = command.NewInvoker(command.Options{Emitters: deliver.Source(deliver.Options{}), Auditor: command.AuditLog{}})

func run(ctx context.Context, cmd string, args []string) error {
	// Registered operations first, by their longest CLI path; the switch
	// below runs the commands not migrated yet and the runtime plumbing.
	if dispatch, rest, ok := command.Default.Dispatch(append([]string{cmd}, args...)); ok {
		return dispatch(ctx, cliEnv{}, invoker, rest)
	}
	// A registered operation whose command line still runs a handler
	// below: run the registered stages (plugin permission rules) first.
	if m, ok := command.Default.Legacy(append([]string{cmd}, args...)); ok {
		if err := command.CheckStages(ctx, m, command.Origin{Source: "cli", Client: "cli"}, command.Detail{}); err != nil {
			return err
		}
	}
	switch cmd {
	case "ai":
		if len(args) > 0 && args[0] == "host" {
			return runAIHost(ctx, args[1:])
		}
	case "policy":
		return runPolicy(ctx, args)
	case "push":
		return runBranchWrite(ctx, "push", args)
	case "ui":
		return runUI(ctx, args)
	case "mcp":
		fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
		client := fs.String("client", "", "calling client name")
		uiOnly := fs.Bool("ui-only", false, "expose only agentflow_ui_open for local console requests")
		autoOpen := fs.Bool("auto-open", false, "the host explicitly identifies an interactive lead session; open its local console at startup")
		session := fs.String("session", "", "serve AI session ID's read-only tools (agentflow passes this to a harness)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *session != "" {
			return mcpserver.ServeSession(ctx, *session)
		}
		u, _ := newUpdater(ctx)
		return mcpserver.Serve(ctx, *client, u, *uiOnly, *autoOpen)
	case "hook":
		return runHook(ctx, args)
	case "lint-msg":
		if len(args) == 1 && args[0] == "--github-event" {
			return validateWorkflowPR(ctx)
		}
		if len(args) != 1 {
			return errors.New("usage: agentflow lint-msg FILE")
		}
		return hooks.CommitMsg(ctx, args[0], os.Stderr)
	case "keys":
		return runTaskKeys(ctx, args)
	case "init":
		return runInit(ctx, args)
	case "remove":
		return runRemove(ctx, args)
	case "pin":
		return runPin(ctx, args)
	case "setup":
		return runProviderSetup(ctx, args)
	case "notify":
		return runNotify(ctx, args)
	case "install":
		return runInstall(ctx, args)
	case "login":
		return runLogin(ctx, args)
	case "logout":
		return runLogout(ctx, args)
	case "doctor":
		return runDoctor(ctx, args)
	case "task":
		return runTask(ctx, args)
	case "work":
		a, err := open(ctx)
		if err != nil {
			return err
		}
		items, err := a.MyWork(ctx)
		if err != nil {
			return err
		}
		for _, i := range items {
			fmt.Printf("%-12s %-14s %-8s %s\n", i.Key, i.Status, i.Priority, i.Summary)
		}
		return nil
	case "start":
		return runStart(ctx, args)
	case "finish":
		return runFinish(ctx, args)
	case "sweep":
		return runSweep(ctx, args)
	case "delegate":
		return runDelegate(ctx, args)
	case "promote":
		return runPromote(ctx, args)
	case "worktree":
		return runWorktree(ctx, args)
	case "context":
		a, err := open(ctx)
		if err != nil {
			return err
		}
		key := ""
		if len(args) > 0 {
			key = args[0]
		}
		tc, err := a.Context(ctx, key)
		if err != nil {
			return err
		}
		fmt.Print(tc.Text)
		return nil
	case "commit":
		return runCommit(ctx, args)
	case "preview":
		return runPreview(ctx, args)
	case "transition":
		return runTransition(ctx, args)
	case "secops":
		return runSecOps(ctx, args)
	case "pr":
		return runPR(ctx, args)
	case "review":
		return runReview(ctx, args)
	case "status":
		a, err := open(ctx)
		if err != nil {
			return err
		}
		out, err := a.ShipStatus(ctx)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "standup":
		fs := flag.NewFlagSet("standup", flag.ContinueOnError)
		since := fs.Duration("since", 0, "look back this long (default: since the last working day)")
		asJSON := fs.Bool("json", false, "print raw JSON")
		if err := fs.Parse(args); err != nil {
			return err
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		out, err := a.Standup(ctx, *since)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(out)
		}
		printStandup(out)
		return nil
	case "events":
		return runEvents(ctx, args)
	case "ac":
		return runAC(ctx, args)
	case "scope":
		return runScope(ctx, args)
	case "comment":
		return runComment(ctx, args)
	case "eod":
		return runEOD(ctx, args)
	case "collisions":
		return runCollisions(ctx, args)
	case "docs":
		return runDocs(ctx, args)
	case "adr":
		if len(args) < 2 || args[0] != "new" {
			return errors.New("usage: agentflow adr new TITLE")
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		return confirmLoop(false, func(token string) (any, error) {
			return a.ADRDraft(ctx, strings.Join(args[1:], " "), "", "", "", token)
		})
	case "release":
		return runRelease(ctx, args)
	case "update":
		return runUpdate(ctx, args)
	case "version", "--version", "-v":
		return runVersion(ctx)
	}
	return fmt.Errorf("unknown command %q; run agentflow help", cmd)
}

// cliEnv is the command.CLIEnv of derived commands: the same open,
// confirmation flow and output as the legacy commands.
type cliEnv struct{}

func (cliEnv) Open(ctx context.Context) (*app.App, error) { return open(ctx) }

func (cliEnv) Origin(a *app.App) command.Origin {
	return command.Origin{Source: "cli", Client: a.Client}
}

func (cliEnv) Confirm(yes bool, fn func(token string) (any, error)) error {
	return confirmLoop(yes, fn)
}

func (cliEnv) Print(v any, err error) error { return writeResult(v, err) }

func open(ctx context.Context) (*app.App, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return app.Open(ctx, dir, "cli")
}

func runHook(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow hook commit-msg FILE | pre-push [REMOTE URL] | post-commit | session-start | pre-bash | stop")
	}
	name := args[0]
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	client := fs.String("client", "", "calling client")
	conventionOnly := fs.Bool("convention-only", false, "pre-bash: check messages, titles, the repository boundary and the plugin add, remove and approve rule; skip the rest of the never-list")
	rest := args[1:]
	var positional []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	switch name {
	case "commit-msg":
		if len(positional) != 1 {
			return errors.New("usage: agentflow hook commit-msg FILE")
		}
		return hooks.CommitMsgHook(ctx, positional[0], os.Stderr)
	case "post-commit":
		// git has already made the commit: this hook only tells the console and must not fail it.
		_ = hooks.PostCommit(ctx)
		return nil
	case "session-start":
		notice := ""
		if u, err := newUpdater(ctx); err == nil && u != nil {
			if err := u.Enforce(ctx); err != nil {
				notice = "agentflow: " + err.Error()
			}
			if n := u.TakeNotice(); n != "" {
				notice = strings.TrimSpace(notice + "\n" + n)
			}
			spawnBackgroundUpdate(u)
		}
		if err := hooks.SessionStart(ctx, *client, notice, os.Stdout); err != nil {
			return err
		}
		if *client == "claude" {
			// A manual hook invocation in a terminal has no event to consume.
			if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
				webui.OpenClaudeSession(ctx, os.Stdin)
			}
		}
		return nil
	case "pre-bash":
		return hooks.PreBash(ctx, os.Stdin, os.Stderr, *conventionOnly)
	case "pre-push":
		return hooks.PrePush(ctx, os.Stdin, os.Stderr, positional...)
	case "stop":
		return hooks.Stop(ctx, *client, os.Stdin, os.Stdout)
	}
	return fmt.Errorf("unknown hook %q", name)
}

func runInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite existing files")
	guidanceOnly := fs.Bool("guidance-only", false, "refresh only the generated AGENTS.md block")
	guided := fs.Bool("guided", false, "prompt for unknown providers and review selections")
	headless := fs.Bool("non-interactive", false, "use existing selections and defaults without prompting")
	detect := fs.Bool("detect-providers", false, "print repository provider evidence as JSON without changing anything")
	answers := map[string]string{}
	fs.Func("provider", "persist a provider for ROLE=ID (repeatable)", func(value string) error { return providerArgument(answers, value) })
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("init operates on the current checkout; unexpected positional argument")
	}
	if *guided && *headless {
		return errors.New("choose either --guided or --non-interactive")
	}
	if *detect && (*guided || *headless || *force || *guidanceOnly || len(answers) > 0) {
		return errors.New("run init --detect-providers separately from initialization")
	}
	if *guidanceOnly && (*guided || *headless || len(answers) > 0) {
		return errors.New("provider selection is unavailable with --guidance-only")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return err
	}
	if *guidanceOnly {
		if *force {
			return errors.New("init --guidance-only cannot be combined with --force")
		}
		res, err := setup.RefreshAgentInstructions(repo.Dir)
		printResult(res)
		return err
	}
	// Only a person at a terminal answers the personal setup questions:
	// --guided alone does not ask them, so an agent piping answers cannot.
	interactive := !*headless && initStdinIsTerminal()
	prompt := *guided || interactive
	var home setup.HomeQuestions
	if !*detect {
		if home, err = setup.CheckHomeQuestions(); err != nil {
			return err
		}
		if home.Needed && interactive {
			if err := askHomeQuestions(home.Path, initStdin, initPromptOut); err != nil {
				return err
			}
			home.Needed = false
		}
	}
	var edit *initproviders.Edit
	if *detect || prompt || len(answers) > 0 {
		plan, err := initproviders.Detect(ctx, repo)
		if err != nil {
			return err
		}
		if *detect {
			return printJSON(plan)
		}
		if prompt {
			if err := guideProviders(plan, answers, initStdin, initPromptOut); err != nil {
				return err
			}
		}
		edit, err = plan.Prepare(answers, *force)
		if err != nil {
			return err
		}
	}
	res, err := app.InitRepo(ctx, repo.Dir, "cli", *force)
	if res != nil {
		printResult(res.Files)
	}
	if err != nil {
		return err
	}
	if edit != nil {
		if err := setup.SaveInitProviderConfig(repo.Dir, edit.Before, edit.After); err != nil {
			return err
		}
		fmt.Println("  updated  .agentflow/config.yml provider selections")
	}
	rel := res.Release
	fmt.Printf("Release units (%s strategy):\n", rel.Strategy)
	for _, u := range rel.Units {
		fmt.Printf("  %-16s %-22s %s\n", u.Name, u.Path, strings.Join(u.Builds, ","))
	}
	for _, f := range rel.Written {
		fmt.Println("  wrote   ", f)
	}
	if home.Needed {
		fmt.Print(setup.HomeQuestionsText(home.Path))
	}
	return nil
}

func runRemove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	var opt setup.RemoveOptions
	fs.BoolVar(&opt.Force, "force", false, "also delete template files changed since agentflow init")
	fs.BoolVar(&opt.DryRun, "dry-run", false, "list what would change without changing anything")
	fs.BoolVar(&opt.KeepReleases, "keep-releases", false, "keep release-please-config.json and .release-please-manifest.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, _ := os.Getwd()
	res, err := app.RemoveRepo(ctx, dir, opt)
	if opt.DryRun {
		fmt.Println("Dry run: nothing changed.")
	}
	printResult(res)
	return err
}

func runPin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	check := fs.Bool("check", false, "report only; exit 1 when the workflow or a stock launcher file is behind this release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, _ := os.Getwd()
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return err
	}
	res, err := setup.Pin(repo.Dir, *check)
	if res == nil {
		return err
	}
	// With an error, res still lists everything pin changed before and after it.
	from := res.From
	if from == "" {
		from = "floating refs"
	}
	switch {
	case res.File == "":
	case !res.Changed:
		fmt.Printf("%s is pinned to %s already.\n", res.File, res.To)
	case *check:
		fmt.Printf("%s: %s, agentflow %s is installed. Run agentflow pin.\n", res.File, from, res.To)
	default:
		fmt.Printf("%s: %s -> %s. Commit it on a branch as `chore: pin agentflow workflows to %s` and open a PR.\n", res.File, from, res.To, res.To)
	}
	updated := false
	for _, f := range res.Launchers {
		switch f.State {
		case setup.LauncherOutdated:
			fmt.Printf("%s is a stock file from an older release. Run agentflow pin.\n", f.Path)
		case setup.LauncherUpdated:
			fmt.Printf("%s: replaced the stock file with the one from %s.\n", f.Path, res.To)
			updated = true
		case setup.LauncherCurrent:
		case setup.LauncherFailed:
			fmt.Fprintf(os.Stderr, "agentflow: %s is a stock file from an older release, but %s\n", f.Path, f.Detail)
		default:
			fmt.Fprintf(os.Stderr, "agentflow: %s: %s\n", f.Path, f.Detail)
		}
	}
	if updated && !res.Changed {
		fmt.Printf("Commit the launcher files on a branch as `chore: update agentflow launchers to %s` and open a PR.\n", res.To)
	}
	if res.File == "" && !res.Behind() {
		fmt.Printf("The stock launcher files are up to date; this repository has no %s.\n", setup.CallerWorkflow)
	}
	if err != nil {
		return err
	}
	if *check && res.Behind() {
		return &hooks.ExitError{Code: 1}
	}
	return nil
}

func runInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	client := fs.String("client", "all", "all, or a comma-separated list: "+strings.Join(setup.Clients, ","))
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, _ := os.Getwd()
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return err
	}
	clients := setup.Detect()
	if *client != "all" {
		clients = strings.Split(*client, ",")
	} else if len(clients) == 0 {
		fmt.Println("No AI client CLIs found on PATH; writing AGENTS.md only. Use --client to choose.")
	}
	res, err := setup.Install(repo.Dir, clients)
	printResult(res)
	return err
}

func printResult(res *setup.Result) {
	if res == nil {
		return
	}
	for _, f := range res.Written {
		fmt.Println("  wrote   ", f)
	}
	for _, f := range res.Merged {
		fmt.Println("  updated ", f)
	}
	for _, f := range res.Removed {
		fmt.Println("  removed ", f)
	}
	for _, f := range res.Skipped {
		fmt.Println("  kept    ", f, "(exists; --force to overwrite)")
	}
	for _, n := range res.Notes {
		fmt.Println("•", n)
	}
}

func runLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	email := fs.String("email", "", "Atlassian account email (default: git user.email)")
	site := fs.String("site", "", "Atlassian site (default: the site in .agentflow/config.yml)")
	stdin := fs.Bool("token-stdin", false, "read the API token from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, _ := os.Getwd()
	if repo, err := gitx.Open(ctx, dir); err == nil {
		if *site == "" {
			*site = config.AtlassianSite(repo.Dir)
		}
		if *email == "" {
			*email = repo.UserEmail(ctx)
		}
	}
	if *site == "" {
		return errors.New("--site is required: set task_management.settings.site in .agentflow/config.yml or pass it")
	}
	if *email == "" {
		return errors.New("--email is required")
	}
	var token string
	if *stdin {
		b, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	} else {
		fmt.Printf("Create an API token at https://id.atlassian.com/manage-profile/security/api-tokens\nAtlassian API token for %s on %s: ", *email, *site)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	cred := auth.Atlassian{Site: *site, Email: *email, Token: token}
	me, err := atlassian.New(cred).Myself(ctx)
	if err != nil {
		return fmt.Errorf("token rejected by %s: %w", *site, err)
	}
	if err := auth.SaveAtlassian(cred); err != nil {
		return err
	}
	fmt.Printf("Signed in to %s as %s. Token saved in the OS keychain.\n", *site, me.DisplayName)
	if _, err := auth.GitHubToken(ctx, ""); err != nil {
		fmt.Println("GitHub: run `gh auth login` so agentflow can open PRs and install updates.")
	}
	return nil
}

func runLogout(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	site := fs.String("site", "", "Atlassian site (default: the site in .agentflow/config.yml)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *site == "" {
		a, err := open(ctx)
		if err != nil {
			return err
		}
		*site = config.AtlassianSite(a.Repo.Dir)
	}
	if *site == "" {
		return errors.New("no Atlassian site: pass --site or set task_management.settings.site in .agentflow/config.yml")
	}
	return auth.DeleteAtlassian(*site)
}

func runStart(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	wt := fs.Bool("worktree", false, "create a worktree")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	key := fs.Arg(0)
	if key == "" {
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
		n := ask("Start which ticket? ")
		var idx int
		if _, err := fmt.Sscanf(n, "%d", &idx); err != nil || idx < 1 || idx > len(items) {
			return errors.New("no ticket chosen")
		}
		key = items[idx-1].Key
	}
	var started *app.StartResult
	err = confirmLoop(*yes, func(tok string) (any, error) {
		out, err := a.Start(ctx, key, *wt, tok)
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

func runCommit(ctx context.Context, args []string) error {
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if *prepare || *token != "" || len(paths) > 0 {
		if *msg == "" || *split || *dry || (*prepare && (*token != "" || *yes)) {
			return errors.New("use -m MSG for an exact commit and select one of --prepare or --confirm-token")
		}
		if *prepare {
			return writeResult(a.CommitPrepare(ctx, *msg, paths))
		}
		if *token != "" {
			return writeResult(a.Commit(ctx, *msg, paths, *token))
		}
		return confirmLoop(*yes, func(tok string) (any, error) { return a.Commit(ctx, *msg, paths, tok) })
	}
	if *split {
		groups, err := a.CommitPlan(ctx)
		if err != nil {
			return err
		}
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
		cc, err := a.CommitContext(ctx, nil)
		if err != nil {
			return err
		}
		var keys []string
		if !a.UsesTaskRefs() || a.Cfg.Commit.KeyRequired() {
			key := cc.Key
			if a.UsesTaskRefs() {
				key = ""
			}
			if k := ask(fmt.Sprintf("Jira key [%s]: ", key)); k != "" {
				key = strings.ToUpper(k)
			}
			keys = []string{key}
		}
		typ := cc.SuggestedType
		if t := ask(fmt.Sprintf("Type (%s) [%s]: ", strings.Join(a.Cfg.Commit.Types, "/"), typ)); t != "" {
			typ = t
		}
		summary := ask("Summary (imperative, e.g. \"add quota API\"): ")
		body := ask("Why (optional, one line): ")
		breaking := len(cc.Breaking) > 0 && strings.HasPrefix(strings.ToLower(ask(fmt.Sprintf("Breaking change? %v [y/N]: ", cc.Breaking))), "y")
		verb := ask("Verb (start/review/qa/done/block, optional): ")
		var verbs, trailers []string
		if verb != "" {
			verbs = []string{strings.TrimPrefix(verb, "#")}
		}
		if breaking {
			trailers = append(trailers, "BREAKING CHANGE: "+ask("What breaks and how to migrate: "))
		}
		if cc.TaskRef != "" {
			trailers = append(trailers, provider.TaskTrailer+cc.TaskRef)
		}
		*msg = commit.Format(typ, keys, breaking, summary, body, verbs, trailers)
	}
	if issues := a.Validate(ctx, *msg); len(issues) > 0 {
		for _, i := range issues {
			fmt.Println("✖", i.Message)
			if i.Fix != "" {
				fmt.Println("  try:", i.Fix)
			}
		}
		return &hooks.ExitError{Code: 1, Msg: "invalid message"}
	}
	effects, warnings, _ := a.Preview(ctx, *msg)
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
	return confirmLoop(*yes, func(tok string) (any, error) { return a.Commit(ctx, m, nil, tok) })
}

func runPreview(ctx context.Context, args []string) error {
	a, err := open(ctx)
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
	for _, m := range msgs {
		effects, warnings, err := a.Preview(ctx, m)
		if err != nil {
			return err
		}
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

func runTransition(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("transition", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "preview only")
	githubEvent := fs.Bool("github-event", false, "process the current GitHub Actions lifecycle event")
	shipped := fs.Bool("shipped", false, "preview a shipped task with the runner identity")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if *githubEvent {
		if fs.NArg() != 0 || *yes || *shipped {
			return errors.New("--github-event cannot be combined with a key, event, or --yes")
		}
		return runGitHubTaskEvent(ctx, *dry)
	}
	key, event := "", ""
	switch fs.NArg() {
	case 1:
		event = fs.Arg(0)
	case 2:
		key, event = fs.Arg(0), fs.Arg(1)
	default:
		return errors.New("usage: agentflow transition [KEY] EVENT")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if *shipped {
		if !*dry || *yes || key == "" {
			return errors.New("--shipped requires an explicit key, event and --dry-run")
		}
		plan, err := a.PlanShippedTask(ctx, key, event)
		if err != nil {
			return err
		}
		return printJSON(plan)
	}
	return confirmLoop(*yes, func(tok string) (any, error) { return a.Transition(ctx, key, event, *dry, tok) })
}

func runPR(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "mark-ready" {
		return runBranchWrite(ctx, "pr mark-ready", args[1:])
	}
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	prepare := fs.Bool("prepare", false, "prepare the exact publication without publishing it")
	token := fs.String("confirm-token", "", "resume the exact reviewed publication")
	body := fs.String("body", "", "pull request body")
	var labels, reviewers []string
	fs.Func("label", "label to apply (repeatable)", func(v string) error { labels = append(labels, v); return nil })
	fs.Func("reviewer", "reviewer to request (repeatable)", func(v string) error { reviewers = append(reviewers, v); return nil })
	check := fs.Bool("check", false, "readiness check only")
	runCmds := fs.Bool("run", false, "also run test and lint commands")
	title := fs.String("title", "", "PR title")
	draft := fs.Bool("draft", false, "open as draft")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	in := app.PROpenInput{Title: *title, Body: *body, Reviewers: reviewers, Labels: labels}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "draft" {
			in.Draft = draft
		}
	})
	if *prepare || *token != "" {
		if *check || *runCmds || (*prepare && (*token != "" || *yes)) {
			return errors.New("use one of --prepare, --confirm-token or --check")
		}
		if *prepare {
			return writeResult(a.PRPrepare(ctx, in))
		}
		return writeResult(a.PROpen(ctx, in, *token))
	}
	r, err := a.PRReady(ctx, *runCmds)
	if err != nil {
		return err
	}
	for _, c := range r.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✖"
		}
		fmt.Printf("%s %-22s %s\n", mark, c.Name, c.Detail)
	}
	if *check {
		if !r.Ready {
			return &hooks.ExitError{Code: 1, Msg: "not ready"}
		}
		return nil
	}
	return confirmLoop(*yes, func(tok string) (any, error) { return a.PROpen(ctx, in, tok) })
}

// confirmLoop runs fn; on needs_confirmation it asks the user and re-runs with the token.
func confirmLoop(yes bool, fn func(token string) (any, error)) error {
	out, done, err := confirmed(yes, fn)
	if err != nil || done {
		return err
	}
	return printJSON(out)
}

// confirmed runs fn through the confirmation flow and returns its final result.
// done is true when nothing ran because the policy only suggests commands.
func confirmed(yes bool, fn func(token string) (any, error)) (any, bool, error) {
	out, err := fn("")
	if err != nil {
		return out, false, err
	}
	if p, ok := out.(*app.Pending); ok {
		switch p.Status {
		case "suggest":
			fmt.Println(p.Reason)
			for _, c := range p.Commands {
				fmt.Println("  " + c)
			}
			return nil, true, nil
		case "needs_confirmation":
			if !yes {
				release, err := p.OpenPrompt()
				if err != nil {
					return nil, false, err
				}
				defer release()
			}
			if err := printApproval(p); err != nil {
				return nil, false, errors.Join(err, p.Cancel())
			}
			if !yes && !strings.HasPrefix(strings.ToLower(ask("Proceed? [y/N]: ")), "y") {
				return nil, false, errors.Join(errors.New("cancelled"), p.Cancel())
			}
			if out, err = fn(p.ConfirmToken); err != nil {
				return out, false, err
			}
		}
	}
	return out, false, nil
}

func ask(prompt string) string {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// reorder moves flags before positional args so `start PLAT-1 --worktree` works.
func reorder(args []string) []string {
	var flags, pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

func printStandup(out map[string]any) {
	section := func(title, key string) {
		items, _ := out[key].([]string)
		if len(items) == 0 {
			return
		}
		fmt.Println(title)
		for _, i := range items {
			fmt.Println("  -", i)
		}
	}
	section("Commits", "commits")
	section("PRs", "prs")
	section("Reviewed", "reviewed")
	section("Tickets", "tickets")
	section("Blocked", "blocked")
}

// ---- updates ----

func newUpdater(ctx context.Context) (*update.Updater, error) {
	opts := update.Options{Channel: "stable"}
	dir, _ := os.Getwd()
	if repo, err := gitx.Open(ctx, dir); err == nil {
		if cfg, _, err := config.LoadRepoCore(repo.Dir); err == nil {
			opts.Channel, opts.Track, opts.Pin, opts.CheckEvery = cfg.Update.Channel, cfg.Update.Track, cfg.Update.Pin, cfg.CheckEvery()
		}
	}
	if u, err := config.LoadUser(); err == nil && u.Update.Auto != nil && !*u.Update.Auto {
		opts.Disabled = true
	}
	tok, err := auth.GitHubToken(ctx, "")
	if err != nil {
		return nil, err
	}
	u, err := update.New(version.Version, opts, gh.New(tok))
	if err != nil {
		return nil, err
	}
	u.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	return u, nil
}

// maybeUpdate enforces the version policy, shows a pending notice, and starts a
// detached background update when one is due. It never delays the command much.
func maybeUpdate(ctx context.Context) {
	u, err := newUpdater(ctx)
	if err != nil || u == nil {
		return
	}
	if n := u.TakeNotice(); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	if u.Opts.Pin != "" {
		fmt.Fprintf(os.Stderr, "agentflow: update.pin=%s is set in .agentflow/config.yml; remove it when the incident is over\n", u.Opts.Pin)
	}
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = u.Enforce(ectx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentflow:", err)
		if errors.Is(err, update.ErrRestartRequired) {
			os.Exit(75) // EX_TEMPFAIL: re-run picks up the new version through the launcher
		}
		os.Exit(1)
	}
	spawnBackgroundUpdate(u)
}

func spawnBackgroundUpdate(u *update.Updater) {
	if !u.Due() {
		return
	}
	u.Touch()
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "update", "--quiet")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	_ = cmd.Start() // detached; it records a notice for the next run
}

func runUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	rollback := fs.Bool("rollback", false, "switch back to the previous version")
	stable := fs.Bool("latest-stable", false, "install GitHub's latest stable release, respecting project version restrictions")
	quiet := fs.Bool("quiet", false, "no output; leave a notice for the next run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	u, err := newUpdater(ctx)
	if err != nil {
		return err
	}
	if *rollback {
		if *stable {
			return errors.New("choose either --latest-stable or --rollback")
		}
		v, err := u.Rollback()
		if err == nil {
			fmt.Printf("agentflow now runs %s. Restart AI sessions to use it.\n", v)
		}
		return err
	}
	u.Touch()
	resolve := u.Latest
	if *stable {
		resolve = u.LatestStable
	}
	rel, err := resolve(ctx)
	if err != nil {
		return err
	}
	if rel == nil || (!*stable && !u.NewerThanCurrent(rel)) || u.Installed(rel.Version().String()) {
		if *stable && rel != nil && !*quiet {
			fmt.Printf("Agentflow %s is already active in the verified installation cache.\n", rel.Tag)
		} else if !*quiet {
			fmt.Printf("agentflow %s is up to date (channel %s)\n", version.Version, u.Opts.Channel)
		}
		return nil
	}
	if err := u.Install(ctx, rel); err != nil {
		if *quiet {
			return nil
		}
		return err
	}
	msg := fmt.Sprintf("agentflow updated %s → %s (%s). Restart AI sessions to use it.", version.Version, rel.Version(), rel.HTMLURL)
	if *quiet {
		u.SetNotice(msg)
		return nil
	}
	fmt.Println(msg)
	return nil
}

func runVersion(ctx context.Context) error {
	fmt.Println(version.String())
	u, err := newUpdater(ctx)
	if err != nil {
		fmt.Println("updates: unavailable (", err, ")")
		return nil
	}
	fmt.Printf("channel: %s · check every %s · auto-update: %v\n", u.Opts.Channel, u.Opts.CheckEvery, !u.Opts.Disabled)
	if st, err := os.Stat(u.Dir + "/last-check"); err == nil {
		fmt.Println("last check:", st.ModTime().Format(time.RFC1123))
	}
	if rel, err := u.Latest(ctx); err == nil && rel != nil && u.NewerThanCurrent(rel) {
		fmt.Printf("newer release available: %s (run agentflow update)\n", rel.Tag)
	}
	return nil
}

// ---- doctor ----

func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fix := fs.Bool("fix", false, "apply safe fixes (git hooksPath)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ok := true
	report := func(pass bool, name, detail string) {
		mark := "✓"
		if !pass {
			mark, ok = "✖", false
		}
		fmt.Printf("%s %-26s %s\n", mark, name, detail)
	}
	fmt.Println(version.String())
	a, err := open(ctx)
	if err != nil {
		report(false, "git repository", err.Error())
		return &hooks.ExitError{Code: 1}
	}
	report(true, "git repository", a.Repo.Dir)
	report(a.HasCfg, ".agentflow/config.yml", map[bool]string{true: "found", false: "missing: run agentflow init"}[a.HasCfg])
	hp, _ := a.Repo.Git(ctx, "config", "core.hooksPath")
	hooksOK := strings.TrimSpace(hp) == ".githooks"
	if !hooksOK && *fix {
		_, err := a.Repo.Git(ctx, "config", "core.hooksPath", ".githooks")
		hooksOK = err == nil
	}
	report(hooksOK, "commit-msg hook", "core.hooksPath="+strings.TrimSpace(hp)+map[bool]string{true: "", false: " (run agentflow doctor --fix)"}[hooksOK])
	if from := setup.PinBehind(a.Repo.Dir); from != "" {
		report(false, "workflow pin", setup.CallerWorkflow+" is pinned to "+from+": run agentflow pin")
	} else if _, err := os.Stat(a.Repo.Dir + "/" + setup.CallerWorkflow); err == nil {
		report(true, "workflow pin", "up to date")
	}
	_, lerr := os.Stat(a.Repo.Dir + "/.agentflow/bin/agentflow")
	report(lerr == nil, "repo launcher", ".agentflow/bin/agentflow")
	if _, err := exec.LookPath("gh"); err != nil {
		report(false, "gh CLI", "not installed: needed for GitHub auth and update verification")
	} else {
		attest := exec.CommandContext(ctx, "gh", "attestation", "verify", "--help").Run() == nil
		report(attest, "gh attestation", map[bool]string{true: "available", false: "upgrade gh to verify updates"}[attest])
	}
	name, login, err := a.LoginCheck(ctx)
	switch {
	case errors.Is(err, auth.ErrNoCredentials):
		report(false, "Atlassian legacy API login", "interactive setup uses the Atlassian MCP; see agentflow setup plan")
	case err != nil && name == "":
		report(false, "Atlassian login", err.Error())
	default:
		report(true, "Atlassian login", name)
		report(err == nil, "GitHub login", login)
	}
	if fm, err := a.FlowMap(ctx); err != nil {
		report(false, "flow map", err.Error())
	} else {
		var missing []string
		for _, p := range a.Cfg.Jira.Projects {
			if !fm.Listed(p) {
				missing = append(missing, p)
			}
		}
		report(len(missing) == 0, "flow map", a.Cfg.FlowMap+map[bool]string{true: "", false: " (no block for " + strings.Join(missing, ",") + ")"}[len(missing) == 0])
	}
	printDaemonHome(os.Stdout, checkDaemonHome(ctx))
	clients := setup.Detect()
	sort.Strings(clients)
	report(true, "AI clients on PATH", strings.Join(clients, ", "))
	if !ok {
		return &hooks.ExitError{Code: 1, Msg: "doctor found problems"}
	}
	return nil
}

// checkDaemonHome asks whether the console daemon watches this process's
// agentflow home. Tests replace it.
var checkDaemonHome = webui.CheckDaemonHome

// printDaemonHome prints the doctor line for the console daemon's home: a
// warning (!) when the daemon watches another AGENTFLOW_HOME, which does not
// fail doctor, and nothing when no daemon runs.
func printDaemonHome(w io.Writer, c watch.HomeCheck) {
	if !c.Checked {
		return
	}
	mark := "✓"
	if c.Warn {
		mark = "!" // a warning does not clear ok
	}
	fmt.Fprintf(w, "%s %-26s %s\n", mark, "console daemon home", c.Detail)
}

func runRelease(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow release init|sync|plan|version|preview|get|publish")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "get":
		return runReleaseGet(ctx, rest)
	case "publish":
		return runReleasePublish(ctx, rest)
	}
	fs := flag.NewFlagSet("release "+sub, flag.ContinueOnError)
	strategy := fs.String("strategy", "", "branching strategy: trunk or gitflow (default: detect)")
	force := fs.Bool("force", false, "re-detect units even if releases.yml exists")
	unit := fs.String("unit", "", "unit name from .agentflow/releases.yml")
	asJSON := fs.Bool("json", false, "print JSON")
	since := fs.String("since", "", "tag or ref (preview)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	switch sub {
	case "init":
		res, err := a.ReleaseInit(ctx, *strategy, *force)
		if err != nil {
			return err
		}
		fmt.Printf("strategy: %s\n", res.Strategy)
		for _, u := range res.Units {
			fmt.Printf("  %-16s %-22s %-17s %s\n", u.Name, u.Path, u.ReleaseType, strings.Join(u.Builds, ","))
		}
		for _, f := range res.Written {
			fmt.Println("  wrote   ", f)
		}
		for _, n := range res.Notes {
			fmt.Println("•", n)
		}
		return nil
	case "sync":
		written, err := a.ReleaseSync(ctx, nil)
		for _, f := range written {
			fmt.Println("  wrote   ", f)
		}
		return err
	case "plan":
		plans, err := a.ReleasePlan(ctx, *unit)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(plans)
		}
		for _, p := range plans {
			mark := " "
			if p.WillRelease {
				mark = "↑"
			}
			fmt.Printf("%s %-16s %-8s → %-8s (%s, %d commits) build version %s\n", mark, p.Unit.Name, p.Current, p.Next, p.Bump, p.Commits, p.Version)
		}
		return nil
	case "version":
		if *unit == "" {
			return errors.New("--unit is required")
		}
		plans, err := a.ReleasePlan(ctx, *unit)
		if err != nil {
			return err
		}
		fmt.Println(plans[0].Version)
		return nil
	case "preview":
		out, err := a.ReleasePreview(ctx, *since)
		if err != nil {
			return err
		}
		return printJSON(out)
	}
	return fmt.Errorf("unknown release command %q", sub)
}
