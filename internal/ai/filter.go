package ai

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
)

// The Content Filter ("Phase 8 prompt context"): every part's text passes
// it before it can reach a prompt. All the code that handles untrusted
// context text sits here and in prompt.go.

// Exclusion reasons.
const (
	ExcludedSecretFile = "secret_file"
	ExcludedBinary     = "binary"
	ExcludedGenerated  = "generated"
	ExcludedTooLarge   = "too_large"
	// ExcludedUnparsed: a file's "diff --git" header did not parse (no a/
	// and b/ prefixes, or a quoted name that does not unquote), so its
	// content is left out rather than checked against the wrong path.
	ExcludedUnparsed = "unparsed_header"
)

// generatedFiles are left out of a diff by base name; generatedSuffixes
// by ending.
var (
	generatedFiles    = []string{"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock", "poetry.lock"}
	generatedSuffixes = []string{".min.js", ".map"}
)

// Generated reports whether a worktree-relative path is a generated file
// whose content a draft never sends.
func Generated(rel string) bool {
	base := path.Base(rel)
	if slices.Contains(generatedFiles, base) {
		return true
	}
	for _, s := range generatedSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

// Caps is an optional interface of a Feature: the per-part cap of the
// feature table, in bytes after filtering, for the whole part and (diffs)
// for each file. Zero means no cap of its own; MaxData always holds.
type Caps interface {
	PartCap(p Part) (total, perFile int)
}

// FilterOptions are what Filter needs from the caller: the worktree root
// and the OS home, which it writes as "." and "~".
type FilterOptions struct {
	Root string // the worktree root, as given and resolved
	Home string
	// Caps is the feature's, or nil.
	Caps Caps
}

// Filter cleans every part in order (paths, left-out files, hidden
// characters, redaction, the secret check, caps) and returns the parts as
// they would be sent. A part that still holds a secret refuses the draft
// secret_in_context, naming the part, never the match.
func Filter(parts []Part, o FilterOptions) ([]Part, error) {
	out := make([]Part, 0, len(parts))
	for _, p := range parts {
		p.Original = len(p.Text)
		p.Label = labelText(p.Label)
		p.Source = labelText(p.Source)
		t := p.Text
		if !utf8.ValidString(t) {
			t = strings.ToValidUTF8(t, "�")
		}
		// 1. Paths of the person's machine.
		t = localPaths(t, o.Root, o.Home)
		// 2. Diffs: secret, binary and generated files lose their content.
		if p.Kind == PartDiff {
			var ex []Exclusion
			t, ex = leaveOut(t)
			p.Excluded = append(p.Excluded, ex...)
		}
		// 3. Hidden characters made visible.
		t = visible(t)
		// 4. Known token shapes.
		red := aisession.Redact(t)
		p.Redacted += max(0, strings.Count(red, aisession.Redacted)-strings.Count(t, aisession.Redacted))
		t = red
		// 6 (per part). The feature's caps.
		if o.Caps != nil {
			total, perFile := o.Caps.PartCap(p)
			if p.Kind == PartDiff && perFile > 0 {
				var cut []Exclusion
				t, cut = capFiles(t, perFile)
				p.Excluded = append(p.Excluded, cut...)
				p.Cut = p.Cut || len(cut) > 0
			}
			if total > 0 && len(t) > total {
				t, p.Cut = cutText(t, total), true
			}
		}
		// 5. A secret that is still there refuses the draft.
		if app.ContainsSecret(t) || (p.Kind == PartDiff && len(app.DraftSecrets(t)) > 0) {
			return nil, refuse(CodeSecretInContext,
				fmt.Sprintf("The %s still holds what looks like a secret, so agentflow does not send it.", p.Label),
				"Remove the secret from the change (or leave its file out), then draft again.")
		}
		p.Text = t
		p.Bytes = len(t)
		out = append(out, p)
	}
	// 6. MaxData over all parts: the largest are cut first.
	for {
		total := 0
		largest := -1
		for i, p := range out {
			total += len(p.Text)
			if largest < 0 || len(p.Text) > len(out[largest].Text) {
				largest = i
			}
		}
		if total <= MaxData || largest < 0 {
			break
		}
		over := total - MaxData
		p := &out[largest]
		keep := max(0, len(p.Text)-over-64)
		p.Text, p.Cut = cutText(p.Text, keep), true
		p.Bytes = len(p.Text)
		if keep == 0 {
			break
		}
	}
	return out, nil
}

// labelText is agentflow's label or source as it may stand in a fence
// header: one line, no quotes or angle brackets.
func labelText(s string) string {
	s = aisession.SanitizeLine(s, 128)
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '<', '>':
			return '\''
		}
		return r
	}, s)
}

// localPaths writes the worktree root as "." and the OS home as "~".
func localPaths(t, root, home string) string {
	var roots []string
	for _, r := range []string{root, resolved(root)} {
		if r != "" && r != "/" && !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}
	// Longest first, so a resolved root under a linked one is replaced whole.
	slices.SortFunc(roots, func(a, b string) int { return len(b) - len(a) })
	for _, r := range roots {
		t = strings.ReplaceAll(t, r, ".")
	}
	var homes []string
	for _, h := range []string{home, resolved(home)} {
		if h != "" && h != "/" && !slices.Contains(homes, h) {
			homes = append(homes, h)
		}
	}
	slices.SortFunc(homes, func(a, b string) int { return len(b) - len(a) })
	for _, h := range homes {
		t = strings.ReplaceAll(t, h, "~")
	}
	return t
}

// visible writes every hazard character other than newline and tab as
// <U+XXXX>, so text a person cannot see is visible to the model and to
// the person reading the manifest.
func visible(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r != '\n' && r != '\t' && aisession.Hazard(r) {
			fmt.Fprintf(&b, "<U+%04X>", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// diffFile is one file's section of a unified diff.
type diffFile struct {
	path  string   // the new path (b/), or the old one for a deletion
	paths []string // every path the section names: a/, b/, ---, +++, rename and copy lines
	bad   bool     // a "diff --git" section whose header did not parse
	text  string
}

// splitDiff splits a git diff at its "diff --git" lines. Text before the
// first one is a section with no path.
func splitDiff(diff string) []diffFile {
	var out []diffFile
	lines := strings.SplitAfter(diff, "\n")
	cur := diffFile{}
	var b strings.Builder
	flush := func() {
		cur.text = b.String()
		if cur.text != "" || cur.path != "" || cur.bad {
			out = append(out, cur)
		}
		b.Reset()
	}
	header := true // in the section's extended header, before its first hunk
	for _, l := range lines {
		line := strings.TrimSuffix(strings.TrimSuffix(l, "\n"), "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur, header = diffFile{}, true
			paths, ok := diffHeader(strings.TrimPrefix(line, "diff --git "))
			if !ok {
				cur.bad = true
			}
			cur.add(paths...)
		case header && strings.HasPrefix(line, "@@"):
			header = false
		case header && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
			p, ok := diffName(line[4:], true)
			if !ok {
				cur.bad = true
			}
			cur.add(p)
		case header:
			for _, k := range []string{"rename from ", "rename to ", "copy from ", "copy to "} {
				if strings.HasPrefix(line, k) {
					p, ok := diffName(line[len(k):], false)
					if !ok {
						cur.bad = true
					}
					cur.add(p)
				}
			}
		}
		b.WriteString(l)
	}
	flush()
	return out
}

// add records the paths a section names; the last one named is its path.
func (f *diffFile) add(paths ...string) {
	for _, p := range paths {
		if p == "" || p == "/dev/null" {
			continue
		}
		if !slices.Contains(f.paths, p) {
			f.paths = append(f.paths, p)
		}
		f.path = p
	}
}

// diffHeader reads the two paths of a "diff --git" line: "a/X b/Y", each
// part C-quoted when git quotes it. It reports false when the line does
// not have the a/ and b/ prefixes agentflow asks git for (a
// diff.noprefix or diff.mnemonicPrefix setting, or a diff agentflow did
// not make), so the caller leaves the section's content out. An unquoted
// header with spaces is ambiguous; every way of reading it is returned.
func diffHeader(rest string) ([]string, bool) {
	if strings.HasPrefix(rest, `"`) {
		first, tail, ok := cQuoted(rest)
		if !ok || !strings.HasPrefix(first, "a/") || !strings.HasPrefix(tail, " ") {
			return nil, false
		}
		second, ok := diffName(tail[1:], true)
		if !ok {
			return []string{first[2:]}, false
		}
		return []string{first[2:], second}, true
	}
	if !strings.HasPrefix(rest, "a/") {
		return nil, false
	}
	if i := strings.Index(rest, ` "b/`); i >= 0 {
		second, ok := diffName(rest[i+1:], true)
		return []string{rest[2:i], second}, ok
	}
	var out []string
	for i := 0; i+3 <= len(rest); i++ {
		if strings.HasPrefix(rest[i:], " b/") {
			out = append(out, rest[2:i], rest[i+3:])
		}
	}
	return out, len(out) > 0
}

// diffName reads one path: C-quoted or plain, with the a/ or b/ prefix
// when prefixed (and "/dev/null" as itself). It reports false when the
// name does not parse or lacks its prefix.
func diffName(s string, prefixed bool) (string, bool) {
	if strings.HasPrefix(s, `"`) {
		q, tail, ok := cQuoted(s)
		if !ok || strings.TrimSpace(tail) != "" {
			return "", false
		}
		s = q
	} else if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i] // "--- a/x\t2024-..." from some tools
	}
	if s == "/dev/null" {
		return s, true
	}
	if !prefixed {
		return s, s != ""
	}
	if !strings.HasPrefix(s, "a/") && !strings.HasPrefix(s, "b/") {
		return "", false
	}
	return s[2:], len(s) > 2
}

// cQuoted reads the C-quoted string git writes at the start of s (octal
// escapes for bytes outside ASCII, backslash escapes for '"', '\\' and
// control characters) and returns it and the rest of s.
func cQuoted(s string) (string, string, bool) {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			v, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", "", false
			}
			return v, s[i+1:], true
		}
	}
	return "", "", false
}

// leaveOut removes the content of secret, binary and generated files from
// a diff; each file's first line stays, with a line saying why.
func leaveOut(diff string) (string, []Exclusion) {
	var b strings.Builder
	var ex []Exclusion
	for _, f := range splitDiff(diff) {
		reason := ""
		secret, generated := false, false
		for _, p := range f.paths {
			secret = secret || aisession.IsSecretFile(p)
			generated = generated || Generated(p)
		}
		switch {
		case f.bad:
			reason = ExcludedUnparsed
		case f.path == "":
		case secret:
			reason = ExcludedSecretFile
		case generated:
			reason = ExcludedGenerated
		case binaryDiff(f.text):
			reason = ExcludedBinary
		}
		if reason == "" {
			b.WriteString(f.text)
			continue
		}
		first, _, _ := strings.Cut(f.text, "\n")
		b.WriteString(first + "\n[content left out: " + reason + "]\n")
		name := f.path
		if name == "" {
			name = first
		}
		ex = append(ex, Exclusion{Path: aisession.SanitizeLine(name, 512), Reason: reason})
	}
	return b.String(), ex
}

func binaryDiff(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch" {
			return true
		}
	}
	return false
}

// capFiles cuts each file's section of a diff to perFile bytes.
func capFiles(diff string, perFile int) (string, []Exclusion) {
	var b strings.Builder
	var cut []Exclusion
	for _, f := range splitDiff(diff) {
		if len(f.text) > perFile {
			b.WriteString(cutText(f.text, perFile))
			if f.path != "" {
				cut = append(cut, Exclusion{Path: aisession.SanitizeLine(f.path, 512), Reason: ExcludedTooLarge})
			}
			continue
		}
		b.WriteString(f.text)
	}
	return b.String(), cut
}

// cutText keeps at most keep bytes of t, cut at a line boundary, and adds
// a line saying how much was left out.
func cutText(t string, keep int) string {
	if len(t) <= keep {
		return t
	}
	head := aisession.Clamp(t, keep)
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	} else {
		head = ""
	}
	if head != "" && !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	return head + fmt.Sprintf("[cut: %d bytes left out]\n", len(t)-len(head))
}
