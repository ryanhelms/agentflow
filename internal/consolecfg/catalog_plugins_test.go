package consolecfg

import (
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

const catalogPlugin = `schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
tier: declarative
permissions:
  subscribe: [gh.ci.updated]
  invoke: [notify]
extensions:
  handler:
    - name: ci-failed
      on: [gh.ci.updated]
      actions:
        - invoke: {command: notify, input: {signal: "{{settings.signal}}"}}
settings:
  - {key: signal, type: string, default: ci.failed, label: Notification signal, description: The signal to send.}
`

func installCatalogPlugin(t *testing.T, home string) {
	t.Helper()
	m, err := manifest.Load([]byte(catalogPlugin))
	if err != nil {
		t.Fatal(err)
	}
	err = manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
		r.Plugins = append(r.Plugins, manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Manifest: m})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepoCatalogJoinsInstalledPlugins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	without := RepoCatalog("")
	if _, _, ok := without.Lookup("personal:plugins.ci-notify.enabled", ""); ok {
		t.Fatal("a plugin that is not installed has a field")
	}
	if len(without.Groups()) != len(config.Groups()) {
		t.Errorf("groups without plugins: %v", without.Groups())
	}

	installCatalogPlugin(t, home)
	c := RepoCatalog("")
	f, rest, ok := c.Lookup("personal:plugins.ci-notify.enabled", "")
	if !ok || len(rest) != 0 || f.Meta.Loosens != config.LoosensEnable || f.Meta.Group != "plugins.ci-notify" {
		t.Fatalf("enabled = %+v, %v, %v", f, rest, ok)
	}
	if f, _, ok := c.Lookup("personal:plugins.ci-notify.settings.signal", ""); !ok || f.Schema.Default != "ci.failed" {
		t.Errorf("signal = %+v, %v", f, ok)
	}
	if _, _, ok := c.Lookup("personal:plugins.ci-notify.settings.other", ""); ok {
		t.Error("an undeclared setting has a field")
	}
	if c.Revision() == without.Revision() {
		t.Error("an installed plugin did not change the revision")
	}
	groups := c.Groups()
	if len(groups) != len(config.Groups())+1 || groups[len(groups)-1].ID != "plugins.ci-notify" {
		t.Errorf("groups = %v", groups)
	}
	// The compiled catalog is untouched by the plugin's fields.
	if n := len(RepoCatalog("").Fields()) - len(without.Fields()); n != 2 {
		t.Errorf("a plugin with one setting added %d fields, want 2", n)
	}
	if !slices.IsSortedFunc(c.Fields(), func(a, b config.CatalogField) int { return strings.Compare(a.ID, b.ID) }) {
		t.Error("fields are not sorted by ID")
	}
}

func TestRevisionCoversGroups(t *testing.T) {
	fields := []config.CatalogField{{ID: "personal:x", Layer: config.FieldPersonal, Path: "x"}}
	a := newCatalog(fields, []config.Group{{ID: "g", Label: "One", Order: 1}})
	b := newCatalog(fields, []config.Group{{ID: "g", Label: "Two", Order: 1}})
	if a.Revision() == b.Revision() {
		t.Error("a renamed group keeps the revision, so the page would not redraw it")
	}
}

func TestUnreadableRegistryAddsNoFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	installCatalogPlugin(t, home)
	put(t, home, "plugins/installed.json", "{ not json")
	c := RepoCatalog("")
	if _, _, ok := c.Lookup("personal:plugins.ci-notify.enabled", ""); ok {
		t.Error("a broken registry still adds fields")
	}
}
