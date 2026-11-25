package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
)

type MCPTool struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Guide    string `json:"guide"`
}

type CLITool struct {
	Command   string   `json:"command"`
	LoginArgs []string `json:"login_args"`
	Guide     string   `json:"guide"`
	Installed bool     `json:"installed"`
}

type ProviderTooling struct {
	Provider string   `json:"provider"`
	Roles    []string `json:"roles"`
	MCP      MCPTool  `json:"mcp"`
	CLI      *CLITool `json:"cli,omitempty"`
}

var atlassianMCP = MCPTool{
	Name:     "atlassian",
	Endpoint: "https://mcp.atlassian.com/v2/mcp",
	Guide:    "https://support.atlassian.com/atlassian-ai-gateway/docs/get-started-with-the-atlassian-remote-mcp-server/",
}

var atlassianCLI = CLITool{
	Command:   "twg",
	LoginArgs: []string{"login"},
	Guide:     "https://developer.atlassian.com/cloud/twg-cli/getting-started/installation/",
}

var githubMCP = MCPTool{
	Name:     "github",
	Endpoint: "https://api.githubcopilot.com/mcp/",
	Guide:    "https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md",
}

var githubCLI = CLITool{
	Command:   "gh",
	LoginArgs: []string{"auth", "login", "--web"},
	Guide:     "https://cli.github.com/",
}

var providerTools = map[string]ProviderTooling{
	"jira":          {MCP: atlassianMCP, CLI: &atlassianCLI},
	"confluence":    {MCP: atlassianMCP, CLI: &atlassianCLI},
	"github":        {MCP: githubMCP, CLI: &githubCLI},
	"github-issues": {MCP: githubMCP, CLI: &githubCLI},
	"asana": {MCP: MCPTool{
		Name:     "asana",
		Endpoint: "https://mcp.asana.com/v2/mcp",
		Guide:    "https://developers.asana.com/docs/using-asanas-mcp-server",
	}},
	"slack": {MCP: MCPTool{
		Name:     "slack",
		Endpoint: "https://mcp.slack.com/mcp",
		Guide:    "https://docs.slack.dev/ai/slack-mcp-server/connect-to-harnesses/",
	}},
}

func (p *ProviderSetupPlan) addProviderTooling(role, provider string) {
	tools, ok := providerTools[provider]
	if !ok {
		return
	}
	for i := range p.Tooling {
		if p.Tooling[i].Provider == provider {
			p.Tooling[i].Roles = append(p.Tooling[i].Roles, role)
			return
		}
	}
	tools.Provider, tools.Roles = provider, []string{role}
	if tools.CLI != nil {
		cli := *tools.CLI
		_, err := exec.LookPath(cli.Command)
		cli.Installed = err == nil
		tools.CLI = &cli
	}
	p.Tooling = append(p.Tooling, tools)
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

func providerLoginCommand(provider, client, server string) ([]string, error) {
	tools, ok := providerTools[provider]
	if !ok {
		return nil, fmt.Errorf("provider %q has no interactive login action", provider)
	}
	if client == "" {
		if server != "" {
			return nil, errors.New("select the MCP client for the server")
		}
		if tools.CLI == nil {
			return nil, fmt.Errorf("%s uses its MCP client's sign-in flow", provider)
		}
		return append([]string{tools.CLI.Command}, tools.CLI.LoginArgs...), nil
	}

	if server == "" {
		server = tools.MCP.Name
	}
	if !serverName.MatchString(server) {
		return nil, errors.New("supply a configured MCP server name")
	}
	switch client = runtimeClient(client); client {
	case "claude", "codex":
		return []string{client, "mcp", "login", server}, nil
	default:
		if !slices.Contains(SetupClients, client) {
			return nil, fmt.Errorf("unknown client %q", client)
		}
		return nil, fmt.Errorf("start the %s connector's OAuth action in %s", tools.MCP.Name, client)
	}
}

// LoginProvider runs the provider CLI or the MCP client's own interactive OAuth
// command. These tools own their credential storage and their browser consent flow.
func LoginProvider(ctx context.Context, dir, provider, client, server string, input io.Reader, output, diagnostics io.Writer) error {
	args, err := providerLoginCommand(provider, client, server)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, input, output, diagnostics
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("provider sign-in via %s: %w", args[0], err)
	}
	return nil
}
