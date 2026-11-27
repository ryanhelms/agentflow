package jsonrpc_test

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// secrets are variables a daemon may hold that must never reach a child.
var secrets = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "AWS_ACCESS_KEY_ID",
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "JIRA_API_TOKEN", "CONFLUENCE_TOKEN",
	"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "AGENTFLOW_HOME", "AGENTFLOW_TOKEN",
	"SSH_AUTH_SOCK", "HTTPS_PROXY", "https_proxy", "NETRC", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES",
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("entry %q has no '='", kv)
		}
		if _, dup := m[name]; dup {
			t.Fatalf("%s appears twice", name)
		}
		m[name] = value
	}
	return m
}

func TestScrubbedEnvPassesOnlyTheAllowlist(t *testing.T) {
	parent := []string{"LANG=en_GB.UTF-8", "TZ=Europe/London", "PATH=/opt/evil/bin:/usr/bin", "HOME=/Users/me", "TMPDIR=/tmp/x", "junk", "=C:=C:\\"}
	for _, s := range secrets {
		parent = append(parent, s+"=secret-"+s)
	}
	data := t.TempDir()
	env, err := jsonrpc.ScrubbedEnv(parent, data, map[string]string{
		jsonrpc.EnvPluginID:   "ci-notify",
		jsonrpc.EnvPluginData: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(env) {
		t.Fatalf("not sorted: %v", env)
	}
	m := envMap(t, env)
	for name := range m {
		if !jsonrpc.EnvAllowed(name) {
			t.Errorf("%s passed but EnvAllowed refuses it", name)
		}
	}
	for _, s := range secrets {
		if _, ok := m[s]; ok {
			t.Errorf("%s reached the child", s)
		}
	}
	for _, kv := range env {
		if strings.Contains(kv, "secret-") || strings.Contains(kv, "/Users/me") || strings.Contains(kv, "evil") {
			t.Errorf("parent value leaked: %s", kv)
		}
	}
	if m["LANG"] != "en_GB.UTF-8" || m["TZ"] != "Europe/London" {
		t.Errorf("locale not inherited: %v", m)
	}
	if m[jsonrpc.EnvPluginID] != "ci-notify" {
		t.Errorf("plugin id = %q", m[jsonrpc.EnvPluginID])
	}
	if runtime.GOOS == "windows" {
		return
	}
	if m["PATH"] != "/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Errorf("PATH = %q", m["PATH"])
	}
	for _, name := range []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		v := m[name]
		rel, err := filepath.Rel(data, v)
		if v == "" || err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			t.Errorf("%s = %q, want a directory under %s", name, v, data)
		}
	}
}

func TestScrubbedEnvRefusesBadInput(t *testing.T) {
	data := t.TempDir()
	cases := map[string]struct {
		data  string
		extra map[string]string
	}{
		"relative data":   {"plugins/x", nil},
		"empty data":      {"", nil},
		"secret in extra": {data, map[string]string{"GITHUB_TOKEN": "x"}},
		"daemon variable": {data, map[string]string{"AGENTFLOW_HOME": "x"}},
		"override PATH":   {data, map[string]string{"PATH": "/opt/bin"}},
		"override HOME":   {data, map[string]string{"HOME": "/Users/me"}},
		"NUL in value":    {data, map[string]string{jsonrpc.EnvPluginID: "a\x00b"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonrpc.ScrubbedEnv(nil, tc.data, tc.extra); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestEnvAllowed(t *testing.T) {
	for _, name := range []string{"LANG", "TZ", "PATH", "HOME", "TMPDIR", "XDG_DATA_HOME", "SystemRoot", "USERPROFILE",
		jsonrpc.EnvPluginID, jsonrpc.EnvPluginDir, jsonrpc.EnvPluginData, jsonrpc.EnvPluginProtocol} {
		if !jsonrpc.EnvAllowed(name) {
			t.Errorf("%s refused", name)
		}
	}
	for _, name := range append([]string{"", "AGENTFLOW_PLUGIN_SECRET", "AGENTFLOW_PLUGIN", "LANGUAGE"}, secrets...) {
		if jsonrpc.EnvAllowed(name) {
			t.Errorf("%s allowed", name)
		}
	}
}
