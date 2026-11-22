// Guided setup inspects the environment running devflow.
package setup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/provision"
)

type ProviderRequirement struct {
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Ready    bool   `json:"ready"`
	Detail   string `json:"detail"`
}

type ProviderSetupPlan struct {
	Root         string                  `json:"root"`
	Platform     provision.Platform      `json:"platform"`
	Requirements []ProviderRequirement   `json:"requirements"`
	InstallGH    *managedgh.Installation `json:"install_gh,omitempty"`
	Registration *ClientRegistration     `json:"registration,omitempty"`
	Ready        bool                    `json:"ready"`
}

func (p *ProviderSetupPlan) check(role, provider string, ready bool, detail string) {
	p.Requirements = append(p.Requirements, ProviderRequirement{role, provider, ready, detail})
	p.Ready = p.Ready && ready
}

type selectedRole struct {
	name string
	config.Selection
}

// PlanProviderSetup loads a fresh effective configuration on every invocation.
// An empty client asks only about providers, suitable for terminal and CI use.
func PlanProviderSetup(ctx context.Context, dir, client string) (*ProviderSetupPlan, error) {
	if client != "" && !slices.Contains(Clients, client) {
		return nil, fmt.Errorf("unknown client %q: select a project adapter (%s) or omit the client for headless checks", client, strings.Join(Clients, ", "))
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	repo, _ := gitx.Open(ctx, root)
	if repo != nil {
		root = repo.Dir
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, found, err := config.LoadRepo(root)
	if err != nil {
		return nil, err
	}
	p := &ProviderSetupPlan{Root: root, Platform: provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, Ready: true, Requirements: []ProviderRequirement{}}
	if !found {
		p.check("configuration", "devflow", false, "create .devflow/config.yml with the provider sections for this workspace")
		return p, nil
	}
	providers := cfg.Providers
	roles := []selectedRole{
		{"code_hosting", providers.CodeHosting}, {"task_management", providers.TaskManagement},
		{"knowledge_base", providers.KnowledgeBase}, {"devops.checks", providers.DevOps.Checks},
		{"devops.deployments", providers.DevOps.Deployments}, {"devops.releases", providers.DevOps.Releases},
		{"secops", providers.SecOps}, {"notifications", providers.Notifications},
	}
	var detection gitx.Detection
	needsDetection := false
	for _, role := range roles {
		switch role.Settings.(type) {
		case *config.CodeHostingSettings, *config.GitHubSettings, *config.GitHubIssuesSettings:
			needsDetection = true
		}
	}
	if repo != nil && needsDetection {
		var opts gitx.DetectOptions
		if settings, ok := providers.CodeHosting.Settings.(*config.CodeHostingSettings); ok {
			opts = gitx.DetectOptions{Hosts: settings.Hosts, Source: value(settings.SourceRemote), Target: value(settings.TargetRemote)}
		}
		detection, err = repo.DetectForge(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	var ghRoles, gitRoles []selectedRole
	for _, r := range roles {
		switch r.Provider {
		case "github", "github-issues":
			ghRoles = append(ghRoles, r)
		case "auto":
			if detection.Forge == gitx.ForgeGitHub {
				ghRoles = append(ghRoles, r)
			}
		case "repository":
			gitRoles = append(gitRoles, r)
		}
	}
	env := provision.Environment{ID: "local", Platform: p.Platform}
	gitOK, err := p.checkGit(ctx, env, gitRoles)
	if err != nil {
		return nil, err
	}
	github := githubProbe{dir: root, detection: detection, auth: map[string]bool{}}
	if len(ghRoles) != 0 {
		selection, err := managedgh.Inspect(ctx, root)
		if err != nil {
			selection.Detail = "cannot use managed gh: " + err.Error()
		}
		p.InstallGH = selection.Install
		github.command = selection.Command
		for _, role := range ghRoles {
			p.check(role.name, role.Provider, github.command != "", selection.Detail)
		}
	}
	for _, role := range roles {
		switch settings := role.Settings.(type) {
		case *config.DisabledSettings:
			continue
		case *config.FileSystemSettings:
			p.checkDirectory(role, settings.Root)
		case *config.FileSystemTaskSettings:
			p.checkDirectory(role, settings.Root)
		case *config.RepositorySettings:
			if gitOK {
				p.checkRepository(ctx, role, settings, repo)
			}
		case *config.RepositoryTaskSettings:
			if gitOK {
				p.checkRepository(ctx, role, &settings.RepositorySettings, repo)
			}
		case *config.CodeHostingSettings:
			if !detection.OK() || detection.Forge != gitx.ForgeGitHub {
				detail := detection.Message
				if detail == "" {
					detail = "select a GitHub target remote for this workspace or disable code_hosting"
				}
				p.check(role.name, role.Provider, false, detail)
			} else if github.command != "" {
				github.check(ctx, p, role, nil)
			}
		case *config.GitHubSettings:
			if github.command != "" {
				github.check(ctx, p, role, settings.Repository)
			}
		case *config.GitHubIssuesSettings:
			if github.command != "" {
				github.check(ctx, p, role, settings.Repository)
			}
		default:
			p.check(role.name, role.Provider, false, fmt.Sprintf("%s requires a read-only %s readiness adapter. Select an available adapter or set %s.provider to none to disable this role", role.name, role.Provider, role.name))
		}
	}
	if client != "" {
		p.checkLauncher(ctx, client, repo != nil)
		p.Registration, err = inspectRegistration(root, client)
		if err != nil {
			return nil, err
		}
		p.Ready = p.Ready && p.Registration.State == "configured"
	}
	return p, ctx.Err()
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type commandProbe struct{ dir string }

func (p commandProbe) Observe(ctx context.Context, _ provision.Environment, target provision.Target) (provision.Observation, error) {
	var seen provision.Observation
	_, err := exec.LookPath(target.Command)
	if errors.Is(err, exec.ErrNotFound) {
		return seen, nil
	}
	if err != nil {
		return seen, err
	}
	seen.Installation.Present = true
	seen.Healthy = probeCommand(ctx, p.dir, target.Command, "--version")
	return seen, ctx.Err()
}

// Check the system Git once across all repository providers.
func (p *ProviderSetupPlan) checkGit(ctx context.Context, env provision.Environment, roles []selectedRole) (bool, error) {
	if len(roles) == 0 {
		return true, nil
	}
	target := provision.Target{ID: "git", Command: "git", Kind: provision.CLI, Health: provision.HealthProbe{Name: "version"}}
	var manifests []provision.Manifest
	for _, role := range roles {
		manifests = append(manifests, provision.Manifest{ProviderID: role.name, Companions: []provision.Companion{{Target: target}}})
	}
	probe := commandProbe{p.Root}
	plan, err := provision.BuildPlan(ctx, env, manifests, probe)
	var conflict *provision.ConflictError
	ready := false
	var detail string
	switch {
	case errors.As(err, &conflict):
		detail = "install git on PATH in the environment running devflow and rerun setup. No installation artifact is declared"
	case err != nil:
		return false, err
	default:
		// No artifact or installer is supplied. Apply only verifies the observed CLI.
		err = provision.Apply(ctx, env, plan, probe, nil)
		if err != nil && !errors.Is(err, provision.ErrVerification) {
			return false, err
		}
		ready = err == nil
		detail = "git --version succeeded"
		if !ready {
			detail = "repair git on PATH so git --version succeeds"
		}
	}
	for _, role := range roles {
		p.check(role.name, role.Provider, ready, detail)
	}
	return ready, nil
}
