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
	"github.com/nscaledev/devflow/internal/provider/slackoauth"
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
	if client == "devflow" {
		return loginDevflowProvider(ctx, provider, server, input, output, diagnostics)
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

func loginDevflowProvider(ctx context.Context, provider, server string, input io.Reader, output, diagnostics io.Writer) error {
	var message string
	var err error
	switch provider {
	case "asana":
		if server != "" && server != "asana" {
			return errors.New("use the asana server for Devflow Asana enrollment")
		}
		err = asanaoauth.Login(ctx, input, diagnostics)
		message = "Devflow connected Asana through its MCP OAuth app."
	case "slack":
		if server != "" && server != "slack" {
			return errors.New("use the slack server for Devflow Slack enrollment")
		}
		err = slackoauth.Login(ctx, input, diagnostics)
		message = "Devflow connected Slack through the approved app."
	default:
		if server != "" && server != "atlassian" {
			return errors.New("use the atlassian server for Devflow Rovo enrollment")
		}
		err = rovooauth.Login(ctx, provider)
		message = fmt.Sprintf("Devflow connected %s through Atlassian OAuth.", provider)
	}
	if err == nil && output != nil {
		_, err = fmt.Fprintln(output, message)
	}
	return err
}

// DisconnectProvider discards the selected native grant enrolled by Devflow.
func DisconnectProvider(ctx context.Context, provider string) error {
	switch provider {
	case "asana":
		return asanaoauth.Disconnect(ctx)
	case "slack":
		return slackoauth.Disconnect(ctx)
	default:
		return rovooauth.Disconnect(ctx, provider)
	}
}
