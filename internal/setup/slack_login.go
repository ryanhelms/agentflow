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
func RegisterProviderLogin(ctx context.Context, dir, provider, client, server, clientID string, interactive bool, input io.Reader, output, diagnostics io.Writer) error {
	if client != claudeDesktop || (provider != "slack" && provider != "asana") {
		return errors.New("--register supports only Slack and Asana in Claude Code Desktop")
	}
	if provider == "asana" {
		return registerAsanaLogin(ctx, dir, server, clientID, interactive, input, output, diagnostics)
	}
	if clientID != "" {
		return errors.New("--client-id is available for Asana registration")
	}
	if server != "" && !serverName.MatchString(server) {
		return errors.New("supply a configured MCP server name")
	}
	tool := providerTools["slack"].MCP
	name, _, status, err := claudeConnector(ctx, dir, server, tool, diagnostics)
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
		return reportClaudeConnection(output, name)
	case connectorAccessDenied(status):
		return fmt.Errorf("access denied for Slack connector %q: check workspace approval and permissions", name)
	case status != "needs authentication":
		return fmt.Errorf("inspect the existing %q connection in Claude with /mcp", name)
	}
	if !interactive || !serverName.MatchString(name) {
		return fmt.Errorf("sign-in deferred for Slack connector %q: use /mcp in this Claude workspace", name)
	}
	return LoginProvider(ctx, dir, provider, client, name, input, output, diagnostics)
}

func reportClaudeConnection(output io.Writer, name string) error {
	if output == nil {
		return nil
	}
	_, err := fmt.Fprintf(output, "Claude reports %q connected.\n", name)
	return err
}

func connectorAccessDenied(status string) bool {
	return strings.Contains(status, "access denied") || strings.Contains(status, "access_denied") ||
		strings.Contains(status, "forbidden") || strings.Contains(status, "403")
}

func claudeConnector(ctx context.Context, dir, server string, tool MCPTool, diagnostics io.Writer) (name, endpoint, status string, err error) {
	list := exec.CommandContext(ctx, "claude", "mcp", "list")
	list.Dir, list.Stderr = dir, diagnostics
	if tool.Name == "asana" {
		list.Env = asanaLoginEnvironment(list.Environ())
	}
	inventory, err := list.Output()
	if err != nil {
		return "", "", "", fmt.Errorf("list Claude workspace connectors: %w", err)
	}
	return parseClaudeConnector(string(inventory), server, tool)
}

// Accept Claude's list rows only. An unfamiliar inventory cannot establish absence.
func parseClaudeConnector(inventory, server string, tool MCPTool) (name, endpoint, status string, err error) {
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
			return "", "", "", errors.New("unrecognized Claude MCP inventory; inspect the workspace with /mcp")
		}
		seen = true
		target, _, _ := strings.Cut(address, " ")
		if server != "" && key != server || server == "" && key != tool.Name && target != tool.Endpoint {
			continue
		}
		if name != "" {
			return "", "", "", fmt.Errorf("multiple %s connectors found; select one with --server", tool.Name)
		}
		name, endpoint = key, target
		status = strings.ToLower(strings.TrimLeft(health, "✔✓✗✘⚠️! "))
	}
	if !seen {
		return "", "", "", errors.New("empty Claude MCP inventory; inspect the workspace with /mcp")
	}
	return name, endpoint, status, nil
}
