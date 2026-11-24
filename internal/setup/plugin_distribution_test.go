package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type pluginCommand struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
}

func readPluginFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func pluginServer(t *testing.T, path string) pluginCommand {
	t.Helper()
	var config struct {
		Servers map[string]pluginCommand `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readPluginFile(t, path)), &config); err != nil {
		t.Fatal(err)
	}
	entry := config.Servers["agentflow"]
	if entry.Command != "node" {
		t.Fatalf("%s: expected a Agentflow Node launcher", path)
	}
	return entry
}

func TestCodexPluginCarriesCurrentVerifiedLaunchers(t *testing.T) {
	entry := pluginServer(t, "adapters/codex/plugin-mcp.json")
	want := []string{
		"-e", readPluginFile(t, "adapters/plugin-mcp.cjs"), "--", "--embedded-launchers", "codex",
		readPluginFile(t, "templates/repo/.agentflow/bin/agentflow"),
		readPluginFile(t, "templates/repo/.agentflow/bin/agentflow.ps1"),
	}
	if !reflect.DeepEqual(entry.Args, want) {
		t.Fatal("stale Codex plugin descriptor: run node scripts/build-codex-plugin.mjs")
	}
}

func TestPluginServersKeepTargetDirectoryAndClient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX fixture for the cached native binary")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	bundle, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	bin := filepath.Join(cache, "agentflow", "current", "agentflow")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\npwd\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ client, path, pluginRoot string }{
		{"codex", "adapters/codex/plugin-mcp.json", ""},
		{"cursor", "adapters/cursor/plugin-mcp.json", "${CURSOR_PLUGIN_ROOT}"},
		{"gemini", "gemini-extension.json", "${extensionPath}"},
		{"vscode", "adapters/vscode/plugin-mcp.json", "${PLUGIN_ROOT}"},
	} {
		t.Run(tc.client, func(t *testing.T) {
			entry := pluginServer(t, tc.path)
			if tc.pluginRoot != "" {
				if len(entry.Args) != 2 || !strings.HasPrefix(entry.Args[0], tc.pluginRoot+"/") {
					t.Fatalf("invalid packaged launcher arguments: %v", entry.Args)
				}
				entry.Args[0] = strings.Replace(entry.Args[0], tc.pluginRoot, bundle, 1)
			}
			if entry.Cwd != "" && entry.Cwd != "${workspacePath}" {
				t.Fatalf("unexpected project directory %q", entry.Cwd)
			}
			cmd := exec.CommandContext(t.Context(), node, entry.Args...)
			cmd.Dir = target
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "XDG_CACHE_HOME=" + cache}
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run plugin server: %v: %s", err, got)
			}
			want := target + "\nmcp\n--client\n" + tc.client + "\n"
			if string(got) != want {
				t.Fatalf("launcher output %q, want %q", got, want)
			}
		})
	}
}
