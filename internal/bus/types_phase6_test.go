package bus

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPhase6TypesRegistered(t *testing.T) {
	if len(phase6Types) != 3 {
		t.Fatalf("phase 6 types = %d, want 3", len(phase6Types))
	}
	for _, info := range phase6Types {
		got, ok := Lookup(info.Type)
		if !ok || got != info {
			t.Fatalf("%s: registered as %+v, %v", info.Type, got, ok)
		}
		if !strings.HasPrefix(string(info.Type), "harness.") || info.Phase != 6 || info.Global != GlobalOnly || info.Transient {
			t.Errorf("%s: %+v", info.Type, info)
		}
		if Audited(info.Type) {
			t.Errorf("%s is audited by the tap", info.Type)
		}
	}
	for _, reserved := range ReservedFamilies {
		if reserved == "harness" {
			t.Error("harness is registered but still reserved")
		}
	}
}

func TestHarnessEventScope(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	repo := RepoID("0123456789abcdef")
	payload, _ := json.Marshal(HarnessChanged{Harness: "gemini", Kind: "native", State: "native", ProbeStatus: "ok", Source: "agentflow", Trigger: "poll"})
	for _, typ := range []Type{TypeHarnessDetected, TypeHarnessChanged, TypeHarnessRemoved} {
		cases := []struct {
			name string
			e    Event
			ok   bool
		}{
			{"global", Event{Type: typ, Repo: GlobalRepo}, true},
			{"a repository", Event{Type: typ, Repo: repo}, false},
			{"a worktree", Event{Type: typ, Repo: repo, Worktree: "main"}, false},
		}
		for _, c := range cases {
			c.e.Source = "harness.detector"
			c.e.Payload = payload
			err := c.e.Normalize(now).Validate()
			if c.ok != (err == nil) {
				t.Errorf("%s in %s: err = %v", typ, c.name, err)
			}
			if err != nil && !errors.Is(err, ErrInvalidEnvelope) {
				t.Errorf("%s in %s: %v is not ErrInvalidEnvelope", typ, c.name, err)
			}
		}
	}
}

func TestHarnessPayloadFields(t *testing.T) {
	// The payloads are codes and clamped versions only: no path, args,
	// agent text or output field exists to fill.
	b, _ := json.Marshal(HarnessChanged{Harness: "gemini", Kind: "native", State: "native", ProbeStatus: "ok", Source: "agentflow", Trigger: "start"})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k := range m {
		switch k {
		case "harness", "kind", "state", "probe_status", "source", "trigger":
		default:
			t.Errorf("unexpected field %q", k)
		}
	}
	b, _ = json.Marshal(HarnessRemoved{Harness: "gemini", Previous: "native", Trigger: "poll"})
	if string(b) != `{"harness":"gemini","previous":"native","trigger":"poll"}` {
		t.Errorf("removed = %s", b)
	}
}
