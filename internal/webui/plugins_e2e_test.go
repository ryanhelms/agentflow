package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/version"
)

const timelineHintsManifest = `schema: 1
id: timeline-hints
name: Timeline hints
version: 0.1.0
description: Adds a hint to the timeline when an operation fails.
tier: process
exec:
  command: timeline-hints
permissions:
  subscribe: [op.failed]
  publish: [plugin.annotation.added]
extensions:
  handler:
    - name: failed-operations
      on: [op.failed]
`

// childHints is examples/plugins/timeline-hints in miniature, run as the
// re-exec child: it answers initialize and subscribes in the same breath,
// and annotates every op.failed with a correlation ID.
func childHints([]string) int {
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	enc := json.NewEncoder(os.Stdout)
	next := 0
	request := func(method string, params any) {
		next++
		raw, _ := json.Marshal(params)
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": next, "method": method, "params": json.RawMessage(raw)})
	}
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &m) != nil {
				return 2
			}
			answer := func(result any) { _ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) }
			switch m.Method {
			case "initialize":
				answer(map[string]int{"protocol_version": 1})
				request("subscribe", map[string][]string{"types": {"op.failed"}})
			case "event.deliver":
				var p struct {
					Event bus.Event `json:"event"`
				}
				_ = json.Unmarshal(m.Params, &p)
				if p.Event.CorrelationID != "" {
					request("event.publish", map[string]any{"type": "plugin.annotation.added", "cause": p.Event.ID,
						"payload": map[string]string{"operation_id": p.Event.CorrelationID, "text": "Prepare it again.", "level": "info"}})
				}
				answer(struct{}{})
			case "ping", "shutdown":
				answer(struct{}{})
			}
		}
		if err != nil {
			return 0
		}
	}
}

// realPluginLauncher makes the daemon's plugin host launch the re-exec child
// for every process plugin.
func realPluginLauncher(t *testing.T) {
	t.Helper()
	old := newPluginOptions
	newPluginOptions = func() plugin.Options {
		o := plugin.DefaultOptions(plugin.Options{Version: version.Version})
		o.Launch = func(ctx context.Context, s jsonrpc.LaunchSpec) (jsonrpc.Process, error) {
			child := jsonrpctest.ChildSpec(t, "hints")
			s.Path, s.Root, s.Args, s.ShutdownGrace = child.Path, child.Root, child.Args, child.ShutdownGrace
			return jsonrpc.Launch(ctx, s)
		}
		return o
	}
	t.Cleanup(func() { newPluginOptions = old })
}

func hostStatus(id string) plugin.Status {
	rt := currentPlugins()
	if rt.host == nil {
		return plugin.Status{}
	}
	for _, st := range rt.host.Status() {
		if st.ID == id {
			return st
		}
	}
	return plugin.Status{}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

// TestSocketEventReachesARealProcessPluginAndItsAnnotationTheTimeline: an
// op.failed that a separate process (CLI, MCP or a hook) hands the daemon's
// notify socket reaches a real process plugin, and the plugin's annotation
// appears on the timeline. A restarted daemon does not deliver it again.
func TestSocketEventReachesARealProcessPluginAndItsAnnotationTheTimeline(t *testing.T) {
	realPluginLauncher(t)
	u := startUserRuntime(t, nil)
	installPlugin(t, u.home, timelineHintsManifest)
	installPlugin(t, u.home, ciNotifyManifest)
	if err := os.WriteFile(filepath.Join(u.home, "config.yml"),
		[]byte("plugins:\n  timeline-hints:\n    enabled: true\n  ci-notify:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "timeline-hints subscribed", func() bool {
		st := hostStatus("timeline-hints")
		return st.State == bus.PluginRunning && len(st.Subscriptions) == 1
	})
	waitFor(t, "ci-notify running", func() bool { return hostStatus("ci-notify").State == bus.PluginRunning })

	ctx := context.Background()
	wt := u.repo.worktreeIDs()[0]
	opID := "op_" + strings.Repeat("7", 32)
	// The envelope a CLI process sends: it sets ID, time and schema itself.
	now := time.Now().UTC()
	failed := bus.Event{ID: bus.NewID(now), Time: now, SchemaVersion: bus.SchemaVersion, Type: bus.TypeOpFailed, Source: "cli",
		Repo: u.repo.id, Worktree: wt, CorrelationID: opID, Payload: json.RawMessage(`{"operation":"commit","code":"invalid_token"}`)}
	if _, err := (daemonAcceptor{u.rt}).Accept(ctx, failed); err != nil {
		t.Fatalf("the socket refused the event: %v", err)
	}
	waitFor(t, "delivered to timeline-hints", func() bool { return hostStatus("timeline-hints").Delivered == 1 })

	timeline := func() TimelineState {
		raw, err := (&timelineProjection{repo: u.repo, now: time.Now}).State(ctx, ProjectionKey{Repo: u.repo.id})
		if err != nil {
			t.Fatal(err)
		}
		var st TimelineState
		_ = json.Unmarshal(raw, &st)
		return st
	}
	waitFor(t, "the annotation on the timeline", func() bool {
		for _, op := range timeline().Operations {
			if op.ID == opID && len(op.Annotations) == 1 && op.Annotations[0].Plugin == "timeline-hints" && op.Annotations[0].Text == "Prepare it again." {
				return true
			}
		}
		return false
	})

	// gh.ci.updated, as the GitHub poller publishes it in the daemon,
	// reaches the declarative ci-notify.
	ci := bus.Event{Type: bus.TypeGHCIUpdated, Source: "watch.github", Repo: u.repo.id, Worktree: wt,
		Payload: json.RawMessage(`{"state":"success","previous":"pending","head_sha":"abc","head_ref":"main"}`)}
	if _, err := u.rt.bus.Publish(ctx, ci); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delivered to ci-notify", func() bool { return hostStatus("ci-notify").Delivered == 1 })

	// A restarted host gets no old event: plugins see live events only.
	if err := currentPlugins().host.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if st := hostStatus("timeline-hints"); st.Delivered != 1 {
		t.Fatalf("delivered %d after reload", st.Delivered)
	}
}
