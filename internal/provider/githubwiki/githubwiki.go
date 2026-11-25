// Package githubwiki publishes managed Markdown to a GitHub wiki's default
// branch. Enable Wikis in the repository settings and create the first page
// in the web UI before using the adapter.
//
// The agentflow-pages directory is reserved for FileSystem document records.
// Ordinary Markdown pages elsewhere in the wiki, including Home.md, stay
// editable in GitHub and are outside this provider's search and write scope.
// To adopt an ordinary page, publish its body under a new provider reference.
// Keep the generated document-<storage key>.md filename and its front matter
// when editing a managed page. GitHub wiki page slugs are wiki-wide, so keep
// these filenames unique across the wiki.
package githubwiki

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/repository"
)

const directory = "agentflow-pages"

// Config identifies a github.com repository by owner/name. Origin optionally
// overrides the Git transport with an absolute local path, for example to test
// against a bare repository. The override does not affect the public page URLs.
type Config struct {
	Instance   string
	Repository string
	Origin     string
	// BeforeWrite checks the resolved base and push target before publishing.
	BeforeWrite func(base, target string) error
}

type Knowledge struct {
	instance    string
	origin      string
	wikiURL     string
	beforeWrite func(base, target string) error
}

var _ provider.Knowledge = (*Knowledge)(nil)

// NewKnowledge validates configuration without contacting the wiki.
func NewKnowledge(c Config) (*Knowledge, error) {
	if err := gitx.ValidateGitHubSlug(c.Repository); err != nil {
		return nil, err
	}
	if err := (provider.Ref{Instance: c.Instance, Scope: "_", ID: "_"}).Validate(); err != nil {
		return nil, fmt.Errorf("invalid wiki provider instance: %w", err)
	}
	base := "https://github.com/" + c.Repository
	origin := c.Origin
	if origin == "" {
		origin = base + ".wiki.git"
	} else if !filepath.IsAbs(origin) || strings.ContainsFunc(origin, unicode.IsControl) {
		return nil, errors.New("wiki origin override must be an absolute local path")
	}
	return &Knowledge{instance: c.Instance, origin: origin, wikiURL: base + "/wiki", beforeWrite: c.BeforeWrite}, nil
}

// Destination returns the wiki Git origin and the path containing managed pages.
func (k *Knowledge) Destination() (origin, path string) {
	return k.origin, directory
}

func (k *Knowledge) pageURL(slug string) string {
	return k.wikiURL + "/" + url.PathEscape(slug)
}

func (k *Knowledge) Get(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	repo, err := k.repository(ctx)
	if err != nil {
		return provider.Document{}, err
	}
	doc, err := repo.Get(ctx, ref)
	if err == nil {
		doc.URL = k.pageURL("document-" + doc.Ref.StorageKey())
	}
	return doc, err
}

func (k *Knowledge) Find(ctx context.Context, query provider.DocumentQuery) ([]provider.Document, error) {
	repo, err := k.repository(ctx)
	if err != nil {
		return nil, err
	}
	docs, err := repo.Find(ctx, query)
	for i := range docs {
		docs[i].URL = k.pageURL("document-" + docs[i].Ref.StorageKey())
	}
	return docs, err
}

func (k *Knowledge) Publish(ctx context.Context, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	repo, err := k.repository(ctx)
	if err != nil {
		return provider.WriteResult{}, err
	}
	result, err := repo.Publish(ctx, ref, draft, opts)
	if err == nil {
		result.URL = k.pageURL("document-" + result.Ref.StorageKey())
	}
	return result, err
}

func (k *Knowledge) repository(ctx context.Context) (*repository.Knowledge, error) {
	branch, err := k.defaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	return repository.NewKnowledge(repository.Config{
		Instance: k.instance, Origin: k.origin, Branch: branch, Directory: directory, Publication: "push",
		BeforeWrite: k.beforeWrite,
	})
}

func (k *Knowledge) defaultBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", "--exit-code", "--", k.origin, "HEAD")
	cmd.Dir = os.TempDir()
	// Isolate Git configuration overrides from the caller's checkout, as the
	// Repository provider does for fetch and push.
	cmd.Env = []string{"GIT_ALLOW_PROTOCOL=file:https:http:ssh:git", "GIT_TERMINAL_PROMPT=0"}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return "", fmt.Errorf("wiki at %s has no initialized default branch; enable Wikis in repository settings and create the first page: %w", k.wikiURL, err)
		}
		return "", fmt.Errorf("cannot reach wiki at %s; enable Wikis in repository settings, create the first page, and check Git credentials and connectivity: %w", k.wikiURL, err)
	}
	for line := range strings.Lines(string(out)) {
		ref, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if branch, found := strings.CutPrefix(ref, "ref: refs/heads/"); ok && found && name == "HEAD" && branch != "" {
			return branch, nil
		}
	}
	return "", fmt.Errorf("wiki at %s has no symbolic default branch; create the first page or restore the wiki's default branch", k.wikiURL)
}
