// Package local provisions raw executables in a caller-owned tool cache.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/nscaledev/agentflow/internal/provision"
)

var (
	// ErrUnsupported identifies platforms where this cache implementation is unavailable.
	ErrUnsupported = errors.New("local companion cache supports native macOS, Linux and Windows only")
	// ErrDigestMismatch identifies source bytes that differ from the declared SHA256.
	ErrDigestMismatch = errors.New("companion artifact sha256 mismatch")

	identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	digest     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// OpenArtifact opens a trusted source for the exact executable bytes declared
// by an artifact. The caller supplies source approval and any transport policy.
// The returned stream should honor cancellation of ctx.
type OpenArtifact func(ctx context.Context, artifact provision.Artifact) (io.ReadCloser, error)

// Cache implements provision.Installer and provision.Prober for one environment.
// Only cooperating installers may write its layout. Keep the cache directory
// and its ancestors stable while using the cache or any paths it returns.
// Use a distinct environment ID for each installation scope.
type Cache struct {
	path string
	env  provision.Environment
	open OpenArtifact
}

var (
	_ provision.Installer = (*Cache)(nil)
	_ provision.Prober    = (*Cache)(nil)
)

// New opens an existing cache directory owned by the effective user and not
// writable by group or others. Use a dedicated directory on a local filesystem
// that supports native file locks and atomic rename. The installer accepts a raw
// executable (including a script), with no archive extraction or format parsing.
// Installations are isolated by environment ID, platform and companion ID.
func New(cacheDir string, env provision.Environment, open OpenArtifact) (*Cache, error) {
	native := provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if (native.OS != "darwin" && native.OS != "linux" && native.OS != "windows") || env.Platform != native {
		return nil, ErrUnsupported
	}
	if cacheDir == "" || !identifier.MatchString(env.ID) || open == nil {
		return nil, errors.New("local cache requires a directory, valid environment id and trusted artifact opener")
	}
	path, err := filepath.Abs(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("resolve cache directory: %w", err)
	}
	root, err := openCache(path)
	if err != nil {
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, fmt.Errorf("close cache directory: %w", err)
	}
	return &Cache{path: path, env: env, open: open}, nil
}

// Receipt identifies the selected, verified executable. Later selections keep
// this artifact at the same path. Call Resolve again to recheck cache integrity
// before handing off a path if it may have been modified since verification.
type Receipt struct {
	Path    string
	Version provision.Version
	SHA256  string
}

// Resolve returns the managed executable path and identity, or fs.ErrNotExist
// if there is no usable selection. Launch this path with the target's arguments.
func (c *Cache) Resolve(ctx context.Context, env provision.Environment, target provision.Target) (Receipt, error) {
	if err := c.validateTarget(ctx, env, target); err != nil {
		return Receipt{}, err
	}
	dir, err := c.targetDir(target, false)
	if err != nil {
		return Receipt{}, err
	}
	if dir == nil {
		return Receipt{}, errNoSelection
	}
	defer dir.Close()
	current, err := readSelection(ctx, dir)
	if err != nil {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if !current.installation().Present {
		return Receipt{}, errNoSelection
	}
	return Receipt{
		Path: filepath.Join(dir.Name(), current.ref.name()), Version: current.ref.version, SHA256: current.ref.sha256,
	}, nil
}

var errNoSelection = fmt.Errorf("no verified local selection: %w", fs.ErrNotExist)

// Observe reports a selection as present and healthy when its bytes and file
// mode verify. A missing or damaged executable is absent and can be reconciled.
// This probe checks cache integrity only. Callers can compose it with runtime
// health and credential checks using the path from Resolve.
func (c *Cache) Observe(ctx context.Context, env provision.Environment, target provision.Target) (provision.Observation, error) {
	receipt, err := c.Resolve(ctx, env, target)
	if errors.Is(err, errNoSelection) {
		return provision.Observation{}, nil
	}
	if err != nil {
		return provision.Observation{}, err
	}
	return provision.Observation{
		Installation: provision.Installation{
			Present: true, VersionKnown: true, Version: receipt.Version, SHA256: receipt.SHA256,
		},
		Healthy: true, AuthReady: target.AuthRef == "",
	}, nil
}

// Install verifies the requested artifact and atomically selects it if the
// observed installation still equals expected. A verified cached copy can be
// reused without invoking the source opener.
func (c *Cache) Install(ctx context.Context, env provision.Environment, target provision.Target, artifact provision.Artifact, expected provision.Installation) error {
	if err := c.validateTarget(ctx, env, target); err != nil {
		return err
	}
	if err := validateArtifact(env, artifact); err != nil {
		return err
	}
	dir, err := c.targetDir(target, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	lock, err := lockDirectory(ctx, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	return c.install(ctx, dir, artifact, expected)
}

func (c *Cache) validateTarget(ctx context.Context, env provision.Environment, target provision.Target) error {
	if c.path == "" || env != c.env {
		return errors.New("local cache requires its configured environment")
	}
	if (target.Kind != provision.CLI && target.Kind != provision.StdioMCP) ||
		!identifier.MatchString(target.ID) || strings.TrimSpace(target.Command) == "" || target.Endpoint != "" {
		return errors.New("local cache requires a valid cli or stdio-mcp target")
	}
	return ctx.Err()
}

func validateArtifact(env provision.Environment, artifact provision.Artifact) error {
	if artifact.Platform != env.Platform {
		return errors.New("artifact platform differs from the cache environment")
	}
	u, err := url.Parse(artifact.Source)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(artifact.Source, "#") {
		return errInvalidSource
	}
	https := u.Scheme == "https" && u.Hostname() != ""
	localFile := u.Scheme == "file" && u.Host == "" && strings.HasPrefix(u.Path, "/")
	if !https && !localFile {
		return errInvalidSource
	}
	if !digest.MatchString(artifact.SHA256) {
		return errors.New("artifact requires a lowercase sha256 digest")
	}
	return nil
}

var errInvalidSource = errors.New("artifact requires an https or absolute local file source without credentials, query or fragment")
