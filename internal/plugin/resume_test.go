package plugin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// heldApproval runs ci-notify until its write waits for a person, and returns
// the approval ID and a person's gate on the same journal.
func heldApproval(t *testing.T, f *fixture) (string, *policy.Gate) {
	t.Helper()
	f.install(ciNotify(), true)
	f.start()
	f.waitState("ci-notify", bus.PluginRunning)
	f.publish(ciUpdated("failure", "success"))
	f.eventually("held", func() bool { return len(f.h.Held()) == 1 })
	person, err := policy.NewGate(f.gateDir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	id := f.h.Held()[0]
	req, err := person.PluginRequestOf(id)
	if err != nil || req.Request.Plugin != "ci-notify" || req.Request.Command != "notify" || req.Request.Subject != "notify ci.failed" {
		t.Fatalf("stored request: %+v %v", req, err)
	}
	return id, person
}

func resolved(id string, state bus.ApprovalState) bus.Event {
	raw, _ := json.Marshal(bus.ApprovalResolved{Approval: bus.Approval{OperationID: id, Action: "notify", Class: "remote"}, State: state, Previous: bus.StatePending})
	return bus.Event{Type: bus.TypeApprovalResolved, Source: "watch.approvals", Repo: repo, Payload: raw}
}

func noRun(t *testing.T, f *fixture) {
	t.Helper()
	select {
	case in := <-f.ran:
		t.Fatalf("the write ran: %+v", in)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestApprovedPluginWriteRunsOnceWithNoOtherAction(t *testing.T) {
	f := newFixture(t, nil)
	id, person := heldApproval(t, f)
	noRun(t, f)
	// An event before the approval changes nothing: the record says pending.
	f.publish(resolved(id, bus.StateApproved))
	noRun(t, f)
	if len(f.h.Held()) != 1 {
		t.Fatal("a pending approval was dropped")
	}
	if _, err := person.ApprovePluginRequest(id); err != nil {
		t.Fatal(err)
	}
	f.publish(resolved(id, bus.StateApproved))
	select {
	case in := <-f.ran:
		if in.Signal != "ci.failed" || in.OperationID != "ci-notify-abc" {
			t.Fatalf("ran %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the approved write never ran")
	}
	// Never twice: a repeated event, and the plugin's state, run nothing more.
	f.publish(resolved(id, bus.StateApproved))
	f.publish(resolved(id, bus.StateConsumed))
	noRun(t, f)
	if len(f.h.Held()) != 0 {
		t.Fatalf("held: %v", f.h.Held())
	}
	f.eventually("op.applied", func() bool {
		for _, e := range f.ops {
			if e.Type == bus.TypeOpApplied && e.CorrelationID == id {
				return e.Source == "plugin:ci-notify"
			}
		}
		return false
	})
}

func TestDeclinedPluginWriteIsDropped(t *testing.T) {
	f := newFixture(t, nil)
	id, person := heldApproval(t, f)
	if _, err := person.DeclinePluginRequest(id); err != nil {
		t.Fatal(err)
	}
	f.publish(resolved(id, bus.StateCancelled))
	f.eventually("dropped", func() bool { return len(f.h.Held()) == 0 })
	noRun(t, f)
}

func TestApprovedWriteOfAPluginTurnedOffIsDropped(t *testing.T) {
	f := newFixture(t, nil)
	id, person := heldApproval(t, f)
	f.layers.SetPersonal("ci-notify", plugin.PersonalPlugin{})
	if err := f.h.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.waitState("ci-notify", bus.PluginStopped)
	if _, err := person.ApprovePluginRequest(id); err != nil {
		t.Fatal(err)
	}
	f.publish(resolved(id, bus.StateApproved))
	f.eventually("dropped", func() bool { return len(f.h.Held()) == 0 })
	noRun(t, f)
}

// A held write runs only for the plugin that asked as it was then: the same
// installed version and hash, still granted the command.
func TestApprovedWriteOfAReplacedPluginIsDropped(t *testing.T) {
	for name, change := range map[string]func(*manifest.Installed){
		"new version":                func(p *manifest.Installed) { p.Version = "0.2.0" },
		"new hash without the grant": func(p *manifest.Installed) { p.Manifest.Permissions.Invoke = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			id, person := heldApproval(t, f)
			p := f.installed()[0]
			p.Hash = "sha256:" + strings.Repeat("b", 64)
			change(&p)
			p.Manifest.Version = p.Version
			f.src.Set(p)
			if err := f.h.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.waitState("ci-notify", bus.PluginRunning)
			if _, err := person.ApprovePluginRequest(id); err != nil {
				t.Fatal(err)
			}
			f.publish(resolved(id, bus.StateApproved))
			f.eventually("dropped", func() bool { return len(f.h.Held()) == 0 })
			noRun(t, f)
		})
	}
}

// When the approval watcher's subscription overflows, the host settles
// every held call: an approval whose event was dropped still runs.
func TestApprovalOverflowSettlesEveryHeldCall(t *testing.T) {
	f := newFixture(t, nil)
	id, person := heldApproval(t, f)
	if _, err := person.ApprovePluginRequest(id); err != nil {
		t.Fatal(err)
	}
	f.h.ApprovalEventForTest(t.Context(), bus.Event{Type: bus.TypeStreamOverflow, Payload: json.RawMessage(`{"subscription":"plugin-host/approvals","dropped":300}`)})
	select {
	case in := <-f.ran:
		if in.Signal != "ci.failed" {
			t.Fatalf("ran %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an approved write was not run after an overflow")
	}
	f.eventually("settled", func() bool { return len(f.h.Held()) == 0 })
}

// processHeld has a process plugin ask for a write, and returns its invoke
// call, the held approval ID, and a person's gate that approved it.
func processHeld(t *testing.T, f *fixture, rc *recorder) (func(plugin.InvokeRequest) (plugin.InvokeResult, error), string, string) {
	t.Helper()
	f.install(processManifest("actor"), true)
	f.start()
	f.waitState("actor", bus.PluginRunning)
	f.eventually("subscribed", func() bool { return len(f.status("actor").Subscriptions) == 1 })
	cause := f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: "op_" + strings.Repeat("5", 32), Payload: json.RawMessage(`{}`)})
	f.eventually("delivered", func() bool { return len(rc.events()) == 1 })
	c := f.launch.Last()
	invoke := func(r plugin.InvokeRequest) (plugin.InvokeResult, error) {
		var out plugin.InvokeResult
		err := c.Host.Call(context.Background(), plugin.MethodCommandInvoke, r, &out)
		return out, err
	}
	res, err := invoke(plugin.InvokeRequest{Command: "notify", Cause: cause.ID, Input: json.RawMessage(`{"signal":"s"}`)})
	if err != nil || res.Outcome != "awaiting_approval" {
		t.Fatalf("held: %+v %v", res, err)
	}
	person, err := policy.NewGate(f.gateDir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := person.ApprovePluginRequest(res.ApprovalID); err != nil {
		t.Fatal(err)
	}
	return invoke, res.ApprovalID, cause.ID
}

func opFailed(f *fixture, id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.ops {
		if e.Type == bus.TypeOpFailed && e.CorrelationID == id {
			n++
		}
	}
	return n
}

// The host and the plugin both resume an approved write: whichever claims
// it first runs it once, and the other runs nothing and publishes nothing,
// no op.failed for an approval already used.
func TestHostAndPluginResumeRunTheWriteOnce(t *testing.T) {
	t.Run("host first", func(t *testing.T) {
		rc := &recorder{}
		f := newFixture(t, rc.child("op.failed"))
		invoke, id, cause := processHeld(t, f, rc)
		f.publish(resolved(id, bus.StateApproved))
		select {
		case <-f.ran:
		case <-time.After(5 * time.Second):
			t.Fatal("the host did not resume the write")
		}
		out, err := invoke(plugin.InvokeRequest{Command: "notify", Cause: cause, Input: json.RawMessage(`{"signal":"s"}`), Resume: id})
		if err != nil || out.Outcome != "failed" || out.Code != plugin.CodeResumedByHost {
			t.Fatalf("the plugin's late resume: %+v %v", out, err)
		}
		noRun(t, f)
		if n := opFailed(f, id); n != 0 {
			t.Fatalf("%d op.failed for the loser", n)
		}
	})
	t.Run("plugin first", func(t *testing.T) {
		rc := &recorder{}
		f := newFixture(t, rc.child("op.failed"))
		invoke, id, cause := processHeld(t, f, rc)
		out, err := invoke(plugin.InvokeRequest{Command: "notify", Cause: cause, Input: json.RawMessage(`{"signal":"s"}`), Resume: id})
		if err != nil || out.Outcome != "applied" {
			t.Fatalf("the plugin's resume: %+v %v", out, err)
		}
		<-f.ran
		if len(f.h.Held()) != 0 {
			t.Fatalf("still held: %v", f.h.Held())
		}
		f.publish(resolved(id, bus.StateApproved))
		noRun(t, f)
		if n := opFailed(f, id); n != 0 {
			t.Fatalf("%d op.failed for the loser", n)
		}
	})
}
