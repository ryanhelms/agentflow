// Package contextx builds task context for the agent: acceptance criteria
// checklists (stored locally in .git/devflow) and scope-drift checks.
package contextx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Item is one acceptance criterion.
type Item struct {
	Text     string `json:"text"`
	Done     bool   `json:"done"`
	Evidence string `json:"evidence,omitempty"` // commit SHA or test name
}

// Checklist is the local acceptance-criteria state for one ticket.
type Checklist struct {
	Key     string    `json:"key"`
	Items   []Item    `json:"items"`
	Updated time.Time `json:"updated"`
}

// Progress returns done/total.
func (c *Checklist) Progress() (int, int) {
	n := 0
	for _, i := range c.Items {
		if i.Done {
			n++
		}
	}
	return n, len(c.Items)
}

var (
	acHeadingRe = regexp.MustCompile(`(?im)^\s*(acceptance criteria|definition of done|ac)\s*:?\s*$`)
	bulletRe    = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)]|\[[ xX]\])\s+(.+)$`)
	taskRe      = regexp.MustCompile(`^\s*\[([ xX])\]\s+(.+)$`)
)

// ParseCriteria extracts acceptance criteria bullets from a plain-text description.
// It prefers a section headed "Acceptance criteria"; otherwise it uses task-list items.
func ParseCriteria(desc string) []Item {
	lines := strings.Split(desc, "\n")
	start := -1
	for i, l := range lines {
		if acHeadingRe.MatchString(l) {
			start = i + 1
			break
		}
	}
	var items []Item
	if start >= 0 {
		for _, l := range lines[start:] {
			t := strings.TrimSpace(l)
			if t == "" {
				if len(items) > 0 {
					break
				}
				continue
			}
			if m := taskRe.FindStringSubmatch(t); m != nil {
				items = append(items, Item{Text: strings.TrimSpace(m[2]), Done: m[1] != " "})
				continue
			}
			if m := bulletRe.FindStringSubmatch(t); m != nil {
				items = append(items, Item{Text: strings.TrimSpace(m[1])})
				continue
			}
			if len(items) > 0 {
				break // next section
			}
		}
		return items
	}
	for _, l := range lines {
		if m := taskRe.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- "))); m != nil {
			items = append(items, Item{Text: strings.TrimSpace(m[2]), Done: m[1] != " "})
		}
	}
	return items
}

// Store keeps checklists under <gitdir>/devflow.
type Store struct {
	Dir string
}

func (s Store) path(key string) string {
	return filepath.Join(s.Dir, key+".json")
}

// Load returns the stored checklist, or nil if none.
func (s Store) Load(key string) (*Checklist, error) {
	b, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Checklist
	return &c, json.Unmarshal(b, &c)
}

// Save writes a checklist.
func (s Store) Save(c *Checklist) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	c.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(c.Key), b, 0o600)
}

// Sync merges freshly parsed criteria into the stored checklist, keeping done
// state for items whose text is unchanged.
func (s Store) Sync(key string, parsed []Item) (*Checklist, error) {
	old, err := s.Load(key)
	if err != nil {
		return nil, err
	}
	c := &Checklist{Key: key, Items: parsed}
	if old != nil {
		prev := map[string]Item{}
		for _, i := range old.Items {
			prev[normalise(i.Text)] = i
		}
		for idx, i := range c.Items {
			if p, ok := prev[normalise(i.Text)]; ok && p.Done {
				c.Items[idx].Done, c.Items[idx].Evidence = true, p.Evidence
			}
		}
	}
	return c, s.Save(c)
}

func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Find returns the index of the item best matching text (exact, prefix or substring), or -1.
func (c *Checklist) Find(text string) int {
	n := normalise(text)
	for i, it := range c.Items {
		if normalise(it.Text) == n {
			return i
		}
	}
	for i, it := range c.Items {
		if strings.Contains(normalise(it.Text), n) || strings.Contains(n, normalise(it.Text)) {
			return i
		}
	}
	return -1
}

// Render formats the checklist as text.
func (c *Checklist) Render() string {
	var b strings.Builder
	for _, it := range c.Items {
		mark := "[ ]"
		if it.Done {
			mark = "[x]"
		}
		b.WriteString("  " + mark + " " + it.Text)
		if it.Evidence != "" {
			b.WriteString("  (" + it.Evidence + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Drift is a scope-drift finding.
type Drift struct {
	Files   []string `json:"files,omitempty"`
	TODOs   []string `json:"todos,omitempty"`
	Summary string   `json:"summary"`
}

var todoRe = regexp.MustCompile(`^\+.*\b(TODO|FIXME|HACK|XXX)\b[:(]?\s*(.*)$`)

// ScopeCheck compares changed files with the areas the ticket's work has
// touched so far (anchor areas) and lists TODOs added in the diff.
// anchors are path prefixes (e.g. from earlier commits on the branch or the
// ticket's components); files outside all anchors are reported.
func ScopeCheck(files, anchors []string, diff string) Drift {
	var d Drift
	if len(anchors) > 0 {
		for _, f := range files {
			in := false
			for _, a := range anchors {
				if strings.HasPrefix(f, a) {
					in = true
					break
				}
			}
			if !in && !housekeeping(f) {
				d.Files = append(d.Files, f)
			}
		}
	}
	for _, l := range strings.Split(diff, "\n") {
		if m := todoRe.FindStringSubmatch(l); m != nil {
			d.TODOs = append(d.TODOs, strings.TrimSpace(m[1]+": "+m[2]))
		}
	}
	switch {
	case len(d.Files) == 0 && len(d.TODOs) == 0:
		d.Summary = "no drift found"
	default:
		d.Summary = "possible work outside this ticket; consider a separate commit under a new linked ticket"
	}
	return d
}

func housekeeping(f string) bool {
	base := filepath.Base(f)
	return base == "go.sum" || base == "go.mod" || base == "package-lock.json" || strings.HasSuffix(base, ".lock") ||
		strings.HasPrefix(f, ".devflow/") || base == "CHANGELOG.md"
}

// Areas returns the distinct top two path segments of files, used as anchors.
func Areas(files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		parts := strings.Split(f, "/")
		a := parts[0] + "/"
		if len(parts) > 2 {
			a = parts[0] + "/" + parts[1] + "/"
		} else if len(parts) == 1 {
			continue
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}
