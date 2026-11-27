package plugin_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin"
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
