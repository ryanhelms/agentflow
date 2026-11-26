// Package managedmcp resolves optional provider MCP companions from the user's
// trusted declarations. Project files only select providers.
package managedmcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/provision"
	"github.com/ryanhelms/agentflow/internal/provision/local"
	"github.com/ryanhelms/agentflow/internal/provision/user"
)

const manifestName = "mcp-companions.yml"

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var envPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type declaration struct {
	ID        string
	Providers []string
	Kind      provision.Kind
	Args      []string
	Endpoint  string
	AuthEnv   string `yaml:"auth_env"`
	Artifacts []user.Pin
}

// Status contains only declared public metadata and measured readiness.
// Command is a verified, digest-specific absolute path.
type Status struct {
	ID        string             `json:"id"`
	Providers []string           `json:"providers"`
	Kind      provision.Kind     `json:"kind"`
	Command   string             `json:"command,omitempty"`
	Args      []string           `json:"args,omitempty"`
	Endpoint  string             `json:"endpoint,omitempty"`
	AuthEnv   string             `json:"auth_env,omitempty"`
	Install   *user.Installation `json:"install,omitempty"`
	Ready     bool               `json:"ready"`
	Detail    string             `json:"detail"`
}

func readManifest(repo string) (string, []declaration, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", nil, err
	}
	// Workspaces with no MCP declarations need no user state or cache.
	if _, err = os.Lstat(filepath.Join(home, manifestName)); errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	home, err = user.ManifestHome(repo)
	if err != nil {
		return "", nil, err
	}
	f, err := local.OpenManifest(home, manifestName)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	var doc struct{ MCP []declaration }
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if d.Decode(&doc) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		// YAML decoder errors can contain supplied scalar values.
		return "", nil, errors.New("invalid mcp-companions.yml: supply one document with supported declaration fields")
	}
	return home, doc.MCP, nil
}

// Inspect checks only declarations matching active provider IDs. Selecting an
// ID narrows both the plan and its verification to that companion.
func Inspect(ctx context.Context, repo string, platform provision.Platform, providers []string, id string) ([]Status, error) {
	home, decls, err := readManifest(repo)
	if err != nil {
		return nil, err
	}
	var statuses []Status
	seen := map[string]bool{}
	for _, d := range decls {
		if !namePattern.MatchString(d.ID) || seen[d.ID] || len(d.Providers) == 0 {
			return nil, errors.New("each MCP declaration requires a unique lowercase id and selected providers")
		}
		seen[d.ID] = true
		selected := false
		for _, p := range d.Providers {
			if !namePattern.MatchString(p) || p == "none" {
				return nil, errors.New("invalid MCP provider id")
			}
			selected = selected || slices.Contains(providers, p)
		}
		if !selected || (id != "" && id != d.ID) {
			continue
		}
		s, err := d.inspect(ctx, home, platform)
		if err != nil {
			return nil, fmt.Errorf("MCP companion %s: %w", d.ID, err)
		}
		statuses = append(statuses, s)
	}
	if id != "" && len(statuses) == 0 {
		return nil, errors.New("no user-declared MCP companion with this id matches an active provider")
	}
	return statuses, ctx.Err()
}

func (d declaration) inspect(ctx context.Context, home string, platform provision.Platform) (Status, error) {
	s := Status{ID: d.ID, Providers: d.Providers, Kind: d.Kind, Args: d.Args, Endpoint: d.Endpoint, AuthEnv: d.AuthEnv}
	if d.AuthEnv != "" && !envPattern.MatchString(d.AuthEnv) {
		return Status{}, errors.New("auth_env must name an environment variable")
	}
	c := provision.Companion{Target: provision.Target{ID: d.ID, Kind: d.Kind, Endpoint: d.Endpoint, Args: d.Args, Health: provision.HealthProbe{Name: "initialize"}}}
	switch d.Kind {
	case provision.StdioMCP:
		c.Target.Command = d.ID
		c.PinArtifact = true
		for _, arg := range d.Args {
			if !slices.Contains([]string{"mcp", "serve", "stdio", "--stdio", "--transport", "--transport=stdio"}, arg) {
				return Status{}, errors.New("args support only MCP launch mode selectors")
			}
		}
		for _, pin := range d.Artifacts {
			a, err := pin.Artifact()
			if err != nil {
				return Status{}, err
			}
			c.Artifacts = append(c.Artifacts, a)
		}
	case provision.RemoteMCP:
		if len(d.Artifacts) != 0 || strings.ContainsAny(d.Endpoint, "$#{}") {
			return Status{}, errors.New("hosted MCP requires a literal HTTPS endpoint and no artifacts")
		}
	default:
		return Status{}, errors.New("kind must be stdio-mcp or remote-mcp")
	}
	if err := c.Validate(); err != nil {
		return Status{}, err
	}
	if d.Kind == provision.StdioMCP {
		env := provision.Environment{ID: "mcp-companions", Platform: platform}
		cache, err := local.New(home, env, user.OpenArtifact)
		if errors.Is(err, local.ErrUnsupported) {
			s.Detail = err.Error()
			return s, nil
		}
		if err != nil {
			return Status{}, err
		}
		manifest := provision.Manifest{ProviderID: d.Providers[0], Companions: []provision.Companion{c}}
		plan, err := provision.BuildPlan(ctx, env, []provision.Manifest{manifest}, cache)
		var conflict *provision.ConflictError
		if errors.As(err, &conflict) {
			s.Detail = conflict.Reason
			return s, nil
		}
		if err != nil {
			return Status{}, err
		}
		step := plan.Steps()[0]
		if step.Action == provision.Install {
			s.Install = user.InstallationFor(home, plan, cache)
			s.Detail = "review and approve the pinned MCP installation"
			return s, nil
		}
		receipt, err := cache.Resolve(ctx, env, c.Target)
		if err != nil {
			return Status{}, err
		}
		if receipt.SHA256 != step.Artifact.SHA256 || receipt.Version != step.Artifact.Version {
			return Status{}, provision.ErrStalePlan
		}
		s.Command = receipt.Path
	}
	s.Ready, s.Detail = s.probe(ctx, home)
	return s, ctx.Err()
}
