// Package version holds build metadata injected by GoReleaser via -ldflags.
package version

// Set at build time:
//
//	-X github.com/ryanhelms/agentflow/internal/version.Version={{.Version}}
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a one-line description of the build.
func String() string {
	return "agentflow " + Version + " (" + Short() + ", " + Date + ")"
}

// Short returns the commit abbreviated to 7 characters.
func Short() string {
	if len(Commit) > 7 {
		return Commit[:7]
	}
	return Commit
}

// Release reports whether this is a release build with a full commit SHA, which
// `agentflow init` pins caller workflows to.
func Release() bool { return ReleaseCommit(Commit, Version) }

// ReleaseCommit reports whether sha is a full commit SHA and ver a released version.
func ReleaseCommit(sha, ver string) bool {
	if ver == "" || ver == "dev" || len(sha) != 40 {
		return false
	}
	for _, c := range sha {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
