package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/install"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

const listedPlugin = `schema: 1
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
        - invoke: {command: notify, input: {signal: ci.failed}}
`

// installPlugin installs a declarative plugin with the given ID and version
// into home, as plugin add does, without the confirmation.
func installPlugin(t *testing.T, home, id, version string) {
	t.Helper()
	src := t.TempDir()
	text := strings.NewReplacer("id: ci-notify", "id: "+id, "version: 0.1.0", "version: "+version).Replace(listedPlugin)
	if err := os.WriteFile(filepath.Join(src, manifest.FileName), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := install.PlanAdd(home, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install.Add(home, plan, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
}

func lifecycle(plugin string, state bus.PluginState, code string) bus.Event {
	payload, _ := json.Marshal(bus.PluginLifecycleChanged{Plugin: plugin, Version: "0.1.0", Tier: "declarative", State: state, Code: code})
	return bus.Event{Type: bus.TypePluginLifecycleChanged, Source: "daemon", Repo: bus.GlobalRepo, Payload: payload}
}

func TestPluginListReportsTheRegistryTheSettingsAndTheLatestState(t *testing.T) {
	home := t.TempDir()
	installPlugin(t, home, "ci-notify", "0.1.0")
	installPlugin(t, home, "other-plugin", "1.0.0")
	set := newStoreSet(t)
	global := set.addGlobal()
	putEvent(t, global, lifecycle("ci-notify", bus.PluginStarting, ""))
	putEvent(t, global, lifecycle("ci-notify", bus.PluginFailed, bus.PluginCodeHashMismatch))
	putEvent(t, global, lifecycle("ci-notify", bus.PluginRunning, ""))
	putEvent(t, global, lifecycle("gone-plugin", bus.PluginRunning, ""))
	putEvent(t, global, bus.Event{Type: bus.TypeConfigLayerChanged, Source: "test", Repo: bus.GlobalRepo, Payload: json.RawMessage(`{"layer":"personal"}`)})
	a := &App{Home: home, User: &config.User{Plugins: map[string]config.UserPlugin{"ci-notify": {Enabled: true}}},
		Cfg: &config.Repo{Plugins: config.RepoPlugins{Disabled: []string{"other-plugin"}}}}

	list, err := a.PluginList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Plugins) != 2 || list.Plugins[0].ID != "ci-notify" || list.Plugins[1].ID != "other-plugin" {
		t.Fatalf("plugins = %+v", list.Plugins)
	}
	ci, other := list.Plugins[0], list.Plugins[1]
	if !ci.Enabled || ci.DisabledInRepo || ci.State != "running" || ci.Code != "" || !ci.HashOK || ci.Name != "CI failure notifications" ||
		ci.Tier != "declarative" || !manifest.ValidHash(ci.Hash) || ci.InstalledAt.IsZero() {
		t.Errorf("ci-notify = %+v", ci)
	}
	if len(ci.Extensions) != 1 || !strings.Contains(ci.Extensions[0], "gh.ci.updated") || ci.Permissions.Invoke[0] != "notify" || ci.Permissions.Publish == nil {
		t.Errorf("ci-notify adds %v, may %+v", ci.Extensions, ci.Permissions)
	}
	if other.Enabled || !other.DisabledInRepo || other.State != "" || !other.HashOK {
		t.Errorf("other-plugin = %+v", other)
	}
	set.onlyReaders(t)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "source") || strings.Contains(string(raw), home) {
		t.Errorf("the list carries a path: %s", raw)
	}
}

func TestPluginListSaysWhenTheInstalledFilesChanged(t *testing.T) {
	home := t.TempDir()
	installPlugin(t, home, "ci-notify", "0.1.0")
	reg, _ := manifest.ReadRegistry(home)
	dir := reg.Plugins[0].Dir
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, manifest.FileName)
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	noGlobalStore(t)
	list, err := (&App{Home: home}).PluginList(context.Background())
	if err != nil || len(list.Plugins) != 1 || list.Plugins[0].HashOK {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestPluginListWithNothingInstalledIsAnEmptyList(t *testing.T) {
	noGlobalStore(t)
	list, err := (&App{Home: t.TempDir()}).PluginList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(list)
	if string(raw) != `{"plugins":[]}` {
		t.Fatalf("list = %s", raw)
	}
}

func letterLine(t *testing.T, consumer, eventID, reason, secret string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"title": secret})
	b, err := json.Marshal(bus.Letter{
		Event:    bus.Event{ID: eventID, Type: bus.TypeGHCIUpdated, Repo: "repo-1", Payload: payload},
		Consumer: consumer, Reason: reason, Time: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPluginListShowsEachPluginsNewestDeadLettersWithoutPayloads(t *testing.T) {
	home := t.TempDir()
	installPlugin(t, home, "ci-notify", "0.1.0")
	installPlugin(t, home, "other-plugin", "1.0.0")
	noGlobalStore(t)
	var lines []string
	for i := range 25 {
		lines = append(lines, letterLine(t, "plugin:ci-notify", fmt.Sprintf("evt-%02d", i), "timeout", "SECRET PR TITLE"))
	}
	lines = append(lines,
		letterLine(t, "plugin:other-plugin", "evt-other", "crashed", "x"),
		letterLine(t, "plugin:removed-plugin", "evt-gone", "exited", "x"),
		letterLine(t, "projection:timeline", "evt-proj", "error", "x"),
		"not json",
		"")
	if err := os.WriteFile(filepath.Join(manifest.PluginsPath(home), manifest.DeadLetterFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := (&App{Home: home}).PluginList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ci, other := list.Plugins[0], list.Plugins[1]
	if len(ci.DeadLetters) != MaxPluginLetters || ci.DeadLetters[0].EventID != "evt-24" || ci.DeadLetters[MaxPluginLetters-1].EventID != "evt-05" {
		t.Fatalf("ci-notify letters = %+v", ci.DeadLetters)
	}
	if l := ci.DeadLetters[0]; l.Reason != "timeout" || l.EventType != bus.TypeGHCIUpdated || l.EventRepo != "repo-1" || l.Time.IsZero() {
		t.Errorf("letter = %+v", l)
	}
	if len(other.DeadLetters) != 1 || other.DeadLetters[0].Reason != "crashed" {
		t.Fatalf("other-plugin letters = %+v", other.DeadLetters)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "SECRET PR TITLE") || strings.Contains(string(raw), "payload") {
		t.Errorf("the list carries an event payload: %s", raw)
	}
}

func TestPluginListWithoutADeadLetterFileHasEmptyLists(t *testing.T) {
	home := t.TempDir()
	installPlugin(t, home, "ci-notify", "0.1.0")
	noGlobalStore(t)
	list, err := (&App{Home: home}).PluginList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(list.Plugins[0].DeadLetters)
	if string(raw) != "[]" {
		t.Fatalf("dead_letters = %s", raw)
	}
}

func TestPluginListReadsOnlyTheTailOfAHugeDeadLetterFile(t *testing.T) {
	home := t.TempDir()
	installPlugin(t, home, "ci-notify", "0.1.0")
	noGlobalStore(t)
	filler := strings.Repeat("x", 1<<20)
	var b strings.Builder
	b.WriteString(letterLine(t, "plugin:ci-notify", "evt-old", "timeout", "x") + "\n")
	for range 9 {
		b.WriteString(filler + "\n")
	}
	b.WriteString(letterLine(t, "plugin:ci-notify", "evt-new", "crashed", "x") + "\n")
	if err := os.WriteFile(filepath.Join(manifest.PluginsPath(home), manifest.DeadLetterFile), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := (&App{Home: home}).PluginList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := list.Plugins[0].DeadLetters
	if len(got) != 1 || got[0].EventID != "evt-new" {
		t.Fatalf("letters = %+v", got)
	}
}
