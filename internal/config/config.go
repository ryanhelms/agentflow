// Package config loads the repo's .devflow/config.yml and the developer's ~/.devflow/config.yml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Local  Level `yaml:"local"`
	Remote Level `yaml:"remote"`
}

// Repo is .devflow/config.yml.
type Repo struct {
	Devflow int `yaml:"devflow"`
	Update  struct {
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
		MaxAutonomy       Autonomy `yaml:"max_autonomy"`
		ProtectedBranches []string `yaml:"protected_branches"`
		TestCmd           string   `yaml:"test_cmd"`
		LintCmd           string   `yaml:"lint_cmd"`
	} `yaml:"policy"`
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
	Autonomy Autonomy `yaml:"autonomy"`
	Update   struct {
		Auto *bool `yaml:"auto"`
	} `yaml:"update"`
	Atlassian struct {
		Email string `yaml:"email"`
	} `yaml:"atlassian"`
}

// Defaults applied when fields are missing.
var (
	DefaultTypes      = []string{"feat", "fix", "perf", "refactor", "docs", "test", "build", "ci", "chore", "revert"}
	DefaultSite       = "https://nscale.atlassian.net"
	DefaultFlowMap    = "nscaledev/devflow@v1:flow-map.yml"
	DefaultProtected  = []string{"main", "master", "develop", "release/*"}
	DefaultCheckEvery = 6 * time.Hour
)

// LoadRepo reads <root>/.devflow/config.yml. A missing file yields defaults and ok=false.
func LoadRepo(root string) (*Repo, bool, error) {
	c := &Repo{}
	b, err := os.ReadFile(filepath.Join(root, ".devflow", "config.yml"))
	found := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if found {
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, true, fmt.Errorf(".devflow/config.yml: %w", err)
		}
	}
	c.applyDefaults()
	return c, found, nil
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
		c.Policy.MaxAutonomy.Local = Auto
	}
	if c.Policy.MaxAutonomy.Remote == "" {
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
	if h := os.Getenv("DEVFLOW_HOME"); h != "" {
		return h, os.MkdirAll(h, 0o700)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".devflow")
	return dir, os.MkdirAll(dir, 0o700)
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
	if u.Autonomy.Local == "" {
		u.Autonomy.Local = Confirm
	}
	if u.Autonomy.Remote == "" {
		u.Autonomy.Remote = Confirm
	}
	return u, nil
}

// SaveUser writes ~/.devflow/config.yml.
func SaveUser(u *User) error {
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
