package plugin

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"sync"

	"github.com/ryanhelms/agentflow/internal/command"
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
		// The rules cannot be read: refuse rather than drop a deny.
		return nil, &DeniedError{Plugin: "*", Rule: "*", Message: "plugin permission rules could not be read; run agentflow plugin list"}
	}
	for _, r := range rules {
		if matchRule(r.rule.Match, c) {
			return nil, &DeniedError{Plugin: r.plugin, Rule: r.rule.Name, Message: oneLine(r.rule.Deny, MaxDenyMessage)}
		}
	}
	return next(ctx, c)
}

// load returns the enabled rules, read again only when the registry's
// revision or the personal layer's plugins section changed.
func (s *PermissionStage) load(ctx context.Context) ([]compiledRule, error) {
	src, layers := s.Source(), s.Layers()
	if src == nil || layers == nil {
		return nil, nil
	}
	personal, err := layers.Personal(ctx)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for id, p := range personal {
		if p.Enabled {
			enabled[id] = true
		}
	}
	fp, _ := json.Marshal(enabled)
	key := src.Revision() + "\x00" + string(fp)
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.key {
		return s.rules, nil
	}
	installed, err := src.Installed(ctx)
	if err != nil {
		return nil, err
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

// matchRule reports whether every non-empty list of m matches c.
func matchRule(m manifest.RuleMatch, c *command.Call) bool {
	if len(m.Operation)+len(m.Class)+len(m.Action)+len(m.Origin)+len(m.Branch) == 0 {
		return false // Validate refuses an empty match; never deny everything
	}
	return globs(m.Operation, c.Meta.Name) && globs(m.Class, string(c.Meta.Class)) && globs(m.Action, c.Meta.Action) &&
		globs(m.Origin, c.Origin.Source) && globs(m.Branch, c.Detail.Branch)
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
