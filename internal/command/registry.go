package command

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ryanhelms/agentflow/internal/policy"
)

// Verb is a CLI command that several Specs share, with its own parsing:
// "commit" runs commit, commit.prepare, commit.plan or commit.context by its
// flags. Its Run must reach each operation through call.Invoke (Lookup the
// Spec, then Invoke), never call the app method directly.
type Verb struct {
	Path []string
	// Usage, Section and Order place the verb's usage block, as CLI.
	Usage   string
	Section Section
	Order   int
	Run     func(ctx context.Context, env CLIEnv, inv Invoker, args []string) error
}

// Registry holds every operation and verb. Specs register from their own
// file's init through Register; tests build their own with NewRegistry.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Command
	byTool map[string]Command
	// cli maps a path ("pr mark-ready") to the Spec that binds it, when one
	// does; verbs maps a path to its Verb.
	cli   map[string]Command
	verbs map[string]Verb
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Command{}, byTool: map[string]Command{}, cli: map[string]Command{}, verbs: map[string]Verb{}}
}

// Default is the registry the agentflow binary uses. internal/command/ops
// fills it from init; internal/cli and internal/mcpserver read it.
var Default = NewRegistry()

// Register adds s to Default. It panics on an invalid or duplicate Spec, so
// a mistake fails every test of every package that links the Spec.
func Register[In, Out any](s Spec[In, Out]) {
	if s.Run == nil {
		panic(fmt.Sprintf("command %q: Run is required", s.Name))
	}
	if s.Policy.Gate == GateByInvoker && s.Plan == nil {
		panic(fmt.Sprintf("command %q: GateByInvoker needs Plan", s.Name))
	}
	if err := Default.Add(s); err != nil {
		panic(err)
	}
}

// RegisterVerb adds v to Default, panicking like Register.
func RegisterVerb(v Verb) {
	if err := Default.AddVerb(v); err != nil {
		panic(err)
	}
}

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)
	toolRe = regexp.MustCompile(`^agentflow_[a-z0-9]+(_[a-z0-9]+)*$`)
	wordRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Add validates and adds c. Rules: a valid unique name; an MCP tool needs a
// CLI path (parity) and a unique, valid tool name; a CLI path that a Spec
// binds belongs to that Spec alone and to no Verb; a write names its policy
// action; a Spec with its own CLI has a usage block.
func (r *Registry) Add(c Command) error {
	m := c.Meta()
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("command %q: invalid name", m.Name)
	}
	if m.Class != policy.Read && m.Action == "" {
		return fmt.Errorf("command %q: a write needs Policy.Action", m.Name)
	}
	if err := validPath(m.CLIPath); err != nil && (m.Tool != "" || m.OwnCLI) {
		return fmt.Errorf("command %q: %w", m.Name, err)
	}
	if m.Tool != "" && !toolRe.MatchString(m.Tool) {
		return fmt.Errorf("command %q: invalid tool name %q", m.Name, m.Tool)
	}
	if m.OwnCLI && strings.TrimSpace(m.Usage) == "" {
		return fmt.Errorf("command %q: CLI needs a usage block", m.Name)
	}
	path := strings.Join(m.CLIPath, " ")
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[m.Name]; dup {
		return fmt.Errorf("command %q registered twice", m.Name)
	}
	if prev, dup := r.byTool[m.Tool]; dup && m.Tool != "" {
		return fmt.Errorf("tool %s: registered by %q and %q", m.Tool, prev.Meta().Name, m.Name)
	}
	if m.OwnCLI {
		if prev, dup := r.cli[path]; dup {
			return fmt.Errorf("CLI path %q: bound by %q and %q", path, prev.Meta().Name, m.Name)
		}
		if _, dup := r.verbs[path]; dup {
			return fmt.Errorf("CLI path %q: bound by %q and owned by a verb", path, m.Name)
		}
		r.cli[path] = c
	}
	r.byName[m.Name] = c
	if m.Tool != "" {
		r.byTool[m.Tool] = c
	}
	return nil
}

// AddVerb validates and adds v.
func (r *Registry) AddVerb(v Verb) error {
	path := strings.Join(v.Path, " ")
	if err := validPath(v.Path); err != nil {
		return fmt.Errorf("verb %q: %w", path, err)
	}
	if v.Run == nil || strings.TrimSpace(v.Usage) == "" {
		return fmt.Errorf("verb %q: needs Run and a usage block", path)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.verbs[path]; dup {
		return fmt.Errorf("verb %q registered twice", path)
	}
	if c, dup := r.cli[path]; dup {
		return fmt.Errorf("verb %q: path bound by %q", path, c.Meta().Name)
	}
	r.verbs[path] = v
	return nil
}

func validPath(path []string) error {
	if len(path) == 0 {
		return fmt.Errorf("no CLI path")
	}
	for _, w := range path {
		if !wordRe.MatchString(w) {
			return fmt.Errorf("invalid CLI path word %q", w)
		}
	}
	return nil
}

// Lookup returns the command named name.
func (r *Registry) Lookup(name string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byName[name]
	return c, ok
}

// Tool returns the command whose MCP tool is name.
func (r *Registry) Tool(name string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byTool[name]
	return c, ok
}

// Commands lists every command, sorted by name.
func (r *Registry) Commands() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Command, 0, len(r.byName))
	for _, c := range r.byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta().Name < out[j].Meta().Name })
	return out
}

// Dispatch finds what runs args: the longest registered path that prefixes
// args, a Spec's own CLI or a Verb. rest is args after the path. ok is false
// when nothing registered matches; internal/cli then falls back to its
// legacy switch while operations migrate.
func (r *Registry) Dispatch(args []string) (run func(ctx context.Context, env CLIEnv, inv Invoker, rest []string) error, rest []string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for n := len(args); n > 0; n-- {
		path := strings.Join(args[:n], " ")
		if c, found := r.cli[path]; found {
			return c.RunCLI, args[n:], true
		}
		if v, found := r.verbs[path]; found {
			return v.Run, args[n:], true
		}
	}
	return nil, args, false
}

// ParityRows maps every registered MCP tool to its CLI path, the derived
// form of mcpToCLI in internal/cli/parity_test.go.
func (r *Registry) ParityRows() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rows := map[string]string{}
	for tool, c := range r.byTool {
		rows[tool] = strings.Join(c.Meta().CLIPath, " ")
	}
	return rows
}

// UsageBlock is one command's block of the usage text.
type UsageBlock struct {
	Path    string
	Section Section
	Order   int
	Text    string
}

// Usage lists the usage blocks of every Spec with its own CLI and every
// Verb, sorted by section (SectionOrder), then Order, then path. Lane 3.1
// renders the usage text from these plus the legacy blocks of commands not
// migrated yet.
func (r *Registry) Usage() []UsageBlock {
	r.mu.RLock()
	var out []UsageBlock
	for path, c := range r.cli {
		m := c.Meta()
		out = append(out, UsageBlock{Path: path, Section: m.Section, Order: m.Order, Text: m.Usage})
	}
	for path, v := range r.verbs {
		out = append(out, UsageBlock{Path: path, Section: v.Section, Order: v.Order, Text: v.Usage})
	}
	r.mu.RUnlock()
	SortUsage(out)
	return out
}

// SortUsage sorts blocks by section, then Order, then path. A section not
// in SectionOrder sorts last.
func SortUsage(blocks []UsageBlock) {
	rank := func(s Section) int {
		if i := slices.Index(SectionOrder, s); i >= 0 {
			return i
		}
		return len(SectionOrder)
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if rank(a.Section) != rank(b.Section) {
			return rank(a.Section) < rank(b.Section)
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Path < b.Path
	})
}
