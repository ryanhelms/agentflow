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
		if cfg, _, err := config.LoadRepo(repo.Dir); err == nil {
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

// SessionStart prints the ticket context for the AI client to load.
func SessionStart(ctx context.Context, client, notice string, stdout io.Writer) error {
	dir, _ := os.Getwd()
	a, err := app.Open(ctx, dir, client)
	if err != nil {
		return nil // not a repo: say nothing
	}
	tc, err := a.Context(ctx, "")
	if err != nil {
		fmt.Fprintf(stdout, "devflow: could not load ticket context: %v\n", err)
		return nil
	}
	fmt.Fprint(stdout, tc.Text)
	if !a.HasCfg {
		fmt.Fprintln(stdout, "devflow: this repo has no .devflow/config.yml; run `devflow init` to enable it.")
	}
	if notice != "" {
		fmt.Fprintln(stdout, notice)
	}
	return nil
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
	msgFlagRe   = regexp.MustCompile(`(?s)(?:-m|--message)(?:\s+|=)("(?:[^"\\]|\\.)*"|'[^']*'|\$\(cat <<'?EOF'?\n.*?\nEOF\n?\s*\))`)
)

// PreBash blocks never-list commands and invalid `git commit -m` messages.
// Claude Code treats exit code 2 as "block" and shows stderr to the model.
func PreBash(ctx context.Context, stdin io.Reader, stderr io.Writer) error {
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
		if cfg, _, err := config.LoadRepo(repo.Dir); err == nil {
			protected, rules = cfg.Policy.ProtectedBranches, cfg.Commit
		}
		branch = repo.Branch(ctx)
		keyHint = gitx.KeyFromBranch(branch)
	}
	if reason := policy.Forbidden(cmd, protected, branch); reason != "" {
		fmt.Fprintln(stderr, "devflow blocked this command: "+reason)
		return &ExitError{Code: 2, Msg: reason}
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
	issues := commit.Validate(msg, rules, keyHint)
	if len(issues) == 0 {
		return nil
	}
	fmt.Fprintln(stderr, "devflow: this commit message does not follow the convention. Use the devflow_commit tool instead of raw git commit.")
	for _, i := range issues {
		fmt.Fprintf(stderr, "- %s", i.Message)
		if i.Fix != "" {
			fmt.Fprintf(stderr, " (try: %s)", i.Fix)
		}
		fmt.Fprintln(stderr)
	}
	return &ExitError{Code: 2, Msg: "invalid commit message"}
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
