package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/managedgh"
	"github.com/nscaledev/agentflow/internal/provision/user"
	"github.com/nscaledev/agentflow/internal/update"
)

const (
	githubMCPRepo    = "github/github-mcp-server"
	githubMCPRelease = "v2.2.0"
	githubOAuthGuide = "https://github.com/" + githubMCPRepo + "/blob/main/docs/oauth-login.md"
)

type githubMCPAsset struct {
	suffix, archiveSHA, binarySHA string
}

// Archive digests are from the official release. Binary digests are derived
// from those verified archives and checked again when reusing the install.
var githubMCPAssets = map[string]githubMCPAsset{
	"darwin/arm64":  {"Darwin_arm64.tar.gz", "5e46b05b87989d23267f0a69ee60a3acd1de51a72512ee5fcc2786ca60c54a6a", "116084337980e1dfbaccbd45d899e84b87d706b40db6402c2dda07617b5776fb"},
	"darwin/amd64":  {"Darwin_x86_64.tar.gz", "a42e667b99915cf368b92aa419fd2f753549ea7133cb3572a3e7f3e2d4a8675b", "70e93196b19e287cbb6e9e7b616bc87993a46587e507fc2832429d356c566d1a"},
	"linux/arm64":   {"Linux_arm64.tar.gz", "d21e4fca8603b59d99380f1c332aaea36d73033d52b389867adf2124ee150983", "7135da4b94631bfc04274ae41c67ecaeefb2a32cc554b65cb18b752a92687612"},
	"linux/amd64":   {"Linux_x86_64.tar.gz", "157c5664b273e5070126d414f88cbab765630acde588f75dafdf9998c2235c51", "4a4eb247b39c0603cc76002b4001764c33d131c73c2b6ee337775f001d454069"},
	"linux/386":     {"Linux_i386.tar.gz", "6525a290cc6925ee3254221f3fd4dd4d151eaa1243494b54345756581588397d", "42270c984c385033d316181fb78d6685d9f00199e8552495b8ad86e1c386c17c"},
	"windows/arm64": {"Windows_arm64.zip", "9f2941e3eee6b4f87a2e26dc5bff609a7ce5d62f82c3b337476a0ce77ce860cb", "dcd36318cb4c102b5fcfc8792053f79710b3475a4e78fb3aa68129742b8eeb61"},
	"windows/amd64": {"Windows_x86_64.zip", "6ecda644e8ea54b26acbc76e80379f4afb6ac1a4c2521366584f2a7f23e3a850", "38c58845e150f609d4fcd122bb8ef3c6ee5f8d5d869d69c00267e1638cadae9c"},
	"windows/386":   {"Windows_i386.zip", "19e0b1683fd1951a8942e19d3151b24899744dd695e1e27535892fd5a50e6556", "06df7c83cbfe4d563e43a23ee60ce465509fe84c6fc9c9e788472063ef8b74cf"},
}

func registerGitHubLogin(ctx context.Context, dir, server string, output, diagnostics io.Writer) error {
	if server != "" && !serverName.MatchString(server) {
		return errors.New("supply a configured MCP server name")
	}
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return err
	}
	name, _, status, err := claudeConnector(ctx, repo.Dir, server, githubMCP, diagnostics)
	if err != nil {
		return err
	}
	if name != "" {
		if status == "connected" {
			return reportClaudeConnection(output, name)
		}
		return fmt.Errorf("review the existing GitHub connector %q in Claude before registering a local OAuth server", name)
	}
	if server != "" && server != githubMCP.Name {
		return fmt.Errorf("configured server %q is absent; guided registration creates only github", server)
	}
	command, err := installGitHubMCP(ctx, repo.Dir, diagnostics)
	if err != nil {
		return err
	}
	entry, err := json.Marshal(githubOAuthEntry(command))
	if err != nil {
		return err
	}
	add := exec.CommandContext(ctx, "claude", "mcp", "add-json", "--scope", "local", githubMCP.Name, string(entry))
	add.Dir, add.Stdout, add.Stderr = repo.Dir, output, diagnostics
	if err := add.Run(); err != nil {
		return fmt.Errorf("register GitHub OAuth MCP in Claude workspace: %w", err)
	}
	if output != nil {
		_, err = fmt.Fprintln(output, "Start a fresh Claude Code session in this workspace and retry the GitHub read. The local server opens GitHub browser sign-in on its first tool call.")
	}
	return err
}

func githubOAuthEntry(command string) map[string]any {
	// Empty overrides prevent ambient static authentication or a different
	// OAuth application from taking precedence over the official browser flow.
	env := map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "",
		"GITHUB_APP_ID":                "",
		"GITHUB_APP_INSTALLATION_ID":   "",
		"GITHUB_APP_PRIVATE_KEY":       "",
		"GITHUB_APP_PRIVATE_KEY_PATH":  "",
		"GITHUB_OAUTH_CLIENT_ID":       "",
		"GITHUB_OAUTH_CLIENT_SECRET":   "",
		"GITHUB_OAUTH_CALLBACK_PORT":   "",
		"GITHUB_OAUTH_SCOPES":          "",
	}
	return map[string]any{
		"type":    "stdio",
		"command": command,
		"args":    []string{"stdio", "--gh-host", "https://github.com", "--toolsets", "issues", "--read-only"},
		"env":     env,
	}
}

func installGitHubMCP(ctx context.Context, dir string, diagnostics io.Writer) (string, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	asset, ok := githubMCPAssets[platform]
	if !ok {
		return "", fmt.Errorf("no pinned GitHub OAuth MCP binary for %s", platform)
	}
	home, err := user.ManifestHome(dir)
	if err != nil {
		return "", err
	}
	cache := filepath.Join(home, "github-mcp", githubMCPRelease, asset.binarySHA)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", err
	}
	bin := "github-mcp-server"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	dest := filepath.Join(cache, bin)
	if err := update.VerifyDigest(dest, asset.binarySHA); !errors.Is(err, os.ErrNotExist) {
		return dest, err
	}
	gh, err := managedgh.Command(ctx, dir)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(cache, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	archive := "github-mcp-server_" + asset.suffix
	cmd := exec.CommandContext(ctx, gh, "release", "download", githubMCPRelease, "--repo", githubMCPRepo, "--pattern", archive, "--dir", stage)
	cmd.Dir, cmd.Stderr = dir, diagnostics
	for _, setting := range cmd.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		if !strings.EqualFold(name, "GH_TOKEN") && !strings.EqualFold(name, "GITHUB_TOKEN") {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("download pinned GitHub MCP release: %w", err)
	}
	source := filepath.Join(stage, archive)
	if err := update.VerifyDigest(source, asset.archiveSHA); err != nil {
		return "", err
	}
	if err := update.ExtractBinary(source, stage, bin); err != nil {
		return "", err
	}
	executable := filepath.Join(stage, bin)
	if err := update.VerifyDigest(executable, asset.binarySHA); err != nil {
		return "", err
	}
	if err := os.Rename(executable, dest); err != nil {
		if cachedErr := update.VerifyDigest(dest, asset.binarySHA); cachedErr != nil {
			return "", err
		}
	}
	return dest, nil
}
