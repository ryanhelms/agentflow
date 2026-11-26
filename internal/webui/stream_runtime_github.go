package webui

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// The daemon's GitHub poller (lane 2.3, ADR "Phase 2, GitHub poller"). It
// is a runtimeExtension: the poller polls every served repository that has
// a GitHub remote, and this file keeps its targets current.

func init() {
	runtimeExtensions = append(runtimeExtensions, func() runtimeExtension { return &githubExtension{} })
}

// The extension's plumbing. Tests replace these to inject fakes.
var (
	// newGitHubPoller builds the poller. api is how it reaches GitHub for
	// one repository.
	newGitHubPoller = func(api func(context.Context, watch.GitHubTarget) (watch.GitHubAPI, error), onError func(error)) watch.GitHubPoller {
		return watch.NewGitHubPoller(watch.GitHubOptions{API: api, Classify: gh.Classify, OnError: onError})
	}
	// githubSlug returns "owner/name" of the GitHub remote of the worktree
	// at root, as every other GitHub call finds it (app.GitHubRepo).
	githubSlug = func(ctx context.Context, root string) (string, error) {
		a, err := openGitHubApp(root)
		if err != nil {
			return "", err
		}
		return a.GitHubRepo(ctx)
	}
	// githubClient returns the client of the worktree at root. It
	// authenticates exactly as every other GitHub call (App.GitHub): the gh
	// CLI login, or GH_TOKEN and GITHUB_TOKEN.
	githubClient = func(ctx context.Context, root string) (watch.GitHubAPI, error) {
		a, err := openGitHubApp(root)
		if err != nil {
			return nil, err
		}
		return a.GitHub(ctx)
	}
	// githubRefreshEvery is how often targets are rebuilt. The rebuild reads
	// a few small files per worktree and sets a target only when it changed.
	githubRefreshEvery = 5 * time.Second
	// githubSlugEvery is how often a repository's GitHub remote is looked up
	// again, and how long a repository without one waits for a retry.
	githubSlugEvery = 5 * time.Minute
)

func openGitHubApp(root string) (*app.App, error) {
	cfg, _, err := config.LoadRepo(root)
	if err != nil {
		return nil, err
	}
	return &app.App{Repo: &gitx.Repo{Dir: root}, Cfg: cfg}, nil
}

// githubExtension runs the poller and feeds it targets.
type githubExtension struct {
	rt     *eventRuntime
	poller watch.GitHubPoller
	cancel context.CancelFunc
	wg     sync.WaitGroup
	kick   chan struct{}

	mu    sync.Mutex
	repos map[bus.RepoID]*githubRepo
}

// githubRepo is what the extension remembers about one repository.
type githubRepo struct {
	slug   string
	lookup time.Time // when slug was last looked up
	client watch.GitHubAPI
	root   string // the worktree the client was made for
	target watch.GitHubTarget
	set    bool // target was passed to the poller
}

func (x *githubExtension) start(rt *eventRuntime) error {
	x.rt = rt
	x.repos = map[bus.RepoID]*githubRepo{}
	x.kick = make(chan struct{}, 1)
	var lastErr string
	x.poller = newGitHubPoller(x.api, func(err error) {
		// A persistent failure repeats at every backoff step; say it once.
		if msg := err.Error(); msg != lastErr {
			lastErr = msg
			fmt.Fprintln(os.Stderr, "agentflow:", msg)
		}
	})
	ctx, cancel := context.WithCancel(rt.ctx)
	x.cancel = cancel
	x.wg.Add(2)
	go func() {
		defer x.wg.Done()
		if err := x.poller.Run(ctx, rt.bus); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "agentflow: github poller stopped:", err)
		}
	}()
	go func() {
		defer x.wg.Done()
		x.refreshLoop(ctx)
	}()
	return nil
}

// attached asks the refresh loop to look at the repository again. It never
// waits: it runs on the daemon's attach path.
func (x *githubExtension) attached(*repoEvents, registeredWorktree) {
	select {
	case x.kick <- struct{}{}:
	default:
	}
}

// stop ends the poller before the bus closes, so it never publishes into a
// closed bus.
func (x *githubExtension) stop() {
	x.cancel()
	x.wg.Wait()
}

// githubCache returns the poller's cache for projections, or nil when the
// poller did not start. Reading it never calls GitHub.
func (rt *eventRuntime) githubCache() watch.GitHubCache {
	for _, e := range rt.exts {
		if x, ok := e.(*githubExtension); ok && x.poller != nil {
			return x.poller
		}
	}
	return nil
}

func (x *githubExtension) refreshLoop(ctx context.Context) {
	tick := time.NewTicker(githubRefreshEvery)
	defer tick.Stop()
	for {
		x.refreshAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-x.kick:
		case <-tick.C:
		}
	}
}

func (x *githubExtension) refreshAll(ctx context.Context) {
	x.rt.mu.Lock()
	repos := make([]*repoEvents, 0, len(x.rt.repos))
	for _, r := range x.rt.repos {
		repos = append(repos, r)
	}
	x.rt.mu.Unlock()
	for _, r := range repos {
		if ctx.Err() != nil {
			return
		}
		x.refresh(ctx, r)
	}
}

// refresh rebuilds r's target and passes it to the poller when it changed:
// which branch each served worktree has checked out, which issue each is
// bound to, and whether anyone watches.
func (x *githubExtension) refresh(ctx context.Context, r *repoEvents) {
	branches := map[bus.WorktreeID]string{}
	var roots []registeredWorktree
	for _, id := range r.worktreeIDs() {
		w, ok := r.worktree(id)
		if !ok || !w.available() {
			continue
		}
		roots = append(roots, w)
		if b := checkedOutBranch(w.GitDir); b != "" {
			branches[id] = b
		}
	}
	slug := x.slugFor(ctx, r.id, roots)
	issues := map[bus.WorktreeID]int{}
	if slug != "" {
		for _, w := range roots {
			if n, ok := boundIssue(w.Root, slug); ok {
				issues[bus.WorktreeID(w.ID)] = n
			}
		}
	}
	t := watch.GitHubTarget{Repo: r.id, Slug: slug, Branches: branches, Issues: issues, Watched: repoWatched(r)}
	x.mu.Lock()
	gr := x.repo(r.id)
	same := gr.set && gr.target.Slug == t.Slug && gr.target.Watched == t.Watched &&
		maps.Equal(gr.target.Branches, t.Branches) && maps.Equal(gr.target.Issues, t.Issues)
	if !same {
		gr.target, gr.set = t, true
	}
	x.mu.Unlock()
	if !same {
		x.poller.Set(t)
	}
}

func (x *githubExtension) repo(id bus.RepoID) *githubRepo {
	gr := x.repos[id]
	if gr == nil {
		gr = &githubRepo{}
		x.repos[id] = gr
	}
	return gr
}

// slugFor returns the repository's GitHub slug, or "" when it has no GitHub
// remote. It looks again every githubSlugEvery, because remotes change.
func (x *githubExtension) slugFor(ctx context.Context, id bus.RepoID, roots []registeredWorktree) string {
	x.mu.Lock()
	gr := x.repo(id)
	slug, fresh := gr.slug, !gr.lookup.IsZero() && time.Since(gr.lookup) < githubSlugEvery
	x.mu.Unlock()
	if fresh || len(roots) == 0 {
		return slug
	}
	root := primaryRoot(roots)
	found, err := githubSlug(ctx, root)
	if err != nil {
		found = "" // no GitHub remote, or the configuration is not readable
	}
	x.mu.Lock()
	gr.slug, gr.lookup = found, time.Now()
	x.mu.Unlock()
	return found
}

// api returns the client for one repository's poll cycle, made once per
// worktree root and kept while that root still exists.
func (x *githubExtension) api(ctx context.Context, t watch.GitHubTarget) (watch.GitHubAPI, error) {
	x.rt.mu.Lock()
	r := x.rt.byID[t.Repo]
	x.rt.mu.Unlock()
	if r == nil {
		return nil, fmt.Errorf("repository %s is not served", t.Repo)
	}
	var roots []registeredWorktree
	for _, id := range r.worktreeIDs() {
		if w, ok := r.worktree(id); ok && w.available() {
			roots = append(roots, w)
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("repository %s has no available worktree", t.Repo)
	}
	root := primaryRoot(roots)
	x.mu.Lock()
	gr := x.repo(t.Repo)
	client, cached := gr.client, gr.root
	x.mu.Unlock()
	// The client runs gh in a worktree directory, so a removed worktree
	// needs a new one.
	if client != nil && cached != "" {
		if _, err := os.Stat(cached); err == nil {
			return client, nil
		}
	}
	client, err := githubClient(ctx, root)
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	gr.client, gr.root = client, root
	x.mu.Unlock()
	return client, nil
}

// primaryRoot prefers the primary checkout, which outlives task worktrees.
func primaryRoot(roots []registeredWorktree) string {
	for _, w := range roots {
		if w.ID == "main" {
			return w.Root
		}
	}
	return roots[0].Root
}

// repoWatched reports whether an event stream of the repository is open.
func repoWatched(r *repoEvents) bool {
	if r.projector == nil {
		return false
	}
	r.projector.mu.Lock()
	defer r.projector.mu.Unlock()
	return len(r.projector.listeners) > 0
}

// checkedOutBranch reads the branch HEAD points to, or "" when HEAD is
// detached or unreadable.
func checkedOutBranch(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil || len(b) > 4096 {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
	if !ok {
		return ""
	}
	return ref
}

// boundIssue returns the GitHub issue the worktree at root is bound to, when
// it belongs to the repository slug: `agentflow start --worktree` records it
// as "owner/name#number".
func boundIssue(root, slug string) (int, bool) {
	rec, err := worktree.ReadRecord(root)
	if err != nil {
		return 0, false
	}
	repo, num, ok := strings.Cut(rec.Issue, "#")
	if !ok || !strings.EqualFold(repo, slug) {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil && n > 0
}
