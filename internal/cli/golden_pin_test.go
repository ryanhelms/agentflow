package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/version"
)

// TestGoldenPin pins what `agentflow pin` prints for the workflow and the launcher files.
func TestGoldenPin(t *testing.T) {
	goldenEnvironment(t, "")
	oldC, oldV := version.Commit, version.Version
	t.Cleanup(func() { version.Commit, version.Version = oldC, oldV })
	version.Commit, version.Version = strings.Repeat("abcdef1234", 4), "0.61.0"

	write := func(dir, rel string, data []byte, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	stock, err := os.ReadFile(filepath.Join("..", "setup", "testdata", "stock", "v0.54.0", ".agentflow", "bin", "agentflow"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := []byte("jobs:\n  lint:\n    uses: ryanhelms/agentflow/.github/workflows/jira-key.yml@" +
		strings.Repeat("0123456789", 4) + " # v0.60.0\n")
	// Each scenario runs pin --check, pin and pin --check again.
	scenario := func(name string, files map[string][]byte) {
		t.Helper()
		dir := t.TempDir()
		goldenGit(t, dir, "init", "-q")
		for rel, data := range files {
			write(dir, rel, data, 0o755)
		}
		t.Chdir(dir)
		g := newGoldenCLI(t)
		g.check("pin/"+name+"_check.txt", g.run("", "pin", "--check"))
		g.check("pin/"+name+"_apply.txt", g.run("", "pin"))
		g.check("pin/"+name+"_recheck.txt", g.run("", "pin", "--check"))
	}

	// Only the workflow: today's output, unchanged.
	scenario("workflow_only", map[string][]byte{".github/workflows/agentflow.yml": workflow})

	// The workflow and a stock launcher from an older release.
	scenario("stock_launcher", map[string][]byte{".github/workflows/agentflow.yml": workflow, ".agentflow/bin/agentflow": stock})

	// A customised hook only warns; no workflow, but a stock launcher.
	scenario("no_workflow", map[string][]byte{".agentflow/bin/agentflow": stock, ".githooks/commit-msg": []byte("#!/bin/sh\nexit 0\n")})
}
