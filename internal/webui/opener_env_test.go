package webui

import (
	"strings"
	"testing"
)

// The caller's environment cannot choose who receives a sign-in link.
func TestOpenerEnvIgnoresTheCaller(t *testing.T) {
	t.Setenv("BROWSER", "/tmp/grab")
	t.Setenv("PATH", "/tmp/evil:/usr/bin")
	t.Setenv("HOME", "/tmp/fakehome")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	t.Setenv("XDG_DATA_DIRS", "/tmp/xdgdata")
	t.Setenv("DISPLAY", ":0")
	env := openerEnv()
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "BROWSER", "XDG_CONFIG_HOME", "XDG_DATA_DIRS":
			t.Errorf("%s passed", kv)
		case "PATH":
			if strings.Contains(value, "/tmp") {
				t.Errorf("PATH %s", value)
			}
		case "HOME":
			if value == "/tmp/fakehome" {
				t.Error("the caller's HOME passed")
			}
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "DISPLAY=:0") {
		t.Error("DISPLAY did not pass")
	}
}
