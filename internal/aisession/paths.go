package aisession

import (
	"errors"
	"io/fs"
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

// foldCase: macOS and Windows compare paths without case. A variable so a
// test on Linux can run the folding platforms' rules.
var foldCase = func() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

// nonASCII reports whether a slash path has a component with a rune past
// ASCII. On macOS and Windows the file system folds characters that
// strings.ToLower does not (APFS opens id_r\u017fa as id_rsa, .agentflow
// with the fl ligature as .agentflow), so no glob or list here can tell what
// such a name opens: only a person may allow it (ReasonUnicodeName).
func nonASCII(rel string) bool {
	for i := 0; i < len(rel); i++ {
		if rel[i] >= 0x80 {
			return true
		}
	}
	return false
}

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
// and returns the Path, or a verdict code. Checks 3 to 5 run on the
// lexical path and again on the resolved one (links followed; for a path
// that does not exist yet, its nearest existing parent and the rest), so a
// link inside the worktree that points into a .git folder or a nested
// repository is refused too. Check 6 (os.Root at the moment of use) and 7
// (the operation's own checks) are fs.go's.
func (w worktree) resolve(asked string) (Path, string) {
	p := Path{Abs: asked}
	if asked == "" || !filepath.IsAbs(asked) || strings.ContainsRune(asked, 0) {
		return p, VerdictRelative
	}
	clean := filepath.Clean(asked)
	p.Abs = clean
	rel, ok := w.lexical(clean)
	if !ok {
		return p, VerdictOutsideWorktree
	}
	p.Rel = rel
	if rel == "" {
		// The root itself is a directory: no fs request names it.
		return p, VerdictNotRegular
	}
	if code := w.checkRel(rel); code != "" {
		return p, code
	}
	full := filepath.Join(w.real, filepath.FromSlash(rel))
	if real, err := filepath.EvalSymlinks(full); err == nil {
		p.Real, p.Exists = real, true
		if info, err := os.Stat(real); err == nil {
			p.info = pinned(info)
		}
	}
	if info, err := os.Stat(filepath.Dir(full)); err == nil && info.IsDir() {
		p.dir = pinned(info)
	}
	resolved, inside := w.resolvedRel(rel)
	if !inside {
		return p, VerdictOutsideWorktree
	}
	if resolved != rel && resolved != "" {
		if code := w.checkRel(resolved); code != "" {
			return p, code
		}
	}
	return p, ""
}

// lexical is p's worktree-relative slash path as spelled.
func (w worktree) lexical(p string) (string, bool) {
	if rel, ok := relOf(w.real, p); ok {
		return rel, true
	}
	return relOf(w.root, p)
}

// checkRel runs checks 3 to 5 on a worktree-relative slash path.
func (w worktree) checkRel(rel string) string {
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if strings.EqualFold(s, ".git") {
			return VerdictGitDir
		}
		if runtime.GOOS == "windows" && badWindowsName(s) {
			return VerdictName
		}
	}
	// No directory on the way below the root holds a .git entry.
	dir := w.real
	for _, s := range segs[:len(segs)-1] {
		dir = filepath.Join(dir, s)
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return VerdictNestedRepository
		}
	}
	return ""
}

// resolvedRel is where rel leads with every link followed: the resolved
// path when it exists, else its nearest existing parent resolved plus the
// rest. inside is false when that leaves the worktree.
func (w worktree) resolvedRel(rel string) (string, bool) {
	full := filepath.Join(w.real, filepath.FromSlash(rel))
	tail := ""
	for cur := full; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			r, ok := relOf(w.real, filepath.Join(real, tail))
			return r, ok
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// forms are the worktree-relative slash paths p may name: as spelled and
// as resolved now. Every classification (secret, control, rule) uses all
// of them and the strictest answer wins. outside is true when the
// resolved form leaves the worktree.
func (w worktree) forms(p Path) (forms []string, outside bool) {
	if !filepath.IsAbs(p.Abs) {
		return nil, true
	}
	lex, ok := w.lexical(filepath.Clean(p.Abs))
	if !ok {
		return nil, true
	}
	forms = []string{lex}
	if lex == "" {
		return forms, false
	}
	res, inside := w.resolvedRel(lex)
	if !inside {
		return forms, true
	}
	if res != lex && res != "" {
		forms = append(forms, res)
	}
	return forms, false
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

// pinned loads info's file identity now. On Windows os.Stat reads the file
// ID lazily, from the path, the first time os.SameFile compares it; a later
// comparison would then read whatever the path names at that moment.
func pinned(info fs.FileInfo) fs.FileInfo {
	_ = os.SameFile(info, info)
	return info
}
