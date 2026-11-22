package setup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
)

func TestListProviders(t *testing.T) {
	dir := t.TempDir()
	setupFile(t, dir, ".devflow/config.yml", headlessConfig, 0o644)
	list, err := ListProviders(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !list.ConfigFound || len(list.Roles) != len(config.ProviderRoles) {
		t.Fatalf("list = %+v", list)
	}
	byRole := map[string]RoleOptions{}
	for i, r := range list.Roles {
		if r.Role != config.ProviderRoles[i] {
			t.Fatalf("role %d = %s, want %s", i, r.Role, config.ProviderRoles[i])
		}
		byRole[r.Role] = r
	}
	tasks := byRole["task_management"]
	if tasks.Selected != "github-issues" || !tasks.Configured {
		t.Fatalf("task_management = %+v", tasks)
	}
	want := map[string]ProviderOption{
		"github-issues": {ID: "github-issues", Selected: true, Readiness: ReadinessBuiltIn},
		"jira":          {ID: "jira", Default: true, Readiness: ReadinessClient},
		"none":          {ID: "none", Readiness: ReadinessNone},
	}
	for _, o := range tasks.Options {
		if w, ok := want[o.ID]; ok && o != w {
			t.Errorf("option %s = %+v, want %+v", o.ID, o, w)
		}
	}
	if got := byRole["notifications"].Selected; got != "none" {
		t.Errorf("notifications selected %q, want none", got)
	}
}

func TestListProvidersWithoutConfig(t *testing.T) {
	list, err := ListProviders(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if list.ConfigFound {
		t.Fatal("config found in an empty directory")
	}
	for _, r := range list.Roles {
		if r.Selected != "" || r.Configured {
			t.Errorf("%s selected without config: %+v", r.Role, r)
		}
		for _, o := range r.Options {
			if o.Selected {
				t.Errorf("%s/%s selected without config", r.Role, o.ID)
			}
		}
	}
}

// A built-in readiness id that no role accepts means the list drifted from the registry.
func TestBuiltInReadinessIDsExist(t *testing.T) {
	var all []string
	for _, r := range config.ProviderCatalog() {
		all = append(all, r.Providers...)
	}
	for _, id := range builtInReadiness {
		if !slices.Contains(all, id) {
			t.Errorf("built-in readiness provider %q is not in the catalog", id)
		}
	}
}

type asanaProbeFixture struct{ calls int }

func (f *asanaProbeFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet || r.URL.Host != "app.asana.com" || r.URL.Path != "/api/1.0/projects/10/tasks" {
		return nil, fmt.Errorf("unexpected readiness probe %s %s", r.Method, r.URL)
	}
	f.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
}

func TestCatalogReadinessMatchesPlan(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	previous := http.DefaultTransport
	fixture := &asanaProbeFixture{}
	http.DefaultTransport = fixture
	t.Cleanup(func() { http.DefaultTransport = previous })

	for _, tc := range []struct {
		id, role, settings, readiness, client, token string
		direct                                       bool
	}{
		{"jira", "task_management", "{}", "client", "", "", false},
		{"confluence", "knowledge_base", "{}", "client", "", "", false},
		{"slack", "notifications", "{}", "client", "", "", false},
		{"asana", "task_management", "{project: '10'}", "client-or-headless", "", "", false},
		{"asana", "task_management", "{project: '10'}", "client-or-headless", "codex", "fixture", false},
		{"asana", "task_management", "{project: '10'}", "client-or-headless", "", "fixture", true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Setenv("ASANA_ACCESS_TOKEN", tc.token)
			root := t.TempDir()
			cfg := fmt.Sprintf("code_hosting: {provider: none}\n%s: {provider: %s, settings: %s}\n", tc.role, tc.id, tc.settings)
			setupFile(t, root, ".devflow/config.yml", cfg, 0600)
			list, err := ListProviders(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			var readiness string
			for _, role := range list.Roles {
				for _, option := range role.Options {
					if role.Role == tc.role && option.ID == tc.id && option.Selected {
						readiness = option.Readiness
					}
				}
			}
			if readiness != tc.readiness {
				t.Fatalf("catalog readiness = %q, want %q", readiness, tc.readiness)
			}
			before := fixture.calls
			plan := planFixture(t, root, tc.client)
			if (fixture.calls > before) != tc.direct {
				t.Fatalf("unexpected probe count %d", fixture.calls-before)
			}
			for _, req := range plan.Requirements {
				if req.Role == tc.role && req.Provider == tc.id {
					if req.VerifyInClient == tc.direct || req.Ready != tc.direct {
						t.Fatalf("unexpected planner requirement %+v", req)
					}
					return
				}
			}
			t.Fatalf("missing %s requirement in %+v", tc.id, plan.Requirements)
		})
	}
}
