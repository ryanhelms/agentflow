package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
)

// Claude reads the approved app secret directly from the terminal and stores
// it for this connector. Devflow handles only its public registration metadata.
func registerAsanaLogin(ctx context.Context, dir, server, clientID string, interactive bool, input io.Reader, output, diagnostics io.Writer) error {
	if server != "" && !serverName.MatchString(server) {
		return errors.New("supply a valid Asana MCP server name")
	}
	if clientID != "" && !serverName.MatchString(clientID) {
		return errors.New("supply the public OAuth client ID from your approved Asana MCP app")
	}
	tool := providerTools["asana"].MCP
	name, endpoint, status, err := claudeConnector(ctx, dir, server, tool, diagnostics)
	if err != nil {
		return err
	}
	if name == "" {
		if clientID == "" {
			return errors.New("register an approved Asana MCP app and supply its --client-id")
		}
		if !interactive {
			return errors.New("open an interactive terminal for Claude’s secure Asana app prompt")
		}
		if server == "" {
			server = tool.Name
		}
		add := exec.CommandContext(ctx, "claude", "mcp", "add", "--transport", "http", "--scope", "local",
			"--client-id", clientID, "--client-secret", "--callback-port", "8080", server, tool.Endpoint)
		add.Dir, add.Stdin, add.Stdout, add.Stderr = dir, input, output, diagnostics
		add.Env = asanaLoginEnvironment(add.Environ())
		if err := add.Run(); err != nil {
			return fmt.Errorf("register Asana in Claude workspace: %w", err)
		}
		name, endpoint, status, err = claudeConnector(ctx, dir, server, tool, diagnostics)
		if err != nil {
			return err
		}
	}
	if name == "" {
		return errors.New("the registered Asana connector was not found in Claude")
	}
	if endpoint != tool.Endpoint {
		return fmt.Errorf("connector %q uses a different endpoint; select an official Asana V2 registration with --server", name)
	}
	switch {
	case status == "connected":
		return reportClaudeConnection(output, name)
	case connectorAccessDenied(status):
		return fmt.Errorf("access denied for Asana connector %q: check workspace approval and permissions", name)
	case status != "needs authentication":
		return fmt.Errorf("inspect the existing %q connection in Claude with /mcp", name)
	case !interactive:
		return fmt.Errorf("sign-in deferred for Asana connector %q: use /mcp in this Claude workspace", name)
	default:
		return LoginProvider(ctx, dir, "asana", claudeDesktop, name, input, output, diagnostics)
	}
}

func asanaLoginEnvironment(env []string) []string {
	return slices.DeleteFunc(env, func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "MCP_CLIENT_SECRET", "ASANA_CLIENT_ID", "ASANA_CLIENT_SECRET", "ASANA_ACCESS_TOKEN":
			return true
		default:
			return false
		}
	})
}
