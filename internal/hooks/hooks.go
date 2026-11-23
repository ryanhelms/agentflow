// Package hooks implements git and AI-client lifecycle hooks.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/commit"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/setup"
)

// ExitError carries a process exit code.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// CommitMsg validates (and strips AI trailers from) a commit message file.
func CommitMsg(ctx context.Context, file string, stderr io.Writer) error {
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
	fmt.Fprintln(stderr, "✖ devflow: commit message does not follow the convention")
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
	return &ExitError{Code: 1, Msg: "invalid commit message"}
}

// SessionStart prints the selected providers and task context for the AI client.
func SessionStart(ctx context.Context, client, notice string, stdout io.Writer) error {
	dir, _ := os.Getwd()
	a, err := app.Open(ctx, dir, client)
	if err != nil {
		return nil // not a repo: say nothing
	}
	if !a.HasCfg {
		autoInit(ctx, a, client, stdout)
	} else {
		fmt.Fprint(stdout, setup.AgentProviderSelection(a.Cfg))
		if a.Cfg.Providers.TaskManagement.Provider != "none" {
			if tc, err := a.Context(ctx, ""); err != nil {
				fmt.Fprintf(stdout, "devflow: could not load task context: %v\n", err)
			} else {
				fmt.Fprint(stdout, tc.Text)
			}
		}
		if from := setup.PinBehind(a.Repo.Dir); from != "" {
			fmt.Fprintf(stdout, "devflow: %s is pinned to devflow %s, older than the installed devflow. "+
				"Offer the developer `/devflow:pin`, which re-pins it and opens a PR for review.\n", setup.CallerWorkflow, from)
		}
	}
	if notice != "" {
		fmt.Fprintln(stdout, notice)
	}
	return nil
}

// autoInit handles a session in a repo without .devflow/config.yml, following the
// developer's init.auto setting (see setup.AutoInitDecision). Files are written but never
// committed: the agent is told to show them and offer a branch and PR.
func autoInit(ctx context.Context, a *app.App, client string, stdout io.Writer) {
	switch setup.AutoInitDecision(ctx, a.Repo, a.User.Init.Auto, a.User.Init.Orgs) {
	case setup.AutoAsk:
		fmt.Fprintln(stdout, "devflow: this repo is not set up for devflow yet. Offer the developer `/devflow:init` (or `devflow init`); `devflow remove` undoes it.")
	case setup.AutoInit:
		res, err := app.InitRepo(ctx, a.Repo.Dir, client, false)
		if err != nil {
			fmt.Fprintf(stdout, "devflow: automatic init failed: %v. The developer can run `devflow init` and `devflow doctor`.\n", err)
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
		fmt.Fprintf(stdout, "devflow set this repo up automatically (init.auto in ~/.devflow/config.yml). "+
			"It wrote %d files and committed nothing: %s.\n"+
			"At the start of your first reply, tell the developer in one or two lines, then offer to: review .devflow/config.yml "+
			"(providers and policy.test_cmd) and .devflow/releases.yml, commit the files on a new branch following "+
			"the configured commit convention, and open a PR. If they don't want devflow here, offer `/devflow:remove`, "+
			"which deletes the files and stops automatic init in this clone.\n",
			len(files), strings.Join(files, ", "))
	}
}

type claudeHookInput struct {
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

// PreBash blocks never-list commands and invalid commit messages, PR titles and squash
// subjects. Claude Code treats exit code 2 as "block" and shows stderr to the model.
// conventionOnly skips the never-list: devflow's own repo uses it, because maintainers
// there ask agents to merge PRs explicitly.
func PreBash(ctx context.Context, stdin io.Reader, stderr io.Writer, conventionOnly bool) error {
	var in claudeHookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return nil
	}
	cmd := in.ToolInput.Command
	if cmd == "" {
		return nil
	}
	dir, _ := os.Getwd()
	protected := config.DefaultProtected
	branch := ""
	var rules = config.Commit{Types: config.DefaultTypes, SubjectMax: 72}
	keyHint := ""
	if repo, err := gitx.Open(ctx, dir); err == nil {
		if cfg, _, err := config.LoadRepoCore(repo.Dir); err == nil {
			protected, rules = cfg.Policy.ProtectedBranches, cfg.Commit
		}
		branch = repo.Branch(ctx)
		keyHint = gitx.KeyFromBranch(branch)
	}
	if reason := policy.Forbidden(cmd, protected, branch); reason != "" && !conventionOnly {
		fmt.Fprintln(stderr, "devflow blocked this command: "+reason)
		return &ExitError{Code: 2, Msg: reason}
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
		hint = " Use the devflow_commit tool instead of raw git commit."
	}
	fmt.Fprintf(stderr, "devflow: this %s does not follow the convention.%s\n", what, hint)
	for _, i := range issues {
		fmt.Fprintf(stderr, "- %s", i.Message)
		if i.Fix != "" {
			fmt.Fprintf(stderr, " (try: %s)", i.Fix)
		}
		fmt.Fprintln(stderr)
	}
	return &ExitError{Code: 2, Msg: "invalid " + what}
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

// Stop reminds about uncommitted work and unticked criteria without blocking.
func Stop(ctx context.Context, client string, stdin io.Reader, stdout io.Writer) error {
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
		notes = append(notes, "there are uncommitted changes (use /devflow:commit)")
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
	return json.NewEncoder(stdout).Encode(map[string]string{"systemMessage": "devflow: " + strings.Join(notes, "; ")})
}
