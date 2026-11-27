package update

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/ryanhelms/agentflow/internal/config"
)

// versionFolder matches the version folder names the POSIX launchers accept for
// `current` (the glob [0-9]*.[0-9]*.[0-9]*, optionally after a v, of version
// characters), so ActiveBinary and af_installed agree.
var versionFolder = regexp.MustCompile(`^v?[0-9][0-9A-Za-z.+-]*\.[0-9][0-9A-Za-z.+-]*\.[0-9][0-9A-Za-z.+-]*$`)

// ActiveBinary returns the agentflow binary that the POSIX launchers (af_installed)
// run from the cache root, as CacheRoot returns it, or "" when there is none or
// they would refuse it; why says why it is refused. A version folder directly in
// the root, without a link on the way, is used when the root is the account's own
// cache or outside every repository, even when the account's home is inside
// another repository. Any other `current` is used only outside every repository.
// The Windows launcher uses current.txt instead; this is the POSIX layout.
func ActiveBinary(root string) (bin, why string) {
	cur := filepath.Join(root, "current")
	if !executableFile(filepath.Join(cur, "agentflow")) {
		return "", ""
	}
	rootP, errR := filepath.EvalSymlinks(root)
	v, errV := filepath.EvalSymlinks(cur)
	if errR == nil && errV == nil && filepath.Dir(v) == rootP && versionFolder.MatchString(filepath.Base(v)) {
		if fi, err := os.Lstat(filepath.Join(v, "agentflow")); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			if isOwnCache(rootP) {
				return filepath.Join(v, "agentflow"), ""
			}
			if inside, _ := config.InsideRepository(rootP); !inside {
				return filepath.Join(v, "agentflow"), ""
			}
		}
	}
	if inside, reason := config.InsideRepository(cur); inside {
		return "", reason
	}
	if errV != nil {
		return "", "cannot be checked (its path does not resolve)"
	}
	return filepath.Join(v, "agentflow"), ""
}

func isOwnCache(physical string) bool {
	home, err := config.AccountHome()
	if err != nil {
		return false
	}
	own, err := filepath.EvalSymlinks(filepath.Join(home, ".cache", "agentflow"))
	return err == nil && own == physical
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
