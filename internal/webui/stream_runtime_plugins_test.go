package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

const ciNotifyManifest = `schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
description: Sends a notification when checks fail.
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
      actions:
        - invoke:
            command: notify
            input:
              signal: ci.failed
              operation_id: "ci-notify-{{event.payload.head_sha}}"
`

// installPlugin does what plugin add does on disk: the read-only copy
// under its hash, then the registry entry. It publishes no event.
func installPlugin(t *testing.T, home, yml string) manifest.Installed {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, manifest.FileName), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := manifest.HashDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := manifest.InstallPath(home, m.ID, hash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	p := manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Hash: hash, Dir: dir, InstalledAt: time.Now().UTC(), Manifest: m}
	if err := manifest.UpdateRegistry(home, func(r *manifest.Registry) error { r.Plugins = append(r.Plugins, p); return nil }); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDaemonNoticesAnInstalledPluginAndStartsItWhenEnabled: plugin add
// publishes no event, so the daemon learns of the registry from the user
// watch (installed.json as a personal-layer change). The plugin appears in
// the plugins projection without a restart, and enabling it in the personal
// layer starts it.
func TestDaemonNoticesAnInstalledPluginAndStartsItWhenEnabled(t *testing.T) {
	u := startUserRuntime(t, storetest.New(bus.GlobalRepo))
	l, stop := u.repo.projector.listen()
	defer stop()
	waitPlugins := func(what string, within time.Duration, ok func(PluginsState) bool) {
		t.Helper()
		deadline := time.After(within)
		for {
			select {
			case up := <-l.ch:
				for _, s := range up.states {
					var st PluginsState
					if s.Projection == ProjectionPlugins && json.Unmarshal(s.State, &st) == nil && ok(st) {
						return
					}
				}
			case <-deadline:
				t.Fatalf("plugins projection never showed %s", what)
			}
		}
	}
	if rt := currentPlugins(); rt.host == nil || rt.source == nil {
		t.Fatal("the daemon started no plugin host")
	}

	start := time.Now()
	installPlugin(t, u.home, ciNotifyManifest)
	waitPlugins("the installed plugin", 2*time.Second, func(st PluginsState) bool {
		return st.Host && len(st.Plugins) == 1 && st.Plugins[0].ID == "ci-notify" && !st.Plugins[0].Enabled
	})
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("took %v to appear", d)
	}

	if err := os.WriteFile(filepath.Join(u.home, "config.yml"), []byte("plugins:\n  ci-notify:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitPlugins("the enabled plugin running", 5*time.Second, func(st PluginsState) bool {
		return len(st.Plugins) == 1 && st.Plugins[0].Enabled && st.Plugins[0].State == bus.PluginRunning
	})
}
