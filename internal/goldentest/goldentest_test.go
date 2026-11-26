package goldentest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sha1a = "0123456789abcdef0123456789abcdef01234567"
	sha1b = "89abcdef0123456789abcdef0123456789abcdef"
	sha2  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestNormalizerReplacesVolatileValues(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"time", `"expires_at": "2026-11-26T09:30:05.123Z", "at": "2026-11-26T11:30:05+02:00"`, `"expires_at": "<time>", "at": "<time>"`},
		{"clock", "owner cli/0123456789abcdef, expires 09:30:05 UTC", "owner cli/<owner>, expires <time>"},
		{"clock without a zone name", "expires 09:30:05 -03", "expires <time>"},
		{"token", `"confirm_token": "cf_AbCdEf0123456789_-AbCdEf0123456789"`, `"confirm_token": "<confirm-token>"`},
		{"approval ID", "request op_0123456789abcdef0123456789abcdef", "request <operation-id>"},
		{"short object ID", `"sha": "89abcde"`, `"sha": "<short-object>"`},
		{"object IDs keep their number", sha1a + " " + sha1b + " " + sha1a, "<object-1> <object-2> <object-1>"},
		{"digest", `"digest": "` + sha2 + `"`, `"digest": "<digest-1>"`},
		{"text that is not an ID", "deadbeef is not 40 characters, PLAT-1 stays", "deadbeef is not 40 characters, PLAT-1 stays"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New().Text(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizerPathsAndWords(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	// The directory does not exist yet, as with a worktree that a command creates later.
	later := filepath.Join(link, "later")
	n := New().Path("<link>", link).Path("<later>", later).Word("<name>", "app")
	for _, tc := range []struct{ in, want string }{
		{link + "/a", "<link>/a"},
		{resolve(real) + "/a", "<link>/a"},
		{later + "/a", "<later>/a"},
		{filepath.Join(resolve(real), "later") + "/a", "<later>/a"},
		{"repo app, application, pineapple", "repo <name>, application, pineapple"},
	} {
		if got := n.Text(tc.in); got != tc.want {
			t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizerElidesCatalogues(t *testing.T) {
	in := "{\n  \"actions\": [\n    {\"a\": \"]\\\"[\"},\n    [1, 2]\n  ],\n  \"personal\": {\n    \"actions\": null,\n    \"providers\": {\n      \"x\": {\"y\": \"}\"}\n    }\n  }\n}\n"
	want := "{\n  \"actions\": \"<elided>\",\n  \"personal\": {\n    \"actions\": null,\n    \"providers\": \"<elided>\"\n  }\n}\n"
	if got := New().ElideArray("actions").ElideObject("providers").Text(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// An unterminated value is left alone.
	if got := New().ElideArray("actions").Text(`"actions": [1, 2`); got != `"actions": [1, 2` {
		t.Errorf("unterminated array changed to %q", got)
	}
}

func TestTranscript(t *testing.T) {
	got := New().Transcript(Run{Command: "agentflow comment", Stdin: "n\n", Stdout: "Proceed? ", Stderr: "warn\n", Err: errors.New("cancelled")})
	want := "$ agentflow comment\n--- stdin\nn\n--- stdout\nProceed? \n--- stderr\nwarn\n--- error\ncancelled\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := New().Transcript(Run{Command: "agentflow x"}), "$ agentflow x\n--- stdout\n"; got != want {
		t.Errorf("empty output: got %q, want %q", got, want)
	}
}

func TestDiff(t *testing.T) {
	got := diff("a\nb\nc\nd\n", "a\nc\nx\nd\n")
	if want := "- b\n+ x\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// recorder stands in for *testing.T so a test can observe how Check reports.
type recorder struct {
	testing.TB
	errors, fatals, logs []string
}

type stopped struct{}

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.errors = append(r.errors, fmt.Sprintf(f, a...)) }
func (r *recorder) Logf(f string, a ...any)   { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }
func (r *recorder) Fatal(a ...any)            { r.fatals = append(r.fatals, fmt.Sprint(a...)); panic(stopped{}) }
func (r *recorder) Fatalf(f string, a ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(f, a...))
	panic(stopped{})
}

// run calls f and returns r, even when f stopped on a fatal report.
func (r *recorder) run(f func(testing.TB)) (out *recorder) {
	out = r
	defer func() { _ = recover() }()
	f(r)
	return out
}

func TestCheck(t *testing.T) {
	originalRoot, originalUpdate := root, *update
	t.Cleanup(func() { root, *update = originalRoot, originalUpdate })
	root = t.TempDir()
	path := filepath.Join(root, "testdata", "golden", "dir", "out.txt")

	*update = false
	if r := new(recorder).run(func(tb testing.TB) { Check(tb, "dir/out.txt", "one\n") }); len(r.fatals) != 1 || !strings.Contains(r.fatals[0], "-update") {
		t.Fatalf("a missing golden file should fail and name -update: %+v", r)
	}

	*update = true
	if r := new(recorder).run(func(tb testing.TB) { Check(tb, "dir/out.txt", "one") }); len(r.fatals)+len(r.errors) != 0 || len(r.logs) != 1 {
		t.Fatalf("update should write quietly: %+v", r)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "one\n" {
		t.Fatalf("golden file = %q, %v; want a trailing newline added", b, err)
	}
	if r := new(recorder).run(func(tb testing.TB) { Check(tb, "dir/out.txt", "one\n") }); len(r.logs) != 0 {
		t.Fatalf("an unchanged file should not be rewritten: %+v", r)
	}

	*update = false
	if r := new(recorder).run(func(tb testing.TB) { Check(tb, "dir/out.txt", "one\n") }); len(r.fatals)+len(r.errors) != 0 {
		t.Fatalf("matching output should pass: %+v", r)
	}
	r := new(recorder).run(func(tb testing.TB) { Check(tb, "dir/out.txt", "two\n") })
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "- one") || !strings.Contains(r.errors[0], "+ two") || !strings.Contains(r.errors[0], "testdata/golden/dir/out.txt") {
		t.Fatalf("a mismatch should show a diff: %+v", r)
	}
}

func TestIsolateRemovesAmbientSettings(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("CI", "true")
	t.Setenv("AGENTFLOW_OPERATION_ID", "abc")
	t.Run("inside", func(t *testing.T) {
		Isolate(t)
		for _, name := range ambient {
			if value, set := os.LookupEnv(name); set {
				t.Errorf("%s is still set to %q", name, value)
			}
		}
		if os.Getenv("GIT_CONFIG_GLOBAL") != os.DevNull || os.Getenv("CODEX_THREAD_ID") == "" {
			t.Error("Isolate did not keep git and the agent session fixed")
		}
	})
	// The variables come back when the test that isolated them ends.
	if os.Getenv("GITHUB_RUN_ID") != "123" || os.Getenv("CI") != "true" || os.Getenv("AGENTFLOW_OPERATION_ID") != "abc" {
		t.Error("Isolate did not restore the environment")
	}
}
