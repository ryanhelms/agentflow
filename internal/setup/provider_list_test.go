package setup

import (
	"context"
	"slices"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
)

func TestListProviders(t *testing.T) {
	dir := t.TempDir()
	setupFile(t, dir, ".devflow/config.yml", headlessConfig, 0o644)
	list, err := ListProviders(context.Background(), dir, false)
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
		"jira":          {ID: "jira", Default: true, Readiness: ReadinessCompanion},
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
	list, err := ListProviders(context.Background(), t.TempDir(), false)
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

func TestListProvidersExplain(t *testing.T) {
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	dir := t.TempDir()
	setupFile(t, dir, ".devflow/config.yml", headlessConfig, 0o644)
	setupFile(t, dir, ".devflow/providers/issues.yml", "version: 1\nproviders:\n  github-issues:\n    task_management:\n      settings: {labels: [devflow]}\n", 0o644)
	list, err := ListProviders(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Definitions) != 2 || list.Definitions[1].File != ".devflow/providers/issues.yml" {
		t.Fatalf("definitions = %v", list.Definitions)
	}
	var tasks RoleOptions
	for _, r := range list.Roles {
		if r.Role == "task_management" {
			tasks = r
		}
	}
	if tasks.Contract != "tasks/v1" {
		t.Errorf("contract = %q", tasks.Contract)
	}
	sources := map[string]string{}
	for _, s := range tasks.Settings {
		sources[s.Path] = s.Source.String()
	}
	for path, want := range map[string]string{
		"repository":   "project:.devflow/config.yml",
		"labels":       "project:.devflow/providers/issues.yml",
		"search.limit": "default",
	} {
		if sources[path] != want {
			t.Errorf("%s source = %q, want %q", path, sources[path], want)
		}
	}
	plain, err := ListProviders(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range plain.Roles {
		if r.Settings != nil {
			t.Errorf("%s has settings without explain", r.Role)
		}
	}
}
