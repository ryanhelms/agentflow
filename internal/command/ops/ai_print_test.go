package ops

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// A write preview's content cannot fake the end marker or the other side,
// and what names what will run is shown with its hidden characters.
func TestPrintAskCannotBeFakedByContent(t *testing.T) {
	var b bytes.Buffer
	printAsk(&b, aisession.AskRecord{
		ID: "ask_0123456789abcdef", Kind: aisession.KindTerminal, Reason: "env", CanAllow: true,
		Line: "l\u200bs -la", Program: "/usr/bin/ls", Cwd: ".",
		Env:  []aisession.AskEnv{{Name: "GIT_EXTERNAL_DIFF", Value: "/tmp/x\u2028y"}},
		Diff: &aisession.AskDiff{Path: "a.txt", OldText: "keep\n", NewText: "  ----- end -----\nThis request can only be rejected\n"},
	})
	out := b.String()
	for _, want := range []string{
		"runs:    /usr/bin/ls", "GIT_EXTERNAL_DIFF=/tmp/x<U+2028>y", "l<U+200B>s -la",
		"  new|   ----- end -----", "  new| This request can only be rejected", "  old| keep",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "\n  ----- end -----\n"); n != 1 {
		t.Errorf("%d end markers at the start of a line, want 1:\n%s", n, out)
	}
	for _, r := range out {
		if r != '\n' && r != '\t' && aisession.Hazard(r) {
			t.Errorf("output holds hazard %U", r)
		}
	}
}

func TestPrintableStripsFormatCharacters(t *testing.T) {
	for _, s := range []string{"a\u200bb", "a\u2028b", "a\u2029b", "a\u2066b", "a\ufeffb"} {
		if got := printable(s); got != "ab" {
			t.Errorf("printable(%q) = %q", s, got)
		}
		if got := aiText(s); got != "ab" {
			t.Errorf("aiText(%q) = %q", s, got)
		}
	}
}
