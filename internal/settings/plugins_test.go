package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
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

func TestClearPluginRemovesOnlyThatPluginsBlock(t *testing.T) {
	f := newFixture(t, "task-1")
	path := filepath.Join(f.home, "config.yml")
	body := "# my notes\natlassian:\n  email: me@example.com # work\nplugins:\n  ci-notify:\n    enabled: true\n    settings:\n      signal: ci.failed\n  other-plugin:\n    enabled: false\nworktree:\n  root: /tmp/trees\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cleared, err := f.svc.ClearPlugin("ci-notify")
	if err != nil || !cleared {
		t.Fatalf("ClearPlugin = %v, %v", cleared, err)
	}
	want := "# my notes\natlassian:\n  email: me@example.com # work\nplugins:\n  other-plugin:\n    enabled: false\nworktree:\n  root: /tmp/trees\n"
	if got := f.personal(t); got != want {
		t.Fatalf("personal settings:\n%s\nwant:\n%s", got, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	// A second call finds nothing and writes nothing.
	if cleared, err := f.svc.ClearPlugin("ci-notify"); err != nil || cleared {
		t.Fatalf("second ClearPlugin = %v, %v", cleared, err)
	}
	// The last plugin takes the empty plugins key with it.
	if cleared, err := f.svc.ClearPlugin("other-plugin"); err != nil || !cleared {
		t.Fatalf("ClearPlugin(other-plugin) = %v, %v", cleared, err)
	}
	if got, want := f.personal(t), "# my notes\natlassian:\n  email: me@example.com # work\nworktree:\n  root: /tmp/trees\n"; got != want {
		t.Fatalf("personal settings:\n%s\nwant:\n%s", got, want)
	}
}

func TestClearPluginLeavesAMissingOrUnrelatedFileAlone(t *testing.T) {
	f := newFixture(t, "task-1")
	if cleared, err := f.svc.ClearPlugin("ci-notify"); err != nil || cleared {
		t.Fatalf("no file: %v, %v", cleared, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "config.yml")); !os.IsNotExist(err) {
		t.Fatalf("ClearPlugin created the file: %v", err)
	}
	// No defaults are written for a file that has no autonomy keys.
	body := "# only a comment and one key\nupdate:\n  auto: false\n"
	if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if cleared, err := f.svc.ClearPlugin("ci-notify"); err != nil || cleared {
		t.Fatalf("nothing to clear: %v, %v", cleared, err)
	}
	if got := f.personal(t); got != body {
		t.Fatalf("the file changed:\n%s", got)
	}
	for _, id := range []string{"", "../x", "Bad", "a"} {
		if _, err := f.svc.ClearPlugin(id); err == nil {
			t.Errorf("ClearPlugin(%q) succeeded", id)
		}
	}
}

func TestClearPluginWritesThroughALinkToADotfilesCheckout(t *testing.T) {
	f := newFixture(t, "task-1")
	real := filepath.Join(t.TempDir(), "dotfiles-config.yml")
	if err := os.WriteFile(real, []byte("plugins:\n  ci-notify:\n    enabled: true\nupdate:\n  auto: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, "config.yml")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	if cleared, err := f.svc.ClearPlugin("ci-notify"); err != nil || !cleared {
		t.Fatalf("ClearPlugin = %v, %v", cleared, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced by a file: %v", err)
	}
	if b, _ := os.ReadFile(real); string(b) != "update:\n  auto: false\n" {
		t.Fatalf("checkout file:\n%s", b)
	}
}

const ruleManifest = `schema: 1
id: no-push
name: No pushes
version: 0.1.0
tier: declarative
extensions:
  permission_rule:
    - name: no-push
      match: {operation: [push]}
      deny: Pushes are paused.
`

// withRulePlugin registers a plugin that has a permission rule, and ci-notify,
// which has none.
func (f *fixture) withRulePlugin(t *testing.T) {
	t.Helper()
	f.withPlugin(t)
	m, err := manifest.Load([]byte(ruleManifest))
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

const bothEnabled = "plugins:\n  ci-notify: {enabled: true}\n  no-push: {enabled: true}\n"

func TestTurningOffAPluginWithAPermissionRuleLoosens(t *testing.T) {
	f := newFixture(t, "task-1")
	f.withRulePlugin(t)
	if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte(bothEnabled), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	for name, tc := range map[string]struct {
		edit  settings.Edit
		loose bool
	}{
		"disable":                  {set("personal:plugins.no-push.enabled", false), true},
		"unset":                    {settings.Edit{ID: "personal:plugins.no-push.enabled", Op: settings.OpUnset}, true},
		"keep on":                  {set("personal:plugins.no-push.enabled", true), false},
		"disable one without rule": {set("personal:plugins.ci-notify.enabled", false), false},
		"turn off for repository":  {set("repo:plugins.disabled", []string{"no-push"}), true},
		"turn off one without":     {set("repo:plugins.disabled", []string{"ci-notify"}), false},
		"turn off one not here":    {set("repo:plugins.disabled", []string{"somewhere-else"}), false},
	} {
		pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{tc.edit}})
		if err != nil {
			t.Fatal(err)
		}
		if got := has(codes(pv), "info:loosens"); got != tc.loose || (len(pv.Confirm) == 1) != tc.loose {
			t.Errorf("%s: loosens = %v (%v, %+v), want %v", name, got, codes(pv), pv.Confirm, tc.loose)
		}
		if tc.loose && len(pv.Confirm) == 1 && !strings.Contains(pv.Confirm[0].Message, "no-push") {
			t.Errorf("%s: confirm %+v", name, pv.Confirm[0])
		}
	}
}

// Turning off a deny rule answers to console.autonomy like any other
// loosening edit: with suggest there, config.set gives only the suggestion.
func TestConsoleAutonomySuggestKeepsADenyRuleOn(t *testing.T) {
	for name, tc := range map[string]struct{ personal, repo string }{
		"personal suggest":   {personal: "autonomy:\n  actions:\n    console.autonomy: suggest\n", repo: repoConfig},
		"repository ceiling": {repo: repoConfig + "    actions:\n      console.autonomy: suggest\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "task-1")
			f.withRulePlugin(t)
			if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte(tc.personal+bothEnabled), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(tc.repo), 0o644); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			before := f.personal(t)
			for _, cs := range []settings.ChangeSet{
				{Edits: []settings.Edit{set("personal:plugins.no-push.enabled", false)}},
				{Edits: []settings.Edit{{ID: "personal:plugins.no-push.enabled", Op: settings.OpUnset}}},
				{Edits: []settings.Edit{set("repo:plugins.disabled", []string{"no-push"})}},
				{Edits: []settings.Edit{set("personal:atlassian.email", "me@example.com"), set("personal:plugins.no-push.enabled", false)}},
			} {
				out, err := f.svc.Save(t.Context(), cs, "", f.app.GateOperation)
				if err != nil {
					t.Fatal(err)
				}
				p, ok := out.(*app.Pending)
				if !ok || p.Status != "suggest" || p.ConfirmToken != "" {
					t.Fatalf("save %v returned %#v, want suggest", cs.Edits, out)
				}
			}
			if f.personal(t) != before || len(f.pub.published) != 0 {
				t.Errorf("a suggest-only save wrote something")
			}
			// A plugin without a rule still turns off under console.settings.
			res := f.save(t, settings.ChangeSet{Edits: []settings.Edit{set("personal:plugins.ci-notify.enabled", false)}})
			if res.Status != settings.StatusSaved {
				t.Errorf("plugin without a rule: %+v", res)
			}
		})
	}
}
