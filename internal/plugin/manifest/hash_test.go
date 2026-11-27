package manifest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// TestHashFormat recomputes the hash of the ADR's definition by hand: one
// line "<path>\x00<x or ->\x00<sha256 hex>\n" per file sorted by path.
func TestHashFormat(t *testing.T) {
	files := []manifest.TreeFile{
		{Path: "run", Exec: true, Data: []byte("#!/bin/sh\n")},
		{Path: "agentflow-plugin.yml", Data: []byte(ciNotify)},
		{Path: "a.txt", Data: []byte("a")},
		{Path: "a/b", Data: []byte("b")},
	}
	var lines strings.Builder
	for _, f := range []manifest.TreeFile{files[2], files[3], files[1], files[0]} { // a.txt, a/b, agentflow-plugin.yml, run
		x := "-"
		if f.Exec {
			x = "x"
		}
		sum := sha256.Sum256(f.Data)
		fmt.Fprintf(&lines, "%s\x00%s\x00%s\n", f.Path, x, hex.EncodeToString(sum[:]))
	}
	all := sha256.Sum256([]byte(lines.String()))
	want := "sha256:" + hex.EncodeToString(all[:])
	if got := manifest.HashFiles(files); got != want {
		t.Errorf("HashFiles = %s, want %s", got, want)
	}
	if !manifest.ValidHash(want) {
		t.Errorf("%s is not a valid hash", want)
	}
	if got := manifest.HashPrefix(want); got != want[7:23] {
		t.Errorf("HashPrefix = %q", got)
	}
	if manifest.HashPrefix("sha256:zz") != "" || manifest.HashPrefix("") != "" {
		t.Error("HashPrefix of a malformed hash is not empty")
	}
}

func TestHashDirDependsOnContentNamesAndExecutableBit(t *testing.T) {
	base := map[string]string{"agentflow-plugin.yml": ciNotify, "lib/data.txt": "one"}
	hash := func(files map[string]string) string {
		t.Helper()
		h, err := manifest.HashDir(tree(t, files))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	want := hash(base)
	if hash(base) != want {
		t.Error("the same tree hashes differently in a new directory")
	}
	for name, files := range map[string]map[string]string{
		"content":     {"agentflow-plugin.yml": ciNotify, "lib/data.txt": "two"},
		"renamed":     {"agentflow-plugin.yml": ciNotify, "lib/other.txt": "one"},
		"extra file":  {"agentflow-plugin.yml": ciNotify, "lib/data.txt": "one", "x": ""},
		"manifest":    {"agentflow-plugin.yml": ciNotify + "\n", "lib/data.txt": "one"},
		"executable":  {"agentflow-plugin.yml": ciNotify, "lib/data.txt*": "one"},
		"moved level": {"agentflow-plugin.yml": ciNotify, "data.txt": "one"},
	} {
		if runtime.GOOS == "windows" && name == "executable" {
			continue
		}
		if hash(files) == want {
			t.Errorf("%s: the hash did not change", name)
		}
	}
}

func TestReadTreeRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links and modes differ on Windows")
	}
	outside := tree(t, map[string]string{"secret": "s"})

	t.Run("file link", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		must(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")))
		wantTreeError(t, dir, "link", "symbolic link")
	})
	t.Run("link inside the tree", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		must(t, os.Symlink("agentflow-plugin.yml", filepath.Join(dir, "alias")))
		wantTreeError(t, dir, "alias", "symbolic link")
	})
	t.Run("directory link", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		must(t, os.Symlink(outside, filepath.Join(dir, "lib")))
		wantTreeError(t, dir, "lib", "symbolic link")
	})
	t.Run("broken link", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		must(t, os.Symlink("missing", filepath.Join(dir, "gone")))
		wantTreeError(t, dir, "gone", "symbolic link")
	})
	t.Run("root link", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		root := filepath.Join(t.TempDir(), "plugin")
		must(t, os.Symlink(dir, root))
		wantTreeError(t, root, "", "symbolic link")
	})
	t.Run("not a directory", func(t *testing.T) {
		dir := tree(t, map[string]string{"one.yml": ciNotify})
		wantTreeError(t, filepath.Join(dir, "one.yml"), "", "not a directory")
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := manifest.ReadTree(filepath.Join(t.TempDir(), "none")); err == nil {
			t.Error("a missing directory was read")
		}
	})
	t.Run("named pipe", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		if err := mkfifo(filepath.Join(dir, "pipe")); err != nil {
			t.Skip("no named pipes here")
		}
		wantTreeError(t, dir, "pipe", "not a regular file")
	})
	t.Run("git directory", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify, ".git/HEAD": "ref"})
		wantTreeError(t, dir, ".git", "Git directory")
	})
	t.Run("control character in a name", func(t *testing.T) {
		dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify})
		if err := os.WriteFile(filepath.Join(dir, "a\nb"), nil, 0o644); err != nil {
			t.Skip("the file system refuses the name")
		}
		wantTreeError(t, dir, "", "not plain text")
	})
	t.Run("too many files", func(t *testing.T) {
		dir := t.TempDir()
		for i := range manifest.MaxFiles + 1 {
			must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d", i)), nil, 0o644))
		}
		wantTreeError(t, dir, "", "more than 256 files")
	})
	t.Run("exactly the maximum number of files", func(t *testing.T) {
		dir := t.TempDir()
		for i := range manifest.MaxFiles {
			must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d", i)), nil, 0o644))
		}
		files, err := manifest.ReadTree(dir)
		if err != nil || len(files) != manifest.MaxFiles {
			t.Errorf("ReadTree = %d files, %v", len(files), err)
		}
	})
	t.Run("too many bytes", func(t *testing.T) {
		dir := t.TempDir()
		big := make([]byte, manifest.MaxTreeBytes/2+1)
		must(t, os.WriteFile(filepath.Join(dir, "a"), big, 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "b"), big, 0o644))
		wantTreeError(t, dir, "", "16 MiB")
	})
	t.Run("nested too deep", func(t *testing.T) {
		dir := t.TempDir()
		deep := filepath.Join(append([]string{dir}, strings.Split(strings.Repeat("d/", 20), "/")...)...)
		must(t, os.MkdirAll(deep, 0o755))
		must(t, os.WriteFile(filepath.Join(deep, "f"), nil, 0o644))
		wantTreeError(t, dir, "", "nested")
	})
}

func TestReadTreeReadsOnlyInsideTheDirectory(t *testing.T) {
	dir := tree(t, map[string]string{"agentflow-plugin.yml": ciNotify, "sub/a": "a", "sub/deeper/b": "b"})
	files, err := manifest.ReadTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	if want := "agentflow-plugin.yml sub/a sub/deeper/b"; strings.Join(got, " ") != want {
		t.Errorf("files = %v, want %s", got, want)
	}
}

func TestReadTreeRecordsExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no executable bit on Windows")
	}
	dir := tree(t, map[string]string{"run*": "#!/bin/sh\n", "data": "d"})
	files, err := manifest.ReadTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !files[1].Exec || files[0].Exec || files[0].Path != "data" || files[1].Path != "run" {
		t.Errorf("files = %+v", files)
	}
}

func wantTreeError(t *testing.T, dir, path, reason string) {
	t.Helper()
	_, err := manifest.ReadTree(dir)
	var te *manifest.TreeError
	if !errors.As(err, &te) {
		t.Fatalf("ReadTree error = %v, want a *TreeError", err)
	}
	if path != "" && te.Path != path {
		t.Errorf("path = %q, want %q", te.Path, path)
	}
	if !strings.Contains(te.Error(), reason) {
		t.Errorf("error %q does not contain %q", te.Error(), reason)
	}
	if _, err := manifest.HashDir(dir); err == nil {
		t.Error("HashDir hashed a tree that ReadTree refuses")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
