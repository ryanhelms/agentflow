package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The event watcher routes approval records by ScopeKey, so it must name
// exactly the files the gate writes.
func TestScopeKeyNamesRecordFiles(t *testing.T) {
	home := t.TempDir()
	g := gateFor(t, home, "owner")
	d, err := g.Check(Local, "commit", "payload", "", false)
	if err != nil || d.Approval == nil {
		t.Fatal(d, err)
	}
	want := filepath.Join(ApprovalsDir(home), ScopeKey("repo")+"-"+d.Approval.OperationID+".json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("record not at %s: %v", want, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(ScopeKey("repo")) {
		t.Fatalf("ScopeKey = %q", ScopeKey("repo"))
	}
	if ScopeKey("repo") == ScopeKey("other") {
		t.Fatal("scopes collide")
	}
}
