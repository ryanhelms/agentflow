//go:build !windows

package aisession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The chain classified notes.txt as a plain file. Any time after that,
// even after the last path recheck, notes.txt becomes a link to .env: the
// read compares the opened file with the one resolve found, so it is
// refused (changed), however small the window.
func TestReadRefusesAFileSwappedAfterTheCheck(t *testing.T) {
	w, _ := fsRig(t)
	f, err := openFiles(w)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(filepath.Join(w.real, ".env"), []byte("SECRET=swapped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(w.real, "notes.txt")
	if err := os.WriteFile(notes, []byte("plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, code := w.resolve(notes)
	if code != "" {
		t.Fatal(code)
	}
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	link(t, ".env", notes)
	s, _, err := f.readChecked(p.Rel, p.info, nil, nil)
	if fsCode(err) != VerdictChanged || strings.Contains(s, "SECRET") {
		t.Errorf("read after the swap = %q, %v; want changed", s, err)
	}
}

// A write's folder is pinned: swapping docs for a link to
// .github/workflows after the chain ran is refused before anything is
// written, and a swap after the last recheck still writes into the folder
// the chain classified, never through the new link.
func TestWriteKeepsTheFolderTheChainClassified(t *testing.T) {
	setup := func(t *testing.T) (worktree, *files, Path, string) {
		w, _ := fsRig(t)
		f, err := openFiles(w)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		writeAt(t, filepath.Join(w.real, ".github", "workflows", "ci.yml"), "on: push\n")
		writeAt(t, filepath.Join(w.real, "docs", "ci.yml"), "docs\n")
		p, code := w.resolve(filepath.Join(w.real, "docs", "ci.yml"))
		if code != "" {
			t.Fatal(code)
		}
		return w, f, p, filepath.Join(w.real, ".github", "workflows", "ci.yml")
	}
	swap := func(t *testing.T, w worktree) {
		if err := os.Rename(filepath.Join(w.real, "docs"), filepath.Join(w.real, "docs-old")); err != nil {
			t.Fatal(err)
		}
		link(t, filepath.Join(".github", "workflows"), filepath.Join(w.real, "docs"))
	}
	t.Run("before the write", func(t *testing.T) {
		w, f, p, workflow := setup(t)
		swap(t, w)
		if _, err := f.writeChecked(p.Rel, "pwned\n", p.dir, nil); fsCode(err) != VerdictChanged {
			t.Errorf("write through a swapped folder = %v, want changed", err)
		}
		if b, _ := os.ReadFile(workflow); string(b) != "on: push\n" {
			t.Errorf("workflow = %q", b)
		}
	})
	t.Run("after the last recheck", func(t *testing.T) {
		w, f, p, workflow := setup(t)
		_, err := f.writeChecked(p.Rel, "pwned\n", p.dir, func() error { swap(t, w); return nil })
		if err != nil {
			t.Fatalf("write = %v", err)
		}
		if b, _ := os.ReadFile(workflow); string(b) != "on: push\n" {
			t.Errorf("workflow = %q, want it untouched", b)
		}
		if b, _ := os.ReadFile(filepath.Join(w.real, "docs-old", "ci.yml")); string(b) != "pwned\n" {
			t.Errorf("classified folder's file = %q, want the write there", b)
		}
	})
}

func writeAt(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
