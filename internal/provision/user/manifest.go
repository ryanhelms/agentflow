// Package user supplies trusted user manifests and pinned companion artifacts.
package user

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/provision"
	"github.com/nscaledev/agentflow/internal/provision/local"
)

// Compare directory identities as well as following aliases. On case-insensitive
// filesystems a different spelling of the checkout still denotes the same tree.
func ManifestHome(repo string) (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) || strings.Contains("/"+filepath.ToSlash(home)+"/", "/../") {
		return "", errors.New("managed companions require a clean, absolute agentflow home outside the workspace")
	}
	home = filepath.Clean(home)
	root, err := os.Stat(repo)
	if err != nil {
		return "", err
	}
	checkAncestors := func(path string) error {
		for {
			info, err := os.Stat(path)
			if err == nil && os.SameFile(info, root) {
				return errors.New("managed companions require a agentflow home outside the workspace")
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			next := filepath.Dir(path)
			if next == path {
				return nil
			}
			path = next
		}
	}
	if err := checkAncestors(home); err != nil {
		return "", err
	}
	// Canonicalize ancestors but let OpenManifest reject a symlink at home.
	parent, err := filepath.EvalSymlinks(filepath.Dir(home))
	if errors.Is(err, os.ErrNotExist) {
		return home, nil
	}
	if err != nil {
		return "", err
	}
	canonical := filepath.Join(parent, filepath.Base(home))
	if home != canonical {
		if err := checkAncestors(canonical); err != nil {
			return "", err
		}
	}
	return canonical, nil
}

// Pin is the on-disk declaration shared by CLI and MCP companions.
type Pin struct {
	Platform provision.Platform
	Version  string
	Source   string
	SHA256   string
}

func (p Pin) Artifact() (provision.Artifact, error) {
	var v provision.Version
	if _, err := fmt.Sscanf(p.Version, "%d.%d.%d", &v.Major, &v.Minor, &v.Patch); err != nil || fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) != p.Version {
		return provision.Artifact{}, errors.New("version must be a stable major.minor.patch pin")
	}
	return provision.Artifact{Platform: p.Platform, Version: v, Source: p.Source, SHA256: p.SHA256}, nil
}

func OpenArtifact(ctx context.Context, artifact provision.Artifact) (io.ReadCloser, error) {
	// Cache.Install validates the URL and expected digest before invoking us.
	u, err := url.Parse(artifact.Source)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		path := u.Path
		if runtime.GOOS == "windows" {
			if len(path) < 4 || path[0] != '/' || path[2] != ':' || path[3] != '/' {
				return nil, errors.New("use an absolute file:///C:/ path for a Windows file source")
			}
			path = filepath.FromSlash(path[1:])
		}
		return local.OpenRawFile(path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.Source, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("companion artifact source returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// Installation describes the exact write offered for confirmation.
type Installation struct {
	Home     string             `json:"home"`
	Artifact provision.Artifact `json:"artifact"`
	cache    *local.Cache
	plan     provision.Plan
}

func (i *Installation) Apply(ctx context.Context) error {
	return provision.Apply(ctx, i.plan.Environment(), i.plan, i.cache, i.cache)
}

// InstallationFor binds the reviewed artifact to its observed cache state.
func InstallationFor(home string, plan provision.Plan, cache *local.Cache) *Installation {
	return &Installation{Home: home, Artifact: *plan.Steps()[0].Artifact, plan: plan, cache: cache}
}
