package provision

import (
	"cmp"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Platform uses GOOS and GOARCH names.
type Platform struct {
	OS, Arch string
}

// Environment identifies one installation and probe scope.
type Environment struct {
	ID       string
	Platform Platform
}

type Kind string

const (
	CLI       Kind = "cli"
	StdioMCP  Kind = "stdio-mcp"
	RemoteMCP Kind = "remote-mcp"
)

// Version represents a stable major.minor.patch release.
type Version struct {
	Major, Minor, Patch uint
}

func compareVersion(a, b Version) int {
	return cmp.Or(cmp.Compare(a.Major, b.Major), cmp.Compare(a.Minor, b.Minor), cmp.Compare(a.Patch, b.Patch))
}

// VersionRange is inclusive at Min and exclusive at Before. A nil Before is unbounded.
type VersionRange struct {
	Min    Version
	Before *Version
}

func (r VersionRange) contains(v Version) bool {
	return compareVersion(v, r.Min) >= 0 && (r.Before == nil || compareVersion(v, *r.Before) < 0)
}

func (r VersionRange) intersect(other VersionRange) VersionRange {
	if compareVersion(r.Min, other.Min) < 0 {
		r.Min = other.Min
	}
	if other.Before != nil && (r.Before == nil || compareVersion(*r.Before, *other.Before) > 0) {
		r.Before = other.Before
	}
	return r
}

// Artifact describes a declared binary. SHA256 is the lowercase hex digest of
// the bytes the installer will publish.
type Artifact struct {
	Platform Platform
	Version  Version
	Source   string
	SHA256   string
}

// HealthProbe identifies an adapter's read-only check and its optional timeout.
type HealthProbe struct {
	Name    string
	Timeout time.Duration
}

// Target describes how to find or launch the companion. ID is stable across
// providers and versions. AuthRef is an opaque reference to credentials.
type Target struct {
	ID       string
	Kind     Kind
	Command  string
	Args     []string
	Endpoint string
	AuthRef  string
	Health   HealthProbe
}

// Companion declares one provider's requirements. An empty Platforms slice
// allows any platform. PinArtifact requires a single exact artifact for the
// current platform.
type Companion struct {
	Target      Target
	Platforms   []Platform
	Versions    VersionRange
	Artifacts   []Artifact
	PinArtifact bool
}

// Manifest describes the companions required by a configured provider.
type Manifest struct {
	ProviderID string
	Companions []Companion
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func (p Platform) valid() bool {
	return identifier.MatchString(p.OS) && identifier.MatchString(p.Arch)
}

func validURL(raw string, allowFile bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Hostname() != ""
	case "file":
		return allowFile && u.Host == "" && strings.HasPrefix(u.Path, "/")
	default:
		return false
	}
}

func (c Companion) validate() error {
	target := c.Target
	if !identifier.MatchString(target.ID) || !identifier.MatchString(target.Health.Name) || target.Health.Timeout < 0 {
		return errors.New("invalid companion id or health probe")
	}
	switch target.Kind {
	case CLI, StdioMCP:
		if strings.TrimSpace(target.Command) == "" || target.Endpoint != "" {
			return errors.New("a local companion requires a command and cannot declare an endpoint")
		}
	case RemoteMCP:
		if !validURL(target.Endpoint, false) || target.Command != "" || len(target.Args) != 0 {
			return errors.New("a remote companion requires an https endpoint without credentials, query or fragment")
		}
		if len(c.Artifacts) != 0 || c.PinArtifact {
			return errors.New("a remote companion cannot declare installation artifacts")
		}
	default:
		return fmt.Errorf("unknown companion kind %q", target.Kind)
	}
	if !c.Versions.contains(c.Versions.Min) {
		return errors.New("empty version range")
	}
	for _, platform := range c.Platforms {
		if !platform.valid() {
			return errors.New("invalid supported platform")
		}
	}
	for _, artifact := range c.Artifacts {
		if !artifact.Platform.valid() || (len(c.Platforms) > 0 && !slices.Contains(c.Platforms, artifact.Platform)) {
			return errors.New("artifact platform is unsupported by its companion")
		}
		digest, err := hex.DecodeString(artifact.SHA256)
		if !validURL(artifact.Source, true) || err != nil || len(digest) != 32 || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
			return errors.New("artifact requires an https or local file source and a lowercase sha256 digest")
		}
	}
	return nil
}
