package webui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/setup"
)

type connectionCheck struct {
	Role           string `json:"role"`
	Provider       string `json:"provider"`
	Ready          bool   `json:"ready"`
	VerifyInClient bool   `json:"verify_in_client"`
	Detail         string `json:"detail"`
}

func (s *server) readiness(w http.ResponseWriter, r *http.Request) {
	var input *struct{}
	if !read(w, r, &input) {
		return
	}
	if input == nil {
		problem(w, 400, "expected an empty object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	// An enclosing repository must never replace the checkout the user opened.
	current, err := gitx.Open(ctx, s.repo.Dir)
	if err != nil || !sameCheckout(s.repo.Dir, current.Dir) {
		problem(w, 409, "reopen the console in the current checkout")
		return
	}
	snapshot, err := consolecfg.View(ctx, s.repo.Dir)
	if err != nil || snapshot.Repository() == nil || snapshot.Providers == nil {
		problem(w, 422, "resolve the current checkout configuration before checking connections")
		return
	}

	if err := readinessFixtures(s.repo.Dir, snapshot.Repository()); err != nil {
		problem(w, 422, "connection checks require regular DevOps fixture files in this checkout")
		return
	}
	plan, err := setup.PlanSetup(ctx, s.repo.Dir, "", "")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		problem(w, 504, "connection checks timed out; check provider connectivity and try again")
		return
	}
	if err != nil || !sameCheckout(s.repo.Dir, plan.Root) {
		problem(w, 422, "connection checks could not finish; inspect the provider setup plan in this checkout")
		return
	}
	selected := map[string]string{}
	for _, role := range snapshot.Providers.Roles {
		selected[role.Role] = role.Selected
	}
	checks := make([]connectionCheck, 0, len(plan.Requirements))
	for _, req := range plan.Requirements {
		id := req.Provider
		switch {
		case req.Role == "companion":
			// Companion names, endpoints, executable paths and arguments come
			// from personal manifests. Show only the outcome of the probe.
			id = "managed MCP"
		case req.Role == "configuration" && req.Provider == "devflow" && !snapshot.Providers.ConfigFound:
		case selected[req.Role] != "" && selected[req.Role] == req.Provider:
		default:
			problem(w, 409, "provider selection changed; refresh the checkout and check again")
			return
		}
		checks = append(checks, connectionCheck{Role: req.Role, Provider: id, Ready: req.Ready, VerifyInClient: req.VerifyInClient, Detail: connectionDetail(req)})
	}
	reply(w, 200, struct {
		Ready        bool              `json:"ready"`
		Requirements []connectionCheck `json:"requirements"`
	}{plan.Ready, checks})
}

// The CLI fixture reader expects a regular JSON file. A special file such as
// a named pipe could otherwise block the HTTP request past its deadline.
func readinessFixtures(dir string, cfg *config.Repo) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, role := range []config.Selection{cfg.Providers.DevOps.Deployments, cfg.Providers.DevOps.Releases} {
		if f, ok := role.Settings.(*config.DevOpsFixtureSettings); ok {
			st, err := root.Stat(f.Path)
			if err == nil && !st.Mode().IsRegular() {
				return errors.New("expected a regular fixture")
			}
		}
	}
	return nil
}

func sameCheckout(a, b string) bool {
	first, err := os.Stat(a)
	if err != nil {
		return false
	}
	second, err := os.Stat(b)
	return err == nil && os.SameFile(first, second)
}

// The planner may include private paths, command lines, remote URLs, or
// external error text in its detail. Project the observed check onto public
// messages instead of trying to recognize every kind of secret in output.
func connectionDetail(req setup.ProviderRequirement) string {
	text := req.Detail
	switch {
	case req.VerifyInClient:
		return "Verify access through the provider’s connected MCP client."
	case req.Role == "configuration":
		return "Configure the providers in .devflow/config.yml."
	case strings.Contains(text, "gh --version") || strings.Contains(text, "install gh") || strings.Contains(text, "managed gh") || strings.Contains(text, "--install-gh"):
		if req.Ready {
			return "GitHub CLI is available."
		}
		return "Check the GitHub CLI installation on this workstation."
	case strings.Contains(text, "git --version") || strings.Contains(text, "install git on PATH"):
		if req.Ready {
			return "Git is available."
		}
		return "Check the Git installation on this workstation."
	case strings.HasPrefix(text, "check connectivity and run gh auth login"):
		return "Check connectivity and sign in to the selected GitHub host."
	case req.Role == "companion" || strings.Contains(text, ": MCP ") || strings.Contains(text, ": review and approve the pinned MCP installation"):
		if req.Ready {
			return "The configured MCP companion responded."
		}
		return "Inspect the configured MCP companion in the provider setup plan."
	case req.Provider == "filesystem":
		if req.Ready {
			return "The configured directory is readable."
		}
		return "Check that the configured directory exists and is readable."
	case req.Provider == "repository":
		if req.Ready {
			return "The configured Git tree is readable."
		}
		return "Check that the selected checkout, branch and directory are available locally."
	case strings.HasPrefix(text, "incident fixture "):
		if req.Ready {
			return "The configured incident fixture is available."
		}
		return "Check that the configured incident fixture is a regular file."
	case req.Provider == "github" || req.Provider == "github-issues" || req.Provider == "auto":
		if req.Ready {
			return "The selected GitHub resource is accessible."
		}
		return "Check the selected GitHub resource and the active identity’s read access."
	case req.Provider == "asana":
		if req.Ready {
			return "The configured Asana readiness check passed."
		}
		return "Check the project settings and the active identity’s read access in Asana."
	case req.Provider == "fixture" || req.Provider == "github-wiki":
		if req.Ready {
			return "The selected data source is readable."
		}
		return "Check that the selected data source exists and is readable."
	case req.Ready:
		return "The provider requirement passed."
	default:
		return "Inspect this provider’s requirements in the setup plan."
	}
}
