// Package boundary keeps an agent session inside its own repository. A session
// may read other repositories but never changes them: it hands that work to a
// session in the other repository (see agentflow delegate). Check finds the
// repository each simple command in a shell command line acts on and refuses
// writes to any repository other than the session's home. See docs/repo-boundary.md.
package boundary

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/gitx"
)

// SessionEnv names the variable that carries the session's home repository (its
// common Git directory) to commands and git hooks.
const SessionEnv = "AGENTFLOW_SESSION_REPO"

// Repo identifies a repository.
type Repo struct {
	// CommonDir is the resolved common Git directory, shared by all worktrees.
	CommonDir string `json:"common_dir"`
	// Dir is the top level of the working tree, when there is one.
	Dir string `json:"dir,omitempty"`
	// Slug is the lower-case GitHub owner/name of origin, or "".
	Slug string `json:"slug,omitempty"`
}

// Name is the repository name: the name part of the slug, or the folder that
// holds the common Git directory.
func (r Repo) Name() string {
	if _, name, ok := strings.Cut(r.Slug, "/"); ok {
		return name
	}
	if filepath.Base(r.CommonDir) == ".git" {
		return filepath.Base(filepath.Dir(r.CommonDir))
	}
	if r.Dir != "" {
		return filepath.Base(r.Dir)
	}
	return strings.TrimSuffix(filepath.Base(r.CommonDir), ".git")
}

// Label names the repository for a person: its slug, or its folder.
func (r Repo) Label() string {
	if r.Slug != "" {
		return r.Slug
	}
	if filepath.Base(r.CommonDir) == ".git" {
		return filepath.Dir(r.CommonDir)
	}
	return r.CommonDir
}

// Same reports whether a and b are the same repository: the same common Git
// directory, or two checkouts of the same GitHub repository.
func Same(a, b Repo) bool {
	if a.CommonDir != "" && a.CommonDir == b.CommonDir {
		return true
	}
	return a.Slug != "" && a.Slug == b.Slug
}

// Resolver finds the repository that contains dir; ok is false outside one.
type Resolver func(dir string) (Repo, bool)

// GitResolver resolves folders with the git CLI and caches the answers.
func GitResolver(ctx context.Context) Resolver {
	cache := map[string]Repo{}
	miss := map[string]bool{}
	return func(dir string) (Repo, bool) {
		if r, ok := cache[dir]; ok {
			return r, true
		}
		if miss[dir] {
			return Repo{}, false
		}
		r, err := ResolveDir(ctx, dir)
		if err != nil {
			miss[dir] = true
			return Repo{}, false
		}
		cache[dir] = r
		return r, true
	}
}

// ResolveDir returns the repository that contains dir.
func ResolveDir(ctx context.Context, dir string) (Repo, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return Repo{}, fmt.Errorf("%s is not a folder", dir)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = cleanGitEnv()
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return Repo{}, fmt.Errorf("%s is not in a Git repository", dir)
	}
	r := Repo{CommonDir: realPath(common)}
	if top, err := git("rev-parse", "--show-toplevel"); err == nil {
		r.Dir = realPath(top)
	}
	if url, err := git("remote", "get-url", "origin"); err == nil {
		r.Slug = strings.ToLower(gitx.ParseGitHubRemote(url))
	}
	return r, nil
}

// cleanGitEnv drops variables that would make git ignore -C.
func cleanGitEnv() []string {
	env := os.Environ()
	return slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") || strings.HasPrefix(kv, "GIT_COMMON_DIR=")
	})
}

func realPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// Refusal explains why a command was refused.
type Refusal struct {
	// Command is the simple command that acts on another repository.
	Command string `json:"command"`
	Home    Repo   `json:"home"`
	Target  Repo   `json:"target"`
}

func (r *Refusal) Error() string { return r.Message() }

// Message says what was refused and how to hand the work off instead.
func (r *Refusal) Message() string {
	name := r.Target.Name()
	return fmt.Sprintf("`%s` changes %s, but this session belongs to %s. "+
		"A session changes only its own repository; reading others is allowed. "+
		"Hand the work to a session in %s instead: `agentflow delegate --repo %s \"<task>\"` "+
		"(MCP: agentflow_delegate) sends it to a session already working there or starts one in its checkout. "+
		"You can also message such a session through your client. See docs/repo-boundary.md in agentflow.",
		r.Command, r.Target.Label(), r.Home.Label(), r.Target.Label(), name)
}

// Checker checks command lines against the home repository.
type Checker struct {
	Home Repo
	// Cwd is the folder the command line starts in.
	Cwd string
	// Resolve finds the repository of a folder.
	Resolve Resolver
	// Getenv reads the environment the command runs in (default os.Getenv).
	Getenv func(string) string
	// UserHome expands ~ and a bare cd (default os.UserHomeDir).
	UserHome string
}

// Check returns a refusal when cmd changes a repository other than the home one.
func (c *Checker) Check(cmd string) *Refusal {
	if c.Home.CommonDir == "" || c.Resolve == nil {
		return nil
	}
	if c.Getenv == nil {
		c.Getenv = os.Getenv
	}
	if c.UserHome == "" {
		c.UserHome, _ = os.UserHomeDir()
	}
	st := &state{c: c, cwd: c.Cwd, env: map[string]string{}}
	return st.line(cmd, 0)
}

type state struct {
	c     *Checker
	cwd   string
	stack []string
	env   map[string]string // exported in this command line
}

const maxDepth = 4

func (st *state) line(cmd string, depth int) *Refusal {
	if depth > maxDepth {
		return nil
	}
	lx := lex(cmd)
	for _, sub := range lx.subs {
		if r := st.fork().line(sub, depth+1); r != nil {
			return r
		}
	}
	var words []string
	skipNext := false
	flush := func() *Refusal {
		w := words
		words = nil
		if len(w) == 0 {
			return nil
		}
		return st.simple(w, depth)
	}
	for _, t := range lx.tokens {
		if t.op == "" {
			if skipNext {
				skipNext = false
				continue
			}
			words = append(words, t.word)
			continue
		}
		if t.op == "redir" {
			skipNext = true
			continue
		}
		if r := flush(); r != nil {
			return r
		}
		switch t.op {
		case "(":
			st.stack = append(st.stack, st.cwd)
		case ")":
			if n := len(st.stack); n > 0 {
				st.cwd, st.stack = st.stack[n-1], st.stack[:n-1]
			}
		}
	}
	return flush()
}

func (st *state) fork() *state {
	env := map[string]string{}
	for k, v := range st.env {
		env[k] = v
	}
	return &state{c: st.c, cwd: st.cwd, env: env}
}

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// simple checks one simple command.
func (st *state) simple(words []string, depth int) *Refusal {
	env := map[string]string{}
	for len(words) > 0 && assignRe.MatchString(words[0]) {
		k, v, _ := strings.Cut(words[0], "=")
		env[k] = v
		words = words[1:]
	}
	words = stripWrappers(words, env)
	if len(words) == 0 {
		return nil
	}
	lookup := func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		if v, ok := st.env[k]; ok {
			return v
		}
		return st.c.Getenv(k)
	}
	name, args := filepath.Base(words[0]), words[1:]
	switch name {
	case "cd", "pushd":
		if name == "pushd" {
			st.stack = append(st.stack, st.cwd)
		}
		st.cwd = st.dir(firstArg(args), lookup)
	case "popd":
		if n := len(st.stack); n > 0 {
			st.cwd, st.stack = st.stack[n-1], st.stack[:n-1]
		}
	case "export":
		for _, a := range args {
			if k, v, ok := strings.Cut(a, "="); ok {
				st.env[k] = v
			}
		}
	case "bash", "sh", "zsh", "dash":
		for i, a := range args {
			if a == "-c" && i+1 < len(args) {
				return st.fork().line(args[i+1], depth+1)
			}
		}
	case "eval":
		return st.fork().line(strings.Join(args, " "), depth+1)
	case "git":
		return st.git(args, lookup, strings.Join(words, " "))
	case "gh":
		return st.gh(args, lookup, strings.Join(words, " "))
	}
	return nil
}

// stripWrappers drops command prefixes such as env, sudo and timeout.
func stripWrappers(words []string, env map[string]string) []string {
	for len(words) > 0 {
		switch filepath.Base(words[0]) {
		case "env":
			words = stripEnv(words[1:], env)
			continue
		case "command", "builtin", "exec", "time", "nohup", "nice", "sudo", "xargs":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
		case "timeout":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
			if len(words) > 0 {
				words = words[1:] // the duration
			}
		default:
			return words
		}
	}
	return words
}

// stripEnv drops the options and assignments of an env prefix.
func stripEnv(words []string, env map[string]string) []string {
	for len(words) > 0 {
		switch {
		case words[0] == "-u" || words[0] == "-C" || words[0] == "-S":
			words = words[min(2, len(words)):]
		case strings.HasPrefix(words[0], "-"):
			words = words[1:]
		case assignRe.MatchString(words[0]):
			k, v, _ := strings.Cut(words[0], "=")
			env[k] = v
			words = words[1:]
		default:
			return words
		}
	}
	return words
}

func firstArg(args []string) string {
	for _, a := range args {
		if a == "--" || (strings.HasPrefix(a, "-") && a != "-") {
			continue
		}
		return a
	}
	return ""
}

var envRefRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

// dir resolves a folder argument against the current folder.
func (st *state) dir(p string, lookup func(string) string) string {
	if p == "" || p == "~" {
		return st.c.UserHome
	}
	if p == "-" {
		return st.cwd
	}
	p = envRefRe.ReplaceAllStringFunc(p, func(m string) string {
		return lookup(envRefRe.FindStringSubmatch(m)[1])
	})
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(st.c.UserHome, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(st.cwd, p)
	}
	return filepath.Clean(p)
}

func (st *state) refuse(cmd string, target Repo) *Refusal {
	return &Refusal{Command: cmd, Home: st.c.Home, Target: target}
}

// ---- git ----

// gitReads are subcommands that never change the repository.
var gitReads = map[string]bool{
	"status": true, "log": true, "show": true, "diff": true, "rev-parse": true, "ls-files": true,
	"ls-tree": true, "ls-remote": true, "blame": true, "annotate": true, "grep": true, "describe": true,
	"cat-file": true, "shortlog": true, "rev-list": true, "merge-base": true, "show-ref": true,
	"for-each-ref": true, "name-rev": true, "show-branch": true, "whatchanged": true, "range-diff": true,
	"diff-tree": true, "diff-files": true, "diff-index": true, "count-objects": true, "check-ignore": true,
	"check-attr": true, "check-ref-format": true, "var": true, "help": true, "version": true,
	"verify-commit": true, "verify-tag": true, "cherry": true, "archive": true,
}

// gitAnywhere are subcommands that do not act on an existing repository.
var gitAnywhere = map[string]bool{"clone": true, "init": true, "help": true, "version": true}

func (st *state) git(args []string, lookup func(string) string, cmd string) *Refusal {
	dir := st.cwd
	gitDir, workTree := lookup("GIT_DIR"), lookup("GIT_WORK_TREE")
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		a := args[i]
		switch {
		case a == "-C" && i+1 < len(args):
			i++
			dir = filepath.Clean(resolveAgainst(dir, st.expand(args[i], lookup)))
		case (a == "--git-dir" || a == "--work-tree" || a == "-c" || a == "--namespace" || a == "--config-env") && i+1 < len(args):
			i++
			switch a {
			case "--git-dir":
				gitDir = args[i]
			case "--work-tree":
				workTree = args[i]
			}
		case strings.HasPrefix(a, "--git-dir="):
			gitDir = strings.TrimPrefix(a, "--git-dir=")
		case strings.HasPrefix(a, "--work-tree="):
			workTree = strings.TrimPrefix(a, "--work-tree=")
		case a == "--version" || a == "--help" || a == "-h":
			return nil
		}
	}
	if i >= len(args) {
		return nil
	}
	sub, rest := args[i], args[i+1:]
	if gitAnywhere[sub] {
		return nil
	}
	target := dir
	switch {
	case gitDir != "":
		target = resolveAgainst(dir, st.expand(gitDir, lookup))
	case workTree != "":
		target = resolveAgainst(dir, st.expand(workTree, lookup))
	}
	repo, ok := st.c.Resolve(filepath.Clean(target))
	if !ok {
		return nil
	}
	if !Same(repo, st.c.Home) {
		if gitRead(sub, rest) {
			return nil
		}
		return st.refuse(cmd, repo)
	}
	if sub == "push" {
		if slug := pushURLSlug(rest); slug != "" && st.c.Home.Slug != "" && slug != st.c.Home.Slug {
			return st.refuse(cmd, Repo{Slug: slug})
		}
	}
	return nil
}

func (st *state) expand(p string, lookup func(string) string) string {
	p = envRefRe.ReplaceAllStringFunc(p, func(m string) string {
		return lookup(envRefRe.FindStringSubmatch(m)[1])
	})
	if p == "~" {
		return st.c.UserHome
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(st.c.UserHome, p[2:])
	}
	return p
}

func resolveAgainst(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// gitRead reports whether a git subcommand with these arguments only reads.
func gitRead(sub string, args []string) bool {
	if gitReads[sub] {
		return true
	}
	has := func(flags ...string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return true
				}
			}
		}
		return false
	}
	positional := 0
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			positional++
		}
	}
	switch sub {
	case "branch":
		if has("-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-u", "--set-upstream-to", "--unset-upstream", "-f", "--force", "--edit-description", "-t", "--track") {
			return false
		}
		return positional == 0 || has("-l", "--list", "-a", "--all", "-r", "--remotes", "--contains", "--merged", "--no-merged", "--points-at")
	case "tag":
		if has("-d", "--delete", "-a", "--annotate", "-s", "--sign", "-f", "--force", "-m", "--message", "-F", "--file", "-u") {
			return false
		}
		return positional == 0 || has("-l", "--list", "--contains", "--points-at", "--merged", "--no-merged", "-v", "--verify")
	case "remote":
		return len(args) == 0 || args[0] == "-v" || args[0] == "--verbose" || args[0] == "show" || args[0] == "get-url"
	case "config":
		if has("--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--show-origin", "--show-scope") {
			return !has("--unset", "--unset-all", "--add", "--replace-all", "--rename-section", "--remove-section", "-e", "--edit")
		}
		if len(args) > 0 && (args[0] == "get" || args[0] == "list") {
			return true
		}
		return positional == 1 && !has("--unset", "--unset-all", "--add", "--replace-all", "--rename-section", "--remove-section", "-e", "--edit")
	case "stash":
		return len(args) > 0 && (args[0] == "list" || args[0] == "show")
	case "worktree":
		return len(args) > 0 && args[0] == "list"
	case "notes":
		return len(args) > 0 && (args[0] == "list" || args[0] == "show")
	case "submodule":
		return len(args) > 0 && (args[0] == "status" || args[0] == "summary")
	case "reflog":
		return len(args) == 0 || args[0] == "show" || args[0] == "exists" || !slices.Contains([]string{"expire", "delete"}, args[0])
	case "symbolic-ref":
		return positional <= 1 && !has("-d", "--delete")
	case "fsck":
		return true
	}
	return false
}

var githubURLRe = regexp.MustCompile(`(?i)(?:^|[/@:])github\.com[:/]+([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?(?:[/#?]|$)`)

// slugFromURL returns the lower-case owner/name of a GitHub URL in s, or "".
func slugFromURL(s string) string {
	m := githubURLRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1] + "/" + m[2])
}

// pushURLSlug returns the GitHub repository a `git push URL` names, or "".
func pushURLSlug(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return slugFromURL(a) // only the first positional is the repository
	}
	return ""
}

// ---- gh ----

// ghReadVerbs are gh subcommand verbs that only read.
var ghReadVerbs = map[string]bool{
	"view": true, "list": true, "ls": true, "diff": true, "checks": true, "status": true,
	"clone": true, "download": true, "watch": true, "verify": true, "check": true,
}

// ghUnscoped are gh command groups that do not act on a repository.
var ghUnscoped = map[string]bool{
	"auth": true, "config": true, "alias": true, "completion": true, "extension": true, "ext": true,
	"gist": true, "gpg-key": true, "ssh-key": true, "help": true, "version": true, "codespace": true,
	"org": true, "status": true, "search": true, "browse": true, "copilot": true, "attestation": true,
	"licenses": true, "preview": true,
}

// ghValued are gh flags that take a separate value, so their value is not a positional.
var ghValued = map[string]bool{
	"-R": true, "--repo": true, "-X": true, "--method": true, "-f": true, "-F": true, "--field": true,
	"--raw-field": true, "-H": true, "--header": true, "--input": true, "-q": true, "--jq": true,
	"-t": true, "--template": true, "--hostname": true, "-b": true, "--body": true, "--body-file": true,
	"--title": true, "-B": true, "--base": true, "--head": true, "-l": true, "--label": true,
	"-a": true, "--assignee": true, "-m": true, "--milestone": true, "--json": true, "-s": true,
	"--state": true, "-L": true, "--limit": true, "--subject": true, "-A": true, "--author-email": true,
	"--match-head-commit": true, "-c": true, "--comment": true, "-r": true, "--reason": true,
	"--cache": true, "-p": true, "--project": true, "--author": true, "--search": true,
}

var apiRepoRe = regexp.MustCompile(`^/?repos/([^/?#]+)/([^/?#]+)`)

func (st *state) gh(args []string, lookup func(string) string, cmd string) *Refusal {
	explicit := ""
	method := ""
	fields := false
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-R" || a == "--repo":
			if i+1 < len(args) {
				i++
				explicit = args[i]
			}
			continue
		case strings.HasPrefix(a, "--repo="):
			explicit = strings.TrimPrefix(a, "--repo=")
			continue
		case strings.HasPrefix(a, "-R") && len(a) > 2:
			explicit = a[2:]
			continue
		case a == "-X" || a == "--method":
			if i+1 < len(args) {
				i++
				method = strings.ToUpper(args[i])
			}
			continue
		case strings.HasPrefix(a, "--method="):
			method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
			continue
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			method = strings.ToUpper(a[2:])
			continue
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input" ||
			strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input="):
			fields = true
		}
		if strings.HasPrefix(a, "-") {
			if ghValued[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) == 0 || ghUnscoped[positional[0]] {
		return nil
	}
	group := positional[0]
	write := false
	var target Repo
	targetKnown := false
	if group == "api" {
		write = (method != "" && method != "GET" && method != "HEAD") || (method == "" && fields)
		if len(positional) > 1 {
			if m := apiRepoRe.FindStringSubmatch(positional[1]); m != nil && !strings.HasPrefix(m[1], "{") {
				target, targetKnown = Repo{Slug: strings.ToLower(m[1] + "/" + strings.TrimSuffix(m[2], ".git"))}, true
			} else if m == nil {
				return nil // not repository-scoped (graphql, /user, /orgs/...)
			}
		}
	} else {
		verb := ""
		if len(positional) > 1 {
			verb = positional[1]
		}
		write = !ghReadVerbs[verb]
		// gh repo create|fork|delete|rename|edit NAME acts on the repository it names.
		if group == "repo" && len(positional) > 2 && write {
			target, targetKnown = Repo{Slug: ghSlug(positional[2])}, true
		}
	}
	if !write {
		return nil
	}
	if !targetKnown {
		switch {
		case explicit != "":
			target, targetKnown = Repo{Slug: ghSlug(explicit)}, true
		case lookup("GH_REPO") != "":
			target, targetKnown = Repo{Slug: ghSlug(lookup("GH_REPO"))}, true
		default:
			for _, p := range positional[1:] {
				if slug := slugFromURL(p); slug != "" {
					target, targetKnown = Repo{Slug: slug}, true
					break
				}
			}
		}
	}
	if !targetKnown {
		repo, ok := st.c.Resolve(st.cwd)
		if !ok {
			return nil
		}
		target = repo
	}
	if st.sameAsHome(target) {
		return nil
	}
	return st.refuse(cmd, target)
}

// ghSlug normalises [HOST/]OWNER/REPO or a URL to lower-case owner/name.
func ghSlug(s string) string {
	if slug := slugFromURL(s); slug != "" {
		return slug
	}
	s = strings.TrimSuffix(strings.Trim(strings.ToLower(s), "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

// sameAsHome compares a target given by slug with the home repository.
func (st *state) sameAsHome(t Repo) bool {
	home := st.c.Home
	if t.CommonDir != "" {
		return Same(t, home)
	}
	if home.Slug == "" {
		_, name, ok := strings.Cut(t.Slug, "/")
		if !ok {
			name = t.Slug
		}
		return strings.EqualFold(name, home.Name())
	}
	if !strings.Contains(t.Slug, "/") {
		return strings.EqualFold(t.Slug, home.Name())
	}
	return t.Slug == home.Slug
}
