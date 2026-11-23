package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"

	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/provider/asanaoauth"
	"github.com/nscaledev/devflow/internal/provider/rovooauth"
)

type MCPTool struct {
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	Guide        string   `json:"guide"`
	RegisterArgs []string `json:"register_args,omitempty"`
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
	"jira":       {MCP: atlassianMCP, CLI: &atlassianCLI},
	"confluence": {MCP: atlassianMCP, CLI: &atlassianCLI},
	"github":     {MCP: githubMCP, CLI: &githubCLI},
	"github-issues": {MCP: githubMCP, CLI: &CLITool{
		Command:   "gh",
		LoginArgs: []string{"auth", "login", "--web", "--hostname", "github.com"},
		Guide:     githubCLI.Guide,
	}},
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

func (p *ProviderSetupPlan) addProviderTooling(role, provider, client string, ghSelection *managedgh.Selection) {
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
	if client == claudeDesktop && (provider == "slack" || provider == "asana") {
		tools.MCP.RegisterArgs = []string{"setup", "login", "--provider", provider, "--client", claudeDesktop, "--register"}
	}
	if tools.CLI != nil {
		cli := *tools.CLI
		if cli.Command == "gh" && ghSelection != nil {
			cli.Installed = ghSelection.Command != ""
		} else {
			_, err := exec.LookPath(cli.Command)
			cli.Installed = err == nil
		}
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

// LoginProvider launches the selected provider OAuth flow. The enrollment
// owner manages its own login and credential storage.
func LoginProvider(ctx context.Context, dir, provider, client, server string, input io.Reader, output, diagnostics io.Writer) error {
	if client == "devflow" && provider == "asana" {
		if server != "" && server != "asana" {
			return errors.New("use the asana server for Devflow Asana enrollment")
		}
		if err := asanaoauth.Login(ctx, input, diagnostics); err != nil {
			return err
		}
		if output != nil {
			_, err := fmt.Fprintln(output, "Devflow connected Asana through its MCP OAuth app.")
			return err
		}
		return nil
	}
	if client == "devflow" {
		if server != "" && server != "atlassian" {
			return errors.New("use the atlassian server for Devflow Rovo enrollment")
		}
		if err := rovooauth.Login(ctx, provider); err != nil {
			return err
		}
		if output != nil {
			_, err := fmt.Fprintf(output, "Devflow connected %s through Atlassian OAuth.\n", provider)
			return err
		}
		return nil
	}
	args, err := providerLoginCommand(provider, client, server)
	if err != nil {
		return err
	}
	if args[0] == "gh" {
		// Outside a checkout, login still works with the user's ordinary PATH.
		// A managed pin must be resolved against the entire checkout.
		if repo, err := gitx.Open(ctx, dir); err == nil {
			args[0], err = managedgh.Command(ctx, repo.Dir)
			if err != nil {
				return err
			}
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, input, output, diagnostics
	if provider == "asana" && args[0] == "claude" {
		cmd.Env = asanaLoginEnvironment(cmd.Environ())
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("provider sign-in via %s: %w", args[0], err)
	}
	return nil
}

// DisconnectProvider discards the native grant enrolled by Devflow.
func DisconnectProvider(ctx context.Context, provider string) error {
	if provider == "asana" {
		return asanaoauth.Disconnect(ctx)
	}
	return rovooauth.Disconnect(ctx, provider)
}
