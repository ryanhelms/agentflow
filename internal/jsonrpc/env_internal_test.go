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
