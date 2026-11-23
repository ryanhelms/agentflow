// Package managedcli resolves and prepares Devflow's supported provider CLIs.
package managedcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/provision"
	"github.com/nscaledev/devflow/internal/provision/local"
	"github.com/nscaledev/devflow/internal/provision/user"
)

type Tool struct {
	Command   string   `json:"command"`
	LoginArgs []string `json:"login_args"`
	Guide     string   `json:"guide"`
	Installed bool     `json:"installed"`
}

// ForProvider returns only CLI adapters implemented by Devflow.
func ForProvider(provider string) (Tool, bool) {
	switch provider {
	case "github", "github-issues":
		tool := Tool{Command: "gh", LoginArgs: []string{"auth", "login", "--web"}, Guide: "https://cli.github.com/"}
		if provider == "github-issues" {
			tool.LoginArgs = append(tool.LoginArgs, "--hostname", "github.com")
		}
		return tool, true
	case "jira", "confluence":
		return Tool{Command: "twg", LoginArgs: []string{"login"}, Guide: "https://developer.atlassian.com/cloud/twg-cli/getting-started/installation/"}, true
	default:
		return Tool{}, false
	}
}

type commandKey string

// WithCommand binds this operation to a verified executable without changing PATH.
func WithCommand(ctx context.Context, tool Tool, path string) context.Context {
	return context.WithValue(ctx, commandKey(tool.Command), path)
}

func Selected(ctx context.Context, name string) string {
	path, _ := ctx.Value(commandKey(name)).(string)
	return path
}

// Resolve uses an existing CLI. Ensure also permits automatic installation of
// a release pinned by Devflow when none is working.
func Resolve(ctx context.Context, repo string, tool Tool) (string, error) {
	return resolve(ctx, repo, tool, false)
}

func Ensure(ctx context.Context, repo string, tool Tool) (string, error) {
	return resolve(ctx, repo, tool, true)
}

func resolve(ctx context.Context, repo string, tool Tool, install bool) (string, error) {
	if command := Selected(ctx, tool.Command); command != "" {
		return command, nil
	}
	if tool.Command == "gh" {
		selected, err := managedgh.Inspect(ctx, repo)
		if err != nil {
			return "", err
		}
		if selected.Command != "" {
			return selected.Command, nil
		}
		if selected.Install != nil {
			if !install {
				return "", errors.New(selected.Detail)
			}
			if err := selected.Install.Apply(ctx); err != nil && !errors.Is(err, provision.ErrStalePlan) {
				return "", err
			}
			return managedgh.Command(ctx, repo)
		}
	} else if path, err := exec.LookPath(tool.Command); err == nil && working(ctx, repo, path) {
		return path, nil
	}
	path, err := managed(ctx, repo, tool.Command, install)
	if err != nil {
		return "", err
	}
	if !working(ctx, repo, path) {
		return "", fmt.Errorf("managed %s failed its version check", tool.Command)
	}
	return path, nil
}

func working(ctx context.Context, repo, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = repo
	cmd.Env = []string{"CI=true", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}
	for _, name := range []string{"HOME", "PATH", "SYSTEMROOT", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "TMPDIR", "TEMP"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	return cmd.Run() == nil
}

func managed(ctx context.Context, repo, command string, install bool) (string, error) {
	platform := provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	release, ok := releases[command][platform.OS+"/"+platform.Arch]
	if !ok {
		return "", fmt.Errorf("%s has no automatic CLI install for %s/%s", command, platform.OS, platform.Arch)
	}
	home, err := user.ManifestHome(repo)
	if err != nil {
		return "", err
	}
	if install {
		if err := os.MkdirAll(home, 0o700); err != nil {
			return "", err
		}
	}
	env := provision.Environment{ID: "provider-cli", Platform: platform}
	cache, err := local.New(home, env, release.open)
	if err != nil {
		return "", err
	}
	target := provision.Target{ID: command, Kind: provision.CLI, Command: command, Health: provision.HealthProbe{Name: "integrity"}}
	artifact := provision.Artifact{Platform: platform, Version: release.version, Source: release.source, SHA256: release.binarySHA}
	if install {
		manifest := provision.Manifest{ProviderID: command, Companions: []provision.Companion{{Target: target, Artifacts: []provision.Artifact{artifact}, PinArtifact: true}}}
		plan, err := provision.BuildPlan(ctx, env, []provision.Manifest{manifest}, cache)
		if err != nil {
			return "", err
		}
		if err := provision.Apply(ctx, env, plan, cache, cache); err != nil && !errors.Is(err, provision.ErrStalePlan) {
			return "", err
		}
	}
	receipt, err := cache.Resolve(ctx, env, target)
	if err != nil {
		return "", fmt.Errorf("%s CLI unavailable: %w", command, err)
	}
	if receipt.Version != artifact.Version || receipt.SHA256 != artifact.SHA256 {
		return "", provision.ErrStalePlan
	}
	return receipt.Path, nil
}
