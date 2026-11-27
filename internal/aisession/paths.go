package aisession

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// ---- worktree-relative globs ----

// MatchGlob reports whether the slash path rel matches pattern: path.Match
// per segment, with "**" matching any number of segments (none included).
// A pattern without a slash matches the base name at any depth only when
// it starts with "**/"; otherwise it is anchored at the worktree root.
func MatchGlob(pattern, rel string, fold bool) bool {
	if fold {
		pattern, rel = strings.ToLower(pattern), strings.ToLower(rel)
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// ValidGlob reports whether pattern compiles.
func ValidGlob(pattern string) bool {
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return false
		}
	}
	return pattern != ""
}

// foldCase: macOS and Windows compare paths without case.
func foldCase() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

// ControlFiles are the worktree-relative globs whose writes always ask a
// person (PersonOnly, reason control_file): files that control execution
// or approval.
var ControlFiles = []string{
	".agentflow/**", ".claude/**", ".mcp.json", ".codex/**", ".gemini/**", ".cursor/**",
	".vscode/**", ".idea/**", ".githooks/**", ".husky/**", ".github/workflows/**",
	".github/actions/**", ".envrc", ".pre-commit-config.yaml", "lefthook.yml",
	"**/AGENTS.md", "**/CLAUDE.md", "**/GEMINI.md",
}

// SecretFiles are the globs whose reads always ask a person (PersonOnly,
// reason secret_file).
var SecretFiles = []string{
	"**/.env", "**/.env.*", "**/*.pem", "**/*.key", "**/*.p12", "**/*.pfx",
	"**/id_rsa*", "**/id_ed25519*", "**/.npmrc", "**/.pypirc", "**/.netrc",
}

// IsControlFile reports whether rel is a control file.
func IsControlFile(rel string) bool { return matchAny(ControlFiles, rel) }

// IsSecretFile reports whether rel is a secret file.
func IsSecretFile(rel string) bool { return matchAny(SecretFiles, rel) }

func matchAny(globs []string, rel string) bool {
	if rel == "" {
		return false
	}
	for _, g := range globs {
		if MatchGlob(g, rel, foldCase()) {
			return true
		}
	}
	return false
}

// ---- resolving a path against the worktree ("Phase 7 file access") ----

// worktree is the session's worktree root: as given and resolved.
type worktree struct {
	root string // cleaned absolute, as the session record has it
	real string // links resolved
}

func newWorktree(root string) (worktree, error) {
	if !filepath.IsAbs(root) {
		return worktree{}, errors.New("aisession: the worktree root must be absolute")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return worktree{}, err
	}
	return worktree{root: filepath.Clean(root), real: real}, nil
}

// relOf returns the slash path of p below base, or ok false when p is not
// base or below it, compared by path component (ignoring case on macOS and
// Windows).
func relOf(base, p string) (string, bool) {
	b, q := base, p
	if foldCase() {
		b, q = strings.ToLower(b), strings.ToLower(q)
	}
	r, err := filepath.Rel(b, q)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	if r == "." {
		return "", true
	}
	// Rel on the folded strings gives folded text; take the original
	// components from p.
	parts := strings.Split(r, string(filepath.Separator))
	orig := strings.Split(filepath.Clean(p), string(filepath.Separator))
	return strings.Join(orig[len(orig)-len(parts):], "/"), true
}

// resolve applies checks 1 to 5 of "Phase 7 file access" to an asked path
// and returns the Path, or a verdict code. Check 6 (os.Root at the moment
// of use) and 7 (the operation's own checks) are fs.go's.
func (w worktree) resolve(asked string) (Path, string) {
	p := Path{Abs: asked}
	if asked == "" || !filepath.IsAbs(asked) || strings.ContainsRune(asked, 0) {
		return p, VerdictRelative
	}
	clean := filepath.Clean(asked)
	p.Abs = clean
	rel, ok := relOf(w.real, clean)
	if !ok {
		rel, ok = relOf(w.root, clean)
	}
	if !ok {
		return p, VerdictOutsideWorktree
	}
	p.Rel = rel
	if rel == "" {
		// The root itself is a directory: no fs request names it.
		return p, VerdictNotRegular
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if strings.EqualFold(s, ".git") {
			return p, VerdictGitDir
		}
		if runtime.GOOS == "windows" && badWindowsName(s) {
			return p, VerdictName
		}
	}
	// No directory on the way below the root holds a .git entry.
	dir := w.real
	for _, s := range segs[:len(segs)-1] {
		dir = filepath.Join(dir, s)
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return p, VerdictNestedRepository
		}
	}
	if real, err := filepath.EvalSymlinks(filepath.Join(w.real, filepath.FromSlash(rel))); err == nil {
		p.Real, p.Exists = real, true
		if _, inside := relOf(w.real, real); !inside {
			return p, VerdictOutsideWorktree
		}
	}
	return p, ""
}

var windowsDevices = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}

func badWindowsName(s string) bool {
	if strings.ContainsRune(s, ':') || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return true
	}
	base := strings.ToLower(s)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsDevices[base] {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9'
}

// display shows a resolved path: worktree-relative, or "~/..." outside.
func display(p Path) string {
	if p.Rel != "" {
		return p.Rel
	}
	return tilde(p.Abs)
}

// tilde shows a path under the OS home as "~/...".
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	for _, h := range []string{home, resolvedPath(home)} {
		if rel, ok := relOf(h, p); ok && rel != "" {
			return "~/" + rel
		}
	}
	return p
}

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
