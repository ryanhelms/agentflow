package setup

import (
	"bytes"
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
	return strings.ReplaceAll(string(data), "\r\n", "\n")
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
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		POSIX   string `json:"posix"`
		Windows string `json:"windows"`
	}{
		readPluginFile(t, "templates/repo/.agentflow/bin/agentflow"),
		readPluginFile(t, "templates/repo/.agentflow/bin/agentflow.ps1"),
	}); err != nil {
		t.Fatal(err)
	}
	entryPoint := "const embeddedLaunchers = " + strings.TrimSuffix(payload.String(), "\n") + ";\n"
	want := []string{
		"-e", entryPoint + readPluginFile(t, "adapters/plugin-mcp.cjs"), "--", "--embedded-launchers", "codex",
	}
	if !reflect.DeepEqual(entry.Args, want) {
		t.Fatal("stale Codex plugin descriptor: run node scripts/build-codex-plugin.mjs")
	}
}

func TestPluginServersKeepTargetDirectoryAndClient(t *testing.T) {
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
	target = filepath.Join(target, "project with 'quotes' & spaces")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	pluginFixture(t, cache)
	// A launcher that refuses the fixture goes on to download a release. This gh comes
	// first on PATH and fails, so the test never calls GitHub.
	stubs := t.TempDir()
	stub, script := "gh", "#!/bin/sh\necho 'test stub gh: no downloads' >&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		stub, script = "gh.cmd", "@echo test stub gh: no downloads 1>&2\r\n@exit /b 1\r\n"
	}
	if err := os.WriteFile(filepath.Join(stubs, stub), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	search := stubs + string(os.PathListSeparator) + os.Getenv("PATH")
	tmp := filepath.Join(t.TempDir(), "launcher with 'quotes' & spaces")
	if err := os.Mkdir(tmp, 0o700); err != nil {
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
			cmd.Env = append(os.Environ(), "PATH="+search, "XDG_CACHE_HOME="+cache, "LOCALAPPDATA="+cache, "TMPDIR="+tmp, "TEMP="+tmp, "TMP="+tmp)
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run plugin server: %v: %s", err, got)
			}
			want := target + "\nmcp\n--client\n" + tc.client + "\n"
			if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
				t.Fatalf("launcher output %q, want %q", got, want)
			}
			if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
				t.Fatalf("temporary launcher was not cleaned up: %v, %v", entries, err)
			}
		})
	}
}

func pluginFixture(t *testing.T, cache string) {
	t.Helper()
	// The install layout: <cache>/agentflow/<version>, named by the current link, or by
	// current.txt on Windows. The launchers refuse a pointer to any other folder.
	root := filepath.Join(cache, "agentflow")
	active := filepath.Join(root, "1.0.0")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(active, "agentflow"), []byte("#!/bin/sh\npwd\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("1.0.0", filepath.Join(root, "current")); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(cache, "agentflow", "current.txt"), []byte(active), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "main.go")
	const fixture = `package main
import ("fmt"; "os")
func main() {
	dir, err := os.Getwd()
	if err != nil { os.Exit(1) }
	fmt.Println(dir)
	for _, arg := range os.Args[1:] { fmt.Println(arg) }
}
`
	if err := os.WriteFile(source, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(active, "agentflow.exe"), source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Windows plugin fixture: %v: %s", err, output)
	}
}
