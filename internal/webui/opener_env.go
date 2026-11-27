package webui

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// openerEnv is the environment the desktop opener gets for a console
// sign-in link. It is built from a fixed list, never inherited, so the
// caller's environment (an agent's, or one a repository set through
// .envrc) cannot choose the program that receives the link: xdg-open
// follows BROWSER, PATH, HOME and XDG_* (ADR, phase 7 integration
// amendment). HOME is the OS account's own; PATH is the system's;
// XDG_RUNTIME_DIR comes from the user ID. DISPLAY, WAYLAND_DISPLAY,
// DBUS_SESSION_BUS_ADDRESS and XDG_CURRENT_DESKTOP pass, since the opener
// needs them to reach the desktop: they choose the desktop and its own
// installed helper, which is the remaining, same-user risk.
func openerEnv() []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	if u, err := user.Current(); err == nil && filepath.IsAbs(u.HomeDir) {
		env = append(env, "HOME="+u.HomeDir, "USER="+u.Username, "LOGNAME="+u.Username)
	}
	if runtime.GOOS == "linux" {
		dir := "/run/user/" + strconv.Itoa(os.Getuid())
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			env = append(env, "XDG_RUNTIME_DIR="+dir)
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "DISPLAY", name == "WAYLAND_DISPLAY", name == "DBUS_SESSION_BUS_ADDRESS", name == "XDG_CURRENT_DESKTOP",
			name == "LANG", name == "TZ", strings.HasPrefix(name, "LC_"):
			env = append(env, kv)
		}
	}
	return env
}
