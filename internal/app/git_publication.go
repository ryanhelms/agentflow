package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
)

type publication struct {
	remotes                gitx.Detection
	branch, base, sha, url string
}

func (a *App) branchPublication(ctx context.Context, willPush bool) (*publication, error) {
	remotes, err := a.githubRemotes(ctx)
	if err != nil {
		return nil, err
	}
	var raw string
	if willPush {
		raw, err = a.pushURL(ctx, remotes.Source)
		if err != nil {
			return nil, err
		}
	}
	branch := a.Repo.Branch(ctx)
	if branch == "" || policy.IsProtected(branch, a.Cfg.Policy.ProtectedBranches) {
		return nil, errors.New("a feature branch is required for publication")
	}
	sha, err := a.Repo.Git(ctx, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return nil, err
	}
	base := "main"
	if sym, err := a.Repo.Git(ctx, "symbolic-ref", "--quiet", "refs/remotes/"+remotes.Target.Name+"/HEAD"); err == nil {
		base = strings.TrimPrefix(strings.TrimSpace(sym), "refs/remotes/"+remotes.Target.Name+"/")
	}
	return &publication{remotes: remotes, branch: branch, base: base, sha: strings.TrimSpace(sha), url: raw}, nil
}

func (p *publication) checkLocal(ctx context.Context, a *App) error {
	current, err := a.branchPublication(ctx, p.url != "")
	if err != nil {
		return err
	}
	if current.branch != p.branch || current.sha != p.sha || current.url != p.url || current.base != p.base || current.remotes.Source.Name != p.remotes.Source.Name || current.remotes.Target.Name != p.remotes.Target.Name || current.remotes.Target.URL.Path != p.remotes.Target.URL.Path {
		return errors.New("branch or publication destination changed; prepare publication again")
	}
	return nil
}

func (p *publication) suggest() string {
	return "git push --no-follow-tags --recurse-submodules=no -- " + shellQuote(p.remotes.Source.Name) + " " + shellQuote(p.sha+":refs/heads/"+p.branch)
}

// Push the approved object ID to the resolved endpoint. A later ref or remote
// config change cannot substitute a newer local commit or a different name.
func (p *publication) push(ctx context.Context, a *App) error {
	if err := p.checkLocal(ctx, a); err != nil {
		return err
	}
	ref := "refs/heads/" + p.branch
	name := p.remotes.Source.Name
	// Keep the named remote so pre-push hooks and custom remote-tracking
	// refspecs retain their usual behavior. Resolve a private alias directly to
	// the already-validated transport, avoiding a second URL rewrite.
	alias := "devflow-approved://" + rand.Text()
	key := "remote." + name
	configArgs := []string{"-c", "url." + p.url + ".insteadOf=" + alias,
		"-c", key + ".pushurl=", "-c", key + ".pushurl=" + alias, "-c", key + ".mirror=false"}
	run := func(args ...string) (string, error) {
		out, err := a.Repo.Git(ctx, append(configArgs, args...)...)
		if err != nil {
			// Never include credentials embedded in the verified transport URL.
			return "", fmt.Errorf("git %s on %s: %s", args[0], name, strings.ReplaceAll(err.Error(), p.url, name))
		}
		return out, nil
	}
	targets, err := run("remote", "get-url", "--push", "--all", "--", name)
	if err != nil {
		return err
	}
	if strings.TrimSuffix(targets, "\n") != p.url {
		return errors.New("git could not pin a single approved push destination")
	}
	if _, err = run("push", "--no-follow-tags", "--recurse-submodules=no", "--", name, p.sha+":"+ref); err != nil {
		return err
	}
	remote, err := run("ls-remote", "--refs", "--", alias, ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(remote) != p.sha+"\t"+ref {
		return errors.New("remote branch changed after push; review its current revision")
	}
	// git push maintains remote-tracking refs, but cannot set a branch upstream
	// for an explicit SHA source. Set it after confirming the remote revision.
	if _, err = a.Repo.Git(ctx, "config", "--local", "branch."+p.branch+".remote", name); err != nil {
		return err
	}
	_, err = a.Repo.Git(ctx, "config", "--local", "branch."+p.branch+".merge", ref)
	return err
}

// PushPrepare describes the feature branch publication without publishing it.
func (a *App) PushPrepare(ctx context.Context) (any, error) { return a.publishBranch(ctx, "", true) }

// Push publishes the current feature branch without opening or promoting a PR.
func (a *App) Push(ctx context.Context, token string) (any, error) {
	return a.publishBranch(ctx, token, false)
}

func (a *App) publishBranch(ctx context.Context, token string, prepare bool) (any, error) {
	p, err := a.branchPublication(ctx, true)
	if err != nil {
		return nil, err
	}
	ready, err := a.PRReady(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, check := range ready.Checks {
		if !check.OK && check.Name != "working tree clean" {
			return map[string]any{"status": "not_ready", "readiness": ready}, nil
		}
	}
	if err = p.checkLocal(ctx, a); err != nil {
		return nil, err
	}
	summary := map[string]any{"repository": a.Repo.Dir, "source_repo": p.remotes.Source.URL.Path, "source_remote": p.remotes.Source.Name, "branch": p.branch, "head_sha": p.sha}
	commands := []string{p.suggest()}
	if prepare {
		return a.prepareWithActions(policy.Remote, "push", nil, summary, commands)
	}
	if pending, err := a.gate(policy.Remote, "push", summary, token, false, commands); pending != nil || err != nil {
		return pending, err
	}
	err = p.push(ctx, a)
	a.audit(policy.Remote, "push", summary, err)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "pushed", "branch": p.branch, "head_sha": p.sha}, nil
}

// MarkReadyPrepare identifies the remote draft and decision without promoting it.
func (a *App) MarkReadyPrepare(ctx context.Context) (any, error) {
	return a.promoteDraft(ctx, "", true)
}

// MarkReady promotes the exact remote draft for the current feature branch.
func (a *App) MarkReady(ctx context.Context, token string) (any, error) {
	return a.promoteDraft(ctx, token, false)
}

func (a *App) promoteDraft(ctx context.Context, token string, prepare bool) (any, error) {
	p, err := a.branchPublication(ctx, false)
	if err != nil {
		return nil, err
	}
	repo := p.remotes.Target.URL.Path
	head := githubHead(p.remotes, p.branch)
	client, err := a.GitHub(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := client.UniquePRForBranch(ctx, repo, head)
	if err != nil {
		return nil, err
	}
	if pr == nil || pr.Number <= 0 || pr.NodeID == "" {
		return nil, errors.New("no open pull request identified for this branch")
	}
	if pr.Head.Ref != p.branch || pr.Head.SHA != p.sha {
		return nil, errors.New("push the reviewed local revision before marking its pull request ready")
	}
	if !pr.Draft {
		return map[string]any{"status": "already_ready", "number": pr.Number, "url": pr.HTMLURL}, nil
	}
	summary := map[string]any{"repo": repo, "number": pr.Number, "node_id": pr.NodeID, "title": pr.Title, "body": pr.Body, "draft": pr.Draft, "base": pr.Base.Ref, "head": head, "head_sha": pr.Head.SHA, "source_repo": p.remotes.Source.URL.Path}
	command := fmt.Sprintf("gh pr ready %d --repo %s", pr.Number, shellQuote(repo))
	commands := []string{command}
	if prepare {
		return a.prepareWithActions(policy.Remote, "pr_ready_for_review", nil, summary, commands)
	}
	if pending, err := a.gate(policy.Remote, "pr_ready_for_review", summary, token, false, commands); pending != nil || err != nil {
		return pending, err
	}
	// GitHub has no compare-and-set head input for this mutation. Recheck the
	// remote draft immediately before it, then verify the head in its response.
	latest, err := client.UniquePRForBranch(ctx, repo, head)
	if err != nil {
		return nil, err
	}
	if latest == nil || latest.NodeID != pr.NodeID || latest.Number != pr.Number || latest.Head != pr.Head || latest.Base != pr.Base || latest.Title != pr.Title || latest.Body != pr.Body || !latest.Draft {
		return nil, errors.New("pull request changed; prepare the current request again")
	}
	err = client.MarkReady(ctx, pr.NodeID, p.sha)
	a.audit(policy.Remote, "pr_ready_for_review", summary, err)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ready_for_review", "number": pr.Number, "url": pr.HTMLURL, "head_sha": p.sha}, nil
}
