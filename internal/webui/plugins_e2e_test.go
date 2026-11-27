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

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/policy"
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

	// A restarted daemon delivers no old event: the store keeps the
	// op.failed, but plugins see live events only.
	u.stop()
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	repo, err := rt.attach(u.worktree)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "timeline-hints subscribed after the restart", func() bool {
		st := hostStatus("timeline-hints")
		return st.State == bus.PluginRunning && len(st.Subscriptions) == 1
	})
	time.Sleep(300 * time.Millisecond)
	if st := hostStatus("timeline-hints"); st.Delivered != 0 {
		t.Fatalf("the restarted daemon delivered %d old events", st.Delivered)
	}
	raw, err := (&timelineProjection{repo: repo, now: time.Now}).State(ctx, ProjectionKey{Repo: repo.id})
	if err != nil {
		t.Fatal(err)
	}
	var st TimelineState
	_ = json.Unmarshal(raw, &st)
	for _, op := range st.Operations {
		if op.ID == opID && len(op.Annotations) != 1 {
			t.Fatalf("annotations after the restart: %+v", op.Annotations)
		}
	}
}

type fakeNotifyIn struct {
	Signal       string `json:"signal"`
	OperationID  string `json:"operation_id,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// TestApprovedPluginWriteRunsOnceThroughTheDaemon: ci-notify's write is held
// for a person; a person approves it in the journal, the daemon's own
// approvals watcher reports it, and the host runs the write once with no
// other action.
func TestApprovedPluginWriteRunsOnceThroughTheDaemon(t *testing.T) {
	ran := make(chan fakeNotifyIn, 4)
	notify := command.Spec[fakeNotifyIn, map[string]string]{
		Name: "notify", Policy: command.Policy{Class: policy.Remote, Action: "notify", Gate: command.GateByInvoker, AuditByInvoker: true},
		Token:    func(in *fakeNotifyIn) *string { return &in.ConfirmToken },
		Describe: func(in fakeNotifyIn) command.Detail { return command.Detail{Subject: "notify " + in.Signal} },
		Plan: func(_ context.Context, _ *app.App, in fakeNotifyIn) (command.Plan, error) {
			return command.Plan{Summary: map[string]string{"signal": in.Signal, "operation_id": in.OperationID}}, nil
		},
		Run: func(_ context.Context, _ *app.App, in fakeNotifyIn) (any, error) {
			ran <- in
			return map[string]string{"sent": in.Signal}, nil
		},
	}
	old := newPluginOptions
	newPluginOptions = func() plugin.Options {
		o := plugin.DefaultOptions(plugin.Options{Version: version.Version})
		o.Commands = func(name string) (command.Command, bool) {
			if name == "notify" {
				return notify, true
			}
			return nil, false
		}
		return o
	}
	t.Cleanup(func() { newPluginOptions = old })

	u := startUserRuntime(t, nil)
	installPlugin(t, u.home, ciNotifyManifest)
	if err := os.WriteFile(filepath.Join(u.home, "config.yml"), []byte("plugins:\n  ci-notify:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ci-notify running", func() bool { return hostStatus("ci-notify").State == bus.PluginRunning })
	ops, err := u.rt.bus.Subscribe(bus.SubscribeOptions{Name: "ops", Buffer: 64}, bus.Topic{Repo: u.repo.id, Worktree: bus.Any, Types: "op.**"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	wt := u.repo.worktreeIDs()[0]
	if _, err := u.rt.bus.Publish(ctx, bus.Event{Type: bus.TypeGHCIUpdated, Source: "watch.github", Repo: u.repo.id, Worktree: wt,
		Payload: json.RawMessage(`{"state":"failure","previous":"success","head_sha":"abc","head_ref":"main"}`)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the write held", func() bool { return len(currentPlugins().host.Held()) == 1 })
	id := currentPlugins().host.Held()[0]
	select {
	case in := <-ran:
		t.Fatalf("ran before approval: %+v", in)
	default:
	}

	person, err := policy.NewGate(u.home, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	person.Configure(u.worktree.Common, nil, nil)
	if _, err := person.ApprovePluginRequest(id); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-ran:
		if in.Signal != "ci.failed" || in.OperationID != "ci-notify-abc" {
			t.Fatalf("ran %+v", in)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the approved write never ran")
	}
	applied := 0
	deadline := time.After(time.Second)
	for done := false; !done; {
		select {
		case e := <-ops.Events():
			if e.Type == bus.TypeOpApplied && e.CorrelationID == id && e.Source == "plugin:ci-notify" {
				applied++
			}
		case <-deadline:
			done = true
		}
	}
	if applied != 1 {
		t.Fatalf("%d op.applied events for the approved write", applied)
	}
	select {
	case in := <-ran:
		t.Fatalf("ran twice: %+v", in)
	default:
	}
	if held := currentPlugins().host.Held(); len(held) != 0 {
		t.Fatalf("held after the run: %v", held)
	}
}
