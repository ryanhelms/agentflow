package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ErrTestGuard is returned when a test binary would find or start a file
// outside the test's temporary directory ("Phase 6 test guard"). Detection
// then fails, naming the entry, so a test that forgets harnesstest.PATH
// cannot reach a real harness.
var ErrTestGuard = errors.New("harness: test guard: a test may only find or start harnesses inside its temporary directory; set PATH with harnesstest.PATH")

// testGuard refuses path in a test binary unless it resolves inside
// os.TempDir() (resolved). The finder calls it for every PATH entry,
// override path and found file; the probe and the sign-in check call it
// again right before jsonrpc.Launch and jsonrpc.Run. Outside a test binary
// it allows everything.
func testGuard(path string) error {
	return guardPath(path, testing.Testing(), os.TempDir())
}

// testGuardList applies testGuard to every entry of a PATH-style list.
func testGuardList(list string) error {
	return guardList(list, testing.Testing(), os.TempDir())
}

func guardList(list string, inTest bool, tmp string) error {
	if !inTest {
		return nil
	}
	for _, dir := range filepath.SplitList(list) {
		if err := guardPath(dir, inTest, tmp); err != nil {
			return err
		}
	}
	return nil
}

// guardPath is testGuard with its inputs explicit. A relative, empty or
// unresolvable path is refused unless its cleaned form is already inside
// tmp; both sides are compared with every symbolic link resolved (on macOS
// /var is /private/var).
func guardPath(path string, inTest bool, tmp string) error {
	if !inTest {
		return nil
	}
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %q is not an absolute path", ErrTestGuard, path)
	}
	bases := []string{filepath.Clean(tmp)}
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		bases = append(bases, real)
	}
	candidate := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		candidate = real
	} else if parent, perr := filepath.EvalSymlinks(filepath.Dir(candidate)); perr == nil {
		// A file that does not exist yet: resolve its directory.
		candidate = filepath.Join(parent, filepath.Base(candidate))
	}
	for _, base := range bases {
		if inside(candidate, base) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrTestGuard, path)
}

// inside reports whether path is base or below it. Both must be clean.
func inside(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
