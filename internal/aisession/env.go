package aisession

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// EnvSession marks every process below the harness as inside a session.
const EnvSession = "AGENTFLOW_ACP_SESSION"

// systemPATH is the fixed system PATH that ends the session PATH.
func systemPATH(windows bool) []string {
	if windows {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return []string{filepath.Join(root, "System32"), root}
	}
	return []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
}

// envInput is what the session environment is built from.
type envInput struct {
	Parent      []string // the starter's own environment
	AccountHome string   // os/user, never $HOME
	SessionDir  string   // <home>/ai/sessions/<id>
	HomeMode    string   // real, isolated
	HarnessDir  string   // the PATH entry the harness was found in
	Definition  harnessdef.Definition
	// WorkDirs: the session's worktree and every repository the daemon
	// serves; PATH entries inside any of their worktrees are dropped.
	WorkDirs []string
	Session  ID
	Windows  bool
}

// sessionEnv is the harness's environment and its terminals'.
type sessionEnv struct {
	Harness  []string // NAME=value, sorted
	Terminal []string // Harness without the credential_env values
	Names    []string // the harness's names, sorted (stored; never values)
	// Credential are the definition's credential_env names that are set.
	Credential []string
	// AllowEnv: the names beyond jsonrpc.EnvAllowed (LaunchSpec.AllowEnv).
	AllowEnv []string
	PATH     []string
	Tmp      string
	Home     string
}

// inherited: names passed as they are when set ("Phase 7 session
// environment"). LC_* is matched by prefix.
var inheritedUnix = []string{"USER", "LOGNAME", "LANG", "TZ"}

var inheritedWindows = []string{
	"SystemRoot", "SystemDrive", "COMSPEC", "PATHEXT", "WINDIR",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "USERNAME",
}

var xdgHomes = []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}

// credentialNeverPassed: names that never pass, even as a definition's
// credential_env (a personal override could name them).
var credentialNeverPassed = []string{"GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "GPG_AGENT_INFO"}

func credentialNameOK(name string) bool {
	up := strings.ToUpper(name)
	switch {
	case name == "", harnessdef.LaunchSafetyName(name), slices.Contains(credentialNeverPassed, up),
		strings.HasPrefix(up, "AGENTFLOW_"), strings.HasPrefix(up, "DEVFLOW_"):
		return false
	}
	return true
}

// buildEnv builds the session environment, or refuses with
// unsafe_environment when the real HOME, a set XDG_*_HOME or a config_env
// directory is inside a git working tree or git directory.
func buildEnv(ctx context.Context, in envInput) (sessionEnv, *Refusal) {
	parent := map[string]string{}
	for _, kv := range in.Parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if in.Windows {
			k = strings.ToUpper(k)
		}
		parent[k] = v
	}
	get := func(name string) (string, bool) {
		if in.Windows {
			name = strings.ToUpper(name)
		}
		v, ok := parent[name]
		return v, ok
	}
	out := map[string]string{}
	set := func(name, value string) { out[name] = value }

	isolated := in.HomeMode == config.AISessionHomeIsolated
	home := in.AccountHome
	if isolated {
		home = filepath.Join(in.SessionDir, "home")
	}
	if home == "" || !filepath.IsAbs(home) {
		return sessionEnv{}, &Refusal{Code: RefuseUnsafeEnvironment, Message: "agentflow cannot find your account's home folder, so it cannot start a session."}
	}
	unsafe := func(dir string) *Refusal {
		if inside, why := config.InsideRepository(dir); inside {
			return &Refusal{Code: RefuseUnsafeEnvironment, Message: "A home or configuration folder of the session " + why + ", so a repository could supply the harness's settings. agentflow does not start the session.", Fix: "Move that folder out of the repository, or unset the variable that points into it."}
		}
		return nil
	}
	if !isolated {
		if r := unsafe(home); r != nil {
			return sessionEnv{}, r
		}
	}
	set("HOME", home)
	if in.Windows {
		for _, n := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH"} {
			if isolated {
				switch n {
				case "USERPROFILE":
					set(n, home)
				case "APPDATA":
					set(n, filepath.Join(home, "AppData", "Roaming"))
				case "LOCALAPPDATA":
					set(n, filepath.Join(home, "AppData", "Local"))
				}
				continue
			}
			if v, ok := get(n); ok {
				set(n, v)
			}
		}
	}
	if !isolated {
		for _, n := range xdgHomes {
			if v, ok := get(n); ok && v != "" {
				if r := unsafe(v); r != nil {
					return sessionEnv{}, r
				}
				set(n, v)
			}
		}
	}
	for _, n := range in.Definition.ConfigEnv {
		if harnessdef.ConfigEnvRefused(n, in.Definition.CredentialEnv) != "" {
			continue
		}
		if v, ok := get(n); ok && v != "" {
			if r := unsafe(v); r != nil {
				return sessionEnv{}, r
			}
			set(n, v)
		}
	}
	names := inheritedUnix
	if in.Windows {
		names = append(append([]string{}, inheritedUnix...), inheritedWindows...)
	}
	for _, n := range names {
		if v, ok := get(n); ok {
			set(n, v)
		}
	}
	for k, v := range parent {
		if strings.HasPrefix(k, "LC_") {
			set(k, v)
		}
	}
	if sh, ok := get("SHELL"); ok && harness.SessionProgramOK(ctx, sh, in.WorkDirs) {
		set("SHELL", sh)
	}
	set("TERM", "dumb")
	tmp := filepath.Join(in.SessionDir, "tmp")
	set("TMPDIR", tmp)
	if in.Windows {
		set("TEMP", tmp)
		set("TMP", tmp)
	}
	pathValue, _ := get("PATH")
	dirs, _, err := harness.SessionPATH(ctx, pathValue, in.WorkDirs)
	if err != nil {
		return sessionEnv{}, &Refusal{Code: RefuseTestGuard, Message: "In a test binary, a session PATH entry is outside the temporary directory."}
	}
	var path []string
	seen := map[string]bool{}
	add := func(d string) {
		key := d
		if in.Windows {
			key = strings.ToLower(d)
		}
		if d != "" && !seen[key] {
			seen[key] = true
			path = append(path, d)
		}
	}
	if in.HarnessDir != "" {
		add(filepath.Clean(in.HarnessDir))
	}
	for _, d := range dirs {
		add(d)
	}
	for _, d := range systemPATH(in.Windows) {
		add(d)
	}
	set("PATH", strings.Join(path, string(filepath.ListSeparator)))
	set(EnvSession, string(in.Session))
	set("GIT_TERMINAL_PROMPT", "0")

	terminal := map[string]string{}
	for k, v := range out {
		terminal[k] = v
	}
	var credential []string
	for _, n := range in.Definition.CredentialEnv {
		if !credentialNameOK(n) {
			continue
		}
		if v, ok := get(n); ok && v != "" {
			set(n, v)
			credential = append(credential, n)
		}
	}
	sort.Strings(credential)
	env := sessionEnv{Credential: credential, PATH: path, Tmp: tmp, Home: home}
	for k, v := range out {
		env.Harness = append(env.Harness, k+"="+v)
		env.Names = append(env.Names, k)
	}
	for k, v := range terminal {
		env.Terminal = append(env.Terminal, k+"="+v)
	}
	sort.Strings(env.Harness)
	sort.Strings(env.Terminal)
	sort.Strings(env.Names)
	env.AllowEnv = append([]string{}, env.Names...)
	return env, nil
}

func isWindows() bool { return runtime.GOOS == "windows" }
