// Guided setup inspects the environment running devflow.
package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/managedmcp"
	"github.com/nscaledev/devflow/internal/provider/asana"
	"github.com/nscaledev/devflow/internal/provider/devopsfixture"
	"github.com/nscaledev/devflow/internal/provision"
)

type ProviderRequirement struct {
	Role           string `json:"role"`
	Provider       string `json:"provider"`
	Ready          bool   `json:"ready"`
	Detail         string `json:"detail"`
	VerifyInClient bool   `json:"verify_in_client,omitempty"`
}

type ProviderSetupPlan struct {
	Root         string                  `json:"root"`
	Platform     provision.Platform      `json:"platform"`
	Requirements []ProviderRequirement   `json:"requirements"`
	Tooling      []ProviderTooling       `json:"provider_tools"`
	Companions   []managedmcp.Status     `json:"companions,omitempty"`
	InstallGH    *managedgh.Installation `json:"install_gh,omitempty"`
	Registration *ClientRegistration     `json:"registration,omitempty"`
	Ready        bool                    `json:"ready"`
}

func (p *ProviderSetupPlan) check(role, provider string, ready bool, detail string) {
	p.Requirements = append(p.Requirements, ProviderRequirement{Role: role, Provider: provider, Ready: ready, Detail: detail})
	p.Ready = p.Ready && ready
}

type selectedRole struct {
	name string
	config.Selection
}

// selectedRoles follows the order of config.ProviderRoles.
func selectedRoles(providers config.Providers) []selectedRole {
	return []selectedRole{
		{"code_hosting", providers.CodeHosting}, {"task_management", providers.TaskManagement},
		{"knowledge_base", providers.KnowledgeBase}, {"devops.checks", providers.DevOps.Checks},
		{"devops.deployments", providers.DevOps.Deployments}, {"devops.releases", providers.DevOps.Releases},
		{"secops", providers.SecOps}, {"notifications", providers.Notifications},
	}
}

const claudeDesktop = "claude-desktop"

// SetupClients includes the explicit desktop mode alongside the installable adapters.
var SetupClients = append(slices.Clone(Clients), claudeDesktop)

// PlanProviderSetup loads a fresh effective configuration on every invocation.
// An empty client asks only about providers, suitable for terminal and CI use.
func PlanProviderSetup(ctx context.Context, dir, client string) (*ProviderSetupPlan, error) {
	return PlanSetup(ctx, dir, client, "")
}

// PlanSetup optionally scopes the plan to one declared provider companion.
func PlanSetup(ctx context.Context, dir, client, companion string) (*ProviderSetupPlan, error) {
	if client != "" && !slices.Contains(SetupClients, client) {
		return nil, fmt.Errorf("unknown client %q: select a project adapter (%s) or omit the client for headless checks", client, strings.Join(SetupClients, ", "))
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
	p := &ProviderSetupPlan{Root: root, Platform: provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, Ready: true, Requirements: []ProviderRequirement{}, Tooling: []ProviderTooling{}}
	if !found {
		p.check("configuration", "devflow", false, "create .devflow/config.yml with the provider sections for this workspace")
		return p, nil
	}
	providers := cfg.Providers
	roles := selectedRoles(providers)
	var detection gitx.Detection
	needsDetection := false
	for _, role := range roles {
		switch role.Settings.(type) {
		case *config.CodeHostingSettings, *config.GitHubSettings, *config.GitHubIssuesSettings, *config.GitHubWikiSettings, *config.GitHubSecOpsSettings:
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
	var ids []string
	for _, r := range roles {
		if r.Provider == "auto" && detection.OK() && detection.Forge == gitx.ForgeGitHub {
			ids = append(ids, "github")
		}
		if r.Provider != "none" {
			ids = append(ids, r.Provider)
		}
	}
	p.Companions, err = managedmcp.Inspect(ctx, root, p.Platform, ids, companion)
	if err != nil {
		return nil, err
	}
	for _, s := range p.Companions {
		p.check("companion", s.ID, s.Ready, s.Detail)
	}
	if companion != "" {
		if client != "" {
			s := p.Companions[0]
			p.Registration, _, err = companionOverlay(root, client, s)
			if err != nil {
				return nil, err
			}
			p.Ready = p.Ready && p.Registration.State == "configured"
		}
		return p, ctx.Err()
	}
	var ghRoles, gitRoles []selectedRole
	for _, r := range roles {
		switch r.Provider {
		case "github", "github-issues":
			if settings, ok := r.Settings.(*config.GitHubSecOpsSettings); ok && len(settings.Findings) == 0 {
				continue
			}
			ghRoles = append(ghRoles, r)
		case "auto":
			if detection.Forge == gitx.ForgeGitHub {
				ghRoles = append(ghRoles, r)
			}
		case "repository", "github-wiki":
			gitRoles = append(gitRoles, r)
		}
	}
	env := provision.Environment{ID: "local", Platform: p.Platform}
	gitOK, err := p.checkGit(ctx, env, gitRoles)
	if err != nil {
		return nil, err
	}
	github := githubProbe{dir: root, detection: detection, auth: map[string]bool{}}
	var ghSelection *managedgh.Selection
	if len(ghRoles) != 0 {
		selection, err := managedgh.Inspect(ctx, root)
		if err != nil {
			selection.Detail = "cannot use managed gh: " + err.Error()
		}
		p.InstallGH = selection.Install
		ghSelection = &selection
		github.command = selection.Command
		for _, role := range ghRoles {
			p.check(role.name, role.Provider, github.command != "", selection.Detail)
		}
	}
	for _, role := range roles {
		toolProvider := role.Provider
		if role.Provider == "auto" && detection.OK() && detection.Forge == gitx.ForgeGitHub {
			toolProvider = "github"
		}
		p.addProviderTooling(role.name, toolProvider, client, ghSelection)
		switch settings := role.Settings.(type) {
		case *config.DisabledSettings:
			continue
		case *config.AsanaSettings:
			if settings.Project == "" {
				p.check(role.name, role.Provider, false, "set task_management.settings.project to the default Asana project GID")
				continue
			}
			// Retain the direct probe for existing headless service jobs with explicit authentication.
			if client == "" && os.Getenv("ASANA_ACCESS_TOKEN") != "" {
				adapter, err := asana.FromEnvironment(settings.AdapterConfig())
				if err == nil {
					err = adapter.CheckAccess(ctx)
				}
				if err != nil {
					p.check(role.name, role.Provider, false, err.Error())
				} else {
					p.check(role.name, role.Provider, true, "can list project tasks and read configured destination sections")
				}
				continue
			}
			p.checkProviderTool(role, "check configured Asana project access with the client's Asana MCP")
		case *config.JiraSettings, *config.ConfluenceSettings:
			p.checkProviderTool(role, "check the Atlassian MCP in the client or use signed-in Teamwork Graph CLI")
		case *config.SlackSettings:
			p.checkProviderTool(role, "check the Slack MCP in the client")
		case *config.FileSystemKnowledgeSettings:
			p.checkDirectory(role, settings.Root)
		case *config.FileSystemTaskSettings:
			p.checkDirectory(role, settings.Root)
		case *config.DevOpsFixtureSettings:
			if err := checkDevOpsFixture(root, role.name, settings.Path); err != nil {
				p.check(role.name, role.Provider, false, err.Error())
			} else {
				p.check(role.name, role.Provider, true, fmt.Sprintf("can read fixture %q", settings.Path))
			}
		case *config.RepositoryKnowledgeSettings:
			if gitOK {
				p.checkRepository(ctx, role, &settings.RepositorySettings, repo)
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
		case *config.GitHubSecOpsSettings:
			if len(settings.Findings) > 0 && github.command != "" {
				github.check(ctx, p, role, settings.Repository)
			}
			if settings.Incidents != nil {
				rootDir, err := os.OpenRoot(root)
				if err == nil {
					info, statErr := rootDir.Stat(settings.Incidents.Fixture)
					_ = rootDir.Close()
					p.check(role.name, role.Provider, statErr == nil && info.Mode().IsRegular(), "incident fixture "+settings.Incidents.Fixture)
				} else {
					p.check(role.name, role.Provider, false, "open the checkout to read the incident fixture")
				}
			}
		case *config.GitHubIssuesSettings:
			if github.command != "" {
				github.check(ctx, p, role, settings.Repository)
			}
		case *config.GitHubWikiSettings:
			if gitOK {
				p.checkWiki(ctx, role, settings, detection)
			}
		default:
			if !p.checkProviderCompanions(role) {
				p.check(role.name, role.Provider, false, fmt.Sprintf("%s requires a read-only %s readiness adapter. Select an available adapter or set %s.provider to none to disable this role", role.name, role.Provider, role.name))
			}
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

func (p *ProviderSetupPlan) checkProviderCompanions(role selectedRole) bool {
	declared := false
	for _, s := range p.Companions {
		if slices.Contains(s.Providers, role.Provider) {
			declared = true
			p.check(role.name, role.Provider, s.Ready, s.ID+": "+s.Detail)
		}
	}
	return declared
}

func (p *ProviderSetupPlan) checkProviderTool(role selectedRole, detail string) {
	if !p.checkProviderCompanions(role) {
		p.check(role.name, role.Provider, false, detail)
		p.Requirements[len(p.Requirements)-1].VerifyInClient = true
	}
}

func checkDevOpsFixture(root, role, path string) error {
	if role == "devops.deployments" {
		_, err := devopsfixture.LoadDeployments(root, path)
		return err
	}
	_, err := devopsfixture.LoadReleases(root, path)
	return err
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
