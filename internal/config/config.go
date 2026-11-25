// Package config loads the repo's .devflow/config.yml and the developer's ~/.devflow/config.yml.
package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// Level is an autonomy level for a class of actions.
type Level string

const (
	Suggest Level = "suggest"
	Confirm Level = "confirm"
	Auto    Level = "auto"
)

func (l Level) rank() int {
	switch l {
	case Suggest:
		return 0
	case Confirm:
		return 1
	case Auto:
		return 2
	}
	return 1
}

// Min returns the more restrictive of two levels. Unknown values count as confirm.
func Min(a, b Level) Level {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a.rank() <= b.rank() {
		return a
	}
	return b
}

// Autonomy holds levels for local and remote actions.
type Autonomy struct {
	Local   Level            `yaml:"local"`
	Remote  Level            `yaml:"remote"`
	Actions map[string]Level `yaml:"actions,omitempty"`

	defaultLocal, defaultRemote bool
}

// Repo is .devflow/config.yml.
type Repo struct {
	Devflow int `yaml:"devflow"`
	// Providers holds the effective role selections populated by LoadRepo.
	Providers Providers `yaml:"-"`
	Update    struct {
		Channel    string `yaml:"channel"`
		Track      *int   `yaml:"track"`
		CheckEvery string `yaml:"check_every"`
		Pin        string `yaml:"pin"`
	} `yaml:"update"`
	Jira struct {
		Site     string   `yaml:"site"`
		Projects []string `yaml:"projects"`
	} `yaml:"jira"`
	Confluence struct {
		Site string `yaml:"site"`
	} `yaml:"confluence"`
	FlowMap string `yaml:"flow_map"`
	Commit  Commit `yaml:"commit"`
	Policy  struct {
		MaxAutonomy       Autonomy       `yaml:"max_autonomy"`
		MaxApprovalWindow *time.Duration `yaml:"max_approval_window,omitempty"`
		ProtectedBranches []string       `yaml:"protected_branches"`
		TestCmd           string         `yaml:"test_cmd"`
		LintCmd           string         `yaml:"lint_cmd"`
	} `yaml:"policy"`

	legacyJiraSite, legacyConfluenceSite bool
}

// Commit holds commit message rules.
type Commit struct {
	Types         []string `yaml:"types"`
	SubjectMax    int      `yaml:"subject_max"`
	RequireKey    *bool    `yaml:"require_key"` // default true; false allows type(scope): or type: without a Jira key
	AITrailer     bool     `yaml:"ai_trailer"`
	BreakingPaths []string `yaml:"breaking_paths"`
}

// KeyRequired reports whether subjects must carry a Jira key (the default).
func (c Commit) KeyRequired() bool {
	return c.RequireKey == nil || *c.RequireKey
}

// User is ~/.devflow/config.yml.
type User struct {
	Autonomy       Autonomy       `yaml:"autonomy"`
	ApprovalWindow *time.Duration `yaml:"approval_window,omitempty"`
	Update         struct {
		Auto *bool `yaml:"auto"`
	} `yaml:"update"`
	Atlassian struct {
		Email string `yaml:"email"`
	} `yaml:"atlassian"`
	// Init controls automatic `devflow init` by the AI-client plugins in repos without
	// .devflow/config.yml: auto (default), ask or off, for repos owned by Orgs.
	Init struct {
		Auto string   `yaml:"auto"`
		Orgs []string `yaml:"orgs"`
	} `yaml:"init"`
}

// Defaults applied when fields are missing.
var (
	DefaultTypes      = []string{"feat", "fix", "perf", "refactor", "docs", "test", "build", "ci", "chore", "revert"}
	DefaultSite       = "https://nscale.atlassian.net"
	DefaultFlowMap    = "nscaledev/devflow@v0:flow-map.yml"
	DefaultInitOrgs   = []string{"nscaledev"}
	DefaultProtected  = []string{"main", "master", "develop", "release/*"}
	DefaultCheckEvery = 6 * time.Hour
)

// LoadRepo reads <root>/.devflow/config.yml. A missing file yields defaults and ok=false.
func LoadRepo(root string) (*Repo, bool, error) {
	c, extra, found, err := loadRepoFile(root)
	if err != nil {
		return nil, found, err
	}
	return resolveRepo(c, extra, found, func(d *definitions, schemas map[string]roleSchema) error {
		return d.loadDir(LayerProject, filepath.Join(root, ".devflow", "providers"), ".devflow/providers", schemas)
	}, false)
}

// InspectRepo resolves a snapshot of the repository inputs against the current
// release and personal layers. A nil content represents an absent config.
// No candidate data is written to the working tree or used to call a provider.
func InspectRepo(content []byte, projectFiles map[string][]byte) (*Repo, bool, error) {
	c, extra, found, err := parseRepoFile(content)
	if err != nil {
		return nil, found, err
	}
	return resolveRepo(c, extra, found, func(d *definitions, schemas map[string]roleSchema) error {
		for _, name := range slices.Sorted(maps.Keys(projectFiles)) {
			src := Source{Layer: LayerProject, File: name}
			if err := d.add(src, projectFiles[name], schemas); err != nil {
				return &DefinitionError{Source: src, Err: err}
			}
			d.files = append(d.files, src)
		}
		return nil
	}, true)
}

func resolveRepo(c *Repo, extra map[string]yaml.Node, found bool, project definitionLoader, trace bool) (*Repo, bool, error) {
	providers, err := resolveProviders(c, extra, project, trace)
	var defErr *DefinitionError
	if errors.As(err, &defErr) {
		return nil, found, err
	}
	if err != nil {
		return nil, found, fmt.Errorf(".devflow/config.yml: %w", err)
	}
	c.Providers = providers
	return c, found, nil
}

// LoadRepoCore reads repository policy, update and commit settings independently
// of optional provider files. Provider operations must use LoadRepo.
func LoadRepoCore(root string) (*Repo, bool, error) {
	c, _, found, err := loadRepoFile(root)
	return c, found, err
}

func loadRepoFile(root string) (*Repo, map[string]yaml.Node, bool, error) {
	b, err := os.ReadFile(filepath.Join(root, ".devflow", "config.yml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, err
	}
	return parseRepoFile(b)
}

func parseRepoFile(b []byte) (*Repo, map[string]yaml.Node, bool, error) {
	var file struct {
		Repo  `yaml:",inline"`
		Extra map[string]yaml.Node `yaml:",inline"`
	}
	found := b != nil
	if found {
		if err := yaml.Unmarshal(b, &file); err != nil {
			return nil, nil, true, fmt.Errorf(".devflow/config.yml: %w", err)
		}
	}
	c := &file.Repo
	if err := validateAutonomy(c.Policy.MaxAutonomy, "policy.max_autonomy", c.Policy.MaxApprovalWindow, "policy.max_approval_window"); err != nil {
		return nil, nil, found, fmt.Errorf(".devflow/config.yml: %w", err)
	}
	c.legacyJiraSite = c.Jira.Site != ""
	c.legacyConfluenceSite = c.Confluence.Site != "" || c.legacyJiraSite
	c.applyDefaults()
	// The repo alone selects the task provider. Its commit convention must
	// remain available even when an unrelated definition cannot be loaded.
	if node, ok := file.Extra["task_management"]; ok && c.Commit.RequireKey == nil {
		var task struct {
			Provider string `yaml:"provider"`
		}
		if err := node.Decode(&task); err == nil && (task.Provider == "github-issues" || task.Provider == "asana") {
			optional := false
			c.Commit.RequireKey = &optional
		}
	}
	return c, file.Extra, found, nil
}

func (c *Repo) applyDefaults() {
	if len(c.Commit.Types) == 0 {
		c.Commit.Types = DefaultTypes
	}
	if c.Commit.SubjectMax == 0 {
		c.Commit.SubjectMax = 72
	}
	if c.Jira.Site == "" {
		c.Jira.Site = DefaultSite
	}
	if c.Confluence.Site == "" {
		c.Confluence.Site = c.Jira.Site
	}
	if c.FlowMap == "" {
		c.FlowMap = DefaultFlowMap
	}
	if c.Update.Channel == "" {
		c.Update.Channel = "stable"
	}
	if len(c.Policy.ProtectedBranches) == 0 {
		c.Policy.ProtectedBranches = DefaultProtected
	}
	if c.Policy.MaxAutonomy.Local == "" {
		c.Policy.MaxAutonomy.defaultLocal = true
		c.Policy.MaxAutonomy.Local = Auto
	}
	if c.Policy.MaxAutonomy.Remote == "" {
		c.Policy.MaxAutonomy.defaultRemote = true
		c.Policy.MaxAutonomy.Remote = Auto
	}
}

// CheckEvery parses update.check_every, defaulting to 6h.
func (c *Repo) CheckEvery() time.Duration {
	if d, err := time.ParseDuration(c.Update.CheckEvery); err == nil && d > 0 {
		return d
	}
	return DefaultCheckEvery
}

// Home returns ~/.devflow, creating it if needed.
func Home() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	return dir, os.MkdirAll(dir, 0o700)
}

// HomeDir returns the configured user directory without creating it.
func HomeDir() (string, error) {
	if h := os.Getenv("DEVFLOW_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devflow"), nil
}

// LoadUser reads ~/.devflow/config.yml, defaulting autonomy to confirm.
func LoadUser() (*User, error) {
	u := &User{}
	dir, err := Home()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err == nil {
		if err := yaml.Unmarshal(b, u); err != nil {
			return nil, fmt.Errorf("~/.devflow/config.yml: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := validateAutonomy(u.Autonomy, "autonomy", u.ApprovalWindow, "approval_window"); err != nil {
		return nil, fmt.Errorf("~/.devflow/config.yml: %w", err)
	}
	if u.Autonomy.Local == "" {
		u.Autonomy.defaultLocal = true
		u.Autonomy.Local = Confirm
	}
	if u.Autonomy.Remote == "" {
		u.Autonomy.defaultRemote = true
		u.Autonomy.Remote = Confirm
	}
	if u.Init.Auto == "" {
		u.Init.Auto = "auto"
	}
	if len(u.Init.Orgs) == 0 {
		u.Init.Orgs = DefaultInitOrgs
	}
	return u, nil
}

// SaveUser writes ~/.devflow/config.yml.
func SaveUser(u *User) error {
	if err := validateAutonomy(u.Autonomy, "autonomy", u.ApprovalWindow, "approval_window"); err != nil {
		return fmt.Errorf("~/.devflow/config.yml: %w", err)
	}
	dir, err := Home()
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(u)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yml"), b, 0o600)
}

// Effective returns the developer's autonomy capped by the repo maximum.
func Effective(u *User, r *Repo) Autonomy {
	return Autonomy{
		Local:  Min(u.Autonomy.Local, r.Policy.MaxAutonomy.Local),
		Remote: Min(u.Autonomy.Remote, r.Policy.MaxAutonomy.Remote),
	}
}
