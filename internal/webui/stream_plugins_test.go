package webui

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
)

// consoleTopics are exactly the console's stream topics today
// (static/app.js liveTopics).
var consoleTopics = []string{"*/main/git.**", "*/main/config.**", "*/*/gh.**", "*/*/commit.created", "*/*/hook.ran",
	"*/*/git.head.changed", "*/*/git.worktrees.changed", "*/*/approval.**", "*/*/op.**", "*/*/ui.**"}

// annotationTopic is the repository topic a live annotation needs: an
// annotation is a repository event, and the stream sends a repository
// event's states only to a stream that follows its type. It is not a
// global plugin topic (ADR decision 10). Cross-lane dependency: the
// console's liveTopics must add it for annotations to arrive live; without
// it they appear on the next op.* event or snapshot.
const annotationTopic = "*/*/plugin.annotation.added"

func topicQuery(topics ...string) string {
	q := make([]string, 0, len(topics))
	for _, topic := range topics {
		q = append(q, "topic="+url.QueryEscape(topic))
	}
	return "?" + strings.Join(q, "&")
}

// TestPluginStatesReachTheConsoleStream checks ADR decision 10 end to end:
// a global plugin.lifecycle.changed reaches the browser as the repository's
// plugins state on the console's existing topics, and a
// plugin.annotation.added reaches it inside the timeline state.
func TestPluginStatesReachTheConsoleStream(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	f.stores[bus.GlobalRepo] = storetest.New(bus.GlobalRepo)
	src, layers := pluginFixture()
	installed, _ := src.Installed(context.Background())
	src.Set()
	ev := &repoEvents{id: repoA, store: f.stores[repoA], worktrees: map[bus.WorktreeID]registeredWorktree{"main": {ID: "main", Root: "/repo", GitDir: "/g", Common: "/g"}}}
	ev.projector = newProjector(repoA, []Projection{
		&pluginsProjection{repo: ev, runtime: func() pluginsRuntime { return pluginsRuntime{source: src, layers: layers} }},
		&timelineProjection{repo: ev, now: time.Now},
	})
	sub, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "projector"}, append([]bus.Topic{{Repo: repoA, Worktree: bus.Any, Types: "**"}}, globalTopics...)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ev.projector.run(ctx, sub)
	f.srv[repoA].events = ev

	c, _ := f.open(repoA, topicQuery(consoleTopics...))
	snap := decode[SnapshotFrame](t, c.expect(t, FrameSnapshot))
	withNotes, _ := f.open(repoA, topicQuery(append(consoleTopics, annotationTopic)...))
	withNotes.expect(t, FrameSnapshot)
	if frameState(snap.States, ProjectionPlugins) == nil || frameState(snap.States, ProjectionTimeline) == nil {
		t.Fatalf("snapshot lacks plugins or timeline: %+v", snap.States)
	}

	src.Set(installed...)
	lifecycle, _ := json.Marshal(bus.PluginLifecycleChanged{Plugin: "ci-notify", Version: "0.1.0", Tier: "declarative", State: bus.PluginRunning})
	if _, err := f.bus.Publish(context.Background(), bus.Event{Type: bus.TypePluginLifecycleChanged, Source: "plugin.host", Repo: bus.GlobalRepo, Payload: lifecycle}); err != nil {
		t.Fatal(err)
	}
	delta := decode[DeltaFrame](t, c.expect(t, FrameDelta))
	raw := frameState(delta.States, ProjectionPlugins)
	var ps PluginsState
	if raw == nil || json.Unmarshal(raw, &ps) != nil || len(ps.Plugins) != 1 || ps.Plugins[0].ID != "ci-notify" {
		t.Fatalf("plugins delta: %s", c2s(delta))
	}
	withNotes.expect(t, FrameDelta)
	c = withNotes

	op := "op_" + strings.Repeat("c", 32)
	for _, e := range []bus.Event{
		{Type: bus.TypeOpFailed, Source: "cli", Repo: repoA, Worktree: "main", CorrelationID: op, Payload: json.RawMessage(`{"operation":"commit","code":"operation_changed"}`)},
		{Type: bus.TypePluginAnnotationAdded, Source: "plugin:" + "hints", Repo: repoA, Worktree: "main", CorrelationID: op,
			Payload: json.RawMessage(`{"plugin":"hints","operation_id":"` + op + `","text":"Prepare it again.","level":"info"}`)},
	} {
		if _, err := f.bus.Publish(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no timeline state with the annotation")
		default:
		}
		d := decode[DeltaFrame](t, c.expect(t, FrameDelta))
		raw := frameState(d.States, ProjectionTimeline)
		if raw == nil {
			continue
		}
		var tl TimelineState
		_ = json.Unmarshal(raw, &tl)
		if len(tl.Operations) == 1 && len(tl.Operations[0].Annotations) == 1 && tl.Operations[0].Annotations[0].Text == "Prepare it again." {
			return
		}
	}
}

func frameState(states []ProjectionState, name string) json.RawMessage {
	for _, s := range states {
		if s.Projection == name {
			return s.State
		}
	}
	return nil
}

func c2s(d DeltaFrame) string { b, _ := json.Marshal(d); return string(b) }
