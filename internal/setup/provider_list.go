package setup

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
)

// Readiness says how devflow setup plan checks a provider.
const (
	ReadinessBuiltIn          = "built-in"           // devflow probes it directly
	ReadinessCompanion        = "companion"          // needs a declared MCP companion
	ReadinessClient           = "client"             // verify through client tooling or a declared companion
	ReadinessClientOrHeadless = "client-or-headless" // also supports a direct headless probe with explicit credentials
	ReadinessNone             = "none"               // the role is disabled
)

// builtInReadiness lists providers whose standard readiness checks run directly in devflow.
var builtInReadiness = []string{"auto", "github", "github-issues", "github-wiki", "filesystem", "repository", "fixture"}

type ProviderOption struct {
	ID        string `json:"id"`
	Default   bool   `json:"default,omitempty"`
	Selected  bool   `json:"selected,omitempty"`
	Readiness string `json:"readiness"`
}

type RoleOptions struct {
	Role       string           `json:"role"`
	Selected   string           `json:"selected,omitempty"`
	Configured bool             `json:"configured"`
	Contract   string           `json:"contract,omitempty"`
	Options    []ProviderOption `json:"options"`
	// Settings is filled only when explaining a selected provider.
	Settings []config.Setting `json:"settings,omitempty"`
}

type ProviderList struct {
	Root        string          `json:"root"`
	ConfigFound bool            `json:"config_found"`
	Definitions []config.Source `json:"definitions"`
	Roles       []RoleOptions   `json:"roles"`
}

// ListProviders reports every provider each role accepts and, when the
// workspace has .devflow/config.yml, which one it selects. It reads local
// configuration only and never contacts a provider. Explain adds each selected
// provider's effective settings and the layer that set them.
func ListProviders(ctx context.Context, dir string, explain bool) (*ProviderList, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if repo, _ := gitx.Open(ctx, root); repo != nil {
		root = repo.Dir
	}
	cfg, found, err := config.LoadRepo(root)
	if err != nil {
		return nil, err
	}
	return ListConfiguredProviders(ctx, root, cfg, found, explain)
}

// ListConfiguredProviders describes the caller's active provider snapshot.
func ListConfiguredProviders(ctx context.Context, root string, cfg *config.Repo, found, explain bool) (*ProviderList, error) {
	selected := map[string]config.Selection{}
	if found {
		for _, r := range selectedRoles(cfg.Providers) {
			selected[r.name] = r.Selection
		}
	}
	out := &ProviderList{Root: root, ConfigFound: found, Definitions: cfg.Providers.Definitions, Roles: []RoleOptions{}}
	if out.Definitions == nil {
		out.Definitions = []config.Source{}
	}
	for _, role := range config.ProviderCatalog() {
		sel, ok := selected[role.Role]
		ro := RoleOptions{Role: role.Role, Selected: sel.Provider, Configured: ok && sel.Configured, Contract: sel.Contract}
		if explain && ok {
			settings, err := sel.Explain()
			if err != nil {
				return nil, err
			}
			ro.Settings = settings
		}
		for _, id := range role.Providers {
			ro.Options = append(ro.Options, ProviderOption{
				ID: id, Default: id == role.Default, Selected: ok && id == sel.Provider, Readiness: readiness(id),
			})
		}
		out.Roles = append(out.Roles, ro)
	}
	return out, ctx.Err()
}

func readiness(id string) string {
	switch {
	case id == "jira" || id == "confluence" || id == "slack":
		return ReadinessClient
	case id == "asana":
		return ReadinessClientOrHeadless
	case id == "none":
		return ReadinessNone
	case slices.Contains(builtInReadiness, id):
		return ReadinessBuiltIn
	}
	return ReadinessCompanion
}
