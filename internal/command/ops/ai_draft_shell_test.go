package ops

import (
	"bytes"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/ai"
)

// hostileWords are drafted words that try to end the quoting in one of the
// shells a person pastes into.
var hostileWords = []string{
	"", "plain", "x'y", `x\'; echo INJECTED; echo '`, `a\\'b`, `\`, `'`, `''`, `\\`,
	"$(echo INJECTED)", "`echo INJECTED`", "${HOME}", "$HOME", "a\nb", "tab\there",
	"=ls", "%self", "~", "-n", "*", "a b", `"quoted"`, `\"`, "{a,b}", "!!", "#comment",
	"ünïcode", "end\\", "'\\'", "feat: add x'); echo INJECTED; ('",
}

// TestShellLineNoEscapesInQuotes: no quote or backslash ever sits inside
// single quotes (fish reads \' and \\ there as escapes), and a plain word
// never starts with = or %.
func TestShellLineNoEscapesInQuotes(t *testing.T) {
	for _, w := range hostileWords {
		q := shellWord(w)
		in := false
		for i := 0; i < len(q); i++ {
			switch c := q[i]; {
			case c == '"' && !in:
				// "'" or "\\": skip to its end.
				end := strings.IndexByte(q[i+1:], '"')
				if end < 0 || !slices.Contains([]string{"'", `\\`}, q[i+1:i+1+end]) {
					t.Fatalf("%q -> %s: bad double-quoted part at %d", w, q, i)
				}
				i += end + 1
			case c == '\'':
				in = !in
			case c == '\\' && in:
				t.Fatalf("%q -> %s: backslash inside single quotes", w, q)
			case !in && !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@", rune(c)):
				t.Fatalf("%q -> %s: %q outside quotes", w, q, c)
			}
		}
		if in {
			t.Fatalf("%q -> %s: quote left open", w, q)
		}
		if strings.HasPrefix(q, "=") || strings.HasPrefix(q, "%") {
			t.Fatalf("%q -> %s: plain word starts with = or %%", w, q)
		}
	}
}

// TestShellLineRoundTrip reads the line back through each shell present:
// the shell is given `printf '%s\0'` and the line as its arguments, and
// must print exactly the words, with nothing run.
func TestShellLineRoundTrip(t *testing.T) {
	ran := 0
	for _, sh := range []string{"bash", "zsh", "fish"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		ran++
		cmd := exec.Command(path, "-c", `printf '%s\0' `+shellLine(hostileWords))
		cmd.Dir = t.TempDir()
		cmd.Env = []string{"HOME=" + cmd.Dir, "PATH=/usr/bin:/bin"}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v %s", sh, err, errb.String())
		}
		got := strings.Split(strings.TrimSuffix(out.String(), "\x00"), "\x00")
		if !slices.Equal(got, hostileWords) {
			t.Errorf("%s read back\n%q\nwant\n%q", sh, got, hostileWords)
		}
		if strings.Contains(errb.String(), "INJECTED") {
			t.Errorf("%s ran something: %s", sh, errb.String())
		}
	}
	if ran == 0 {
		t.Skip("no bash, zsh or fish here")
	}
}

// TestDraftResultPasteLine: the label names the shells, and on Windows no
// paste-ready line is printed.
func TestDraftResultPasteLine(t *testing.T) {
	d := ai.Draft{State: ai.DraftReady, Candidates: []ai.Candidate{{Operation: "commit.prepare", CLI: []string{"agentflow", "commit", "--prepare", "-m", "x'y"}}}}
	var b bytes.Buffer
	if err := printDraftResult(&b, d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `use it (bash, zsh or fish): agentflow commit --prepare -m 'x'"'"'y'`) {
		t.Errorf("output:\n%s", b.String())
	}
	old := pasteGOOS
	pasteGOOS = "windows"
	t.Cleanup(func() { pasteGOOS = old })
	b.Reset()
	_ = printDraftResult(&b, d)
	if strings.Contains(b.String(), "agentflow commit") || !strings.Contains(b.String(), "no paste-ready line") {
		t.Errorf("windows output:\n%s", b.String())
	}
}
