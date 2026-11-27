package jsonrpc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Directories ScrubbedEnv points the child's home and temp variables at,
// relative to its data directory.
const (
	homeSubdir = "home"
	tmpSubdir  = "tmp"
)

// Unix and Windows variables ScrubbedEnv sets itself.
var (
	unixSet    = []string{EnvPath, EnvHome, EnvTmp, "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"}
	windowsSet = []string{EnvPath, EnvHome, "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP"}
	pluginSet  = []string{EnvPluginID, EnvPluginDir, EnvPluginData, EnvPluginProtocol}
)

const unixPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// EnvAllowed reports whether a child may receive the variable name: one of
// EnvAllowlist, one ScrubbedEnv sets itself (PATH, HOME, TMPDIR, the XDG
// and Windows profile and temp variables) or an AGENTFLOW_PLUGIN_* name the
// host sets. On Windows the comparison ignores case.
func EnvAllowed(name string) bool {
	return envAllowed(name, runtime.GOOS == "windows")
}

func envAllowed(name string, foldCase bool) bool {
	if name == "" {
		return false
	}
	for _, set := range [][]string{EnvAllowlist, unixSet, windowsSet, pluginSet} {
		for _, n := range set {
			if n == name || (foldCase && strings.EqualFold(n, name)) {
				return true
			}
		}
	}
	return false
}

// ScrubbedEnv builds a child's complete environment. parent is normally
// os.Environ(); only EnvAllowlist names pass from it. PATH is a fixed
// system path, never the parent's. HOME (USERPROFILE, APPDATA, LOCALAPPDATA
// on Windows) is data/home and the temp variables are data/tmp; the XDG
// directories are under data/home on Unix. Both directories are created
// with mode 0700. extra adds the AGENTFLOW_PLUGIN_* variables; a name
// EnvAllowed refuses, or one ScrubbedEnv sets itself, fails. The result is
// sorted.
func ScrubbedEnv(parent []string, data string, extra map[string]string) ([]string, error) {
	return scrubbedEnv(parent, data, extra, runtime.GOOS == "windows")
}

func scrubbedEnv(parent []string, data string, extra map[string]string, windows bool) ([]string, error) {
	if data == "" || !filepath.IsAbs(data) {
		return nil, errors.New("jsonrpc: data directory must be an absolute path")
	}
	data = filepath.Clean(data)
	home := filepath.Join(data, homeSubdir)
	tmp := filepath.Join(data, tmpSubdir)
	for _, d := range []string{home, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("jsonrpc: create %s directory: %w", filepath.Base(d), err)
		}
	}

	// name (as the child sees it) -> value. On Windows names are matched
	// without case, and the allowlist spelling is used.
	env := map[string]string{}
	for _, kv := range parent {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue // Windows "=C:=C:\" entries and junk
		}
		for _, allowed := range EnvAllowlist {
			if name == allowed || (windows && strings.EqualFold(name, allowed)) {
				env[allowed] = value
			}
		}
	}

	own := unixSet
	if windows {
		root := env["SystemRoot"]
		if root == "" {
			root = `C:\Windows`
		}
		own = windowsSet
		env[EnvPath] = root + `\System32;` + root
		env[EnvHome] = home
		env["USERPROFILE"] = home
		env["APPDATA"] = home
		env["LOCALAPPDATA"] = home
		env["TEMP"] = tmp
		env["TMP"] = tmp
	} else {
		env[EnvPath] = unixPath
		env[EnvHome] = home
		env[EnvTmp] = tmp
		env["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
		env["XDG_CACHE_HOME"] = filepath.Join(home, ".cache")
		env["XDG_DATA_HOME"] = filepath.Join(home, ".local", "share")
	}

	for name, value := range extra {
		if !envAllowed(name, windows) {
			return nil, fmt.Errorf("jsonrpc: environment variable %q is not allowed for a child", name)
		}
		for _, n := range own {
			if n == name || (windows && strings.EqualFold(n, name)) {
				return nil, fmt.Errorf("jsonrpc: environment variable %q is set by the launcher", name)
			}
		}
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("jsonrpc: environment variable %q has a NUL byte", name)
		}
		env[name] = value
	}

	out := make([]string, 0, len(env))
	for name, value := range env {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out, nil
}

// checkEnv validates a complete child environment for Launch and Run. A
// name passes when EnvAllowed accepts it or allow lists it (on Windows
// without case).
func checkEnv(env, allow []string) error {
	return checkEnvFor(env, allow, runtime.GOOS == "windows")
}

func checkEnvFor(env, allow []string, windows bool) error {
	for _, name := range allow {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return errors.New("jsonrpc: an AllowEnv name is empty or has '=' or a NUL byte") // never echo it
		}
	}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			return errors.New("jsonrpc: environment entry without '='") // never echo it: it may be a value
		}
		if !envAllowed(name, windows) && !listed(allow, name, windows) {
			return fmt.Errorf("jsonrpc: environment variable %q is not allowed for a child", name)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("jsonrpc: environment variable %q has a NUL byte", name)
		}
	}
	return nil
}

func listed(list []string, name string, foldCase bool) bool {
	for _, n := range list {
		if n == name || (foldCase && strings.EqualFold(n, name)) {
			return true
		}
	}
	return false
}
