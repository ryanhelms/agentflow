// Package managedgh resolves the GitHub CLI for provider operations.
package managedgh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/provision"
	"github.com/nscaledev/devflow/internal/provision/local"
)

const manifestName = "companions.yml"

var target = provision.Target{ID: "gh", Kind: provision.CLI, Command: "gh", Health: provision.HealthProbe{Name: "integrity"}}

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

type Selection struct {
	Command string
	Install *Installation
	Detail  string
}

func working(ctx context.Context, dir, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	return cmd.Run() == nil
}

// Inspect prefers a working PATH installation. Only a pin in the user's
// devflow home can select a managed executable. Planning never opens its source.
func Inspect(ctx context.Context, repo string) (Selection, error) {
	missing := Selection{Detail: "install gh on PATH in the environment running devflow and rerun setup"}
	if path, err := exec.LookPath("gh"); err == nil {
		if working(ctx, repo, path) {
			return Selection{Command: path, Detail: "gh --version succeeded"}, nil
		}
		missing.Detail = "repair gh on PATH so gh --version succeeds"
	}
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		missing.Detail += ". Managed installation supports native macOS and Linux"
		return missing, nil
	}
	home, err := manifestHome(repo)
	if err != nil {
		return Selection{}, err
	}
	file, err := local.OpenManifest(home, manifestName)
	if errors.Is(err, os.ErrNotExist) {
		missing.Detail += ". No installation artifact is declared in " + filepath.Join(home, manifestName)
		return missing, nil
	}
	if err != nil {
		return Selection{}, err
	}
	defer file.Close()
	pins, err := readPins(file)
	if err != nil {
		return Selection{}, fmt.Errorf("%s: %w", manifestName, err)
	}
	env := provision.Environment{ID: "companions", Platform: provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}}
	cache, err := local.New(home, env, openArtifact)
	if err != nil {
		return Selection{}, err
	}
	manifest := provision.Manifest{ProviderID: "github", Companions: []provision.Companion{{Target: target, Artifacts: pins, PinArtifact: true}}}
	plan, err := provision.BuildPlan(ctx, env, []provision.Manifest{manifest}, cache)
	var conflict *provision.ConflictError
	if errors.As(err, &conflict) {
		missing.Detail += fmt.Sprintf(". Provide exactly one gh pin for %s/%s in %s", env.Platform.OS, env.Platform.Arch, manifestName)
		return missing, nil
	}
	if err != nil {
		return Selection{}, err
	}
	step := plan.Steps()[0]
	if step.Action == provision.Install {
		return Selection{Install: &Installation{Home: home, Artifact: *step.Artifact, cache: cache, plan: plan},
			Detail: "run devflow setup apply --install-gh to install the pinned gh executable"}, nil
	}
	receipt, err := cache.Resolve(ctx, env, target)
	if err != nil {
		return Selection{}, err
	}
	if receipt.Version != step.Artifact.Version || receipt.SHA256 != step.Artifact.SHA256 {
		return Selection{}, provision.ErrStalePlan
	}
	if !working(ctx, repo, receipt.Path) {
		return Selection{Detail: "managed gh --version failed at " + receipt.Path + ". Check the pinned raw executable for this platform"}, ctx.Err()
	}
	return Selection{Command: receipt.Path, Detail: "managed gh --version succeeded at " + receipt.Path}, nil
}

// Compare directory identities as well as following aliases. On case-insensitive
// filesystems a different spelling of the checkout still denotes the same tree.
func manifestHome(repo string) (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) || strings.Contains("/"+home+"/", "/../") {
		return "", errors.New("managed gh requires a clean, absolute devflow home outside the workspace")
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
				return errors.New("managed gh requires a devflow home outside the workspace")
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

func readPins(r io.Reader) ([]provision.Artifact, error) {
	var manifest struct {
		GH []struct {
			Platform provision.Platform
			Version  string
			Source   string
			SHA256   string
		}
	}
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected a single manifest document")
	}
	var pins []provision.Artifact
	for _, entry := range manifest.GH {
		var v provision.Version
		if _, err := fmt.Sscanf(entry.Version, "%d.%d.%d", &v.Major, &v.Minor, &v.Patch); err != nil ||
			fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) != entry.Version {
			return nil, errors.New("gh version must be a stable major.minor.patch pin")
		}
		pins = append(pins, provision.Artifact{Platform: entry.Platform, Version: v, Source: entry.Source, SHA256: entry.SHA256})
	}
	return pins, nil
}

func openArtifact(ctx context.Context, artifact provision.Artifact) (io.ReadCloser, error) {
	// Cache.Install validates the URL and expected digest before invoking us.
	u, err := url.Parse(artifact.Source)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		return local.OpenRawFile(u.Path)
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
		return nil, fmt.Errorf("gh artifact source returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}
