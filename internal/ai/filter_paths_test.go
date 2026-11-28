package ai

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

// TestFilterDiffPaths: a secret file's content is left out however git
// names it (C-quoted, renamed away from, deleted), and a section whose
// header lacks the a/ and b/ prefixes is left out rather than checked
// against the wrong path.
func TestFilterDiffPaths(t *testing.T) {
	const marker = "+MARKER_never_sent\n"
	for _, c := range []struct {
		name, diff, reason string
	}{
		{"quoted non-ASCII", "diff --git \"a/caf\\303\\251/.env\" \"b/caf\\303\\251/.env\"\nindex 1..2 100644\n--- \"a/caf\\303\\251/.env\"\n+++ \"b/caf\\303\\251/.env\"\n@@ -0,0 +1 @@\n" + marker, ExcludedSecretFile},
		{"quoted quote", "diff --git \"a/x\\\"y/.env\" \"b/x\\\"y/.env\"\n--- /dev/null\n+++ \"b/x\\\"y/.env\"\n@@ -0,0 +1 @@\n" + marker, ExcludedSecretFile},
		{"one side quoted", "diff --git a/.env \"b/caf\\303\\251.txt\"\nrename from .env\nrename to \"caf\\303\\251.txt\"\n@@ -1 +1 @@\n" + marker, ExcludedSecretFile},
		{"renamed away", "diff --git a/.env b/notes.txt\nsimilarity index 90%\nrename from .env\nrename to notes.txt\n--- a/.env\n+++ b/notes.txt\n@@ -1 +1 @@\n" + marker, ExcludedSecretFile},
		{"deleted", "diff --git a/.env b/.env\ndeleted file mode 100644\n--- a/.env\n+++ /dev/null\n@@ -1 +0,0 @@\n" + marker, ExcludedSecretFile},
		{"spaces", "diff --git a/my dir/.env b/my dir/.env\n--- a/my dir/.env\n+++ b/my dir/.env\n@@ -0,0 +1 @@\n" + marker, ExcludedSecretFile},
		{"noprefix", "diff --git .env .env\n--- .env\n+++ .env\n@@ -0,0 +1 @@\n" + marker, ExcludedUnparsed},
		{"mnemonic", "diff --git i/.env w/.env\n--- i/.env\n+++ w/.env\n@@ -0,0 +1 @@\n" + marker, ExcludedUnparsed},
		{"bad quote", "diff --git \"a/.env b/.env\n@@ -0,0 +1 @@\n" + marker, ExcludedUnparsed},
		{"prefixless minus line", "diff --git a/x b/x\n--- x\n+++ b/x\n@@ -0,0 +1 @@\n" + marker, ExcludedUnparsed},
	} {
		t.Run(c.name, func(t *testing.T) {
			parts, err := Filter([]Part{{Kind: PartDiff, Label: "staged changes", Source: "git", Text: c.diff}}, FilterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			p := parts[0]
			if strings.Contains(p.Text, "MARKER_never_sent") {
				t.Errorf("content sent:\n%s", p.Text)
			}
			if len(p.Excluded) != 1 || p.Excluded[0].Reason != c.reason {
				t.Errorf("exclusions %+v, want %s", p.Excluded, c.reason)
			}
		})
	}
	// An ordinary file next to them still goes.
	parts, _ := Filter([]Part{{Kind: PartDiff, Text: "diff --git \"a/caf\\303\\251.go\" \"b/caf\\303\\251.go\"\n--- \"a/caf\\303\\251.go\"\n+++ \"b/caf\\303\\251.go\"\n@@ -0,0 +1 @@\n+ok\n"}}, FilterOptions{})
	if !strings.Contains(parts[0].Text, "+ok") || len(parts[0].Excluded) != 0 {
		t.Errorf("an ordinary quoted file: %+v", parts[0])
	}
}

// TestGitDiffPrefixes: GitDiff gives a/ and b/ names and runs no diff
// driver whatever the repository sets, so a staged secret file under a
// non-ASCII folder is still recognised.
func TestGitDiffPrefixes(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "diff.noprefix", "true")
	git("config", "diff.mnemonicPrefix", "true")
	git("config", "diff.external", "/bin/false")
	if err := os.MkdirAll(filepath.Join(dir, "café"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "café", ".env"), []byte("MARKER_never_sent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	out, err := GitDiff(context.Background(), &app.App{Repo: &gitx.Repo{Dir: dir}}, "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `diff --git "a/caf`) {
		t.Fatalf("diff header:\n%s", out)
	}
	parts, err := Filter([]Part{{Kind: PartDiff, Text: out}}, FilterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(parts[0].Text, "MARKER_never_sent") || len(parts[0].Excluded) != 1 || parts[0].Excluded[0].Reason != ExcludedSecretFile {
		t.Errorf("filtered: %+v", parts[0])
	}
}

// TestFilterCombinedDiff: during a conflict git writes "diff --cc" for an
// unmerged path; its content is left out, not joined to the file before
// it. Changed lines before any header are left out too.
func TestFilterCombinedDiff(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		_, _ = cmd.CombinedOutput() // the merge fails on purpose
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.go", "package a\n")
	write("config.ini", "x=1\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	write("config.ini", "DB_PASSWORD=MARKER_never_sent_other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	write("config.ini", "DB_PASSWORD=MARKER_never_sent_main\n")
	git("commit", "-q", "-am", "main")
	git("merge", "other")
	write("a.go", "package a\n\n// changed\n")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	out, err := GitDiff(context.Background(), &app.App{Repo: &gitx.Repo{Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "diff --cc config.ini") || !strings.Contains(out, "MARKER_never_sent") {
		t.Fatalf("no combined diff to test:\n%s", out)
	}
	for _, diff := range []string{out, "+MARKER_never_sent before any header\n" + out} {
		parts, err := Filter([]Part{{Kind: PartDiff, Text: diff}}, FilterOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(parts[0].Text, "MARKER_never_sent") {
			t.Errorf("combined content sent:\n%s", parts[0].Text)
		}
		if !strings.Contains(parts[0].Text, "+// changed") {
			t.Errorf("the ordinary file was left out:\n%s", parts[0].Text)
		}
	}
}

// TestFilterMovedSecret: a secret file moved under another name shows as a
// delete and an add when rename detection is off or the move came with
// large edits. No added file's content reaches the built prompt then, in
// the same diff part or another one.
func TestFilterMovedSecret(t *testing.T) {
	for _, renames := range []string{"false", "true"} {
		t.Run("diff.renames="+renames, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q", "-b", "main")
			git("config", "diff.renames", renames)
			write(".env", "A=1\nB=2\nC=3\n")
			write("a.go", "package a\n")
			git("add", ".")
			git("commit", "-q", "-m", "base")
			git("mv", ".env", "settings.txt")
			// Heavily edited, so even rename detection sees a delete and an add.
			write("settings.txt", "DB_PASSWORD=MARKER_never_sent\nX=MARKER_never_sent_too\n")
			write("a.go", "package a\n\n// changed\n")
			git("add", ".")
			t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			out, err := GitDiff(context.Background(), &app.App{Repo: &gitx.Repo{Dir: dir}}, "--cached")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "MARKER_never_sent") || !strings.Contains(out, "new file mode") {
				t.Fatalf("no delete and add to test:\n%s", out)
			}
			// The same diff, and the delete and the add in two parts.
			i := strings.Index(out, "diff --git a/settings.txt")
			splits := [][]string{{out}, {out[:i], out[i:]}}
			for _, texts := range splits {
				var parts []Part
				for _, tx := range texts {
					parts = append(parts, Part{Kind: PartDiff, Label: "changes", Source: "git", Text: tx})
				}
				filtered, err := Filter(parts, FilterOptions{})
				if err != nil {
					t.Fatal(err)
				}
				built := PromptBuilder{Feature: FeatureCommit, Task: "t", Parts: filtered}.Build()
				if strings.Contains(built.Prompt, "MARKER_never_sent") {
					t.Errorf("%d parts: the moved secret is in the prompt", len(parts))
				}
				if !strings.Contains(built.Prompt, "+// changed") {
					t.Errorf("%d parts: an edited file was left out", len(parts))
				}
				found := false
				for _, p := range filtered {
					for _, e := range p.Excluded {
						found = found || e.Path == "settings.txt" && e.Reason == ExcludedSecretMoved
					}
				}
				if !found {
					t.Errorf("%d parts: settings.txt not listed as %s: %+v", len(parts), ExcludedSecretMoved, filtered)
				}
			}
		})
	}
}
