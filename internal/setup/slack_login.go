package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/nscaledev/devflow/internal/provider/slackoauth"
)

// LoginPublicSlack enrolls the Devflow-owned desktop registration through browser consent.
func LoginPublicSlack(ctx context.Context, provider, client, server, clientID, teamID string, output io.Writer) error {
	if provider != "slack" || client != "devflow" || (server != "" && server != "slack") {
		return errors.New("--public-client-id and --team-id require --provider slack --client devflow and the slack server")
	}
	if clientID == "" || teamID == "" {
		return errors.New("supply both --public-client-id and --team-id for Slack desktop consent")
	}
	if err := slackoauth.LoginPublic(ctx, clientID, teamID); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "Devflow connected Slack workspace %s through the approved app.\n", teamID)
	return err
}

// RegisterProviderLogin handles the explicit workspace registration option on setup login.
func RegisterProviderLogin(ctx context.Context, dir, provider, client, server string, interactive bool, input io.Reader, output, diagnostics io.Writer) error {
	if provider != "slack" || client != claudeDesktop {
		return errors.New("--register supports only --provider slack --client claude-desktop")
	}
	if server != "" && !serverName.MatchString(server) {
		return errors.New("supply a configured MCP server name")
	}
	list := exec.CommandContext(ctx, "claude", "mcp", "list")
	list.Dir, list.Stderr = dir, diagnostics
	inventory, err := list.Output()
	if err != nil {
		return fmt.Errorf("list Claude workspace connectors: %w", err)
	}
	tool := providerTools["slack"].MCP
	name, status, err := slackConnector(string(inventory), server, tool)
	if err != nil {
		return err
	}
	switch {
	case name == "":
		if server != "" && server != tool.Name {
			return fmt.Errorf("configured server %q is absent; guided registration creates only slack", server)
		}
		name = tool.Name
		// Public OAuth parameters from Slack's Claude Code connector guide.
		add := exec.CommandContext(ctx, "claude", "mcp", "add", "--transport", "http", "--scope", "local",
			"--client-id", "1601185624273.8899143856786", "--callback-port", "3118", name, tool.Endpoint)
		add.Dir, add.Stdout, add.Stderr = dir, output, diagnostics
		if err := add.Run(); err != nil {
			return fmt.Errorf("register Slack in Claude workspace: %w", err)
		}
	case status == "connected":
		if output == nil {
			return nil
		}
		_, err := fmt.Fprintf(output, "Claude reports %q connected.\n", name)
		return err
	case strings.Contains(status, "access denied"), strings.Contains(status, "access_denied"), strings.Contains(status, "forbidden"), strings.Contains(status, "403"):
		return fmt.Errorf("access denied for Slack connector %q: check workspace approval and permissions", name)
	case status != "needs authentication":
		return fmt.Errorf("inspect the existing %q connection in Claude with /mcp", name)
	}
	if !interactive || !serverName.MatchString(name) {
		return fmt.Errorf("sign-in deferred for Slack connector %q: use /mcp in this Claude workspace", name)
	}
	return LoginProvider(ctx, dir, provider, client, name, input, output, diagnostics)
}

// Accept Claude's list rows only. An unfamiliar inventory cannot establish absence.
func slackConnector(inventory, server string, tool MCPTool) (name, status string, err error) {
	seen := false
	for line := range strings.SplitSeq(inventory, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "Checking MCP server health…" || line == "Checking MCP server health..." {
			continue
		}
		if strings.HasPrefix(line, "No MCP servers configured") {
			seen = true
			continue
		}
		key, entry, ok := strings.Cut(line, ": ")
		address, health, hasStatus := strings.Cut(entry, " - ")
		if !ok || !hasStatus || key == "" {
			return "", "", errors.New("unrecognized Claude MCP inventory; inspect the workspace with /mcp")
		}
		seen = true
		endpoint, _, _ := strings.Cut(address, " ")
		if server != "" && key != server || server == "" && key != tool.Name && endpoint != tool.Endpoint {
			continue
		}
		if name != "" {
			return "", "", errors.New("multiple Slack connectors found; select one with --server")
		}
		name = key
		status = strings.ToLower(strings.TrimLeft(health, "✔✓✗✘⚠️! "))
	}
	if !seen {
		return "", "", errors.New("empty Claude MCP inventory; inspect the workspace with /mcp")
	}
	return name, status, nil
}
