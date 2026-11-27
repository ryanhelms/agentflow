package bus

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPhase7TypesRegistered(t *testing.T) {
	if len(phase7Types) != 6 {
		t.Fatalf("phase 7 types = %d, want 6", len(phase7Types))
	}
	for _, info := range phase7Types {
		got, ok := Lookup(info.Type)
		if !ok || got != info {
			t.Fatalf("%s: registered as %+v, %v", info.Type, got, ok)
		}
		if !strings.HasPrefix(string(info.Type), "ai.session.") || info.Phase != 7 || info.Global != GlobalNever || info.Scope != ScopeWorktree {
			t.Errorf("%s: %+v", info.Type, info)
		}
		if info.Transient != (info.Type == TypeAISessionOutput) {
			t.Errorf("%s: transient = %v", info.Type, info.Transient)
		}
		if Audited(info.Type) {
			t.Errorf("%s is audited by the tap", info.Type)
		}
	}
	for _, reserved := range ReservedFamilies {
		if reserved == "ai" {
			t.Error("ai is registered but still reserved")
		}
	}
}

func TestAISessionEventScope(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	repo := RepoID("0123456789abcdef")
	payload, _ := json.Marshal(AISessionChanged{Session: "ais_0123456789abcdef0123456789abcdef", State: "running", Previous: "creating"})
	for _, info := range phase7Types {
		cases := []struct {
			name string
			e    Event
			ok   bool
		}{
			{"a worktree", Event{Type: info.Type, Repo: repo, Worktree: "wt"}, true},
			{"a repository", Event{Type: info.Type, Repo: repo}, false},
			{"global", Event{Type: info.Type, Repo: GlobalRepo}, false},
		}
		for _, c := range cases {
			c.e.Source = "cli"
			c.e.Payload = payload
			if err := c.e.Normalize(now).Validate(); c.ok != (err == nil) {
				t.Errorf("%s in %s: err = %v", info.Type, c.name, err)
			}
		}
	}
}

// The payloads carry IDs, codes, counts and kinds only: no field exists for
// a prompt, message, title, path, command, option name, cost or agent text.
func TestAISessionPayloadFields(t *testing.T) {
	allowed := map[string]bool{
		"session": true, "harness": true, "started_by": true, "client": true, "ceiling": true,
		"state": true, "previous": true, "code": true, "seq": true, "items": true, "kinds": true,
		"ask": true, "kind": true, "tool_kind": true, "reason": true, "decision": true, "by": true,
		"handler": true, "duration_ms": true, "tool_calls": true, "asks": true, "allowed": true,
		"denied": true, "unasked": true,
	}
	for _, v := range []any{AISessionStarted{}, AISessionChanged{}, AISessionOutput{}, AISessionAsked{}, AISessionAnswered{}, AISessionEnded{}} {
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			f := rt.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !allowed[name] {
				t.Errorf("%s.%s: field %q is not an ID, code, count or kind", rt.Name(), f.Name, name)
			}
			switch f.Type.Kind() {
			case reflect.String, reflect.Int, reflect.Int64:
			case reflect.Slice:
				if f.Type.Elem().Kind() != reflect.String || name != "kinds" {
					t.Errorf("%s.%s: unexpected list", rt.Name(), f.Name)
				}
			default:
				t.Errorf("%s.%s: unexpected type %s", rt.Name(), f.Name, f.Type)
			}
		}
	}
}
