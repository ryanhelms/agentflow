package aisession

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp"
)

const module = "github.com/ryanhelms/agentflow/"

// TestAISessionImports: the "package dependencies" import rule.
func TestAISessionImports(t *testing.T) {
	list := func(format string) []string {
		t.Helper()
		out, err := exec.Command("go", "list", "-f", format, ".").Output()
		if err != nil {
			t.Skipf("go list: %v", err)
		}
		return strings.Fields(string(out))
	}
	allowed := []string{"acp", "jsonrpc", "harness", "policy", "config", "boundary", "bus", "notifysock", "filelock", "gitx"}
	for _, p := range list(`{{join .Imports " "}}`) {
		if !strings.Contains(p, ".") && !strings.HasPrefix(p, module) {
			continue // the standard library
		}
		rel, ok := strings.CutPrefix(p, module+"internal/")
		if !ok {
			t.Errorf("aisession imports %s", p)
			continue
		}
		top, _, _ := strings.Cut(rel, "/")
		if !slices.Contains(allowed, top) {
			t.Errorf("aisession imports %s", p)
		}
	}
	// By exact path (internal/command by prefix): a leaf such as
	// internal/plugin/manifest, which policy and harness import, is no cycle.
	never := []string{"app", "webui", "mcpserver", "plugin", "cli", "command"}
	for _, p := range list(`{{join .Deps " "}}`) {
		rel, ok := strings.CutPrefix(p, module+"internal/")
		if !ok {
			continue
		}
		if slices.Contains(never, rel) || strings.HasPrefix(rel, "command/") {
			t.Errorf("aisession depends on %s", p)
		}
	}
}

func TestCountersJSON(t *testing.T) {
	b, err := json.Marshal(Counters{Updates: 1, ToolCalls: 2, Asks: 3, Allowed: 4, Denied: 5, Unasked: 6, Terminals: 7, BytesRead: 8, BytesWritten: 9, TranscriptBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"updates":1,"tool_calls":2,"asks":3,"allowed":4,"denied":5,"unasked":6,"terminals":7,"bytes_read":8,"bytes_written":9,"transcript_bytes":10}`
	if string(b) != want {
		t.Errorf("Counters = %s", b)
	}
}

// TestAskFormatsFitThePage: the console accepts ask IDs matching
// ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ and fingerprints of 16 to 128
// characters of [A-Za-z0-9+/=_:-]; what the host makes must fit both.
func TestAskFormatsFitThePage(t *testing.T) {
	pageID := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	pageFP := regexp.MustCompile(`^[A-Za-z0-9+/=_:-]{16,128}$`)
	for range 100 {
		id := newAskID()
		if !validAskID(id) || !pageID.MatchString(id) {
			t.Fatalf("ask ID %q", id)
		}
		fp := fingerprintOf(AskRecord{ID: id, Session: NewID(), Kind: KindWrite, Paths: []string{"a"}})
		if !pageFP.MatchString(fp) || len(fp) != 64 {
			t.Fatalf("fingerprint %q", fp)
		}
	}
}

// TestFingerprintCoversThePreview: a different new content is a different
// ask.
func TestFingerprintCoversThePreview(t *testing.T) {
	a := AskRecord{ID: "ask_0000000000000000", Kind: KindWrite, NewSHA256: strings.Repeat("a", 64)}
	b := a
	b.NewSHA256 = strings.Repeat("b", 64)
	if fingerprintOf(a) == fingerprintOf(b) {
		t.Error("the fingerprint ignores the new content's SHA-256")
	}
}

func TestPickOptionNeverAllowsAlways(t *testing.T) {
	opts := []acp.PermissionOption{
		{OptionID: "always", Kind: acp.AllowAlways},
		{OptionID: "never", Kind: acp.RejectAlways},
	}
	if got := pickOption(opts, Allow); got.Outcome.OptionID != "never" {
		t.Errorf("allow with only allow_always = %+v", got)
	}
	opts = append(opts, acp.PermissionOption{OptionID: "once", Kind: acp.AllowOnce}, acp.PermissionOption{OptionID: "no", Kind: acp.RejectOnce})
	if got := pickOption(opts, Allow); got.Outcome.OptionID != "once" {
		t.Errorf("allow = %+v", got)
	}
	if got := pickOption(opts, Deny); got.Outcome.OptionID != "no" {
		t.Errorf("deny = %+v", got)
	}
	if got := pickOption([]acp.PermissionOption{{OptionID: "always", Kind: acp.AllowAlways}}, Deny); got.Outcome.Outcome != acp.OutcomeCancelled {
		t.Errorf("deny without a reject option = %+v", got)
	}
}

func TestSanitizeNeutralizesTerminalSequences(t *testing.T) {
	in := "a\x1b[31mred\x1b[0m\x1b]0;title\x07b\u202ec\u009b31md\re\x00f\xff\n\tg ghp_" + strings.Repeat("x", 30)
	got := Sanitize(in, 0)
	want := "aredbcdef\n\tg " + Redacted
	if got != want {
		t.Errorf("Sanitize = %q, want %q", got, want)
	}
	if got := SanitizeLine("one\ntwo\tthree", 0); got != "one two three" {
		t.Errorf("SanitizeLine = %q", got)
	}
	if got := Clamp("héllo", 2); got != "h" {
		t.Errorf("Clamp = %q", got)
	}
	if got := ClampStart("héllo", 4); got != "llo" {
		t.Errorf("ClampStart = %q", got)
	}
}
