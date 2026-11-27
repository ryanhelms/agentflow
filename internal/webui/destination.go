package webui

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
)

// Destination is a screen in the current worktree's console.
type Destination struct {
	Screen string `json:"screen,omitempty" jsonschema:"Console screen: overview, providers (default for chat), autonomy, plugins, configuration, or handoff."`
	Role   string `json:"role,omitempty" jsonschema:"Optional role to select on the providers screen: code_hosting, task_management, knowledge_base, devops.checks, devops.deployments, devops.releases, secops, or notifications."`
}

func (d Destination) Normalize(defaultScreen string) (Destination, error) {
	if d.Screen == "" {
		d.Screen = defaultScreen
		if d.Role != "" {
			d.Screen = "providers"
		}
	}
	switch d.Screen {
	case "overview", "providers", "autonomy", "plugins", "configuration", "handoff":
	default:
		return Destination{}, errors.New("choose a screen: overview, providers, autonomy, plugins, configuration, or handoff")
	}
	if d.Role != "" {
		if d.Screen != "providers" {
			return Destination{}, errors.New("a provider role requires the providers screen")
		}
		if !slices.Contains(config.ProviderRoles, d.Role) {
			return Destination{}, fmt.Errorf("choose a provider role: %s", strings.Join(config.ProviderRoles, ", "))
		}
	}
	return d, nil
}

// Fragment returns the destination fields to include in a one-time launch link.
func (d Destination) Fragment() string {
	fields := url.Values{"screen": {d.Screen}}
	if d.Role != "" {
		fields.Set("role", d.Role)
	}
	return fields.Encode()
}

func (d Destination) command() string {
	cmd := "agentflow ui --screen " + d.Screen
	if d.Role != "" {
		cmd += " --role " + d.Role
	}
	return cmd
}
