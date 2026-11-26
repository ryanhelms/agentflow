package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUserPluginsSectionLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	const user = `
plugins:
  ci-notify:
    enabled: true
    settings:
      signal: ci.failed
      limit: 3
  quiet: {}
`
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Plugins["ci-notify"]; !got.Enabled || got.Settings["signal"] != "ci.failed" || got.Settings["limit"] != 3 {
		t.Errorf("ci-notify = %+v", got)
	}
	if got, ok := u.Plugins["quiet"]; !ok || got.Enabled || len(got.Settings) != 0 {
		t.Errorf("quiet = %+v, %v: a plugin starts disabled", got, ok)
	}
}

func TestRepoPluginsDisabledLoads(t *testing.T) {
	c, found, err := ParseRepoCore([]byte("plugins:\n  disabled: [ci-notify, timeline-hints]\n"))
	if err != nil || !found {
		t.Fatalf("ParseRepoCore = %v, %v", found, err)
	}
	if want := []string{"ci-notify", "timeline-hints"}; !slices.Equal(c.Plugins.Disabled, want) {
		t.Errorf("disabled = %v, want %v", c.Plugins.Disabled, want)
	}
}

// A repository section has no enabled flag: strict YAML elsewhere would
// refuse it, and this one must at least never turn a plugin on.
func TestRepoPluginsCannotEnable(t *testing.T) {
	c, _, err := ParseRepoCore([]byte("plugins:\n  enabled: [ci-notify]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Plugins.Disabled) != 0 {
		t.Errorf("a repository listed plugins: %v", c.Plugins.Disabled)
	}
}

func TestCatalogHasRepoPluginsDisabledOnly(t *testing.T) {
	var found bool
	for _, f := range Catalog() {
		if strings.HasPrefix(f.Path, "plugins") && f.Layer == FieldPersonal {
			t.Errorf("%s: the compiled walk leaves out personal:plugins; installed plugins add their fields", f.ID)
		}
		if f.ID != "repo:plugins.disabled" {
			continue
		}
		found = true
		m := f.Meta
		if m.Group != "plugins" || m.Widget != WidgetChips || m.Loosens != LoosensShrink || m.Applies != AppliesLive {
			t.Errorf("repo:plugins.disabled meta = %+v", m)
		}
	}
	if !found {
		t.Error("the catalog has no repo:plugins.disabled")
	}
	if !slices.ContainsFunc(Groups(), func(g Group) bool { return g.ID == "plugins" && g.Order == GroupOrderPlugins }) {
		t.Error("Groups() has no plugins group")
	}
}
