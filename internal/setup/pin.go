package setup

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/version"
)

// CallerWorkflow is the workflow `agentflow init` writes; its ryanhelms/agentflow references
// are pinned to a release commit.
const (
	CallerWorkflow       = ".github/workflows/agentflow.yml"
	legacyCallerWorkflow = ".github/workflows/devflow.yml"
)

var (
	// a floating @v0 reference, as in the template
	agentflowRefRe = regexp.MustCompile(`(uses: ryanhelms/agentflow/[^@\s]+)@v0\b`)
	// any reference, with the `# vX.Y.Z` comment a pin carries
	agentflowAnyRefRe = regexp.MustCompile(`uses: (?:nscaledev|ryanhelms)/(?:agentflow|devflow)/([^@\s]+)@[^\s#]+(?:[ \t]+#[ \t]*v[0-9][^\s]*)?`)
	pinCommentRe      = regexp.MustCompile(`uses: (?:nscaledev|ryanhelms)/(?:agentflow|devflow)/[^@\s]+@[0-9a-f]{40}[ \t]+#[ \t]*(v[0-9][^\s]*)`)
)

// PinWorkflow pins every floating @v0 ryanhelms/agentflow reference in a caller workflow to
// the commit of this release (`@<sha> # v<version>`), so a moved tag can't change what
// runs with the repo's secrets. Non-release builds leave the @v0 references.
func PinWorkflow(data []byte, sha, ver string) []byte {
	if !version.ReleaseCommit(sha, ver) {
		return data
	}
	return agentflowRefRe.ReplaceAll(data, []byte("${1}@"+sha+" # v"+strings.TrimPrefix(ver, "v")))
}

// RepinWorkflow moves every ryanhelms/agentflow reference, pinned or floating, to the
// commit of this release. `agentflow pin` uses it to update a repo to the installed
// agentflow; the developer reviews the change in a PR.
func RepinWorkflow(data []byte, sha, ver string) []byte {
	if !version.ReleaseCommit(sha, ver) {
		return data
	}
	return agentflowAnyRefRe.ReplaceAll(data, []byte("uses: ryanhelms/agentflow/${1}@"+sha+" # v"+strings.TrimPrefix(ver, "v")))
}

// PinnedVersion returns the oldest release the caller workflow is pinned to ("" when it
// has no pinned references).
func PinnedVersion(data []byte) string {
	oldest := ""
	for _, m := range pinCommentRe.FindAllSubmatch(data, -1) {
		v := string(m[1])
		if oldest == "" || VersionLess(v, oldest) {
			oldest = v
		}
	}
	return oldest
}

// VersionLess compares two vX.Y.Z versions numerically.
func VersionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var out [3]int
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// PinResult is what Pin found and changed.
type PinResult struct {
	File    string `json:"file,omitempty"` // "" when the repository has no caller workflow
	From    string `json:"from,omitempty"` // oldest pinned release before ("" = floating)
	To      string `json:"to"`
	Changed bool   `json:"changed"`
	// Launchers lists the launcher files found (missing ones are left out).
	Launchers []LauncherFile `json:"launchers,omitempty"`
}

// Behind reports whether the workflow or a stock launcher file is older than this release.
func (r *PinResult) Behind() bool {
	if r.Changed {
		return true
	}
	for _, f := range r.Launchers {
		if f.State == LauncherOutdated || f.State == LauncherUpdated {
			return true
		}
	}
	return false
}

// ErrNotRelease means the running agentflow is not a release build and has no commit to
// pin to.
var ErrNotRelease = errors.New("this agentflow is not a release build; install a release (agentflow update) to pin workflows")

// Pin re-pins the caller workflow in repoDir to the running agentflow release and
// replaces stock launcher files from older releases (RefreshLaunchers). With check it
// only reports. A missing caller workflow is an error unless a launcher file is stock.
func Pin(repoDir string, check bool) (*PinResult, error) {
	if !version.Release() {
		return nil, ErrNotRelease
	}
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	res := &PinResult{To: "v" + strings.TrimPrefix(version.Version, "v")}
	if res.Launchers, err = RefreshLaunchers(root, true); err != nil {
		return nil, err
	}
	file, data, err := readCallerWorkflow(repoDir)
	switch {
	case errors.Is(err, os.ErrNotExist) && slices.ContainsFunc(res.Launchers, LauncherFile.Stock):
	case err != nil:
		return nil, err
	default:
		res.File, res.From = file, PinnedVersion(data)
	}
	var out []byte
	if res.File != "" {
		out = RepinWorkflow(data, version.Commit, version.Version)
		res.Changed = string(out) != string(data)
	}
	if check {
		return res, nil
	}
	if res.Changed {
		if err := os.WriteFile(filepath.Join(repoDir, filepath.FromSlash(file)), out, 0o644); err != nil {
			return res, err
		}
	}
	res.Launchers, err = RefreshLaunchers(root, false)
	return res, err
}

// PinBehind reports the release the repo's caller workflow is pinned to when it is older
// than the running agentflow release, and "" otherwise.
func PinBehind(repoDir string) string {
	if !version.Release() {
		return ""
	}
	_, data, err := readCallerWorkflow(repoDir)
	if err != nil {
		return ""
	}
	if from := PinnedVersion(data); from != "" && VersionLess(from, "v"+strings.TrimPrefix(version.Version, "v")) {
		return from
	}
	return ""
}

func readCallerWorkflow(repoDir string) (string, []byte, error) {
	file := CallerWorkflow
	data, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(file)))
	if errors.Is(err, os.ErrNotExist) {
		legacy, legacyErr := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(legacyCallerWorkflow)))
		if !errors.Is(legacyErr, os.ErrNotExist) {
			return legacyCallerWorkflow, legacy, legacyErr
		}
	}
	return file, data, err
}
