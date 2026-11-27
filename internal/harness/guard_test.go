package harness

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGuardRefusesRealPATHEntries(t *testing.T) {
	// TestGuardRefusesRealPATH (ADR "Phase 6 test guard") runs detection
	// with this PATH; it lands with detect.go. This covers the guard itself.
	list := strings.Join([]string{"/usr/bin", "/bin"}, string(filepath.ListSeparator))
	if runtime.GOOS == "windows" {
		list = `C:\Windows\System32;C:\Windows`
	}
	err := testGuardList(list)
	if !errors.Is(err, ErrTestGuard) {
		t.Fatalf("err = %v, want ErrTestGuard", err)
	}
	first := filepath.SplitList(list)[0]
	if !strings.Contains(err.Error(), first) {
		t.Fatalf("err = %v, want it to name %s", err, first)
	}
}

func TestGuardAllowsTheTestTempDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, bin, filepath.Join(dir, "not-yet")} {
		if err := testGuard(p); err != nil {
			t.Errorf("testGuard(%s) = %v", p, err)
		}
	}
	if err := testGuardList(dir + string(filepath.ListSeparator) + t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestGuardRefuses(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir() // a stand-in for the real system, outside tmp
	real := filepath.Join(outside, "claude")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":           "",
		"relative":        "bin",
		"dot":             ".",
		"outside":         outside,
		"outside file":    real,
		"dot-dot escape":  filepath.Join(tmp, "..", filepath.Base(outside), "claude"),
		"missing outside": filepath.Join(outside, "nope", "agent"),
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(tmp, "claude")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		cases["link out of tmp"] = link
		dirLink := filepath.Join(tmp, "bin")
		if err := os.Symlink(outside, dirLink); err != nil {
			t.Fatal(err)
		}
		cases["dir link out of tmp"] = dirLink
		cases["file under dir link"] = filepath.Join(dirLink, "claude")
	}
	for name, p := range cases {
		if err := guardPath(p, true, tmp); !errors.Is(err, ErrTestGuard) {
			t.Errorf("%s: guardPath(%q) = %v, want ErrTestGuard", name, p, err)
		}
	}
	if err := guardList(tmp+string(filepath.ListSeparator)+outside, true, tmp); !errors.Is(err, ErrTestGuard) {
		t.Errorf("list with an outside entry = %v", err)
	}
	if err := guardList(string(filepath.ListSeparator)+tmp, true, tmp); !errors.Is(err, ErrTestGuard) {
		t.Errorf("list with an empty entry = %v", err)
	}
}

func TestGuardResolvesTheTempDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	realTmp := t.TempDir()
	linkTmp := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(realTmp, linkTmp); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(realTmp, "agent")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// TMPDIR names the link; the found file is under its target.
	if err := guardPath(bin, true, linkTmp); err != nil {
		t.Fatal(err)
	}
	if err := guardPath(filepath.Join(linkTmp, "agent"), true, realTmp); err != nil {
		t.Fatal(err)
	}
}

func TestGuardIsOffOutsideTests(t *testing.T) {
	if err := guardPath("/usr/bin/claude", false, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := guardList("/usr/bin:/bin", false, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
