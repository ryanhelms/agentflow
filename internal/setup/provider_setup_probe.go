package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
)

// Run probes without stdin, a pager, or printing the command's response. In
// particular, gh auth status output and API records never enter the setup plan.
func probeCommand(ctx context.Context, dir, command string, args ...string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func (p *ProviderSetupPlan) checkWiki(ctx context.Context, role selectedRole, settings *config.GitHubWikiSettings, detection gitx.Detection) {
	slug := value(settings.Repository)
	if settings.Repository == nil && detection.OK() && detection.Forge == gitx.ForgeGitHub && detection.Target.URL.Host == "github.com" {
		slug = detection.Target.URL.Path
	}
	if err := gitx.ValidateGitHubSlug(slug); err != nil {
		p.check(role.name, role.Provider, false, "configure knowledge_base.settings.repository with a GitHub.com owner/name or select a GitHub.com default remote")
		return
	}
	url := "https://github.com/" + slug + ".wiki.git"
	if !probeCommand(ctx, p.Root, "git", "-c", "credential.interactive=never", "ls-remote", "--exit-code", url, "HEAD") {
		p.check(role.name, role.Provider, false, "enable the GitHub wiki, create its first page, and configure Git access to "+url)
		return
	}
	p.check(role.name, role.Provider, true, "can read the wiki default branch at "+url)
}

func (p *ProviderSetupPlan) checkDirectory(role selectedRole, path string) {
	if path == "" {
		p.check(role.name, role.Provider, false, "set "+role.name+".settings.root to a readable directory")
		return
	}
	dir := path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(p.Root, dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		p.check(role.name, role.Provider, false, fmt.Sprintf("provide directory %q or update %s.settings.root", path, role.name))
		return
	}
	f, err := os.Open(dir)
	if err == nil {
		defer f.Close()
		_, err = f.Readdirnames(1)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		p.check(role.name, role.Provider, false, fmt.Sprintf("make %q a readable directory or update %s.settings.root", path, role.name))
		return
	}
	p.check(role.name, role.Provider, true, fmt.Sprintf("can list directory %q", path))
}

func (p *ProviderSetupPlan) checkRepository(ctx context.Context, role selectedRole, s *config.RepositorySettings, repo *gitx.Repo) {
	fail := func(detail string) { p.check(role.name, role.Provider, false, detail) }
	if s.Repository != nil {
		dest := *s.Repository
		// Only inspect an already available checkout. Never clone or fetch a
		// destination, or mistake a subdirectory for its enclosing checkout.
		if dest == "" {
			fail("set " + role.name + ".settings.repository to a local checkout or leave it unset for the current repository")
			return
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(p.Root, dest)
		}
		path, err := filepath.EvalSymlinks(dest)
		if err == nil {
			repo, err = gitx.Open(ctx, path)
		}
		if err != nil || repo == nil || filepath.Clean(repo.Dir) != filepath.Clean(path) {
			fail("provide an existing local checkout at " + role.name + ".settings.repository. Remote destinations require a checkout resolver")
			return
		}
	}
	if repo == nil {
		fail("open a Git working tree with the selected repository checked out")
		return
	}
	dir := filepath.Clean(s.Directory)
	if s.Directory == "" || !filepath.IsLocal(dir) {
		fail("set " + role.name + ".settings.directory to a directory inside the Git tree")
		return
	}
	branch := repo.DefaultBranch(ctx)
	if s.Branch != nil {
		branch = *s.Branch
	}
	if _, err := repo.Git(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		fail("set " + role.name + ".settings.branch to a valid Git branch name")
		return
	}
	var ref string
	// The tracked destination is preferred over a potentially stale local branch.
	for _, candidate := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if _, err := gitObject(ctx, repo, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
			ref = candidate
			break
		}
	}
	if ref == "" {
		fail(fmt.Sprintf("make branch %q available locally or as an origin tracking ref, or set %s.settings.branch", branch, role.name))
		return
	}
	path := filepath.ToSlash(dir)
	if path == "." {
		path = ""
	}
	kind, err := gitObject(ctx, repo, "cat-file", "-t", ref+":"+path)
	if err != nil || strings.TrimSpace(string(kind)) != "tree" {
		fail(fmt.Sprintf("provide directory %q on branch %q or update %s.settings.directory", dir, branch, role.name))
		return
	}
	p.check(role.name, role.Provider, true, fmt.Sprintf("can read Git tree %s:%s", ref, path))
}

// A partial clone can fetch and store missing objects during an ordinary read.
// Keep readiness local, including on Git versions predating GIT_NO_LAZY_FETCH.
func gitObject(ctx context.Context, repo *gitx.Repo, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo.Dir
	cmd.Env = append(cmd.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}

type githubProbe struct {
	dir       string
	detection gitx.Detection
	auth      map[string]bool
}

var githubRepoName = regexp.MustCompile(`^[a-zA-Z0-9-]+/[a-zA-Z0-9_.-]+$`)
var githubHostName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

// Probe the selected surface with GETs only. Empty collections count as
// readable. A successful read makes no claim about write permissions.
func (g *githubProbe) check(ctx context.Context, p *ProviderSetupPlan, role selectedRole, override *string) {
	check := func(ok bool, detail string) { p.check(role.name, role.Provider, ok, detail) }
	var repository string
	host := "github.com"
	if g.detection.OK() && g.detection.Forge == gitx.ForgeGitHub {
		host, _, _ = strings.Cut(g.detection.Target.ID(), "/")
		repository = g.detection.Target.URL.Path
	}
	if override != nil {
		repository = *override
	}
	if !githubRepoName.MatchString(repository) || strings.HasSuffix(repository, "/..") || strings.HasSuffix(repository, "/.") || !githubHostName.MatchString(host) {
		check(false, "select a GitHub remote in code_hosting or configure a GitHub owner/name for this provider")
		return
	}
	authenticated, checked := g.auth[host]
	if !checked {
		authenticated = probeCommand(ctx, g.dir, "gh", "auth", "status", "--hostname", host)
		g.auth[host] = authenticated
	}
	if !authenticated {
		check(false, "check connectivity and run gh auth login --hostname "+host+" or supply a token for that host in this environment")
		return
	}
	endpoints := map[string][]string{
		"code_hosting": {""}, "task_management": {"/issues?per_page=1"},
		"devops.checks": {"/commits/HEAD/check-runs?per_page=1"}, "devops.deployments": {"/deployments?per_page=1"},
		"devops.releases": {"/releases?per_page=1"}, "secops": {"/dependabot/alerts?per_page=1", "/code-scanning/alerts?per_page=1"},
	}
	reads, ok := endpoints[role.name]
	if !ok {
		check(false, "provide a read-only GitHub API probe for "+role.name)
		return
	}
	for _, suffix := range reads {
		endpoint := "repos/" + repository + suffix
		if !probeCommand(ctx, g.dir, "gh", "api", "--hostname", host, "--method", "GET", "--silent", endpoint) {
			check(false, fmt.Sprintf("verify %s exists on %s and the active identity can GET %s", repository, host, endpoint))
			return
		}
	}
	check(true, fmt.Sprintf("read %s on %s/%s", role.name, host, repository))
}
