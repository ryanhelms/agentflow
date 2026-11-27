package aisession

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fsRig is a worktree with a file inside, a file outside and a nested
// repository.
func fsRig(t *testing.T) (w worktree, outside string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(out, "secret.txt")
	for p, body := range map[string]string{
		filepath.Join(root, "README.md"):              "hello\n",
		filepath.Join(root, "sub", "x.txt"):           "x\n",
		filepath.Join(root, "nested", "a.txt"):        "a\n",
		filepath.Join(root, "nested", ".git", "HEAD"): "ref: refs/heads/main\n",
		outside: "outside\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "out-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "out-dir")); err != nil {
		t.Fatal(err)
	}
	if w, err = newWorktree(root); err != nil {
		t.Fatal(err)
	}
	return w, outside
}

func TestResolveRefusesPaths(t *testing.T) {
	w, outside := fsRig(t)
	in := func(rel string) string { return filepath.Join(w.real, filepath.FromSlash(rel)) }
	tests := []struct{ name, asked, code string }{
		{"relative", "README.md", VerdictRelative},
		{"empty", "", VerdictRelative},
		{"NUL", in("README.md") + "\x00", VerdictRelative},
		{"outside", outside, VerdictOutsideWorktree},
		{"dot dot", in("sub/../../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt"), VerdictOutsideWorktree},
		{"a link out", in("out-link"), VerdictOutsideWorktree},
		{"a directory link out", in("out-dir/secret.txt"), VerdictOutsideWorktree},
		{".git", in(".git/config"), VerdictGitDir},
		{".GIT in another case", in(".GIT/config"), VerdictGitDir},
		{".Git below", in("sub/.Git/HEAD"), VerdictGitDir},
		{"a nested repository", in("nested/a.txt"), VerdictNestedRepository},
		{"the root itself", w.real, VerdictNotRegular},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, code := w.resolve(tt.asked); code != tt.code {
				t.Errorf("resolve(%q) = %q, want %q", tt.asked, code, tt.code)
			}
		})
	}
	if p, code := w.resolve(in("sub/x.txt")); code != "" || p.Rel != "sub/x.txt" || !p.Exists {
		t.Errorf("sub/x.txt = %+v %q", p, code)
	}
	if p, code := w.resolve(in("new/file.txt")); code != "" || p.Rel != "new/file.txt" || p.Exists {
		t.Errorf("new/file.txt = %+v %q", p, code)
	}
}

func TestWindowsNames(t *testing.T) {
	for _, s := range []string{"file.txt:stream", "a:b", "CON", "con.txt", "NUL", "aux.log", "COM1", "lpt9.txt", "name.", "name "} {
		if !badWindowsName(s) {
			t.Errorf("%q is allowed", s)
		}
	}
	for _, s := range []string{"file.txt", "console.txt", "com", "lpt", "comX", "a.b.c"} {
		if badWindowsName(s) {
			t.Errorf("%q is refused", s)
		}
	}
	if runtime.GOOS == "windows" {
		w, _ := fsRig(t)
		if _, code := w.resolve(filepath.Join(w.real, "README.md:secret")); code != VerdictName {
			t.Errorf("a stream name = %q", code)
		}
	}
}

// TestRootRefusesSwappedLinks: a path that passed the checks and is then
// swapped for a link out cannot escape at the moment of use.
func TestRootRefusesSwappedLinks(t *testing.T) {
	w, outside := fsRig(t)
	f, err := openFiles(w)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, code := w.resolve(filepath.Join(w.real, "sub", "x.txt")); code != "" {
		t.Fatalf("checks refused sub/x.txt: %s", code)
	}
	// After the checks: the file, then its folder, become links out.
	p := filepath.Join(w.real, "sub", "x.txt")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.read("sub/x.txt", nil, nil); fsCode(err) != VerdictOutsideWorktree {
		t.Errorf("read through a swapped file link = %v", err)
	}
	if _, err := f.write("sub/x.txt", "pwned"); fsCode(err) != VerdictLink {
		t.Errorf("write through a swapped file link = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(w.real, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(w.real, "sub")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.read("sub/secret.txt", nil, nil); fsCode(err) != VerdictOutsideWorktree {
		t.Errorf("read through a swapped folder link = %v", err)
	}
	if _, err := f.write("sub/new.txt", "pwned"); fsCode(err) != VerdictOutsideWorktree {
		t.Errorf("write through a swapped folder link = %v", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "outside\n" {
		t.Errorf("the outside file changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "new.txt")); err == nil {
		t.Error("a file was created outside the worktree")
	}
}

func TestWriteRefusesLinksAndHardlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link counts are Unix")
	}
	w, outside := fsRig(t)
	f, err := openFiles(w)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// A final-component link inside the worktree.
	if err := os.Symlink("README.md", filepath.Join(w.real, "inner-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.write("inner-link", "x"); fsCode(err) != VerdictLink {
		t.Errorf("write a final link = %v", err)
	}
	// A hard link to a file outside.
	if err := os.Link(outside, filepath.Join(w.real, "hard.txt")); err != nil {
		t.Skipf("hard links: %v", err)
	}
	if _, err := f.write("hard.txt", "pwned"); fsCode(err) != VerdictHardlink {
		t.Errorf("write a hard-linked file = %v", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "outside\n" {
		t.Errorf("the outside file changed: %q", b)
	}
	// A plain write keeps the mode and writes exactly the bytes.
	if err := os.Chmod(filepath.Join(w.real, "README.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := f.write("README.md", "new\n")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(w.real, "README.md"))
	if b, _ := os.ReadFile(filepath.Join(w.real, "README.md")); string(b) != "new\n" || info.Mode().Perm() != 0o600 || len(sum) != 64 {
		t.Errorf("README.md = %q %v %s", b, info.Mode(), sum)
	}
	if left, _ := filepath.Glob(filepath.Join(w.real, ".agentflow-write-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestReadRefusals(t *testing.T) {
	w, _ := fsRig(t)
	f, err := openFiles(w)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(filepath.Join(w.real, "bin.dat"), []byte{0xff, 0xfe, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.read("bin.dat", nil, nil); fsCode(err) != VerdictNotText {
		t.Errorf("binary = %v", err)
	}
	if _, _, err := f.read("sub", nil, nil); fsCode(err) != VerdictNotRegular {
		t.Errorf("a folder = %v", err)
	}
	if _, _, err := f.read("missing.txt", nil, nil); fsCode(err) != VerdictNotFound {
		t.Errorf("missing = %v", err)
	}
	if _, err := f.write("big.txt", string(make([]byte, MaxFileBytes+1))); fsCode(err) != VerdictTooLarge {
		t.Errorf("too large = %v", err)
	}
}
