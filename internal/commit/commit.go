// Package commit implements the Nscale commit convention:
//
//	type(KEY-1[,KEY-2])[!]: imperative summary [#verb ...]
//
// It parses and validates messages, suggests types, detects breaking changes,
// groups changes into logical commits and strips AI attribution trailers.
package commit

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/nscaledev/agentflow/internal/config"
)

// Verbs are the canonical flow-map verbs a commit may carry.
var Verbs = []string{"start", "review", "qa", "done", "block"}

var (
	headerRe  = regexp.MustCompile(`^([a-z]+)\(([A-Z][A-Z0-9]+-[0-9]+(?:,\s*[A-Z][A-Z0-9]+-[0-9]+)*)\)(!?): (.+)$`)
	scopeRe   = regexp.MustCompile(`^([a-z]+)(?:\(([^()]*)\))?(!?): (.+)$`)
	verbRe    = regexp.MustCompile(`(?:^|\s)#([a-z]+)\b`)
	commentRe = regexp.MustCompile(`#comment\s+([^#\n]+)`)
	timeRe    = regexp.MustCompile(`#time\s+((?:\d+[wdhm]\s*)+)`)
	skipRe    = regexp.MustCompile(`(?i)\[agentflow skip\]|^agentflow:\s*skip\s*$`)
	pastRe    = regexp.MustCompile(`^(added|fixed|updated|removed|changed|implemented|refactored|created|deleted|improved|renamed|moved)\b`)
)

// exempt subjects are produced by git itself and are always accepted.
var exemptPrefixes = []string{"Merge ", "Revert \"", "fixup! ", "squash! ", "amend! "}

// Message is a parsed commit message.
type Message struct {
	Raw          string
	Subject      string
	Type         string
	Keys         []string
	Breaking     bool
	Summary      string // subject summary without trailing #commands
	Body         string
	Verbs        []string // canonical flow verbs found anywhere in the message
	Comment      string   // Smart Commit #comment text
	Time         string   // Smart Commit #time value
	Skip         bool     // [agentflow skip] or Agentflow: skip trailer
	Exempt       bool     // merge/revert/fixup subjects
	Valid        bool     // header matched the convention with Jira keys
	Conventional bool     // header matched type(scope)?!?: summary, keys or not
	Scope        string   // scope text when it is not a list of keys
}

// Parse parses a full commit message. It never fails; check Valid and Validate.
func Parse(raw string) Message {
	raw = stripComments(raw)
	m := Message{Raw: raw}
	lines := strings.SplitN(strings.TrimSpace(raw), "\n", 2)
	m.Subject = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		m.Body = strings.TrimSpace(lines[1])
	}
	for _, p := range exemptPrefixes {
		if strings.HasPrefix(m.Subject, p) {
			m.Exempt = true
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		if skipRe.MatchString(strings.TrimSpace(line)) {
			m.Skip = true
		}
	}
	if h := headerRe.FindStringSubmatch(m.Subject); h != nil {
		m.Valid = true
		m.Type = h[1]
		for _, k := range strings.Split(h[2], ",") {
			m.Keys = append(m.Keys, strings.TrimSpace(k))
		}
		m.Breaking = h[3] == "!"
		m.Summary = strings.TrimSpace(trimCommands(h[4]))
		m.Conventional = true
	} else if h := scopeRe.FindStringSubmatch(m.Subject); h != nil {
		m.Conventional = true
		m.Type, m.Scope = h[1], h[2]
		m.Keys = KeysIn(h[2])
		m.Breaking = h[3] == "!"
		m.Summary = strings.TrimSpace(trimCommands(h[4]))
	}
	if strings.Contains(m.Body, "BREAKING CHANGE:") || strings.Contains(m.Body, "BREAKING-CHANGE:") {
		m.Breaking = true
	}
	for _, v := range verbRe.FindAllStringSubmatch(raw, -1) {
		if slices.Contains(Verbs, v[1]) && !slices.Contains(m.Verbs, v[1]) {
			m.Verbs = append(m.Verbs, v[1])
		}
	}
	if c := commentRe.FindStringSubmatch(raw); c != nil {
		m.Comment = strings.TrimSpace(c[1])
	}
	if t := timeRe.FindStringSubmatch(raw); t != nil {
		m.Time = strings.TrimSpace(t[1])
	}
	return m
}

func stripComments(raw string) string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "#comment") && !strings.HasPrefix(l, "#time") && !verbLine(l) {
			continue // git's commented template lines
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func verbLine(l string) bool {
	for _, v := range Verbs {
		if strings.HasPrefix(l, "#"+v) {
			return true
		}
	}
	return false
}

func trimCommands(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return s[:i]
	}
	return s
}

// Issue is a validation problem. Fix, when set, is a corrected subject.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

// Validate checks a message against the rules. A nil/empty result means valid.
// keyHint (usually from the branch) is used to build fixes for missing keys.
func Validate(raw string, rules config.Commit, keyHint string) []Issue {
	m := Parse(raw)
	if m.Exempt {
		return nil
	}
	var issues []Issue
	if m.Subject == "" {
		return []Issue{{Code: "empty", Message: "commit message is empty"}}
	}
	keylessOK := m.Conventional && !rules.KeyRequired()
	if !m.Valid && !keylessOK {
		iss := Issue{Code: "format", Message: "subject must look like: feat(PLAT-123): add quota API"}
		if !rules.KeyRequired() {
			iss.Message = "subject must look like: feat(scope): add quota API, or feat(PLAT-123): ..."
		}
		if fix := suggestHeader(m.Subject, rules, keyHint); fix != "" {
			iss.Fix = fix
		}
		return append(issues, iss)
	}
	if !slices.Contains(rules.Types, m.Type) {
		issues = append(issues, Issue{Code: "type", Message: fmt.Sprintf("unknown type %q; use one of %s", m.Type, strings.Join(rules.Types, ", "))})
	}
	max := rules.SubjectMax
	if max == 0 {
		max = 72
	}
	if n := len([]rune(m.Subject)); n > max {
		issues = append(issues, Issue{Code: "length", Message: fmt.Sprintf("subject is %d characters; keep it to %d", n, max)})
	}
	if m.Summary != "" && pastRe.MatchString(strings.ToLower(m.Summary)) {
		issues = append(issues, Issue{Code: "mood", Message: "use the imperative mood (\"add\", not \"added\")",
			Fix: strings.Replace(m.Subject, m.Summary, imperative(m.Summary), 1)})
	}
	if strings.HasSuffix(m.Summary, ".") {
		issues = append(issues, Issue{Code: "period", Message: "drop the trailing period from the subject"})
	}
	if m.Summary != "" && m.Summary != strings.ToLower(m.Summary[:1])+m.Summary[1:] && !startsWithAcronym(m.Summary) {
		issues = append(issues, Issue{Code: "case", Message: "start the summary with a lowercase letter"})
	}
	return issues
}

func startsWithAcronym(s string) bool {
	f := strings.Fields(s)
	return len(f) > 0 && len(f[0]) > 1 && strings.ToUpper(f[0]) == f[0]
}

var imperatives = map[string]string{
	"added": "add", "fixed": "fix", "updated": "update", "removed": "remove", "changed": "change",
	"implemented": "implement", "refactored": "refactor", "created": "create", "deleted": "delete",
	"improved": "improve", "renamed": "rename", "moved": "move",
}

func imperative(s string) string {
	f := strings.SplitN(s, " ", 2)
	if r, ok := imperatives[strings.ToLower(f[0])]; ok {
		f[0] = r
	}
	return strings.Join(f, " ")
}

var looseRe = regexp.MustCompile(`^(?:([a-z]+)(?:\(([^)]*)\))?(!)?:\s*)?(.+)$`)

// suggestHeader tries to turn a non-conforming subject into a valid one.
func suggestHeader(subject string, rules config.Commit, keyHint string) string {
	m := looseRe.FindStringSubmatch(subject)
	if m == nil {
		return ""
	}
	typ, scope, bang, summary := m[1], m[2], m[3], m[4]
	keys := KeysIn(scope + " " + summary)
	if len(keys) == 0 && keyHint != "" {
		keys = []string{keyHint}
	}
	if len(keys) == 0 {
		return ""
	}
	for _, k := range keys {
		summary = strings.TrimSpace(strings.ReplaceAll(summary, k, ""))
	}
	summary = strings.TrimLeft(summary, ":-– ")
	if summary == "" {
		return ""
	}
	if !slices.Contains(rules.Types, typ) {
		typ = TypeFromSummary(summary)
	}
	summary = imperative(strings.ToLower(summary[:1]) + summary[1:])
	summary = strings.TrimSuffix(summary, ".")
	return fmt.Sprintf("%s(%s)%s: %s", typ, strings.Join(keys, ","), bang, summary)
}

// SquashSuffixReserve is the room left in a PR title for the " (#1234)" GitHub appends
// when it squash-merges, so the resulting commit on the default branch still fits.
const SquashSuffixReserve = 8

// ValidatePRTitle validates a PR title as the subject of its future squash commit.
func ValidatePRTitle(title string, rules config.Commit, keyHint string) []Issue {
	max := rules.SubjectMax
	if max == 0 {
		max = 72
	}
	r := rules
	r.SubjectMax = max - SquashSuffixReserve
	issues := Validate(title, r, keyHint)
	for i := range issues {
		if issues[i].Code == "length" {
			issues[i].Message = fmt.Sprintf("PR title is %d characters; keep it to %d so the squash commit (title + \" (#123)\") fits in %d",
				len([]rune(strings.TrimSpace(title))), r.SubjectMax, max)
		}
	}
	return issues
}

// KeysIn returns unique Jira keys in s, in order of appearance.
func KeysIn(s string) []string {
	var keys []string
	for _, k := range regexp.MustCompile(`[A-Z][A-Z0-9]+-[0-9]+`).FindAllString(s, -1) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// TypeFromSummary guesses a type from summary wording.
func TypeFromSummary(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.HasPrefix(l, "fix"), strings.Contains(l, " bug"):
		return "fix"
	case strings.HasPrefix(l, "refactor"), strings.HasPrefix(l, "rename"), strings.HasPrefix(l, "move"):
		return "refactor"
	case strings.HasPrefix(l, "doc"):
		return "docs"
	case strings.HasPrefix(l, "test"):
		return "test"
	case strings.HasPrefix(l, "bump"), strings.HasPrefix(l, "upgrade"):
		return "chore"
	}
	return "feat"
}

// TypeFromFiles suggests a type from the set of changed paths.
func TypeFromFiles(files []string) string {
	if len(files) == 0 {
		return "feat"
	}
	counts := map[string]int{}
	for _, f := range files {
		counts[Category(f)]++
	}
	if counts["code"] > 0 {
		return "feat" // the agent should refine to fix/refactor/perf from the diff
	}
	best, n := "chore", 0
	for _, c := range []string{"test", "docs", "ci", "build", "chore"} {
		if counts[c] > n {
			best, n = c, counts[c]
		}
	}
	return best
}

// Category classifies a path into a commit-type bucket.
func Category(p string) string {
	base := path.Base(p)
	switch {
	case strings.HasPrefix(p, ".github/"), strings.HasPrefix(p, ".gitlab"), base == "Jenkinsfile":
		return "ci"
	case strings.HasSuffix(base, "_test.go"), strings.Contains(p, "/test/"), strings.HasPrefix(p, "test/"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."), strings.HasPrefix(base, "test_"):
		return "test"
	case strings.HasSuffix(base, ".md"), strings.HasPrefix(p, "docs/"), strings.HasPrefix(p, "adr/"):
		return "docs"
	case base == "go.mod", base == "go.sum", base == "package.json", base == "package-lock.json",
		base == "Dockerfile", base == "Makefile", strings.HasSuffix(base, ".lock"), base == "pyproject.toml":
		return "build"
	case strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".yaml") && strings.HasPrefix(p, ".agentflow/"):
		return "chore"
	}
	return "code"
}

// Group is one proposed commit.
type Group struct {
	Type  string   `json:"type"`
	Files []string `json:"files"`
	Why   string   `json:"why"`
}

// Plan groups changed paths into logical commits: code with its tests by top-level
// directory, then docs, CI, build and chore changes on their own.
func Plan(files []string) []Group {
	code := map[string][]string{}
	var order []string
	other := map[string][]string{}
	for _, f := range files {
		cat := Category(f)
		if cat == "code" || cat == "test" {
			area := topArea(f)
			if _, ok := code[area]; !ok {
				order = append(order, area)
			}
			code[area] = append(code[area], f)
			continue
		}
		other[cat] = append(other[cat], f)
	}
	var groups []Group
	for _, area := range order {
		fs := code[area]
		groups = append(groups, Group{Type: TypeFromFiles(fs), Files: fs, Why: "code and tests under " + area})
	}
	for _, cat := range []string{"docs", "ci", "build", "chore"} {
		if fs := other[cat]; len(fs) > 0 {
			groups = append(groups, Group{Type: cat, Files: fs, Why: cat + " changes"})
		}
	}
	// Docs that sit beside a single code area belong with it.
	if len(order) == 1 && len(other["docs"]) > 0 && len(groups) == 2 {
		groups[0].Files = append(groups[0].Files, other["docs"]...)
		groups = groups[:1]
	}
	return groups
}

func topArea(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) == 1 {
		return "."
	}
	if (parts[0] == "internal" || parts[0] == "pkg" || parts[0] == "cmd" || parts[0] == "src") && len(parts) > 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// Breaking reports changed paths that match breaking patterns (path.Match, with ** support).
func Breaking(files, patterns []string) []string {
	var hits []string
	for _, f := range files {
		for _, p := range patterns {
			if Match(p, f) {
				hits = append(hits, f)
				break
			}
		}
	}
	return hits
}

// Match is path.Match with "**" matching any number of directories.
func Match(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	prefix, suffix, _ := strings.Cut(pattern, "**")
	prefix = strings.TrimSuffix(prefix, "/")
	suffix = strings.TrimPrefix(suffix, "/")
	if prefix != "" && !strings.HasPrefix(name, prefix+"/") {
		return false
	}
	rest := strings.TrimPrefix(name, prefix+"/")
	if suffix == "" {
		return true
	}
	segs := strings.Split(rest, "/")
	for i := range segs {
		if ok, _ := path.Match(suffix, strings.Join(segs[i:], "/")); ok {
			return true
		}
	}
	return false
}

var aiTrailerRe = regexp.MustCompile(`(?im)^(co-authored-by|assisted-by|generated-by):.*(anthropic|claude|openai|codex|chatgpt|x\.ai|grok|copilot|gemini|cursor).*$\n?`)
var aiFooterRe = regexp.MustCompile(`(?im)^.*generated with \[?(claude code|codex|copilot)\]?.*$\n?`)

// StripAITrailers removes AI attribution trailers and footers.
func StripAITrailers(msg string) string {
	msg = aiTrailerRe.ReplaceAllString(msg, "")
	msg = aiFooterRe.ReplaceAllString(msg, "")
	return strings.TrimRight(msg, "\n ") + "\n"
}

// Format builds a message from parts.
func Format(typ string, keys []string, breaking bool, summary, body string, verbs []string, trailers []string) string {
	var b strings.Builder
	bang := ""
	if breaking {
		bang = "!"
	}
	b.WriteString(typ)
	if len(keys) != 0 {
		fmt.Fprintf(&b, "(%s)", strings.Join(keys, ","))
	}
	fmt.Fprintf(&b, "%s: %s", bang, summary)
	for _, v := range verbs {
		b.WriteString(" #" + v)
	}
	b.WriteString("\n")
	if body = strings.TrimSpace(body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	if len(trailers) > 0 {
		b.WriteString("\n" + strings.Join(trailers, "\n") + "\n")
	}
	return b.String()
}
