package webui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/plugin/plugintest"
)

func pluginFixture() (*plugintest.Source, *plugintest.Layers) {
	src, layers := &plugintest.Source{}, &plugintest.Layers{}
	m := manifest.Manifest{Schema: 1, ID: "ci-notify", Name: "CI", Version: "0.1.0", Tier: manifest.TierDeclarative,
		Permissions: manifest.Permissions{Subscribe: []string{"gh.ci.updated"}, Invoke: []string{"notify"}},
		Extensions: manifest.Extensions{
			Handler:        []manifest.Handler{{Name: "ci-failed", On: []string{"gh.ci.updated"}, Actions: []manifest.Action{{Invoke: &manifest.InvokeAction{Command: "notify"}}}}},
			PermissionRule: []manifest.PermissionRule{{Name: "no-push", Match: manifest.RuleMatch{Operation: []string{"push"}}, Deny: "No.\n<b>x</b>"}},
		}}
	src.Set(manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Hash: "sha256:" + strings.Repeat("b", 64), Manifest: m})
	layers.SetPersonal("ci-notify", plugin.PersonalPlugin{Enabled: true})
	layers.SetDisabled("/repo", "ci-notify")
	return src, layers
}

func TestPluginsExtensionRunsTheHostAndTheProjectionShowsIt(t *testing.T) {
	src, layers := pluginFixture()
	old := newPluginOptions
	t.Cleanup(func() { newPluginOptions = old })
	newPluginOptions = func() plugin.Options { return plugin.Options{Source: src, Layers: layers, Home: t.TempDir()} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &eventRuntime{ctx: ctx, bus: bus.New(bus.Options{}), repos: map[string]*repoEvents{}, byID: map[bus.RepoID]*repoEvents{}}
	x := &pluginsExtension{}
	if err := x.start(rt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rtp := currentPlugins()
		if st := rtp.host.Status(); len(st) == 1 && st[0].State == bus.PluginRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host status: %+v", rtp.host.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	r := &repoEvents{id: "0123456789abcdef", worktrees: map[bus.WorktreeID]registeredWorktree{"main": {ID: "main", Root: "/repo", GitDir: "/repo/.git", Common: "/repo/.git"}}}
	p := &pluginsProjection{repo: r, runtime: currentPlugins}
	if len(p.Keys(bus.Event{Type: bus.TypePluginLifecycleChanged, Repo: bus.GlobalRepo})) != 1 || len(p.Keys(bus.Event{Type: bus.TypeGitHeadChanged, Repo: r.id})) != 0 {
		t.Fatal("keys")
	}
	raw, err := p.State(ctx, ProjectionKey{Repo: r.id})
	if err != nil {
		t.Fatal(err)
	}
	var st PluginsState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Host || len(st.Plugins) != 1 {
		t.Fatalf("state: %s", raw)
	}
	it := st.Plugins[0]
	if it.State != bus.PluginRunning || !it.Enabled || !it.DisabledHere || it.SettingsGroup != "plugins.ci-notify" || it.Since == nil ||
		len(it.Extensions) != 2 || it.Extensions[0].Invokes[0] != "notify" || it.Extensions[1].Deny != "No." || it.Permissions.Publish == nil {
		t.Fatalf("item: %+v", it)
	}
	x.stop()
	if currentPlugins().host != nil {
		t.Fatal("stop left the host reachable")
	}
}

func TestPluginsStateWithoutAHost(t *testing.T) {
	st, err := pluginsStateOf(context.Background(), pluginsRuntime{}, "")
	if err != nil || st.Host || st.Plugins == nil || st.DeadLetters == nil {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestTimelineAttachesAnnotations(t *testing.T) {
	op := "op_" + strings.Repeat("a", 32)
	now := time.Now().UTC()
	ev := func(typ bus.Type, payload string, at int) bus.Event {
		return bus.Event{ID: "e" + string(rune('a'+at)), Type: typ, Repo: "0123456789abcdef", CorrelationID: op, Time: now.Add(time.Duration(at) * time.Second), Payload: json.RawMessage(payload)}
	}
	events := []bus.Event{ev(bus.TypeOpFailed, `{"operation":"commit","code":"operation_changed"}`, 0)}
	for i := 1; i <= 7; i++ {
		events = append(events, ev(bus.TypePluginAnnotationAdded, `{"plugin":"hints","operation_id":"`+op+`","text":"hint `+string(rune('0'+i))+`\nmore","url":"javascript:x","level":"loud"}`, i))
	}
	events = append(events, ev(bus.TypePluginAnnotationAdded, `{"plugin":"hints","operation_id":"op_`+strings.Repeat("f", 32)+`","text":"elsewhere"}`, 8))
	st := timelineOf(events, now)
	attachAnnotations(&st, events)
	if len(st.Operations) != 1 {
		t.Fatalf("operations: %+v", st.Operations)
	}
	a := st.Operations[0].Annotations
	if len(a) != MaxTimelineAnnotations || a[0].Text != "hint 3" || a[4].Text != "hint 7" || a[0].URL != "" || a[0].Level != bus.AnnotationInfo || a[0].Plugin != "hints" {
		t.Fatalf("annotations: %+v", a)
	}
}
