package setup

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/version"
)

// CallerWorkflow is the workflow `devflow init` writes; its nscaledev/devflow references
// are pinned to a release commit.
const CallerWorkflow = ".github/workflows/devflow.yml"

var (
	// a floating @v0 reference, as in the template
	devflowRefRe = regexp.MustCompile(`(uses: nscaledev/devflow/[^@\s]+)@v0\b`)
	// any reference, with the `# vX.Y.Z` comment a pin carries
	devflowAnyRefRe = regexp.MustCompile(`(uses: nscaledev/devflow/[^@\s]+)@[^\s#]+(?:[ \t]+#[ \t]*v[0-9][^\s]*)?`)
	pinCommentRe    = regexp.MustCompile(`uses: nscaledev/devflow/[^@\s]+@[0-9a-f]{40}[ \t]+#[ \t]*(v[0-9][^\s]*)`)
)

// PinWorkflow pins every floating @v0 nscaledev/devflow reference in a caller workflow to
// the commit of this release (`@<sha> # v<version>`), so a moved tag can't change what
// runs with the repo's secrets. Non-release builds leave the @v0 references.
func PinWorkflow(data []byte, sha, ver string) []byte {
	if !version.ReleaseCommit(sha, ver) {
		return data
	}
	return devflowRefRe.ReplaceAll(data, []byte("${1}@"+sha+" # v"+strings.TrimPrefix(ver, "v")))
}

// RepinWorkflow moves every nscaledev/devflow reference, pinned or floating, to the
// commit of this release. `devflow pin` uses it to update a repo to the installed
// devflow; the developer reviews the change in a PR.
func RepinWorkflow(data []byte, sha, ver string) []byte {
	if !version.ReleaseCommit(sha, ver) {
		return data
	}
	return devflowAnyRefRe.ReplaceAll(data, []byte("${1}@"+sha+" # v"+strings.TrimPrefix(ver, "v")))
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
	File    string `json:"file"`
	From    string `json:"from,omitempty"` // oldest pinned release before ("" = floating)
	To      string `json:"to"`
	Changed bool   `json:"changed"`
}

// ErrNotRelease means the running devflow is not a release build and has no commit to
// pin to.
var ErrNotRelease = errors.New("this devflow is not a release build; install a release (devflow update) to pin workflows")

// Pin re-pins the caller workflow in repoDir to the running devflow release. With check
// it only reports.
func Pin(repoDir string, check bool) (*PinResult, error) {
	if !version.Release() {
		return nil, ErrNotRelease
	}
	dest := filepath.Join(repoDir, filepath.FromSlash(CallerWorkflow))
	data, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	res := &PinResult{File: CallerWorkflow, From: PinnedVersion(data), To: "v" + strings.TrimPrefix(version.Version, "v")}
	out := RepinWorkflow(data, version.Commit, version.Version)
	res.Changed = string(out) != string(data)
	if !res.Changed || check {
		return res, nil
	}
	return res, os.WriteFile(dest, out, 0o644)
}

// PinBehind reports the release the repo's caller workflow is pinned to when it is older
// than the running devflow release, and "" otherwise.
func PinBehind(repoDir string) string {
	if !version.Release() {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(CallerWorkflow)))
	if err != nil {
		return ""
	}
	if from := PinnedVersion(data); from != "" && VersionLess(from, "v"+strings.TrimPrefix(version.Version, "v")) {
		return from
	}
	return ""
}
