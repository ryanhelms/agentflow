package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitMsg(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := filepath.Join(dir, "MSG")
	_ = os.WriteFile(f, []byte("feat(PLAT-1): add x\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n"), 0o644)
	var errBuf bytes.Buffer
	if err := CommitMsg(context.Background(), f, &errBuf); err != nil {
		t.Fatalf("valid message rejected: %v %s", err, errBuf.String())
	}
	if b, _ := os.ReadFile(f); strings.Contains(string(b), "anthropic") {
		t.Error("trailer not stripped")
	}
	_ = os.WriteFile(f, []byte("updated stuff\n"), 0o644)
	var ee *ExitError
	if err := CommitMsg(context.Background(), f, &errBuf); !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("invalid message accepted: %v", err)
	}
}

func TestPreBash(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := map[string]int{
		`{"tool_input":{"command":"git push --force"}}`:                                                2,
		`{"tool_input":{"command":"git commit -m \"updated stuff\""}}`:                                 2,
		`{"tool_input":{"command":"git commit -m \"feat(PLAT-1): add quota api\""}}`:                   0,
		`{"tool_input":{"command":"git commit -F msg.txt"}}`:                                           0,
		`{"tool_input":{"command":"ls -la"}}`:                                                          0,
		"{\"tool_input\":{\"command\":\"git commit -m \\\"$(cat <<'EOF'\\nfix stuff\\nEOF\\n)\\\"\"}}": 2,
	}
	for in, want := range cases {
		var errBuf bytes.Buffer
		err := PreBash(context.Background(), strings.NewReader(in), &errBuf)
		got := 0
		var ee *ExitError
		if errors.As(err, &ee) {
			got = ee.Code
		}
		if got != want {
			t.Errorf("%s: exit %d want %d (%s)", in, got, want, errBuf.String())
		}
	}
}
