package managedmcp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provision"
)

func TestTrustedMCPManifest(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("MCP_MISSING", "")
	const good = `mcp:
- id: fixture
  kind: remote-mcp
  providers: [filesystem]
  endpoint: https://fixture.invalid/mcp
  auth_env: MCP_MISSING
`
	platform := provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	inspect := func() ([]Status, error) { return Inspect(t.Context(), repo, platform, []string{"filesystem"}, "") }
	put := func(dir, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put(repo, good)
	if s, err := inspect(); err != nil || len(s) != 0 {
		t.Fatal("accepted project declaration", err)
	}
	t.Setenv("AGENTFLOW_HOME", repo)
	if _, err := inspect(); err == nil {
		t.Fatal("accepted project-owned home")
	}
	t.Setenv("AGENTFLOW_HOME", home)
	put(home, good)
	s, err := inspect()
	if err != nil || len(s) != 1 || s[0].Ready || !strings.Contains(s[0].Detail, "MCP_MISSING") {
		t.Fatalf("missing auth not diagnosed: %+v %v", s, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(home, manifestName), 0o666); err != nil {
			t.Fatal(err)
		}
		if _, err := inspect(); err == nil {
			t.Fatal("accepted writable manifest")
		}
		if err := os.Remove(filepath.Join(home, manifestName)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(repo, manifestName), filepath.Join(home, manifestName)); err != nil {
			t.Fatal(err)
		}
		if _, err := inspect(); err == nil {
			t.Fatal("accepted symlink to project manifest")
		}
		if err := os.Remove(filepath.Join(home, manifestName)); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{
		strings.Replace(good, "https://", "http://", 1),
		strings.Replace(good, "https://", "https://sensitive-value@", 1),
		strings.Replace(good, "/mcp", "/mcp?token=sensitive-value", 1),
		strings.Replace(good, "/mcp", "/mcp#sensitive-value", 1),
		strings.Replace(good, "/mcp", "/${env:MCP_MISSING}", 1),
		strings.Replace(good, "MCP_MISSING", "sensitive-value", 1),
		good + "  args: [sensitive-value]\n",
		good + "  headers: {Authorization: sensitive-value}\n",
		good + "  auth: {token: sensitive-value}\n",
		good + "  artifacts: [{version: '1.2.0'}]\n",
		good + "---\nsensitive-value\n",
		strings.Replace(good, "remote-mcp", "stdio-mcp", 1),
		strings.Replace(good, "kind: remote-mcp", "kind: stdio-mcp\n  command: sensitive-value", 1),
		strings.Replace(good, "kind: remote-mcp", "kind: stdio-mcp\n  args: [sensitive-value]", 1),
	} {
		put(home, invalid)
		if _, err := inspect(); err == nil || strings.Contains(err.Error(), "sensitive-value") {
			t.Fatal("invalid manifest accepted or sensitive value in error")
		}
	}
}
