// Package initproviders proposes repository provider selections from local evidence.
package initproviders

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/nscaledev/devflow/internal/commit"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/templates"
	"gopkg.in/yaml.v3"
)

const configFile = ".devflow/config.yml"

type Evidence struct {
	Provider string `json:"provider"`
	Source   string `json:"source"`
}

type Role struct {
	Role     string     `json:"role"`
	Default  string     `json:"default"`
	Options  []string   `json:"options"`
	Selected string     `json:"selected,omitempty"`
	Source   string     `json:"source"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type Plan struct {
	Root        string `json:"root"`
	Roles       []Role `json:"roles"`
	original    []byte
	definitions map[string][]byte
}

// Detect reads repository files and Git remotes. It never probes a service or inspects credentials.
func Detect(ctx context.Context, repo *gitx.Repo) (*Plan, error) {
	root, err := os.OpenRoot(repo.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readFile(root, configFile)
	if err != nil {
		return nil, err
	}
	defs := map[string][]byte{}
	for _, pattern := range []string{".devflow/providers/*.yml", ".devflow/providers/*.yaml"} {
		names, err := fs.Glob(root.FS(), pattern)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			defs[name], err = readFile(root, name)
			if err != nil {
				return nil, err
			}
		}
	}
	if _, _, err := config.InspectRepo(data, defs); err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	signals, err := discover(ctx, repo, root, fields)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Root: repo.Dir, original: data, definitions: defs}
	for _, r := range config.ProviderCatalog() {
		role := Role{Role: r.Role, Default: r.Default, Options: r.Providers, Source: "unknown", Evidence: signals[r.Role]}
		if local, ok := property(fields, r.Role+".settings").(map[string]any); ok && len(local) > 0 {
			role.Evidence = append(role.Evidence, Evidence{r.Default, configFile + " " + r.Role + ".settings uses the default provider schema"})
		}
		if id, ok := property(fields, r.Role+".provider").(string); ok {
			role.Selected, role.Source = id, "saved"
			role.Evidence = []Evidence{{id, configFile + " " + r.Role + ".provider"}}
		} else if len(role.Evidence) > 0 {
			role.Selected, role.Source = role.Evidence[0].Provider, "detected"
			for _, e := range role.Evidence[1:] {
				if e.Provider != role.Selected {
					role.Selected, role.Source = "", "ambiguous"
					break
				}
			}
		}
		plan.Roles = append(plan.Roles, role)
	}
	return plan, nil
}

func property(m map[string]any, dotted string) any {
	var cur any = m
	for _, k := range strings.Split(dotted, ".") {
		obj, _ := cur.(map[string]any)
		cur = obj[k]
	}
	return cur
}

func readFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("%s must be a regular file of at most 1 MiB", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, (1<<20)+1))
}

func discover(ctx context.Context, repo *gitx.Repo, root *os.Root, fields map[string]any) (map[string][]Evidence, error) {
	signals := map[string][]Evidence{}
	add := func(role, id, source string) {
		if strings.ContainsFunc(source, unicode.IsControl) {
			return
		}
		ev := Evidence{id, source}
		if !slices.Contains(signals[role], ev) {
			signals[role] = append(signals[role], ev)
		}
	}
	forge, err := repo.DetectForge(ctx, gitx.DetectOptions{})
	if err != nil {
		return nil, err
	}
	if forge.OK() && forge.Forge == gitx.ForgeGitHub {
		add("code_hosting", "github", "Git remote resolves to GitHub")
	}
	// The generated sample preferences do not establish a team's provider.
	template, err := templates.Repo.ReadFile("repo/" + configFile)
	if err != nil {
		return nil, err
	}
	var sample map[string]any
	if err := yaml.Unmarshal(template, &sample); err != nil {
		return nil, err
	}
	projects, _ := property(fields, "jira.projects").([]any)
	if site, _ := property(fields, "jira.site").(string); (site != "" && site != property(sample, "jira.site")) ||
		(len(projects) > 0 && !reflect.DeepEqual(projects, property(sample, "jira.projects"))) {
		add("task_management", "jira", configFile+" Jira configuration")
	}
	if site, _ := property(fields, "confluence.site").(string); site != "" {
		add("knowledge_base", "confluence", configFile+" Confluence site")
	}
	// Inspect only conventional subjects and structured task references, never arbitrary issue text.
	history, _ := repo.Git(ctx, "log", "-25", "--no-patch", "--format=%s%n%(trailers:key=Task-Ref)")
	for line := range strings.SplitSeq(history, "\n") {
		if m := commit.Parse(line); m.Conventional && len(m.Keys) > 0 {
			add("task_management", "jira", "recent Git commits use Jira-style issue scopes")
		}
		value, ok := strings.CutPrefix(line, provider.TaskTrailer)
		if !ok {
			continue
		}
		ref, err := provider.ParseRef(strings.TrimSpace(value))
		if err == nil {
			switch ref.Instance {
			case "jira", "asana", "github-issues", "repository", "filesystem":
				add("task_management", ref.Instance, "recent Git commits contain qualified Task-Ref trailers")
			}
		}
	}
	for _, pattern := range []string{".github/ISSUE_TEMPLATE/*.md", ".github/ISSUE_TEMPLATE/*.yml", ".github/ISSUE_TEMPLATE/*.yaml"} {
		names, _ := fs.Glob(root.FS(), pattern)
		for _, name := range names {
			if path.Base(name) != "config.yml" && path.Base(name) != "config.yaml" {
				add("task_management", "github-issues", ".github/ISSUE_TEMPLATE")
				break
			}
		}
	}
	for _, filename := range []string{"docs/index.md", "docs/knowledge/index.md"} {
		b, _ := readFile(root, filename)
		normalized := strings.ReplaceAll(string(b), "\r\n", "\n")
		if header, ok := strings.CutPrefix(normalized, "---\n"); ok {
			if y, _, found := strings.Cut(header, "\n---"); found {
				var index struct {
					Version string `yaml:"okf_version"`
				}
				if yaml.Unmarshal([]byte(y), &index) == nil && index.Version != "" {
					if _, err := repo.Git(ctx, "ls-files", "--error-unmatch", "--", filename); err == nil {
						add("knowledge_base", "repository", filename+" tracked OKF bundle")
					} else {
						add("knowledge_base", "filesystem", filename+" local OKF bundle")
					}
				}
			}
		}
	}
	if b, _ := readFile(root, "docs/confluence.yml"); len(b) > 0 {
		sampleMapping, _ := templates.Repo.ReadFile("repo/docs/confluence.yml")
		var mapping struct {
			Space string      `yaml:"space"`
			Pages []yaml.Node `yaml:"pages"`
		}
		if !bytes.Equal(b, sampleMapping) && yaml.Unmarshal(b, &mapping) == nil && mapping.Space != "" && len(mapping.Pages) > 0 {
			add("knowledge_base", "confluence", "docs/confluence.yml publication mapping")
		}
	}
	for _, f := range []string{".github/dependabot.yml", ".github/dependabot.yaml"} {
		if b, _ := readFile(root, f); len(b) > 0 {
			var cfg struct {
				Updates []yaml.Node `yaml:"updates"`
			}
			if yaml.Unmarshal(b, &cfg) == nil && len(cfg.Updates) > 0 {
				add("secops", "github", f)
			}
		}
	}
	for _, pattern := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml"} {
		names, _ := fs.Glob(root.FS(), pattern)
		for _, name := range names {
			// Devflow installs its own caller even when a team disables these roles.
			if name == ".github/workflows/devflow.yml" {
				continue
			}
			b, _ := readFile(root, name)
			var wf struct {
				Jobs map[string]struct {
					Environment yaml.Node `yaml:"environment"`
					Uses        string    `yaml:"uses"`
					Steps       []struct {
						Uses string `yaml:"uses"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if yaml.Unmarshal(b, &wf) != nil || len(wf.Jobs) == 0 {
				continue
			}
			add("devops.checks", "github", name+" Actions jobs")
			for _, job := range wf.Jobs {
				if job.Environment.Kind != 0 {
					add("devops.deployments", "github", name+" deployment environment")
				}
				uses := []string{job.Uses}
				for _, s := range job.Steps {
					uses = append(uses, s.Uses)
				}
				for _, action := range uses {
					a, _, _ := strings.Cut(strings.ToLower(action), "@")
					switch a {
					case "googleapis/release-please-action", "goreleaser/goreleaser-action", "softprops/action-gh-release", "nscaledev/devflow/.github/workflows/semver-release.yml":
						add("devops.releases", "github", name+" release action")
					case "github/codeql-action/analyze":
						add("secops", "github", name+" CodeQL action")
					case "slackapi/slack-github-action":
						add("notifications", "slack", name+" Slack action")
					}
				}
			}
		}
	}
	return signals, nil
}
