package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goldenPluginManifest = `schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
description: Sends a notification when the checks of a branch you work on start failing.
tier: declarative
permissions:
  subscribe: [gh.ci.updated]
  invoke: [notify]
extensions:
  handler:
    - name: ci-failed
      on: [gh.ci.updated]
      when:
        - {field: payload.state, equals: failure}
        - {field: payload.previous, not_equals: failure}
      actions:
        - invoke:
            command: notify
            input:
              signal: "{{settings.signal}}"
              operation_id: "ci-notify-{{event.payload.head_sha}}"
settings:
  - key: signal
    type: string
    default: ci.failed
    label: Notification signal
    description: The notification signal to send.
`

// TestGoldenPlugin pins what the plugin commands print: list (empty and with
// a plugin), add (the confirmation summary, the result, a second add that
// changes nothing, an upgrade, a refusal) and remove.
func TestGoldenPlugin(t *testing.T) {
	g := goldenConfigRepo(t)
	t.Cleanup(func() {
		// An installed copy is read-only: make it deletable for the temporary directory.
		_ = filepath.WalkDir(os.Getenv("AGENTFLOW_HOME"), func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "agentflow-plugin.yml"), []byte(goldenPluginManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	g.norm.Path("<plugin-src>", src)

	g.check("plugin/list_empty.txt", g.run("", "plugin", "list"))
	g.check("plugin/add_refused_address.txt", g.run("", "plugin", "add", "https://example.com/plugin", "--yes"))
	g.check("plugin/add_refused_missing.txt", g.run("", "plugin", "add", filepath.Join(src, "missing"), "--yes"))
	g.check("plugin/add_declined.txt", g.run("n\n", "plugin", "add", src))
	g.check("plugin/add.txt", g.run("", "plugin", "add", src, "--yes"))
	g.check("plugin/add_unchanged_json.txt", g.run("", "plugin", "add", src, "--json"))
	g.check("plugin/list.txt", g.run("", "plugin", "list"))
	g.check("plugin/list_json.txt", g.run("", "plugin", "list", "--json"))

	// The command the documentation gives for turning a plugin on.
	g.check("plugin/enable.txt", g.run("", "config", "set", "plugins.ci-notify.enabled=true", "--yes"))
	g.check("plugin/list_enabled.txt", g.run("", "plugin", "list"))

	upgraded := strings.Replace(goldenPluginManifest, "version: 0.1.0", "version: 0.2.0", 1)
	if err := os.WriteFile(filepath.Join(src, "agentflow-plugin.yml"), []byte(upgraded), 0o644); err != nil {
		t.Fatal(err)
	}
	g.check("plugin/add_upgrade.txt", g.run("", "plugin", "add", src, "--yes"))

	g.check("plugin/remove_unknown.txt", g.run("", "plugin", "remove", "never-added", "--yes"))
	g.check("plugin/remove_declined.txt", g.run("n\n", "plugin", "remove", "ci-notify"))
	g.check("plugin/remove.txt", g.run("", "plugin", "remove", "ci-notify", "--yes"))
	if b, err := os.ReadFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml")); err != nil || strings.Contains(string(b), "ci-notify") {
		t.Errorf("plugin remove left plugins.ci-notify in the personal settings: %v\n%s", err, b)
	}
	g.check("plugin/remove_json_again.txt", g.run("", "plugin", "remove", "ci-notify", "--yes", "--json"))
}
