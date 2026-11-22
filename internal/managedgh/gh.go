// Package managedgh resolves the GitHub CLI for provider operations.
package managedgh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nscaledev/devflow/internal/provision"
	"github.com/nscaledev/devflow/internal/provision/local"
	"github.com/nscaledev/devflow/internal/provision/user"
)

const manifestName = "companions.yml"

var target = provision.Target{ID: "gh", Kind: provision.CLI, Command: "gh", Health: provision.HealthProbe{Name: "integrity"}}

type Installation = user.Installation

type Selection struct {
	Command string
	Install *Installation
	Detail  string
}

// Command resolves an installed GitHub CLI for a repository without installing one.
func Command(ctx context.Context, repo string) (string, error) {
	selection, err := Inspect(ctx, repo)
	if err != nil {
		return "", err
	}
	if selection.Command == "" {
		return "", fmt.Errorf("GitHub CLI unavailable: %s", selection.Detail)
	}
	return selection.Command, nil
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
	home, err := user.ManifestHome(repo)
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
	cache, err := local.New(home, env, user.OpenArtifact)
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
		return Selection{Install: user.InstallationFor(home, plan, cache),
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

func readPins(r io.Reader) ([]provision.Artifact, error) {
	var manifest struct{ GH []user.Pin }
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected a single manifest document")
	}
	var pins []provision.Artifact
	for _, pin := range manifest.GH {
		a, err := pin.Artifact()
		if err != nil {
			return nil, err
		}
		pins = append(pins, a)
	}
	return pins, nil
}
