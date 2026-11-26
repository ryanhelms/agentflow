package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/settings"
)

const pluginManifest = `schema: 1
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
  - {key: limit, type: integer, min: 1, max: 5, default: 2, label: Limit, description: How many.}
`

// withPlugin registers the plugin in the fixture's agentflow home and opens
// the service again, so its catalog has the plugin's fields.
func (f *fixture) withPlugin(t *testing.T) {
	t.Helper()
	m, err := manifest.Load([]byte(pluginManifest))
	if err != nil {
		t.Fatal(err)
	}
	err = manifest.UpdateRegistry(f.home, func(r *manifest.Registry) error {
		r.Plugins = append(r.Plugins, manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Manifest: m})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
}

func TestPluginFieldsAndGroupsReachThePage(t *testing.T) {
	f := newFixture(t, "task-1")
	view, err := f.svc.Fields(context.Background(), settings.FieldsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(view.Groups, func(g config.Group) bool { return strings.HasPrefix(g.ID, "plugins.") }) {
		t.Fatalf("no plugin installed, yet groups %v", view.Groups)
	}
	before := view.CatalogRevision

	f.withPlugin(t)
	view, err = f.svc.Fields(context.Background(), settings.FieldsRequest{Group: "plugins.ci-notify"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, fd := range view.Fields {
		ids = append(ids, fd.ID)
	}
	want := []string{"personal:plugins.ci-notify.enabled", "personal:plugins.ci-notify.settings.limit", "personal:plugins.ci-notify.settings.signal"}
	if !slices.Equal(ids, want) {
		t.Errorf("fields = %v, want %v", ids, want)
	}
	if view.CatalogRevision == before {
		t.Error("installing a plugin did not change the catalog revision")
	}
	var group *config.Group
	for i, g := range view.Groups {
		if g.ID == "plugins.ci-notify" {
			group = &view.Groups[i]
		}
	}
	if group == nil || group.Label != "CI failure notifications" {
		t.Fatalf("groups = %v", view.Groups)
	}
	if i := slices.IndexFunc(view.Groups, func(g config.Group) bool { return g.ID == "plugins" }); i < 0 || view.Groups[i+1].ID != "plugins.ci-notify" {
		t.Errorf("the plugin's group does not follow the plugins group: %v", view.Groups)
	}
	// A search of the whole catalog finds the plugin's setting.
	view, _ = f.svc.Fields(context.Background(), settings.FieldsRequest{Query: "signal to send"})
	if len(view.Fields) != 1 || view.Fields[0].ID != "personal:plugins.ci-notify.settings.signal" {
		t.Errorf("search found %d fields", len(view.Fields))
	}
}

func TestEnablingAPluginLoosensAndNeedsTheTick(t *testing.T) {
	f := newFixture(t, "task-1")
	f.withPlugin(t)
	pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{set("personal:plugins.ci-notify.enabled", true)}})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Status != settings.StatusOK || len(pv.Changes) != 1 || !pv.Changes[0].Loosens || len(pv.Confirm) != 1 || !has(codes(pv), "info:loosens") {
		t.Fatalf("preview %+v (%v)", pv, codes(pv))
	}
	if pv.Confirm[0].ID != "personal:plugins.ci-notify.enabled" || !strings.Contains(pv.Confirm[0].Message, "ci-notify") {
		t.Errorf("confirm %+v", pv.Confirm)
	}

	res := f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:plugins.ci-notify.enabled", true)}})
	if res.Status != settings.StatusSaved {
		t.Fatalf("save = %+v", res)
	}
	if got := f.personal(t); !strings.Contains(got, "ci-notify:") || !strings.Contains(got, "enabled: true") {
		t.Errorf("personal file:\n%s", got)
	}

	// Turning it off again, or changing its settings, loosens nothing.
	for name, e := range map[string]settings.Edit{
		"disable":        set("personal:plugins.ci-notify.enabled", false),
		"unset":          {ID: "personal:plugins.ci-notify.enabled", Op: settings.OpUnset},
		"change setting": set("personal:plugins.ci-notify.settings.signal", "ci.broken"),
		"set again":      set("personal:plugins.ci-notify.enabled", true),
	} {
		pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{e}})
		if err != nil {
			t.Fatal(err)
		}
		if has(codes(pv), "info:loosens") || len(pv.Confirm) != 0 {
			t.Errorf("%s loosens: %v %+v", name, codes(pv), pv.Confirm)
		}
	}
}

func TestPluginSettingsAreChecked(t *testing.T) {
	f := newFixture(t, "task-1")
	f.withPlugin(t)
	for name, tc := range map[string]struct {
		edit settings.Edit
		code string
	}{
		"wrong type":      {set("personal:plugins.ci-notify.settings.signal", 3), "error:type"},
		"not whole":       {set("personal:plugins.ci-notify.settings.limit", "many"), "error:type"},
		"unknown setting": {set("personal:plugins.ci-notify.settings.other", true), "error:unknown"},
		"unknown plugin":  {set("personal:plugins.nobody.enabled", true), "error:unknown"},
		"out of range":    {set("personal:plugins.ci-notify.settings.limit", 9), "warning:range"},
		"enabled as text": {set("personal:plugins.ci-notify.enabled", "yes"), "error:type"},
	} {
		pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{tc.edit}})
		if err != nil {
			t.Fatal(err)
		}
		if !has(codes(pv), tc.code) {
			t.Errorf("%s: diagnostics %v, want %s", name, codes(pv), tc.code)
		}
	}
}

func TestRepositoryCanOnlyTurnPluginsOff(t *testing.T) {
	f := newFixture(t, "task-1")
	f.withPlugin(t)
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(repoConfig+"plugins:\n  disabled: [ci-notify, other]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	for name, tc := range map[string]struct {
		value []string
		loose bool
	}{
		"drop one":  {[]string{"other"}, true},
		"drop all":  {[]string{}, true},
		"add one":   {[]string{"ci-notify", "other", "third"}, false},
		"reorder":   {[]string{"other", "ci-notify"}, false},
		"unchanged": {[]string{"ci-notify", "other"}, false},
	} {
		pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{set("repo:plugins.disabled", tc.value)}})
		if err != nil {
			t.Fatal(err)
		}
		if got := has(codes(pv), "info:loosens"); got != tc.loose || (len(pv.Confirm) == 1) != tc.loose {
			t.Errorf("%s: loosens = %v (%v), want %v", name, got, codes(pv), tc.loose)
		}
	}
}
