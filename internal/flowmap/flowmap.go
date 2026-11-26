// Package flowmap resolves canonical lifecycle events and commit verbs to each
// Jira project's own status names, using flow-map.yml.
package flowmap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Canonical events.
const (
	BranchCreated    = "branch.created"
	PROpened         = "pr.opened"
	PRMerged         = "pr.merged"
	DeployStaging    = "deploy.staging"
	DeployProduction = "deploy.production"
)

// Project is one project's overrides. A nil pointer value means "inherit";
// an explicit YAML null means "do nothing".
type Project struct {
	Events     map[string]*string
	Verbs      map[string]*string
	Order      []string // optional status order; transitions never move backwards in it
	Slack      string
	Confluence struct {
		Space              string `yaml:"space"`
		ReleaseNotesParent string `yaml:"release_notes_parent"`
		DesignDocTemplate  string `yaml:"design_doc_template"`
	}
}

// Map is a parsed flow-map.yml.
type Map struct {
	Defaults Project
	Projects map[string]Project
}

// Parse parses flow-map.yml content.
func Parse(b []byte) (*Map, error) {
	var raw struct {
		Defaults map[string]yaml.Node            `yaml:"defaults"`
		Projects map[string]map[string]yaml.Node `yaml:"projects"`
	}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("flow-map.yml: %w", err)
	}
	m := &Map{Projects: map[string]Project{}}
	var err error
	if m.Defaults, err = parseProject(raw.Defaults); err != nil {
		return nil, err
	}
	for key, nodes := range raw.Projects {
		if m.Projects[key], err = parseProject(nodes); err != nil {
			return nil, fmt.Errorf("project %s: %w", key, err)
		}
	}
	return m, nil
}

func parseProject(nodes map[string]yaml.Node) (Project, error) {
	p := Project{Events: map[string]*string{}, Verbs: map[string]*string{}}
	for k, n := range nodes {
		switch k {
		case "verbs":
			var vs map[string]yaml.Node
			if err := n.Decode(&vs); err != nil {
				return p, err
			}
			for v, vn := range vs {
				p.Verbs[v] = nullable(vn)
			}
		case "order":
			if err := n.Decode(&p.Order); err != nil {
				return p, err
			}
		case "slack":
			_ = n.Decode(&p.Slack)
		case "confluence":
			_ = n.Decode(&p.Confluence)
		default:
			p.Events[k] = nullable(n)
		}
	}
	return p, nil
}

func nullable(n yaml.Node) *string {
	if n.Tag == "!!null" {
		empty := ""
		return &empty
	}
	s := n.Value
	return &s
}

// Resolve returns the target status for an event ("pr.merged") or verb ("#qa")
// in a project. ok is false when nothing should happen.
func (m *Map) Resolve(project, event string) (status string, ok bool) {
	lookup := func(p Project) (*string, bool) {
		if v, isVerb := strings.CutPrefix(event, "#"); isVerb {
			s, found := p.Verbs[v]
			return s, found
		}
		s, found := p.Events[event]
		return s, found
	}
	p, exists := m.Projects[project]
	if !exists {
		return "", false // projects not listed are ignored entirely
	}
	if s, found := lookup(p); found {
		return *s, *s != ""
	}
	if s, found := lookup(m.Defaults); found {
		return *s, *s != ""
	}
	return "", false
}

// Backwards reports whether moving from -> to goes backwards in the project's order list.
func (m *Map) Backwards(project, from, to string) bool {
	order := m.Projects[project].Order
	fi, ti := -1, -1
	for i, s := range order {
		if strings.EqualFold(s, from) {
			fi = i
		}
		if strings.EqualFold(s, to) {
			ti = i
		}
	}
	return fi >= 0 && ti >= 0 && ti < fi
}

// Listed reports whether a project has its own block (unlisted projects are ignored).
func (m *Map) Listed(project string) bool {
	_, ok := m.Projects[project]
	return ok
}

// Project returns a project's settings merged with defaults for routing fields.
func (m *Map) Project(key string) Project {
	p := m.Projects[key]
	if p.Slack == "" {
		p.Slack = m.Defaults.Slack
	}
	if p.Confluence.Space == "" {
		p.Confluence = m.Defaults.Confluence
	}
	return p
}

// Ref is a parsed "owner/repo@ref:path" reference.
type Ref struct {
	Repo, Ref, Path string
}

// ParseRef parses config flow_map values such as "ryanhelms/agentflow@v0:flow-map.yml".
// A value without "@" or ":" is treated as a local path.
func ParseRef(s string) (Ref, bool) {
	repoRef, p, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{Path: s}, false
	}
	repo, ref, _ := strings.Cut(repoRef, "@")
	if ref == "" {
		ref = "main"
	}
	return Ref{Repo: repo, Ref: ref, Path: p}, true
}

// Fetcher downloads a file from GitHub at a ref.
type Fetcher func(repo, ref, path string) ([]byte, error)

// Load loads the flow map from a local path (relative to root) or from GitHub,
// caching remote copies under cacheDir for ttl.
func Load(spec, root, cacheDir string, ttl time.Duration, fetch Fetcher) (*Map, error) {
	ref, remote := ParseRef(spec)
	if !remote {
		p := ref.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		return Parse(b)
	}
	cache := filepath.Join(cacheDir, "flowmap-"+strings.NewReplacer("/", "_", "@", "_", ":", "_").Replace(spec))
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < ttl {
		if b, err := os.ReadFile(cache); err == nil {
			return Parse(b)
		}
	}
	b, err := fetch(ref.Repo, ref.Ref, ref.Path)
	if err != nil {
		if old, rerr := os.ReadFile(cache); rerr == nil {
			return Parse(old) // stale but usable when offline
		}
		return nil, fmt.Errorf("fetch flow map %s: %w", spec, err)
	}
	_ = os.MkdirAll(cacheDir, 0o700)
	_ = os.WriteFile(cache, b, 0o600)
	return Parse(b)
}
