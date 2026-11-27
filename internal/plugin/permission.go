package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// MaxDenyMessage caps a permission rule's message.
const MaxDenyMessage = 256

// exemptOperations are the operation families permission rules never
// apply to, so a person can always turn a plugin off or answer an
// approval, whatever a rule says.
var exemptOperations = []string{"config", "plugin", "approval", "policy"}

// exempt reports whether operation name is in an exempt family.
func exempt(name string) bool {
	for _, f := range exemptOperations {
		if name == f || strings.HasPrefix(name, f+".") {
			return true
		}
	}
	return false
}

// PermissionStage is the Invoker Stage of every enabled declarative
// plugin's permission rules (Chain of Responsibility). It only denies: it
// calls next with the Call unchanged, or stops the chain with a
// *DeniedError before any event is published. It never returns a result
// of its own and never skips the gate, which runs after the stages or
// inside Run.
type PermissionStage struct {
	// Source and Layers read the registry and the personal layer. A nil
	// result means no plugins are installed.
	Source func() manifest.Source
	Layers func() Layers
	// RegistryPath and ConfigPath name the files a refusal asks the person
	// to fix; nil means the agentflow home's installed.json and config.yml.
	RegistryPath func() string
	ConfigPath   func() string

	mu    sync.Mutex
	key   string
	rules []compiledRule
}

// compiledRule is one rule of one enabled plugin.
type compiledRule struct {
	plugin string
	rule   manifest.PermissionRule
}

// stage is the PermissionStage every Invoker runs.
var stage = &PermissionStage{Source: DefaultSource, Layers: DefaultLayers}

func init() { command.RegisterStage(stage) }

// Handle implements command.Stage.
func (s *PermissionStage) Handle(ctx context.Context, c *command.Call, next command.Handler) (any, error) {
	if exempt(c.Meta.Name) {
		return next(ctx, c)
	}
	rules, err := s.load(ctx)
	if err != nil {
		// The rules cannot be read: refuse rather than drop a deny, and say
		// which file to fix.
		return nil, &DeniedError{Plugin: "*", Rule: "*", Message: s.unreadable(err)}
	}
	b := branchOf(ctx, c)
	for _, r := range rules {
		if matchRule(r.rule.Match, c, b) {
			return nil, &DeniedError{Plugin: r.plugin, Rule: r.rule.Name, Message: oneLine(r.rule.Deny, MaxDenyMessage)}
		}
	}
	return next(ctx, c)
}

// load returns the enabled rules, read again only when the registry's
// revision or the personal layer's plugins section changed.
//
// With no plugin enabled there are no rules and the registry is not read,
// so a broken installed.json cannot block anything. With a plugin
// enabled, a registry that cannot be found or read fails closed.
func (s *PermissionStage) load(ctx context.Context) ([]compiledRule, error) {
	layers := s.Layers()
	if layers == nil {
		return nil, nil // no personal layer: nothing is enabled
	}
	personal, err := layers.Personal(ctx)
	if err != nil {
		return nil, &rulesError{config: true, err: err}
	}
	enabled := map[string]bool{}
	for id, p := range personal {
		if p.Enabled {
			enabled[id] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(enabled) == 0 {
		s.key, s.rules = "", nil
		return nil, nil
	}
	src := s.Source()
	if src == nil {
		return nil, &rulesError{err: errNoRegistry}
	}
	fp, _ := json.Marshal(enabled)
	key := src.Revision() + "\x00" + string(fp)
	if key == s.key {
		return s.rules, nil
	}
	installed, err := src.Installed(ctx)
	if err != nil {
		return nil, &rulesError{err: err}
	}
	var rules []compiledRule
	for _, p := range installed {
		if !enabled[p.ID] || p.Tier != manifest.TierDeclarative {
			continue
		}
		for _, r := range p.Manifest.Extensions.PermissionRule {
			rules = append(rules, compiledRule{plugin: p.ID, rule: r})
		}
	}
	s.key, s.rules = key, rules
	return rules, nil
}

// errNoRegistry: the agentflow home, and so installed.json, cannot be
// found while a plugin is enabled.
var errNoRegistry = errors.New("the agentflow home cannot be found")

// branchWriters are the actions that write a branch. With no branch named
// (Detail.Branch), each writes the current branch: commit commits on it,
// push publishes it, a PR's head is it, and finish deletes it.
var branchWriters = map[string]bool{"commit": true, "push": true, "pr_open": true, "pr_ready_for_review": true, "finish": true}

// callBranch is the branch a call writes, for a rule's branch condition.
type callBranch struct {
	name    string
	unknown bool // the call writes a branch that cannot be determined
}

// branchOf returns the branch c writes: Detail.Branch when the operation
// names one, else the current branch for a branch-writing action.
func branchOf(ctx context.Context, c *command.Call) callBranch {
	if c.Detail.Branch != "" {
		return callBranch{name: c.Detail.Branch}
	}
	if !branchWriters[c.Meta.Action] {
		return callBranch{}
	}
	if c.App != nil && c.App.Repo != nil {
		if b := c.App.Repo.Branch(ctx); b != "" {
			return callBranch{name: b}
		}
	}
	return callBranch{unknown: true}
}

// matchRule reports whether every non-empty list of m matches c, whose
// written branch is b. A branch condition matches a branch-writing call
// whose branch cannot be determined (so it denies), and never matches a
// call that writes no branch.
func matchRule(m manifest.RuleMatch, c *command.Call, b callBranch) bool {
	if len(m.Operation)+len(m.Class)+len(m.Action)+len(m.Origin)+len(m.Branch) == 0 {
		return false // Validate refuses an empty match; never deny everything
	}
	if !globs(m.Operation, c.Meta.Name) || !globs(m.Class, string(c.Meta.Class)) || !globs(m.Action, c.Meta.Action) ||
		!globs(m.Origin, c.Origin.Source) {
		return false
	}
	switch {
	case len(m.Branch) == 0, b.unknown:
		return true
	case b.name == "":
		return false
	}
	return globs(m.Branch, b.name)
}

// globs reports whether value matches any of patterns; an empty list
// matches everything.
func globs(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, err := path.Match(p, value); err == nil && ok {
			return true
		}
	}
	return false
}

// rulesError says which file kept the rules from being read.
type rulesError struct {
	config bool // the personal layer, else the registry
	err    error
}

func (e *rulesError) Error() string { return e.err.Error() }
func (e *rulesError) Unwrap() error { return e.err }

// unreadable is the refusal's message: the file and how to fix it.
func (s *PermissionStage) unreadable(err error) string {
	var re *rulesError
	if errors.Is(err, errNoRegistry) {
		return "plugin permission rules could not be read because the agentflow home cannot be found. Set HOME, or turn plugins off in config.yml."
	}
	if errors.As(err, &re) && re.config {
		return fmt.Sprintf("plugin permission rules could not be read because %s is not valid. Fix that file, then check it with agentflow config.", pathOr(s.ConfigPath, defaultConfigPath))
	}
	return fmt.Sprintf("plugin permission rules could not be read because %s is not a valid plugin registry. Fix or restore that file, then check it with agentflow plugin list.", pathOr(s.RegistryPath, defaultRegistryPath))
}

func pathOr(f, fallback func() string) string {
	if f != nil {
		return f()
	}
	return fallback()
}

func defaultRegistryPath() string {
	home, err := config.HomeDir()
	if err != nil {
		return filepath.Join("~", ".agentflow", manifest.PluginsDir, manifest.RegistryFile)
	}
	return filepath.Join(home, manifest.PluginsDir, manifest.RegistryFile)
}

func defaultConfigPath() string {
	home, err := config.Home()
	if err != nil {
		return filepath.Join("~", ".agentflow", "config.yml")
	}
	return filepath.Join(home, "config.yml")
}
