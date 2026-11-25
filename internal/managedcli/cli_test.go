package managedcli

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nscaledev/agentflow/internal/provision"
)

func TestEnsureInstallsAndReusesVerifiedCLI(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX executable fixture")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	ctx, repo := t.Context(), t.TempDir()
	raw := []byte("#!/bin/sh\n[ \"$1\" = --version ]\n")
	source := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	for _, provider := range []string{"jira", "github-issues"} {
		t.Run(provider, func(t *testing.T) {
			tool, _ := ForProvider(provider)
			current := releases[tool.Command][platform]
			releases[tool.Command][platform] = pinnedRelease{
				version: provision.Version{Major: 1}, source: (&url.URL{Scheme: "file", Path: source}).String(),
				binarySHA: fmt.Sprintf("%x", sha256.Sum256(raw)),
			}
			t.Cleanup(func() { releases[tool.Command][platform] = current })
			if _, err := Resolve(ctx, repo, tool); err == nil {
				t.Fatal("passive lookup unexpectedly installed the CLI")
			}
			path, err := Ensure(ctx, repo, tool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Resolve(ctx, repo, tool)
			if err != nil || got != path {
				t.Fatalf("cached CLI: %q, %v", got, err)
			}
		})
	}
}
