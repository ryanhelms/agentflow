// Package consolepublish proposes one reviewed repository setting through GitHub.
// Its caller owns the interactive policy gate and validates the candidate with
// consolecfg.Preview immediately before publication.
package consolepublish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/provider"
)

type Result struct {
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Revision string `json:"revision,omitempty"`
	Warning  string `json:"warning,omitempty"`
	CI       string `json:"ci,omitempty"`
}

// Destination resolves the configured GitHub target offline, for display and
// binding to the user's approval. Each publish verifies this selection again.
func Destination(ctx context.Context, root string) (string, error) {
	a, err := localApp(ctx, root)
	if err != nil {
		return "", err
	}
	return destination(ctx, a)
}

func localApp(ctx context.Context, root string) (*app.App, error) {
	r, err := gitx.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	snapshot, err := consolecfg.View(ctx, r.Dir)
	if err != nil {
		return nil, err
	}
	cfg := snapshot.Repository()
	if cfg == nil {
		return nil, errors.New("the current repository configuration must resolve before its publication destination can be trusted")
	}
	return &app.App{Repo: r, Cfg: cfg, Client: "console"}, nil
}

func destination(ctx context.Context, a *app.App) (string, error) {
	// GitHubRepo validates the effective provider, hostname, ports and both
	// remotes. Publishing in a fork needs a separate source-side branch.
	target, err := a.GitHubRepo(ctx)
	if err != nil {
		return "", err
	}
	opts := gitx.DetectOptions{}
	if settings, ok := a.Cfg.Providers.CodeHosting.Settings.(*config.CodeHostingSettings); ok {
		opts.Hosts = settings.Hosts
		if settings.SourceRemote != nil {
			opts.Source = *settings.SourceRemote
		}
		if settings.TargetRemote != nil {
			opts.Target = *settings.TargetRemote
		}
	}
	d, err := a.Repo.DetectForge(ctx, opts)
	if err != nil {
		return "", err
	}
	if !d.OK() || d.Fork {
		return "", errors.New("configuration publication requires a single GitHub source and target repository")
	}
	return target, nil
}

// PublishTo creates or recovers a single-file PR. It never stages, commits or
// checks out files in the developer's workspace.
func PublishTo(ctx context.Context, root, expectedDestination, path, original, content, title string, draft bool, reviewed map[string]string, taskRef ...string) (Result, error) {
	a, err := localApp(ctx, root)
	if err != nil {
		return Result{}, err
	}
	repo, err := destination(ctx, a)
	if err != nil {
		return Result{}, err
	}
	if expectedDestination == "" || !strings.EqualFold(expectedDestination, repo) {
		return Result{}, errors.New("the publication destination changed; preview again")
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
		return Result{}, errors.New("configuration must be UTF-8 text")
	}
	if app.ContainsSecret(title) {
		return Result{}, errors.New("the commit subject contains a credential-like value")
	}
	if !commit.Parse(title).Conventional || strings.ContainsFunc(title, unicode.IsControl) || strings.TrimSpace(title) != title || skipDirective(title) {
		return Result{}, errors.New("enter a single conventional commit subject without CI skip directives")
	}
	if issues := commit.ValidatePRTitle(title, a.Cfg.Commit, ""); len(issues) > 0 {
		return Result{}, fmt.Errorf("invalid PR title: %s", issues[0].Message)
	}
	trailer, err := taskTrailer(a, taskRef)
	if err != nil {
		return Result{}, err
	}
	msg := title
	if trailer != "" {
		msg += "\n\n" + trailer
	}
	if issues := a.Validate(ctx, msg); len(issues) > 0 {
		return Result{}, fmt.Errorf("invalid commit: %s", issues[0].Message)
	}
	digest := sha256.Sum256([]byte(original))
	plan, err := consolecfg.Preview(ctx, root, path, content, hex.EncodeToString(digest[:]))
	if err != nil {
		return Result{}, err
	}
	deps, err := SourceBaseline(root, plan.Sources, path, original)
	if err != nil {
		return Result{}, err
	}
	if !maps.Equal(reviewed, deps) {
		return Result{}, errors.New("configuration inputs changed; review the current proposal again")
	}
	client, err := a.GitHub(ctx)
	if err != nil {
		return Result{}, err
	}
	change := proposal{Path: path, Original: original, Content: content, Title: title, Trailer: trailer}
	return publish(ctx, client, repo, change, deps, draft)
}

func taskTrailer(a *app.App, explicit []string) (string, error) {
	if len(explicit) > 1 {
		return "", errors.New("expected at most one task reference")
	}
	ref := ""
	if len(explicit) == 1 {
		ref = explicit[0]
	}
	if ref == "" {
		return "", nil
	}
	if !a.UsesTaskRefs() {
		return "", errors.New("this task provider uses issue keys in the commit subject")
	}
	if app.ContainsSecret(ref) {
		return "", errors.New("the task reference contains a credential-like value")
	}
	parsed, err := provider.ParseRef(ref)
	if err != nil {
		return "", err
	}
	if parsed.Instance != a.Cfg.Providers.TaskManagement.Provider {
		return "", errors.New("task reference belongs to another provider")
	}
	return provider.TaskTrailer + parsed.String(), nil
}

type proposal struct {
	Path, Original, Content, Title, Trailer string
	quiet                                   bool
}

func (p proposal) body() string {
	if p.quiet {
		return strings.TrimSpace("[skip actions]\n\n" + p.Trailer)
	}
	return p.Trailer
}

// A retry may see different branch rules or a caller with different access
// to those rules. Both generated messages represent the same reviewed edit.
func (p proposal) matchesMessage(actual string) bool {
	for _, quiet := range []bool{false, true} {
		p.quiet = quiet
		if actual == strings.TrimSpace(p.Title+"\n\n"+p.body()) {
			return true
		}
	}
	return false
}

func (p proposal) branch(defaultName string) string {
	data, _ := json.Marshal([6]string{defaultName, p.Path, p.Original, p.Content, p.Title, p.Trailer})
	sum := sha256.Sum256(data)
	return "agentflow/config-" + hex.EncodeToString(sum[:16])
}

type repository struct {
	Default string `json:"default_branch"`
	Squash  bool   `json:"allow_squash_merge"`
	Auto    bool   `json:"allow_auto_merge"`
}

func publish(ctx context.Context, client *gh.Client, repo string, p proposal, deps map[string]string, draft bool) (Result, error) {
	api := remote{client: client, slug: repo, url: "/repos/" + repo}
	var info repository
	if err := client.Do(ctx, "GET", api.url, nil, &info); err != nil {
		return Result{}, err
	}
	if info.Default == "" {
		return Result{}, errors.New("GitHub did not return the default branch")
	}
	branch := p.branch(info.Default)
	pr, err := api.pull(ctx, info.Default, branch)
	if err != nil {
		return Result{}, err
	}
	base, landed, err := api.baseline(ctx, info.Default, p, deps)
	if err != nil {
		return Result{}, err
	}
	if landed {
		result := Result{State: "already_applied", Revision: base}
		if pr != nil && pr.MergedAt != "" {
			result.URL, result.Branch = pr.HTMLURL, branch
		}
		return result, nil
	}
	if pr != nil && pr.State != "open" {
		return Result{State: "closed", URL: pr.HTMLURL, Branch: branch, Warning: "The prior configuration PR was closed. Review its outcome before proposing again."}, nil
	}
	head, err := api.ensureHead(ctx, base, branch)
	if err != nil {
		return Result{}, err
	}
	p.quiet = api.quietCI(ctx, info.Default, branch)
	head, err = api.ensureCommit(ctx, base, branch, head, p)
	if err != nil {
		return Result{}, err
	}
	if pr == nil {
		body := "Update the repository configuration reviewed in the Agentflow console."
		if p.Trailer != "" {
			body += "\n\n" + p.Trailer
		}
		_, createErr := client.CreatePR(ctx, repo, p.Title, body, branch, info.Default, draft)
		pr, err = api.pull(ctx, info.Default, branch)
		if err != nil || pr == nil {
			return Result{}, errors.Join(errors.New("could not verify the configuration pull request; retry to reconcile"), createErr, err)
		}
	}
	if pr.Head.SHA != head || pr.Title != p.Title || pr.State != "open" {
		return Result{}, errors.New("the configuration pull request changed; review it before continuing")
	}
	result := Result{State: "open", URL: pr.HTMLURL, Branch: branch, Revision: head}
	if pr.Draft {
		result.State = "draft"
	}
	if !draft {
		var latest repository
		if err := client.Do(ctx, "GET", api.url, nil, &latest); err != nil || latest.Default != info.Default {
			result.Warning = "Could not confirm the current default branch. Review the configuration PR before merging."
			return result, nil
		}
		latestBase, err := api.ref(ctx, latest.Default)
		if err != nil {
			result.Warning = "Could not confirm the current default revision. Open the PR or retry."
			return result, nil
		}
		if latestBase != base {
			revision, applied, err := api.baseline(ctx, latest.Default, p, deps)
			if err != nil {
				result.Warning = "The default branch changed while creating the PR. Refresh your configuration and preview again."
				return result, nil
			}
			if applied {
				result.State = "already_applied"
				result.Revision = revision
				return result, nil
			}
		}
		info = latest
		// Rules can change while the PR is being opened. Leave the squash push
		// under normal triggers if the host can no longer confirm a safe skip.
		if p.quiet {
			p.quiet = api.quietCI(ctx, info.Default, branch)
		}
	}
	result = api.finish(ctx, info, *pr, p, head, draft)
	if p.quiet {
		result.CI = "Requested [skip actions] for eligible GitHub Actions push and pull_request runs. Branch creation, other events, and other CI providers can still trigger work."
	} else {
		result.CI = "Normal CI triggers apply. A compatible skip and clear branch rules could not be verified."
	}
	return result, nil
}
