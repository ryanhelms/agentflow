package ai

import (
	"fmt"
	"path"
	"slices"
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
	path string
	text string
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
		if cur.text != "" || cur.path != "" {
			out = append(out, cur)
		}
		b.Reset()
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			flush()
			cur = diffFile{path: diffPath(l)}
		}
		b.WriteString(l)
	}
	flush()
	return out
}

// diffPath is the b/ path of a "diff --git a/X b/X" line.
func diffPath(line string) string {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if i := strings.LastIndex(line, " b/"); i >= 0 {
		return strings.Trim(line[i+3:], `"`)
	}
	return strings.TrimPrefix(line, "diff --git ")
}

// leaveOut removes the content of secret, binary and generated files from
// a diff; each file's first line stays, with a line saying why.
func leaveOut(diff string) (string, []Exclusion) {
	var b strings.Builder
	var ex []Exclusion
	for _, f := range splitDiff(diff) {
		reason := ""
		switch {
		case f.path == "":
		case aisession.IsSecretFile(f.path):
			reason = ExcludedSecretFile
		case Generated(f.path):
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
		ex = append(ex, Exclusion{Path: aisession.SanitizeLine(f.path, 512), Reason: reason})
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
