package jsonrpc

import (
	"strings"
	"testing"
)

func TestScrubbedEnvWindows(t *testing.T) {
	data := t.TempDir()
	parent := []string{`SYSTEMROOT=D:\Win`, "Path=C:\\evil", "GITHUB_TOKEN=x", "comspec=cmd.exe", "=C:=C:\\"}
	env, err := scrubbedEnv(parent, data, map[string]string{EnvPluginID: "p"}, true)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["SystemRoot"] != `D:\Win` || m["COMSPEC"] != "cmd.exe" {
		t.Fatalf("allowlist not matched without case: %v", m)
	}
	if m["PATH"] != `D:\Win\System32;D:\Win` {
		t.Fatalf("PATH = %q", m["PATH"])
	}
	for _, name := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "HOME"} {
		if !strings.HasPrefix(m[name], data) {
			t.Fatalf("%s = %q", name, m[name])
		}
	}
	if _, ok := m["GITHUB_TOKEN"]; ok {
		t.Fatal("token passed")
	}
	if _, err := scrubbedEnv(nil, data, map[string]string{"path": "x"}, true); err == nil {
		t.Fatal("extra overrode PATH by case")
	}
	if !envAllowed("systemroot", true) || envAllowed("systemroot", false) {
		t.Fatal("case folding is not Windows only")
	}
}

func TestTailBufferKeepsNewest(t *testing.T) {
	b := &tailBuffer{limit: 5}
	for _, s := range []string{"ab", "cd", "efg"} {
		_, _ = b.Write([]byte(s))
	}
	if got := string(b.Bytes()); got != "cdefg" {
		t.Fatalf("got %q", got)
	}
	_, _ = b.Write([]byte("0123456789"))
	if got := string(b.Bytes()); got != "56789" {
		t.Fatalf("got %q", got)
	}
}

func TestStderrStreamSplitWrites(t *testing.T) {
	var out strings.Builder
	s := newStderrStream(&out, 1<<20)
	for _, w := range []string{"ab", "c\nd", "", "\n\n", strings.Repeat("x", 3000), strings.Repeat("y", 3000), "z\nok"} {
		_, _ = s.Write([]byte(w))
	}
	s.finish()
	want := "abc\nd\n\n" + strings.Repeat("x", 3000) + strings.Repeat("y", 1096) + " [line truncated]\nok\n"
	if out.String() != want {
		t.Fatalf("got %q", out.String())
	}
}

func TestHarnessEnvWindows(t *testing.T) {
	data := t.TempDir()
	parent := []string{`SYSTEMROOT=D:\Win`, `Path=C:\evil`, "GITHUB_TOKEN=x"}
	env, err := harnessEnv(parent, data, []string{`C:\Program Files\nodejs`, `\\server\share\bin`}, true)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if want := `C:\Program Files\nodejs;\\server\share\bin;D:\Win\System32;D:\Win`; m["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", m["PATH"], want)
	}
	if _, ok := m["GITHUB_TOKEN"]; ok {
		t.Fatal("token passed")
	}
	for _, bad := range []string{"/usr/bin", `bin`, `C:\a;D:\b`, `C:`, `\\`} {
		if _, err := harnessEnv(parent, data, []string{bad}, true); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := harnessEnv(parent, data, []string{`C:\bin`}, false); err == nil {
		t.Error("a Windows path was accepted on Unix")
	}
}

func TestCheckEnvAllowEnv(t *testing.T) {
	env := []string{"PATH=/usr/bin", "USER=me", "CODEX_HOME=/c"}
	if err := checkEnvFor(env, nil, false); err == nil {
		t.Fatal("USER and CODEX_HOME passed without AllowEnv")
	}
	if err := checkEnvFor(env, []string{"USER", "CODEX_HOME"}, false); err != nil {
		t.Fatal(err)
	}
	if err := checkEnvFor(env, []string{"user", "codex_home"}, false); err == nil {
		t.Fatal("AllowEnv ignored case on Unix")
	}
	if err := checkEnvFor(env, []string{"user", "codex_home"}, true); err != nil {
		t.Fatalf("AllowEnv did not ignore case on Windows: %v", err)
	}
	for _, bad := range []string{"", "A=B", "A\x00"} {
		if err := checkEnvFor(nil, []string{bad}, false); err == nil {
			t.Errorf("AllowEnv %q accepted", bad)
		}
	}
	if err := checkEnvFor([]string{"PATH=/usr/bin", "HOME=/h"}, nil, false); err != nil {
		t.Fatalf("a spec without AllowEnv changed: %v", err)
	}
}
