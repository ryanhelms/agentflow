package bus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPhase3TypesRegistered(t *testing.T) {
	for _, info := range phase3Types {
		got, ok := Lookup(info.Type)
		if !ok || got != info {
			t.Fatalf("%s: registered as %+v, %v", info.Type, got, ok)
		}
		if !strings.HasPrefix(string(info.Type), "op.") || info.Phase != 3 || info.Transient || info.Global != GlobalNever {
			t.Errorf("%s: %+v", info.Type, info)
		}
		if Audited(info.Type) {
			t.Errorf("%s is audited by the tap; the Invoker audits operations", info.Type)
		}
	}
	for _, reserved := range ReservedFamilies {
		if reserved == "op" {
			t.Error("op is registered but still reserved")
		}
	}
}

func TestOpEventScope(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	repo := RepoID("0123456789abcdef")
	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"worktree operation", Event{Type: TypeOpApplied, Repo: repo, Worktree: "main"}, true},
		{"repository operation", Event{Type: TypeOpRequested, Repo: repo}, true},
		{"global operation", Event{Type: TypeOpFailed, Repo: GlobalRepo}, false},
		{"no repository", Event{Type: TypeOpFailed}, false},
	}
	for _, c := range cases {
		c.e.Source = "cli"
		err := c.e.Normalize(now).Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestOpPayloadClamp(t *testing.T) {
	long := strings.Repeat("é", MaxPayloadText) // 2 bytes per rune
	links := make([]OpLink, MaxPayloadNames+5)
	for i := range links {
		links[i] = OpLink{Kind: OpLinkCommit, ID: long}
	}
	p := OpPayload{Operation: "commit", Client: "cli", Subject: "feat: add x\n\nbody text", Branch: long, Links: links}.Clamp()
	if p.Subject != "feat: add x" {
		t.Errorf("subject %q", p.Subject)
	}
	if len(p.Branch) > MaxPayloadText || !utf8.ValidString(p.Branch) || len(p.Branch) < MaxPayloadText-1 {
		t.Errorf("branch clamped to %d bytes", len(p.Branch))
	}
	if len(p.Links) != MaxPayloadNames || len(p.Links[0].ID) > MaxPayloadText {
		t.Errorf("links %d, first id %d bytes", len(p.Links), len(p.Links[0].ID))
	}
	if links[0].ID != long {
		t.Error("Clamp changed the caller's links")
	}
	b, err := json.Marshal(OpPayload{Operation: "commit", Client: "cli"})
	if err != nil || string(b) != `{"operation":"commit","client":"cli"}` {
		t.Errorf("minimal payload %s, %v", b, err)
	}
}
