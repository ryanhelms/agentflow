package policy

import (
	"errors"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
)

func TestGateConfirmFlow(t *testing.T) {
	g, err := NewGate(t.TempDir(), config.Autonomy{Local: config.Confirm, Remote: config.Auto}, "test")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"message": "feat(PLAT-1): add x", "files": []string{"a.go"}}
	d, err := g.Check(Local, "commit", payload, "", false)
	if err != nil || d.Outcome != NeedsConfirmation || d.Token == "" {
		t.Fatalf("d=%+v err=%v", d, err)
	}
	if _, err := g.Check(Local, "commit", map[string]any{"message": "changed"}, d.Token, false); err == nil {
		t.Fatal("token accepted for a different payload")
	}
	for _, malformed := range []string{strings.TrimPrefix(d.Token, "cf_"), d.Token + "\n"} {
		if _, err := g.Check(Local, "commit", payload, malformed, false); !errors.Is(err, ErrToken) {
			t.Fatal("accepted noncanonical confirmation token", err)
		}
	}
	ok, err := g.Check(Local, "commit", payload, d.Token, false)
	if err != nil || ok.Outcome != Proceed {
		t.Fatalf("redeem: %+v %v", ok, err)
	}
	if _, err := g.Check(Local, "commit", payload, d.Token, false); err == nil {
		t.Fatal("token reused")
	}
	if d, _ := g.Check(Remote, "comment", payload, "", false); d.Outcome != Proceed {
		t.Errorf("remote auto should proceed: %+v", d)
	}
	if d, _ := g.Check(Remote, "comment", payload, "", true); d.Outcome != NeedsConfirmation {
		t.Errorf("forceConfirm should require confirmation: %+v", d)
	}
}

func TestForbidden(t *testing.T) {
	prot := config.DefaultProtected
	cases := map[string]bool{
		"git push --force origin PLAT-1-x":    true,
		"git push -f":                         true,
		"git push origin +PLAT-1:PLAT-1":      true,
		"git push origin --delete PLAT-1-x":   true,
		"gh pr merge 12 --squash":             true,
		"git push origin main":                true,
		"git push origin HEAD:release/1.2":    true,
		"git push -u origin PLAT-1-gpu-quota": false,
		"git status && git log --oneline":     false,
	}
	for cmd, want := range cases {
		if got := Forbidden(cmd, prot, "PLAT-1-gpu-quota") != ""; got != want {
			t.Errorf("%q: forbidden=%v want %v", cmd, got, want)
		}
	}
	if Forbidden("git push", prot, "main") == "" {
		t.Error("bare push from main should be forbidden")
	}
}
