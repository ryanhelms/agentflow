package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/ryanhelms/agentflow/internal/version"
	"github.com/ryanhelms/agentflow/templates"
)

// LauncherFiles are the repository files `agentflow pin` refreshes along with the caller
// workflow: the launchers and the git hooks that start agentflow.
var LauncherFiles = []string{
	".agentflow/bin/agentflow",
	".agentflow/bin/agentflow.ps1",
	".githooks/commit-msg",
	".githooks/pre-push",
}

// stockLaunchers lists the SHA-256 of every version of each launcher file that agentflow
// shipped from v0.53.0, the first agentflow-named release, and of the current template.
// `agentflow pin` replaces a file only when its bytes match one of these.
//
// When you change one of these templates, add the new hash and keep the old ones, and copy
// the released file to testdata/stock/<first release>/<path>; the tests check both.
// Devflow-era hooks (before v0.53.0) are left out on purpose: replacing one would make an
// unmigrated devflow repository call agentflow (see docs/agentflow-migration.md).
var stockLaunchers = map[string][]string{
	".agentflow/bin/agentflow": {
		"b58279dfd914ae7d1dfc132c29111d6d9f39fbf6c1aa508a557a7bb8139d1d3e", // v0.53.0
		"928b00b9d2d9f7e67a56dce9d19e0db51fdb345356b3d1166986fc0d2375b4aa", // v0.54.0 - v0.60.0
		"e1bebd98d3e1c07b28204582ebd0ef1565bab64613c202b15b8e652e3e1ec74e", // repository root checks (#129)
	},
	".agentflow/bin/agentflow.ps1": {
		"c4b59ba292ee93cec33e68c6faed6d6c5ce54280623551cee387fa4e8d53a82b", // v0.53.0
		"c58858ab02256499800722c6d3502cc59d568f1e1ba719673cbdb04effdaf041", // v0.54.0 - v0.60.0
		"45d8983c2365f49b54346d1b561329b2973f2553105b20560f3a7ef1a0dc6fef", // repository root checks (#129)
	},
	".githooks/commit-msg": {
		"7e8c1307c1b51da8e82582779a12ac78da429b843f3cb13735debc76994b2116", // v0.53.0 - v0.60.0
		"cc223b0e4a6b9717c9c9465fea343a12ffbe876013c18de14fb69767b797840a", // repository root checks (#129)
	},
	".githooks/pre-push": {
		"3136cbec7ea3ea52b80ee8a95db7e7e7ac5d414dde12eda47a4a2364447cda5a", // v0.55.0 - v0.55.1
		"6c577768a02b720a70c7542c9388a45957e97e193158f0e7d49faa242d9284b5", // v0.56.0 - v0.60.0
		"1c2ec0804b6ec89777a13e01f7212690e48b505ab430a5a120a47072e33c298c", // repository root checks (#129)
	},
}

// Launcher file states.
const (
	LauncherCurrent  = "current"    // equals this release's template
	LauncherOutdated = "outdated"   // a stock file from an older release; pin replaces it
	LauncherUpdated  = "updated"    // pin replaced it with this release's template
	LauncherCustom   = "customised" // differs from every stock version; left alone
	LauncherNotFile  = "not_file"   // a link or other non-regular file; left alone
	LauncherUnsafe   = "unsafe"     // its folder is a link or outside the repository; left alone
)

// LauncherFile is what pin found for one launcher file. Missing files are not listed.
type LauncherFile struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"` // for the states left alone: what to do by hand
}

// Stock reports whether the file is a stock version, old or current.
func (f LauncherFile) Stock() bool {
	return f.State == LauncherCurrent || f.State == LauncherOutdated || f.State == LauncherUpdated
}

// RefreshLaunchers replaces each launcher file in root that is a stock version from an
// older release with this binary's template. It never creates a missing file, never
// writes through a link and leaves every other file alone, reporting why. With check it
// only reports. Callers must only call it from a release build (Pin does).
func RefreshLaunchers(root *os.Root, check bool) ([]LauncherFile, error) {
	var out []LauncherFile
	for _, rel := range LauncherFiles {
		f, data, err := classifyLauncher(root, rel)
		if err != nil {
			return out, err
		}
		if f == nil {
			continue
		}
		if f.State == LauncherOutdated && !check {
			if err := replaceLauncher(root, rel, data); err != nil {
				return out, fmt.Errorf("%s: %w", rel, err)
			}
			f.State = LauncherUpdated
		}
		out = append(out, *f)
	}
	return out, nil
}

// classifyLauncher returns nil for a missing file, and the template bytes for an
// outdated one.
func classifyLauncher(root *os.Root, rel string) (*LauncherFile, []byte, error) {
	tmpl, err := templates.Repo.ReadFile(path.Join("repo", rel))
	if err != nil {
		return nil, nil, err
	}
	f := &LauncherFile{Path: rel}
	byHand := fmt.Sprintf("to replace it, review it and copy %s by hand", templateURL(rel))
	// Every parent must be a real folder: os.Root follows links that stay inside it.
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		info, err := root.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil, nil
		case err != nil && !errors.Is(err, fs.ErrPermission):
			f.State, f.Detail = LauncherUnsafe, fmt.Sprintf("left it unchanged: %s is outside the repository or unreadable (%v); %s", dir, err, byHand)
			return f, nil, nil
		case err != nil:
			return nil, nil, err
		case !info.IsDir():
			f.State, f.Detail = LauncherUnsafe, fmt.Sprintf("left it unchanged: %s is not a folder; %s", dir, byHand)
			return f, nil, nil
		}
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.State, f.Detail = LauncherNotFile, "left it unchanged: it is a link or not a regular file; "+byHand
		return f, nil, nil
	}
	data, err := root.ReadFile(rel)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case bytes.Equal(data, tmpl):
		f.State = LauncherCurrent
	case isStockLauncher(rel, data):
		f.State = LauncherOutdated
		return f, tmpl, nil
	default:
		f.State, f.Detail = LauncherCustom, "left it unchanged: it differs from every file agentflow shipped; "+byHand
	}
	return f, nil, nil
}

func isStockLauncher(rel string, data []byte) bool {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	for _, s := range stockLaunchers[rel] {
		if s == h {
			return true
		}
	}
	return false
}

// replaceLauncher swaps the file for tmpl, checking again just before the rename that it
// is still a regular stock file.
func replaceLauncher(root *os.Root, rel string, tmpl []byte) error {
	err := publishAfter(root, rel, tmpl, func() error {
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("changed to a non-regular file during pin")
		}
		data, err := root.ReadFile(rel)
		if err != nil {
			return err
		}
		if !isStockLauncher(rel, data) {
			return fmt.Errorf("changed during pin")
		}
		return nil
	})
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if executables[rel] {
		mode = 0o755
	}
	return root.Chmod(rel, mode)
}

// templateURL is where this release's copy of a template file can be read.
func templateURL(rel string) string {
	return "https://github.com/ryanhelms/agentflow/blob/v" + strings.TrimPrefix(version.Version, "v") + "/templates/repo/" + rel
}
