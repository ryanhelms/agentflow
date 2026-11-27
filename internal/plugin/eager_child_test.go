package plugin_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// childEager speaks the protocol the way examples/plugins/timeline-hints
// does: it answers initialize and sends subscribe in the same breath, before
// the host has read the answer, never retries, and annotates every op.failed
// it receives.
func childEager([]string) int {
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
						"payload": map[string]string{"operation_id": p.Event.CorrelationID, "text": "hint", "level": "info"}})
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

// TestEagerChildSubscribesAndAnnotates: a real child that subscribes right
// after answering initialize, as the timeline-hints sample does, receives
// op.failed and its annotation is published.
func TestEagerChildSubscribesAndAnnotates(t *testing.T) {
	for i := 0; i < 5; i++ { // the race was timing-dependent: try it a few times
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f, _ := realHost(t, "eager")
			m := processManifest("hints")
			m.Permissions.Subscribe = []string{"op.failed"}
			f.install(m, true)
			notes, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "notes"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginAnnotationAdded)})
			if err != nil {
				t.Fatal(err)
			}
			f.start()
			f.waitState("hints", bus.PluginRunning)
			f.eventually("subscribed", func() bool { return len(f.status("hints").Subscriptions) == 1 })
			opID := "op_" + strings.Repeat("9", 32)
			f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: opID,
				Payload: json.RawMessage(`{"operation":"commit","code":"invalid_token"}`)})
			select {
			case e := <-notes.Events():
				if e.CorrelationID != opID {
					t.Fatalf("annotation %+v", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no annotation; status %+v", f.status("hints"))
			}
		})
	}
}
