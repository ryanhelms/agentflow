// Package hooks implements git and AI-client lifecycle hooks.
package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/boundary"
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/setup"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// ExitError carries a process exit code.
type ExitError struct {
	Code int
	Msg  string
	// Reason is the fixed code a hook.ran event carries for this refusal
	// (ReasonConvention, ReasonRepoBoundary, ...). Never text.
	Reason string
}

func (e *ExitError) Error() string { return e.Msg }

// CommitMsg validates (and strips AI trailers from) a commit message file.
// It is what `agentflow lint-msg` runs, for every commit in the pre-push hook
// and in CI, so it publishes nothing. The git hook is CommitMsgHook.
func CommitMsg(ctx context.Context, file string, stderr io.Writer) error {
	return commitMsg(ctx, file, stderr)
}

// CommitMsgHook is the git commit-msg hook (`agentflow hook commit-msg`): it
// does what CommitMsg does and publishes hook.ran.
func CommitMsgHook(ctx context.Context, file string, stderr io.Writer) (err error) {
	defer func(start time.Time) { hookRan(ctx, "commit-msg", "git", start, err, false) }(time.Now())
	return commitMsg(ctx, file, stderr)
}

func commitMsg(ctx context.Context, file string, stderr io.Writer) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	dir, _ := os.Getwd()
	rules := config.Commit{Types: config.DefaultTypes, SubjectMax: 72}
	keyHint := ""
	if repo, err := gitx.Open(ctx, dir); err == nil {
		if cfg, _, err := config.LoadRepoCore(repo.Dir); err == nil {
			rules = cfg.Commit
		}
		keyHint = gitx.KeyFromBranch(repo.Branch(ctx))
	}
	msg := string(raw)
	if !rules.AITrailer {
		if stripped := commit.StripAITrailers(msg); stripped != msg {
			if err := os.WriteFile(file, []byte(stripped), 0o644); err != nil {
				return err
			}
			msg = stripped
		}
	}
	issues := commit.Validate(msg, rules, keyHint)
	if len(issues) == 0 {
		return nil
	}
	fmt.Fprintln(stderr, "✖ agentflow: commit message does not follow the convention")
	for _, i := range issues {
		fmt.Fprintf(stderr, "  - %s\n", i.Message)
		if i.Fix != "" {
			fmt.Fprintf(stderr, "    try: %s\n", i.Fix)
		}
	}
	if rules.KeyRequired() {
		fmt.Fprintln(stderr, "  format: type(KEY-123): imperative summary   e.g. feat(PLAT-123): add quota API")
	} else {
		fmt.Fprintln(stderr, "  format: type[(scope)][!]: imperative summary   e.g. feat(cli): add release plan")
	}
	return &ExitError{Code: 1, Msg: "invalid commit message", Reason: ReasonConvention}
}

// SessionStart prints the selected providers and task context for the AI
// client, and publishes hook.ran.
func SessionStart(ctx context.Context, client, notice string, stdout io.Writer) (err error) {
	defer func(start time.Time) { hookRan(ctx, "session-start", client, start, err, false) }(time.Now())
	return sessionStart(ctx, client, notice, stdout)
}

func sessionStart(ctx context.Context, client, notice string, stdout io.Writer) error {
	dir, _ := os.Getwd()
	a, err := app.Open(ctx, dir, client)
	var refused *config.HomeInRepositoryError
	if errors.As(err, &refused) {
		fmt.Fprintln(stdout, "agentflow: "+refused.Error())
		return nil
	}
	if err != nil {
		return nil // not a repo: say nothing
	}
	// The agent reads stdout, not stderr: repeat an ignored-home warning
	// so it can tell the person.
	for _, n := range config.HomeNotices() {
		fmt.Fprintln(stdout, n)
	}
	if !a.HasCfg {
		autoInit(ctx, a, client, stdout)
	} else {
		fmt.Fprint(stdout, setup.AgentProviderSelection(a.Cfg))
		if a.Cfg.Providers.TaskManagement.Provider != "none" {
			if tc, err := a.Context(ctx, ""); err != nil {
				fmt.Fprintf(stdout, "agentflow: could not load task context: %v\n", err)
			} else {
				fmt.Fprint(stdout, tc.Text)
			}
		}
		if from := setup.PinBehind(a.Repo.Dir); from != "" {
			fmt.Fprintf(stdout, "agentflow: %s is pinned to agentflow %s, older than the installed agentflow. "+
				"Offer the developer `/agentflow:pin`, which re-pins it and opens a PR for review.\n", setup.CallerWorkflow, from)
		}
		worktreeNotes(ctx, a, stdout)
	}
	exportSessionRepo(ctx, a.Repo.Dir)
	if notice != "" {
		fmt.Fprintln(stdout, notice)
	}
	return nil
}

// exportSessionRepo appends AGENTFLOW_SESSION_REPO to CLAUDE_ENV_FILE, which
// Claude Code sources before every Bash command, so the git hooks know the
// session's home repository. It does nothing for clients without that file.
func exportSessionRepo(ctx context.Context, dir string) {
	file := os.Getenv("CLAUDE_ENV_FILE")
	if file == "" {
		return
	}
	if project := os.Getenv("CLAUDE_PROJECT_DIR"); project != "" {
		dir = project
	}
	home, err := boundary.ResolveDir(ctx, dir)
	if err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "export %s=%s\n", boundary.SessionEnv, shellQuote(home.CommonDir))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SweepSummaryTimeout bounds the session-start worktree summary.
var SweepSummaryTimeout = 3 * time.Second

// worktreeNotes tells the agent to work in a worktree when the session starts in
// a primary checkout, and summarises worktrees that need attention. The summary
// is offline (no fetch, no gh) and time-boxed so session start stays fast.
func worktreeNotes(ctx context.Context, a *app.App, stdout io.Writer) {
	if a.InPrimaryCheckout(ctx) {
		fmt.Fprintf(stdout, "agentflow: this session is in the primary checkout of %s. Make every change in a task worktree: "+
			"run `agentflow start <issue> --worktree`, then work in the directory it prints. Commits here are refused by the pre-commit hook.\n",
			worktree.RepoName(ctx, a.Repo))
	}
	ctx, cancel := context.WithTimeout(ctx, SweepSummaryTimeout)
	defer cancel()
	res, _, err := a.Sweep(ctx, app.SweepRequest{DryRun: true, Offline: true}, "")
	if err != nil || res == nil {
		return
	}
	counts := map[string]int{}
	for _, w := range res.Worktrees {
		counts[w.State]++
	}
	var parts []string
	for _, state := range []string{worktree.StateWouldFinish, worktree.StateDirty, worktree.StateUnpushed, worktree.StateStale, worktree.StateOrphaned} {
		if n := counts[state]; n > 0 {
			label := strings.ReplaceAll(state, "_", " ")
			if state == worktree.StateWouldFinish {
				label = "merged and ready to finish"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	if len(parts) > 0 {
		fmt.Fprintf(stdout, "agentflow worktrees: %s. Offer `agentflow sweep` to finish merged ones and review the rest.\n", strings.Join(parts, ", "))
	}
}

// autoInit handles a session in a repo without .agentflow/config.yml, following the
// developer's init.auto setting (see setup.AutoInitDecision). Files are written but never
// committed: the agent is told to show them and offer a branch and PR.
func autoInit(ctx context.Context, a *app.App, client string, stdout io.Writer) {
	switch setup.AutoInitDecision(ctx, a.Repo, a.User.Init.Auto, a.User.Init.Orgs) {
	case setup.AutoAsk:
		fmt.Fprintln(stdout, "agentflow: this repo is not set up for agentflow yet. Offer the developer `/agentflow:init` (or `agentflow init`); `agentflow remove` undoes it.")
	case setup.AutoInit:
		res, err := app.InitRepo(ctx, a.Repo.Dir, client, false)
		if err != nil {
			fmt.Fprintf(stdout, "agentflow: automatic init failed: %v. The developer can run `agentflow init` and `agentflow doctor`.\n", err)
			return
		}
		var files []string
		if res.Files != nil {
			files = append(files, res.Files.Written...)
			files = append(files, res.Files.Merged...)
		}
		if res.Release != nil {
			files = append(files, res.Release.Written...)
		}
		fmt.Fprintf(stdout, "agentflow set this repo up automatically (init.auto in ~/.agentflow/config.yml). "+
			"It wrote %d files and committed nothing: %s.\n"+
			"At the start of your first reply, tell the developer in one or two lines, then offer to: review .agentflow/config.yml "+
			"(providers and policy.test_cmd) and .agentflow/releases.yml, commit the files on a new branch following "+
			"the configured commit convention, and open a PR. If they don't want agentflow here, offer `/agentflow:remove`, "+
			"which deletes the files and stops automatic init in this clone.\n",
			len(files), strings.Join(files, ", "))
	}
}

type claudeHookInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	StopHookActive bool `json:"stop_hook_active"`
}

var (
	gitCommitRe = regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?commit\b`)
	ghPRTitleRe = regexp.MustCompile(`\bgh\s+pr\s+(?:create|edit)\b`)
	ghMergeRe   = regexp.MustCompile(`\bgh\s+pr\s+merge\b`)
	titleFlagRe = regexp.MustCompile(`(?:--title|-t)(?:\s+|=)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s"']+)`)
	subjFlagRe  = regexp.MustCompile(`--subject(?:\s+|=)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s"']+)`)
	msgFlagRe   = regexp.MustCompile(`(?s)(?:-m|--message)(?:\s+|=)("(?:[^"\\]|\\.)*"|'[^']*'|\$\(cat <<'?EOF'?\n.*?\nEOF\n?\s*\))`)
)

// SessionHome returns the session's home repository: CLAUDE_PROJECT_DIR, then
// AGENTFLOW_SESSION_REPO, then fallback. ok is false outside a repository.
func SessionHome(ctx context.Context, fallback string) (boundary.Repo, bool) {
	for _, dir := range []string{os.Getenv("CLAUDE_PROJECT_DIR"), os.Getenv(boundary.SessionEnv), fallback} {
		if dir == "" {
			continue
		}
		if r, err := boundary.ResolveDir(ctx, dir); err == nil {
			return r, true
		}
	}
	return boundary.Repo{}, false
}

// RepoBoundary refuses a command that changes a repository other than the
// session's home. cwd is the folder the command runs in ("" for this process's).
func RepoBoundary(ctx context.Context, cmd, cwd string) *boundary.Refusal {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	home, ok := SessionHome(ctx, cwd)
	if !ok {
		return nil
	}
	c := &boundary.Checker{Home: home, Cwd: cwd, Resolve: boundary.GitResolver(ctx)}
	return c.Check(cmd)
}

// PreBash blocks commands that change another repository, never-list commands and invalid commit messages, PR titles and squash
// subjects. Claude Code treats exit code 2 as "block" and shows stderr to the model.
// The repository boundary and the plugin rule (policy.ForbiddenAlways) apply in every mode. conventionOnly skips the rest of the
// never-list: agentflow's own repo uses it, because maintainers there ask agents to merge PRs explicitly.
// It runs before every shell command, so it publishes hook.ran only when it
// blocks a command or fails.
func PreBash(ctx context.Context, stdin io.Reader, stderr io.Writer, conventionOnly bool) (err error) {
	defer func(start time.Time) { hookRan(ctx, "pre-bash", "claude", start, err, true) }(time.Now())
	return preBash(ctx, stdin, stderr, conventionOnly)
}

func preBash(ctx context.Context, stdin io.Reader, stderr io.Writer, conventionOnly bool) error {
	var in claudeHookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return nil
	}
	cmd := in.ToolInput.Command
	if cmd == "" {
		return nil
	}
	dir, _ := os.Getwd()
	if refusal := RepoBoundary(ctx, cmd, in.Cwd); refusal != nil {
		msg := refusal.Message()
		fmt.Fprintln(stderr, "agentflow blocked this command: "+msg)
		return &ExitError{Code: 2, Msg: msg, Reason: ReasonRepoBoundary}
	}
	protected := config.DefaultProtected
	branch := ""
	var rules = config.Commit{Types: config.DefaultTypes, SubjectMax: 72}
	keyHint := ""
	// gh pr merge without a number merges the PR of the branch in the command's folder.
	mergeDir := dir
	if in.Cwd != "" {
		mergeDir = in.Cwd
	}
	never := policy.NeverOptions{MergeBase: func(cmd string) (string, error) { return MergeBase(ctx, mergeDir, cmd) }}
	if repo, err := gitx.Open(ctx, dir); err == nil {
		if cfg, _, err := config.LoadRepoCore(repo.Dir); err == nil {
			protected, rules = cfg.Policy.ProtectedBranches, cfg.Commit
			never.CoordinatorMerge = cfg.Policy.CoordinatorMerge
		}
		branch = repo.Branch(ctx)
		keyHint = gitx.KeyFromBranch(branch)
	}
	reason := policy.ForbiddenAlways(cmd)
	if conventionOnly {
		never.CoordinatorMerge = false
	} else if reason == "" {
		reason = policy.ForbiddenWith(cmd, protected, branch, never)
	}
	if reason != "" {
		fmt.Fprintln(stderr, "agentflow blocked this command: "+reason)
		return &ExitError{Code: 2, Msg: reason, Reason: ReasonNeverList}
	}
	// PR titles become the squash commit on the default branch: check them before the
	// PR exists, leaving room for the " (#123)" GitHub appends.
	if ghPRTitleRe.MatchString(cmd) {
		if m := titleFlagRe.FindStringSubmatch(cmd); m != nil {
			return report(stderr, "PR title", commit.ValidatePRTitle(unquote(m[1]), rules, keyHint))
		}
		return nil
	}
	if ghMergeRe.MatchString(cmd) {
		if m := subjFlagRe.FindStringSubmatch(cmd); m != nil {
			return report(stderr, "squash commit subject", commit.Validate(unquote(m[1]), rules, keyHint))
		}
		return nil
	}
	if !gitCommitRe.MatchString(cmd) {
		return nil
	}
	var parts []string
	for _, m := range msgFlagRe.FindAllStringSubmatch(cmd, -1) {
		parts = append(parts, unquote(m[1]))
	}
	if len(parts) == 0 {
		return nil // -F/editor: the commit-msg hook validates
	}
	msg := strings.Join(parts, "\n\n")
	return report(stderr, "commit message", commit.Validate(msg, rules, keyHint))
}

// report blocks the command (exit 2) when there are issues.
func report(stderr io.Writer, what string, issues []commit.Issue) error {
	if len(issues) == 0 {
		return nil
	}
	hint := ""
	if what == "commit message" {
		hint = " Use the agentflow_commit tool instead of raw git commit."
	}
	fmt.Fprintf(stderr, "agentflow: this %s does not follow the convention.%s\n", what, hint)
	for _, i := range issues {
		fmt.Fprintf(stderr, "- %s", i.Message)
		if i.Fix != "" {
			fmt.Fprintf(stderr, " (try: %s)", i.Fix)
		}
		fmt.Fprintln(stderr)
	}
	return &ExitError{Code: 2, Msg: "invalid " + what, Reason: ReasonConvention}
}

func unquote(s string) string {
	switch {
	case strings.HasPrefix(s, `"`):
		s = strings.Trim(s, `"`)
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(s)
	case strings.HasPrefix(s, `'`):
		return strings.Trim(s, `'`)
	case strings.HasPrefix(s, "$(cat"):
		_, body, _ := strings.Cut(s, "\n")
		body, _, _ = strings.Cut(body, "\nEOF")
		return body
	}
	return s
}

// Stop reminds about uncommitted work and unticked criteria without
// blocking, and publishes hook.ran.
func Stop(ctx context.Context, client string, stdin io.Reader, stdout io.Writer) (err error) {
	defer func(start time.Time) { hookRan(ctx, "stop", client, start, err, false) }(time.Now())
	return stop(ctx, client, stdin, stdout)
}

func stop(ctx context.Context, client string, stdin io.Reader, stdout io.Writer) error {
	var in claudeHookInput
	_ = json.NewDecoder(stdin).Decode(&in)
	if in.StopHookActive {
		return nil
	}
	dir, _ := os.Getwd()
	a, err := app.Open(ctx, dir, client)
	if err != nil {
		return nil
	}
	var notes []string
	if a.Repo.HasUncommitted(ctx) {
		notes = append(notes, "there are uncommitted changes (use /agentflow:commit)")
	}
	if key := a.CurrentKey(ctx); key != "" {
		if cl, err := a.Checklists(ctx).Load(key); err == nil && cl != nil {
			if d, n := cl.Progress(); n > 0 && d < n {
				notes = append(notes, fmt.Sprintf("%s acceptance criteria %d/%d done", key, d, n))
			}
		}
	}
	if len(notes) == 0 {
		return nil
	}
	return json.NewEncoder(stdout).Encode(map[string]string{"systemMessage": "agentflow: " + strings.Join(notes, "; ")})
}

// SessionMismatch returns a refusal when AGENTFLOW_SESSION_REPO names a
// repository other than the one containing dir, or "".
func SessionMismatch(ctx context.Context, dir string) string {
	session := os.Getenv(boundary.SessionEnv)
	if session == "" {
		return ""
	}
	home, err := boundary.ResolveDir(ctx, session)
	if err != nil {
		return ""
	}
	here, err := boundary.ResolveDir(ctx, dir)
	if err != nil || boundary.Same(home, here) {
		return ""
	}
	r := boundary.Refusal{Command: "git push", Home: home, Target: here}
	return r.Message() + " (If you are a person in a shell that inherited it, run `unset " + boundary.SessionEnv + "`.)"
}

// foreignPushURL refuses a push to a URL that is none of the repository's
// configured remotes, such as `git push https://github.com/other/repo.git`.
func foreignPushURL(ctx context.Context, dir, remote, url string) string {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return ""
	}
	if _, err := repo.Git(ctx, "remote", "get-url", remote); err == nil {
		return "" // a configured remote
	}
	remotes, _ := repo.Remotes(ctx)
	slug := strings.ToLower(gitx.ParseGitHubRemote(url))
	for _, u := range remotes {
		if u == url || (slug != "" && strings.ToLower(gitx.ParseGitHubRemote(u)) == slug) {
			return ""
		}
	}
	return fmt.Sprintf("%s is not a remote of this repository. A session pushes only to its own repository; "+
		"hand work for another repository to a session there with `agentflow delegate --repo NAME \"<task>\"`.", url)
}

// MergeGH runs gh for MergeBase; tests replace it.
var MergeGH worktree.GH = worktree.ExecGH

// MergeBase returns the base branch of the PR a `gh pr merge` command names,
// or of the current branch's PR when it names none. dir is the folder the
// command runs in. It fails, so the merge is refused, unless the command is
// exactly `gh pr merge` with known flags, at most one PR and one repository,
// and nothing before it but an optional GH_REPO=: any other form could make
// gh merge a different PR than the one looked up here.
func MergeBase(ctx context.Context, dir, cmd string) (string, error) {
	m, err := parseMerge(cmd)
	if err != nil {
		return "", err
	}
	args := []string{"pr", "view"}
	if m.pr != "" {
		args = append(args, m.pr)
	}
	// Look the PR up in its own repository, not the current folder's: a URL names
	// it, otherwise -R/--repo or GH_REPO does, as for gh itself.
	if m.repo != "" && !strings.Contains(m.pr, "://") {
		args = append(args, "--repo", m.repo)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := MergeGH(ctx, dir, append(args, "--json", "baseRefName", "--jq", ".baseRefName")...)
	return strings.TrimSpace(out), err
}

// ghMergeFlags are the flags of `gh pr merge` and whether each takes a value.
var ghMergeFlags = map[string]bool{
	"-A": true, "--author-email": true, "-b": true, "--body": true, "-F": true, "--body-file": true,
	"--match-head-commit": true, "-t": true, "--subject": true, "-R": true, "--repo": true,
	"--admin": false, "--auto": false, "-d": false, "--delete-branch": false, "--disable-auto": false,
	"-m": false, "--merge": false, "-r": false, "--rebase": false, "-s": false, "--squash": false,
}

type mergeCommand struct{ pr, repo string }

// parseMerge reads the PR and repository of a `gh pr merge` command line, with
// the words split as the shell splits them.
func parseMerge(cmd string) (mergeCommand, error) {
	var m mergeCommand
	words, ok := boundary.Words(cmd)
	if !ok {
		return m, errors.New("not a single command")
	}
	envRepo, hasEnv := "", false
	if len(words) > 0 {
		envRepo, hasEnv = strings.CutPrefix(words[0], "GH_REPO=")
		if hasEnv {
			words = words[1:]
		}
	}
	if len(words) < 3 || boundary.ProgramName(words[0]) != "gh" || words[1] != "pr" || words[2] != "merge" {
		return m, errors.New("not a plain gh pr merge")
	}
	var prs, repos []string
	args := words[3:]
	for i := 0; i < len(args); i++ {
		w := args[i]
		name, value, inline := strings.Cut(w, "=")
		switch {
		case w == "--":
			prs = append(prs, args[i+1:]...)
			i = len(args)
		case !strings.HasPrefix(w, "-") || w == "-":
			prs = append(prs, w)
		case strings.HasPrefix(w, "-R") && len(w) > 2:
			repos = append(repos, w[2:])
		case strings.HasPrefix(w, "--") && inline:
			if _, known := ghMergeFlags[name]; !known {
				return m, fmt.Errorf("unknown flag %s", name)
			}
			if name == "--repo" {
				repos = append(repos, value)
			}
		default:
			valued, known := ghMergeFlags[w]
			switch {
			case !known: // grouped short flags (-sR) or a flag this check does not know
				return m, fmt.Errorf("unknown flag %s", w)
			case !valued:
			case i+1 >= len(args):
				return m, fmt.Errorf("%s needs a value", w)
			default:
				i++
				if w == "-R" || w == "--repo" {
					repos = append(repos, args[i])
				}
			}
		}
	}
	if len(prs) > 1 {
		return m, errors.New("more than one PR")
	}
	if len(prs) == 1 {
		m.pr = prs[0]
	}
	for _, r := range repos {
		if !strings.EqualFold(r, repos[0]) {
			return m, errors.New("more than one repository")
		}
	}
	switch {
	case len(repos) > 0:
		m.repo = repos[0]
	case hasEnv:
		m.repo = envRepo
	default:
		m.repo = os.Getenv("GH_REPO")
	}
	// gh merges the PR a URL names, whatever -R or GH_REPO says; a command that
	// names two repositories is refused.
	if strings.Contains(m.pr, "://") && (len(repos) > 0 || hasEnv) && !urlInRepo(m.pr, m.repo) {
		return m, errors.New("the PR URL names another repository than -R or GH_REPO")
	}
	return m, nil
}

// urlInRepo reports whether a PR URL belongs to repo ([HOST/]OWNER/NAME).
func urlInRepo(url, repo string) bool {
	parts := strings.Split(strings.Trim(strings.ToLower(repo), "/"), "/")
	if len(parts) < 2 {
		return false
	}
	slug := parts[len(parts)-2] + "/" + strings.TrimSuffix(parts[len(parts)-1], ".git")
	return strings.Contains(strings.ToLower(url), "/"+slug+"/pull/")
}

// PrePush is the git pre-push hook. It reads "<local ref> <local sha> <remote ref>
// <remote sha>" lines and blocks any update of a branch in policy.protected_branches.
// AGENTFLOW_ALLOW_PROMOTE=1 allows main and master, for a release promotion only.
// args are git's REMOTE and URL. It also refuses a push from a repository other
// than the session's (AGENTFLOW_SESSION_REPO) and a push to a URL that is none of
// the repository's remotes. It publishes hook.ran.
func PrePush(ctx context.Context, stdin io.Reader, stderr io.Writer, args ...string) (err error) {
	defer func(start time.Time) { hookRan(ctx, "pre-push", "git", start, err, false) }(time.Now())
	return prePush(ctx, stdin, stderr, args...)
}

func prePush(ctx context.Context, stdin io.Reader, stderr io.Writer, args ...string) error {
	dir, _ := os.Getwd()
	if msg := SessionMismatch(ctx, dir); msg != "" {
		fmt.Fprintln(stderr, "✖ agentflow: "+msg)
		return &ExitError{Code: 1, Msg: "push from another repository", Reason: ReasonRepoBoundary}
	}
	if len(args) == 2 {
		if msg := foreignPushURL(ctx, dir, args[0], args[1]); msg != "" {
			fmt.Fprintln(stderr, "✖ agentflow: "+msg)
			return &ExitError{Code: 1, Msg: "push to another repository", Reason: ReasonRepoBoundary}
		}
	}
	protected := config.DefaultProtected
	if repo, err := gitx.Open(ctx, dir); err == nil {
		if cfg, _, err := config.LoadRepoCore(repo.Dir); err == nil {
			protected = cfg.Policy.ProtectedBranches
		}
	}
	promote := os.Getenv("AGENTFLOW_ALLOW_PROMOTE") == "1"
	var blocked []string
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 4 {
			continue
		}
		branch := strings.TrimPrefix(fields[2], "refs/heads/")
		if !strings.HasPrefix(fields[2], "refs/heads/") || !policy.IsProtected(branch, protected) {
			continue
		}
		if promote && slices.Contains(policy.ReleaseBranches, branch) {
			continue
		}
		blocked = append(blocked, branch)
	}
	if len(blocked) == 0 {
		return nil
	}
	for _, b := range blocked {
		fmt.Fprintf(stderr, "✖ agentflow: pushing to protected branch %s is blocked; push a feature branch and open a PR\n", b)
	}
	return &ExitError{Code: 1, Msg: "push to a protected branch", Reason: ReasonProtectedBranch}
}
