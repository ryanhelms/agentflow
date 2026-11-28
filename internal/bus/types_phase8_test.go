package bus

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPhase8TypesRegistered(t *testing.T) {
	if len(phase8Types) != 1 {
		t.Fatalf("phase 8 types = %d, want 1", len(phase8Types))
	}
	info, ok := Lookup(TypeAIDraftFinished)
	if !ok || info != phase8Types[0] {
		t.Fatalf("registered as %+v, %v", info, ok)
	}
	if info.Phase != 8 || info.Global != GlobalNever || info.Scope != ScopeWorktree || info.Transient {
		t.Errorf("%+v", info)
	}
	if Audited(info.Type) {
		t.Error("ai.draft.finished is audited by the tap")
	}
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(AIDraftFinished{Session: "ais_0123456789abcdef0123456789abcdef", Feature: "commit", State: "ready", Candidates: 1})
	for _, c := range []struct {
		e  Event
		ok bool
	}{
		{Event{Type: TypeAIDraftFinished, Repo: "0123456789abcdef", Worktree: "wt"}, true},
		{Event{Type: TypeAIDraftFinished, Repo: "0123456789abcdef"}, false},
		{Event{Type: TypeAIDraftFinished, Repo: GlobalRepo}, false},
	} {
		c.e.Source, c.e.Payload = "cli", payload
		if err := c.e.Normalize(now).Validate(); c.ok != (err == nil) {
			t.Errorf("%+v: err = %v", c.e, err)
		}
	}
}

// The payload carries IDs, codes and counts only.
func TestAIDraftPayloadFields(t *testing.T) {
	allowed := map[string]bool{"session": true, "feature": true, "state": true, "code": true, "candidates": true, "issues": true, "warnings": true}
	rt := reflect.TypeOf(AIDraftFinished{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !allowed[name] {
			t.Errorf("field %q is not an ID, code or count", name)
		}
		if k := f.Type.Kind(); k != reflect.String && k != reflect.Int {
			t.Errorf("%s: unexpected type %s", f.Name, f.Type)
		}
	}
}
