// Package goldentest pins the exact output of an operation in golden files.
//
// A test renders its output as text, replaces the parts that change from run to
// run (times, tokens, temporary paths, object IDs) with fixed placeholders and
// calls Check. Run a package's tests with -update to rewrite its golden files,
// then review the diff.
//
// Golden files live in testdata/golden next to the test that checks them.
package goldentest

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files from the current output instead of comparing")

// root is the directory of the package under test. Tests change directory, so
// it is captured before any test runs.
var root = func() string {
	dir, _ := os.Getwd()
	return dir
}()

// Check compares got with testdata/golden/name, or rewrites that file under -update.
func Check(t testing.TB, name, got string) {
	t.Helper()
	if !strings.HasSuffix(got, "\n") {
		got += "\n"
	}
	path := filepath.Join(root, "testdata", "golden", filepath.FromSlash(name))
	shown := filepath.ToSlash(filepath.Join("testdata", "golden", name))
	want, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		t.Fatalf("read golden file %s: %v", shown, err)
	}
	if *update {
		if !missing && string(want) == got {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", shown)
		return
	}
	if missing {
		t.Fatalf("missing golden file %s; run this package's tests with -update to create it", shown)
	}
	if string(want) != got {
		t.Errorf("output differs from %s (- golden, + got); if the change is intended, run this package's tests with -update and review the diff\n%s", shown, diff(string(want), got))
	}
}

// ambient lists the settings from the environment that change what an
// operation prints or does: CI and GitHub Actions variables (which select the
// operation ID of a write), tokens and credentials, and the agent session.
var ambient = []string{
	"CI", "GITHUB_ACTIONS", "GITHUB_REPOSITORY", "GITHUB_REPOSITORY_ID", "GITHUB_SERVER_URL",
	"GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_JOB", "GITHUB_EVENT_NAME", "GITHUB_EVENT_PATH",
	"GITHUB_TOKEN", "GH_TOKEN", "GH_REPO",
	"AGENTFLOW_OPERATION_ID", "AGENTFLOW_DEFAULT_BRANCH", "AGENTFLOW_ALLOW_PROMOTE",
	"AGENTFLOW_SECOPS_TOKEN", "AGENTFLOW_APPROVED_ISSUE",
	"AGENT_COMMANDER_HOME", "AGENT_COMMANDER_ROLE", "AGENT_COMMANDER_RUN_ID",
	"AGENT_COMMANDER_SOCKET", "AGENT_COMMANDER_TOKEN", "AGENT_COMMANDER_WORKER_ID",
	"CLAUDE_PROJECT_DIR", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDECODE",
	"CODEX_HOME", "TMUX_PANE", "FLOW_MAP", "GIT_AUTHOR_NAME",
	"JIRA_USER", "JIRA_TOKEN", "JIRA_BASE", "CONFLUENCE_USER", "CONFLUENCE_TOKEN", "ASANA_ACCESS_TOKEN",
}

// Isolate makes the test independent of the machine it runs on. Call it first,
// before the test sets the few variables it needs: it unsets every variable in
// ambient, gives the agent a fixed session so the prompt owner is stable, and
// keeps git and the update check from reading the developer's configuration.
func Isolate(t testing.TB) {
	t.Helper()
	for _, name := range ambient {
		// Setenv registers the restore; the unset then leaves the variable absent,
		// which differs from empty for code that tests presence.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_THREAD_ID", "golden-session")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_NO_UPDATE", "1")
}

// Normalizer replaces the volatile parts of an output with stable placeholders.
// One Normalizer serves one test, so object IDs are numbered from the first one
// it sees and repeated IDs keep their number.
type Normalizer struct {
	paths  [][2]string
	words  [][2]string
	elided []elision
	ids    map[string]string
}

// elision hides the value of one JSON key: an array when open is '[', an object when it is '{'.
type elision struct {
	key         string
	open, close byte
}

// New returns a Normalizer with the built-in rules for times, confirmation
// tokens, approval IDs, prompt owners and object IDs.
func New() *Normalizer { return &Normalizer{ids: map[string]string{}} }

// Path replaces dir, and dir with symbolic links resolved, with placeholder.
// Git reports resolved paths, while t.TempDir may not (macOS /var is a link).
// The directory need not exist yet: its nearest existing parent is resolved.
func (n *Normalizer) Path(placeholder, dir string) *Normalizer {
	if dir == "" {
		return n
	}
	forms := []string{dir}
	if resolved := resolve(dir); resolved != dir {
		forms = append(forms, resolved)
	}
	for _, form := range forms {
		n.paths = append(n.paths, [2]string{form, placeholder})
	}
	return n
}

// resolve follows symbolic links in the part of path that exists.
func resolve(path string) string {
	var rest []string
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return path
		}
		rest = append(rest, filepath.Base(dir))
	}
}

// Word replaces word wherever it stands alone, for example a directory name
// that a path rule cannot reach because only its base name is printed.
func (n *Normalizer) Word(placeholder, word string) *Normalizer {
	if word != "" {
		n.words = append(n.words, [2]string{word, placeholder})
	}
	return n
}

// ElideArray replaces every array that is the value of key with "<elided>".
// Use it for a long catalogue that another test already pins, so that adding
// an entry does not change every golden file that happens to print it.
func (n *Normalizer) ElideArray(key string) *Normalizer {
	n.elided = append(n.elided, elision{key, '[', ']'})
	return n
}

// ElideObject replaces every object that is the value of key with "<elided>".
func (n *Normalizer) ElideObject(key string) *Normalizer {
	n.elided = append(n.elided, elision{key, '{', '}'})
	return n
}

// elide replaces the value of e.key in indented JSON text.
func elide(s string, e elision) string {
	marker := `"` + e.key + `": ` + string(e.open)
	var out strings.Builder
	for {
		i := strings.Index(s, marker)
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		start := i + len(marker) - 1
		end := closing(s, start, e)
		if end < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:start])
		out.WriteString(`"<elided>"`)
		s = s[end+1:]
	}
}

// closing returns the index of the bracket that closes the one at start, or -1.
func closing(s string, start int, e elision) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case e.open:
			depth++
		case e.close:
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

var (
	timeRE      = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)`)
	clockRE     = regexp.MustCompile(`expires \d\d:\d\d:\d\d \S+`)
	tokenRE     = regexp.MustCompile(`cf_[A-Za-z0-9_-]{16,}`)
	operationRE = regexp.MustCompile(`op_[0-9a-f]{32}`)
	ownerRE     = regexp.MustCompile(`([A-Za-z0-9_.-]+)/[0-9a-f]{16}\b`)
	shortRE     = regexp.MustCompile(`("sha": ")[0-9a-f]{7,12}(")`)
	digestRE    = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	objectRE    = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
)

// Text normalises s. Path rules run first, longest path first, so a nested
// directory is not hidden by its parent.
func (n *Normalizer) Text(s string) string {
	paths := append([][2]string(nil), n.paths...)
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i][0]) > len(paths[j][0]) })
	for _, p := range paths {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	for _, e := range n.elided {
		s = elide(s, e)
	}
	for _, w := range n.words {
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(w[0])+`\b`).ReplaceAllString(s, w[1])
	}
	s = timeRE.ReplaceAllString(s, "<time>")
	s = clockRE.ReplaceAllString(s, "expires <time>")
	s = tokenRE.ReplaceAllString(s, "<confirm-token>")
	s = operationRE.ReplaceAllString(s, "<operation-id>")
	s = ownerRE.ReplaceAllString(s, "$1/<owner>")
	s = shortRE.ReplaceAllString(s, "${1}<short-object>${2}")
	s = digestRE.ReplaceAllStringFunc(s, n.number("digest"))
	return objectRE.ReplaceAllStringFunc(s, n.number("object"))
}

func (n *Normalizer) number(kind string) func(string) string {
	return func(id string) string {
		if placeholder, ok := n.ids[id]; ok {
			return placeholder
		}
		placeholder := fmt.Sprintf("<%s-%d>", kind, len(n.ids)+1)
		n.ids[id] = placeholder
		return placeholder
	}
}

// Run is one CLI invocation and what it produced.
type Run struct {
	Command string // the command line as a person would type it
	Stdin   string
	Stdout  string
	Stderr  string
	Err     error
}

// Transcript renders a CLI invocation: the command line, then standard input,
// standard output, standard error and the returned error when each is present.
// Everything is normalised.
func (n *Normalizer) Transcript(r Run) string {
	var b strings.Builder
	section := func(name, text string) {
		fmt.Fprintf(&b, "--- %s\n%s", name, n.Text(text))
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "$ %s\n", n.Text(r.Command))
	if r.Stdin != "" {
		section("stdin", r.Stdin)
	}
	section("stdout", r.Stdout)
	if r.Stderr != "" {
		section("stderr", r.Stderr)
	}
	if r.Err != nil {
		section("error", r.Err.Error())
	}
	return b.String()
}

// diff lists the lines that differ between want and got in order, marking
// golden-only lines with "-" and new lines with "+". It matches lines with a
// longest common subsequence, which is cheap for files this small.
func diff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			fmt.Fprintf(&out, "+ %s\n", b[j])
			j++
		default:
			fmt.Fprintf(&out, "- %s\n", a[i])
			i++
		}
	}
	return out.String()
}
