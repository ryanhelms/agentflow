package aisession

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/boundary"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// ---- the session never-list ("Phase 7 terminals") ----

// shells and wrappers: only a person may allow them.
var shellsAndWrappers = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "csh": true, "tcsh": true,
	"cmd": true, "powershell": true, "pwsh": true, "env": true, "xargs": true, "nohup": true, "timeout": true,
	"nice": true, "time": true, "script": true, "exec": true, "eval": true, "expect": true, "watch": true,
	"setsid": true, "stdbuf": true, "chroot": true, "busybox": true, "nsenter": true, "unshare": true,
}

// interpreters count as wrappers when they are given code to run.
var interpreters = map[string][]string{
	"python": {"-c"}, "python3": {"-c"}, "node": {"-e", "--eval", "-p", "--print"}, "deno": {"eval"},
	"bun": {"-e", "--eval"}, "ruby": {"-e"}, "perl": {"-e", "-E"}, "php": {"-r"}, "lua": {"-e"},
	"osascript": {"-e"}, "awk": nil, "gawk": nil,
}

var privileged = map[string]bool{"sudo": true, "doas": true, "su": true, "pkexec": true, "runas": true}

// readOnlyAgentflow are the agentflow commands a session's terminal may run.
var readOnlyAgentflow = [][]string{
	{"context"}, {"scope"}, {"ac"}, {"lint-msg"}, {"docs", "search"}, {"docs", "get"},
	{"policy", "explain"}, {"version"}, {"ai", "harnesses"},
}

// NeverList is the session never-list for a terminal command line, beyond
// policy.ForbiddenWith: it returns a verdict code or "".
func NeverList(line string) string {
	for _, words := range boundary.SimpleCommands(line) {
		if len(words) == 0 {
			continue
		}
		if privileged[harnessdef.LauncherName(words[0])] {
			return VerdictPrivilege
		}
		if args, ok := agentflowArgs(words); ok && !readOnlyInvocation(args) {
			return VerdictAgentflowInSession
		}
	}
	return ""
}

// agentflowArgs reports whether words run agentflow, and its arguments. It
// finds agentflow the way policy.changesPlugins does: by program name, the
// plugin launcher, a cached build, or go run.
func agentflowArgs(words []string) ([]string, bool) {
	program, args := words[0], words[1:]
	switch name := boundary.ProgramName(program); {
	case name == "agentflow", name == "devflow":
	case name == "launch" && (strings.Contains(strings.ToLower(program), "agentflow") || strings.Contains(program, "CLAUDE_PLUGIN_ROOT")):
	case name == "go" && len(args) > 1 && args[0] == "run":
		found := false
		for i, a := range args[1:] {
			if boundary.ProgramName(a) == "agentflow" || strings.Contains(a, "cmd/agentflow") {
				args, found = args[i+2:], true
				break
			}
		}
		if !found {
			return nil, false
		}
	default:
		return nil, false
	}
	if len(args) > 1 && args[0] == "--native-client" {
		args = args[2:]
	}
	return args, true
}

func readOnlyInvocation(args []string) bool {
	var words []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	if slices.Contains(args, "--probe") || slices.Contains(args, "--yes") || slices.Contains(args, "-y") {
		return false
	}
	for _, ro := range readOnlyAgentflow {
		if len(words) >= len(ro) && slices.Equal(words[:len(ro)], ro) {
			// ac: only reading the checklist, never "ac done".
			if ro[0] == "ac" && len(words) > 1 {
				return false
			}
			return true
		}
	}
	return false
}

// isShellOrWrapper reports whether a terminal's program (or a command it
// runs) is a shell, a wrapper, or an interpreter given code to run.
func isShellOrWrapper(program string, args []string) bool {
	name := harnessdef.LauncherName(program)
	if shellsAndWrappers[name] {
		return true
	}
	if flags, ok := interpreters[name]; ok {
		if flags == nil {
			return true
		}
		for _, a := range args {
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return true
				}
			}
		}
	}
	return false
}

// refusedEnvName reports whether an agent env name denies the whole
// terminal request.
func refusedEnvName(name string) bool {
	up := strings.ToUpper(name)
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return true
	}
	if harnessdef.ConfigEnvRefused(name, nil) != "" {
		return true
	}
	switch up {
	case "PATH", "HOME", "SHELL", "TMPDIR", "TMP", "TEMP", "GIT_EXEC_PATH", "GIT_DIR", "GIT_WORK_TREE",
		"GIT_ASKPASS", "SSH_ASKPASS", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"USERPROFILE", "APPDATA", "LOCALAPPDATA", "COMSPEC", EnvSession:
		return true
	}
	return strings.HasPrefix(up, "GIT_CONFIG") || strings.HasPrefix(up, "GIT_SSH") ||
		strings.HasPrefix(up, "XDG_") || strings.HasPrefix(up, "BASH_FUNC_")
}

// terminal runs the never-list of a terminal request. ok is true when it
// decided (a deny).
func (l *neverLink) terminal(ctx context.Context, r *Request) (Verdict, bool) {
	cmd := r.Command
	if cmd == nil || cmd.Resolved == "" || !filepath.IsAbs(cmd.Resolved) {
		return deny(HandlerNever, VerdictProgram), true
	}
	if l.c.InTest && l.c.Guard != nil {
		if err := l.c.Guard(cmd.Resolved); err != nil {
			return deny(HandlerNever, VerdictTestGuard), true
		}
	}
	env := stateOf(ctx).env
	if len(env) > MaxAgentEnv {
		return deny(HandlerNever, VerdictEnv), true
	}
	for _, e := range env {
		if refusedEnvName(e.Name) || len(e.Value) > MaxAgentEnvValue || strings.ContainsRune(e.Value, 0) {
			return deny(HandlerNever, VerdictEnv), true
		}
	}
	if privileged[harnessdef.LauncherName(cmd.Program)] || privileged[harnessdef.LauncherName(cmd.Resolved)] {
		return deny(HandlerNever, VerdictPrivilege), true
	}
	line := cmd.Line
	if reason := policy.ForbiddenWith(line, l.c.Protected, l.c.Branch, policy.NeverOptions{CoordinatorMerge: l.c.CoordinatorMerge, MergeBase: l.c.MergeBase}); reason != "" {
		return deny(HandlerNever, VerdictNeverList), true
	}
	if code := NeverList(line); code != "" {
		return deny(HandlerNever, code), true
	}
	// agentflow itself, started by its resolved path under another name.
	if args, ok := agentflowArgs(append([]string{cmd.Resolved}, cmd.Args...)); ok && !readOnlyInvocation(args) {
		return deny(HandlerNever, VerdictAgentflowInSession), true
	}
	if l.namesProtected(cmd, env) {
		return deny(HandlerNever, VerdictAgentflowHome), true
	}
	switch {
	case isShellOrWrapper(cmd.Program, cmd.Args) || isShellOrWrapper(cmd.Resolved, cmd.Args):
		r.PersonOnly, r.Reason = true, ReasonShell
	case l.insideWorktree(cmd.Resolved) || l.insideWorktree(cmd.Program):
		r.PersonOnly, r.Reason = true, ReasonRepositoryProgram
	default:
		if r.Reason == "" {
			r.Reason = ReasonTerminal
		}
	}
	for _, words := range boundary.SimpleCommands(line) {
		if len(words) > 0 && isShellOrWrapper(words[0], words[1:]) {
			r.PersonOnly, r.Reason = true, ReasonShell
		}
	}
	return Verdict{}, false
}

func (l *neverLink) insideWorktree(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if _, ok := relOf(l.w.real, resolvedPath(p)); ok {
		return true
	}
	_, ok := relOf(l.w.root, filepath.Clean(p))
	return ok
}

// namesProtected reports whether the program, any argument, the cwd or any
// env value names a path under a protected folder (agentflow_home). Each
// word, and each NAME=value and --flag=value part, is expanded (~, $HOME,
// ${HOME}, $AGENTFLOW_HOME, ${AGENTFLOW_HOME}), cleaned, made absolute
// against cwd and resolved where it exists, then compared by path
// component with both the given and the resolved protected folders. It is
// a text heuristic: a path built at run time or a script file evades it.
func (l *neverLink) namesProtected(cmd *Command, env []acp.EnvVariable) bool {
	words := []string{cmd.Program, cmd.Resolved, cmd.Cwd.Abs}
	words = append(words, cmd.Args...)
	for _, sc := range boundary.SimpleCommands(cmd.Line) {
		words = append(words, sc...)
	}
	for _, e := range env {
		words = append(words, e.Value)
	}
	var protect []string
	for _, p := range l.c.Protect {
		if p == "" {
			continue
		}
		protect = append(protect, filepath.Clean(p), resolvedPath(p))
	}
	if len(protect) == 0 {
		return false
	}
	cwd := cmd.Cwd.Abs
	for _, w := range words {
		for _, part := range wordParts(w) {
			for _, cand := range l.expand(part) {
				if !filepath.IsAbs(cand) {
					if cwd == "" {
						continue
					}
					cand = filepath.Join(cwd, cand)
				}
				cand = filepath.Clean(cand)
				forms := []string{cand}
				if real, err := filepath.EvalSymlinks(cand); err == nil {
					forms = append(forms, real)
				} else if parent, err := filepath.EvalSymlinks(filepath.Dir(cand)); err == nil {
					forms = append(forms, filepath.Join(parent, filepath.Base(cand)))
				}
				for _, f := range forms {
					for _, p := range protect {
						if _, ok := relOf(p, f); ok {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// wordParts: the word, the value of NAME=value or --flag=value, and every
// part separated by the path-list separator.
func wordParts(w string) []string {
	out := []string{w}
	if i := strings.IndexByte(w, '='); i >= 0 {
		out = append(out, w[i+1:])
	}
	for _, p := range slices.Clone(out) {
		if strings.ContainsRune(p, os.PathListSeparator) {
			out = append(out, filepath.SplitList(p)...)
		}
	}
	return out
}

// expand returns the forms of w after expanding ~ and the variables.
func (l *neverLink) expand(w string) []string {
	if w == "" {
		return nil
	}
	forms := []string{w}
	homes := l.c.Expand["HOME"]
	if strings.HasPrefix(w, "~") {
		rest := strings.TrimPrefix(w, "~")
		if rest == "" || rest[0] == '/' || rest[0] == filepath.Separator {
			for _, h := range homes {
				forms = append(forms, h+rest)
			}
		}
	}
	for _, name := range []string{"HOME", "AGENTFLOW_HOME"} {
		for _, v := range l.c.Expand[name] {
			for _, f := range slices.Clone(forms) {
				g := strings.ReplaceAll(f, "${"+name+"}", v)
				g = replaceVar(g, "$"+name, v)
				if g != f {
					forms = append(forms, g)
				}
			}
		}
	}
	return forms
}

// replaceVar replaces $NAME where it is not followed by another name
// character.
func replaceVar(s, ref, v string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, ref)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(ref)
		if end < len(s) && (s[end] == '_' || s[end] >= 'A' && s[end] <= 'Z' || s[end] >= 'a' && s[end] <= 'z' || s[end] >= '0' && s[end] <= '9') {
			b.WriteString(s[:end])
			s = s[end:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteString(v)
		s = s[end:]
	}
}
