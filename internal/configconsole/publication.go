// Package configconsole provides repository configuration operations shared by
// the browser console, MCP server and CLI.
package configconsole

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nscaledev/agentflow/internal/commit"
	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/consolecfg"
	"github.com/nscaledev/agentflow/internal/consolepublish"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/policy"
)

type Edit struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Base    string `json:"base_digest"`
	Title   string `json:"title"`
	Task    string `json:"task_ref"`
	Draft   bool   `json:"draft"`
}

type Publisher func(context.Context, string, string, string, string, string, string, bool, map[string]string, ...string) (consolepublish.Result, error)

type PublicationPreview struct {
	Plan        consolecfg.Plan `json:"plan"`
	Before      string          `json:"before"`
	After       string          `json:"after"`
	Title       string          `json:"title"`
	Task        string          `json:"task_ref,omitempty"`
	Draft       bool            `json:"draft"`
	CI          string          `json:"ci"`
	Destination string          `json:"destination,omitempty"`
	Warning     string          `json:"warning,omitempty"`
}

type ReviewResult struct {
	policy.Decision
	PublicationPreview
	Published *consolepublish.Result `json:"published,omitempty"`
	Commands  []string               `json:"commands,omitempty"`
	// Gate preserves the browser's prompt owner across requests.
	Gate *policy.Gate `json:"-"`
}

type reviewError struct {
	status int
	err    error
}

func (e *reviewError) Error() string { return e.err.Error() }
func (e *reviewError) Unwrap() error { return e.err }

func ErrorStatus(err error, fallback int) int {
	var e *reviewError
	if errors.As(err, &e) {
		return e.status
	}
	return fallback
}

func CurrentFile(ctx context.Context, root, path, digest string) (consolecfg.File, error) {
	snapshot, err := consolecfg.View(ctx, root)
	if err != nil {
		return consolecfg.File{}, &reviewError{409, err}
	}
	for _, f := range snapshot.Files {
		if f.Path != path {
			continue
		}
		if !f.Editable {
			return consolecfg.File{}, &reviewError{409, errors.New("this source is managed outside the repository editor")}
		}
		if f.Digest != digest {
			return consolecfg.File{}, &reviewError{409, errors.New("the source changed; reload and review your edit")}
		}
		return f, nil
	}
	return consolecfg.File{}, &reviewError{409, errors.New("unknown configuration source")}
}

// CurrentGate reads the active policy, retaining the original client's prompt
// ownership without changing state shared by concurrent MCP calls.
func CurrentGate(ctx context.Context, repo *gitx.Repo, prior *policy.Gate, client string) (*policy.Gate, error) {
	cfg, _, err := config.LoadRepoCore(repo.Dir)
	if err != nil {
		return nil, errors.New("cannot read the current repository approval policy")
	}
	return configuredGate(ctx, repo, prior, client, cfg)
}

func configuredGate(ctx context.Context, repo *gitx.Repo, prior *policy.Gate, client string, cfg *config.Repo) (*policy.Gate, error) {
	user, err := config.LoadUser()
	if err != nil {
		return nil, errors.New("cannot read the current personal approval settings")
	}
	if prior == nil {
		home, err := config.Home()
		if err != nil {
			return nil, err
		}
		prior, err = policy.NewGate(home, config.Effective(user, cfg), client)
		if err != nil {
			return nil, err
		}
	}
	return prior.WithConfiguration(repo.GitDir(ctx), user, cfg), nil
}

func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// Review validates one caller-owned candidate. An empty-token call can return
// a prompt, never a write. Browsers supply their session; CLI and MCP callers
// share a stable scope for an exact same-worktree handoff.
func Review(ctx context.Context, repo *gitx.Repo, gate *policy.Gate, session string, edit Edit, token string, apply bool, publishTo Publisher) (*ReviewResult, error) {
	file, err := CurrentFile(ctx, repo.Dir, edit.Path, edit.Base)
	if err != nil {
		return nil, err
	}
	plan, err := consolecfg.Preview(ctx, repo.Dir, edit.Path, edit.Content, edit.Base)
	if err != nil {
		return nil, &reviewError{422, err}
	}
	if file.Content == edit.Content {
		return nil, &reviewError{422, errors.New("there are no changes to review")}
	}
	out := &ReviewResult{
		Decision: policy.Decision{Outcome: "preview_only"},
		PublicationPreview: PublicationPreview{
			Plan: plan, Before: file.Content, After: edit.Content,
			Title: edit.Title, Task: edit.Task, Draft: edit.Draft,
			CI: ciImpact(repo.Dir, edit.Path, ""),
		},
	}
	target, err := consolepublish.Destination(ctx, repo.Dir)
	if err != nil {
		if apply {
			return nil, &reviewError{422, err}
		}
		out.Warning = err.Error()
		return out, nil
	}
	out.Destination = target
	baseline, err := consolepublish.SourceBaseline(repo.Dir, plan.Sources, edit.Path, file.Content)
	if err != nil {
		if apply {
			return nil, &reviewError{409, err}
		}
		out.Warning = err.Error()
		return out, nil
	}
	evidence, err := json.Marshal(plan)
	if err != nil {
		return nil, &reviewError{500, errors.New("could not encode the preview")}
	}
	head, err := repo.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, &reviewError{409, errors.New("repository has no commit to review")}
	}
	payload := struct {
		Edit                                           Edit
		Baseline                                       map[string]string
		Root, Destination, Head, Evidence, Session, CI string
	}{edit, baseline, repo.Dir, target, strings.TrimSpace(head), hash(evidence), hash([]byte(session)), out.CI}
	if !apply {
		token = ""
	}
	gate, err = publicationGate(ctx, repo, gate, edit.Title)
	if err == nil {
		out.Gate = gate
		out.Decision, err = gate.Check(policy.Remote, "console.publish", payload, token, true)
	}
	if err != nil {
		if apply {
			return nil, &reviewError{409, err}
		}
		out.Outcome = "preview_only"
		out.Warning = err.Error()
		return out, nil
	}
	if out.Outcome == policy.SuggestOnly {
		out.Warning = out.Reason + ". For a manual edit, save the preview diff as agentflow-config.patch."
		out.Commands = []string{"git apply --check agentflow-config.patch", "git apply agentflow-config.patch"}
	}
	if !apply || out.Outcome != policy.Proceed || token == "" {
		return out, nil
	}
	if publishTo == nil {
		publishTo = consolepublish.PublishTo
	}
	published, err := publishTo(ctx, repo.Dir, target, edit.Path, file.Content, edit.Content, edit.Title, edit.Draft, baseline, edit.Task)
	if err != nil {
		gate.Audit(policy.Remote, "console.publish", payload, "error")
		return nil, &reviewError{409, err}
	}
	gate.Audit(policy.Remote, "console.publish", payload, published.State)
	out.Outcome = "published"
	out.Published = &published
	return out, nil
}

func publicationGate(ctx context.Context, repo *gitx.Repo, gate *policy.Gate, title string) (*policy.Gate, error) {
	cfg, _, err := config.LoadRepoCore(repo.Dir)
	if err != nil {
		return nil, errors.New("cannot read the current repository approval policy")
	}
	message := commit.Parse(title)
	if strings.ContainsAny(title, "\r\n") || !message.Conventional || message.Exempt {
		return nil, errors.New("enter a conventional commit title")
	}
	if issues := commit.Validate(title, cfg.Commit, gitx.KeyFromBranch(repo.Branch(ctx))); len(issues) > 0 {
		return nil, errors.New(issues[0].Message)
	}
	return configuredGate(ctx, repo, gate, "repository-console", cfg)
}
