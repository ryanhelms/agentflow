// Package version holds build metadata injected by GoReleaser via -ldflags.
package version

// Set at build time:
//
//	-X github.com/nscaledev/devflow/internal/version.Version={{.Version}}
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a one-line description of the build.
func String() string {
	return "devflow " + Version + " (" + Commit + ", " + Date + ")"
}
