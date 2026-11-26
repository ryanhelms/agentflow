package config

import (
	"slices"
	"testing"
)

func TestFieldPathRoundTrip(t *testing.T) {
	for _, keys := range [][]string{
		{"policy", "max_autonomy", "local"},
		{"autonomy", "actions", "console.publish"},
		{"worktree", "checkouts", `C:\repo`},
		{"units", "*", "name"},
	} {
		path := FormatFieldPath(keys...)
		got, err := ParseFieldPath(path)
		if err != nil || !slices.Equal(got, keys) {
			t.Fatalf("ParseFieldPath(%q) = %q, %v; want %q", path, got, err, keys)
		}
	}
	if got := FormatFieldPath("autonomy", "actions", "console.publish"); got != `autonomy.actions.console\.publish` {
		t.Fatalf("FormatFieldPath = %q", got)
	}
}

func TestParseFieldPathRejects(t *testing.T) {
	for _, path := range []string{"", ".a", "a.", "a..b", `a\x`, `a\`, "a\nb"} {
		if _, err := ParseFieldPath(path); err == nil {
			t.Errorf("ParseFieldPath(%q) accepted", path)
		}
	}
}

func TestParseFieldID(t *testing.T) {
	layer, path, err := ParseFieldID("repo:policy.max_autonomy.local")
	if err != nil || layer != FieldRepo || path != "policy.max_autonomy.local" {
		t.Fatalf("ParseFieldID = %q, %q, %v", layer, path, err)
	}
	if FieldID(FieldPersonal, "autonomy.local") != "personal:autonomy.local" {
		t.Fatal("FieldID did not join layer and path")
	}
	for _, id := range []string{"policy.max_autonomy.local", "client:x", "repo:", "repo:a..b"} {
		if _, _, err := ParseFieldID(id); err == nil {
			t.Errorf("ParseFieldID(%q) accepted", id)
		}
	}
}

func TestMatchFieldPath(t *testing.T) {
	pattern := []string{"units", "*", "name"}
	if !MatchFieldPath(pattern, []string{"units", "0", "name"}) {
		t.Fatal("wildcard did not match an index")
	}
	if MatchFieldPath(pattern, []string{"units", "0"}) || MatchFieldPath(pattern, []string{"unit", "0", "name"}) {
		t.Fatal("matched a different path")
	}
}
