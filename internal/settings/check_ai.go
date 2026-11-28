package settings

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// CodeAI: a session setting the code would refuse (ADR "Phase 7
// configuration keys"): a rule with an unknown kind or decision, a glob that
// does not compile, an allow for a person-only tool kind, a bad mode, a
// repository path pattern that leaves the worktree, or a limit outside its
// range. It is an error in the Save review.
const CodeAI = "ai"

var (
	aiSessionsKeys    = []string{"ai", "sessions"}
	aiRulesKeys       = []string{"ai", "permissions", "rules"}
	aiModesKeys       = []string{"ai", "sessions", "modes"}
	aiRepoPermissions = []string{"ai", "permissions"}
	aiFeaturesKeys    = []string{"ai", "features"}
	aiDraftsKeys      = []string{"ai", "drafts"}
)

// maxModeLen bounds a session mode ID (the agent's own word).
const maxModeLen = 128

func hasPrefix(keys, prefix []string) bool {
	return len(keys) >= len(prefix) && slices.Equal(keys[:len(prefix)], prefix)
}

// aiLink checks every session edit in its layer file as it is after the
// edit: personal ai.sessions and ai.permissions.rules through
// config.ValidateAI and config.ValidateAIRule, modes by harness ID and mode
// text, and the repository's path patterns.
func aiLink(c *check) {
	for _, e := range c.live() {
		if e.next == ([2]string{}) || e.Op == OpUnset {
			continue
		}
		switch {
		case e.layer == config.FieldPersonal && (hasPrefix(e.keys, aiSessionsKeys) || hasPrefix(e.keys, aiRulesKeys) || slices.Equal(e.keys, aiRulesKeys[:2])):
			u, err := parseUser(e.next[0])
			if err != nil {
				continue // filesLink reports a file that does not load
			}
			if msg := aiPersonalProblem(u.AI, e.keys); msg != "" {
				c.add(SeverityError, e, CodeAI, msg)
			}
		case e.layer == config.FieldPersonal && (hasPrefix(e.keys, aiFeaturesKeys) || hasPrefix(e.keys, aiDraftsKeys) || slices.Equal(e.keys, aiFeaturesKeys[:1])):
			u, err := parseUser(e.next[0])
			if err != nil {
				continue
			}
			if err := config.ValidateAI(config.UserAI{Drafts: u.AI.Drafts}); err != nil {
				c.add(SeverityError, e, CodeAI, sentence(err.Error()))
				continue
			}
			severity, msg := aiFeatureProblem(u.AI, e.keys)
			if msg != "" {
				c.add(severity, e, CodeAI, msg)
			}
		case e.layer == config.FieldRepo && hasPrefix(e.keys, aiRepoPermissions):
			repo, _, err := config.ParseRepoCore([]byte(e.next[1]))
			if err != nil || repo == nil {
				continue
			}
			if msg := aiRepoProblem(repo.AI); msg != "" {
				c.add(SeverityError, e, CodeAI, msg)
			}
		}
	}
}

func aiPersonalProblem(a config.UserAI, keys []string) string {
	if err := config.ValidateAI(config.UserAI{Sessions: a.Sessions, Permissions: a.Permissions}); err != nil {
		return sentence(err.Error())
	}
	if hasPrefix(keys, aiRulesKeys) || slices.Equal(keys, aiRulesKeys[:2]) {
		names := make([]string, 0, len(a.Permissions.Rules))
		for name := range a.Permissions.Rules {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !ruleName(name) {
				return "A session permission rule name may use letters, digits, dots, underscores and hyphens only."
			}
			if err := config.ValidateAIRule(name, a.Permissions.Rules[name]); err != nil {
				return sentence(err.Error())
			}
		}
	}
	if hasPrefix(keys, aiModesKeys) || slices.Equal(keys, aiSessionsKeys) {
		ids := make([]string, 0, len(a.Sessions.Modes))
		for id := range a.Sessions.Modes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if harnessdef.SafeID(id) != id {
				return "A session mode is set for a harness ID that is not valid."
			}
			if !modeID(a.Sessions.Modes[id]) {
				return fmt.Sprintf("The session mode for %s must be 1 to %d characters with no spaces or control characters.", id, maxModeLen)
			}
		}
	}
	return ""
}

// aiFeatureProblem checks the draft feature harness IDs an edit touches
// (ADR "Phase 8 configuration keys"): an ID that breaks the phase 6 ID
// rule is an error; an ID with no definition in any layer, or one in
// ai.harnesses.disabled, is a warning, since the harness may be installed
// or turned on later. A message never quotes an invalid ID.
func aiFeatureProblem(a config.UserAI, keys []string) (Severity, string) {
	ids := a.Features.IDs()
	if len(keys) > len(aiFeaturesKeys) && hasPrefix(keys, aiFeaturesKeys) {
		if !slices.Contains(ids, keys[len(aiFeaturesKeys)]) {
			return "", ""
		}
		ids = []string{keys[len(aiFeaturesKeys)]}
	} else if !hasPrefix(keys, aiFeaturesKeys) && !slices.Equal(keys, aiFeaturesKeys[:1]) {
		return "", ""
	}
	var known map[string]bool
	for _, id := range ids {
		h := a.Features.Harness(id)
		if h == "" {
			continue
		}
		if harnessdef.SafeID(h) != h {
			return SeverityError, fmt.Sprintf("The harness for %s drafts is not a valid harness ID: use lowercase letters, digits and hyphens.", id)
		}
		if slices.Contains(a.Harnesses.Disabled, h) {
			return SeverityWarning, fmt.Sprintf("The harness %s for %s drafts is turned off in ai.harnesses.disabled, so those drafts are refused until you turn it on.", h, id)
		}
		if known == nil {
			known = knownHarnesses(a)
		}
		if !known[h] {
			return SeverityWarning, fmt.Sprintf("No harness definition is called %s yet, so %s drafts are refused until one is installed or added.", h, id)
		}
	}
	return "", ""
}

// knownHarnesses is every harness ID a definition layer names: the pinned
// registry, agentflow's overlay and the person's overrides. Plugin
// harnesses are not counted: an ID only a plugin adds gives a warning.
func knownHarnesses(a config.UserAI) map[string]bool {
	known := map[string]bool{}
	if cat, err := acpregistry.Load(); err == nil && cat != nil {
		for _, e := range cat.Entries {
			known[e.ID] = true
		}
	}
	if overlay, err := harness.LoadOverlay(); err == nil {
		for id := range overlay {
			known[id] = true
		}
	}
	for id := range a.Harnesses.Overrides {
		known[id] = true
	}
	return known
}

func aiRepoProblem(a config.RepoAI) string {
	for _, list := range []struct {
		name  string
		items []string
	}{{"ai.permissions.ask", a.Permissions.Ask}, {"ai.permissions.deny", a.Permissions.Deny}} {
		if len(list.items) > config.MaxAIRepoPaths {
			return fmt.Sprintf("%s holds at most %d patterns.", list.name, config.MaxAIRepoPaths)
		}
		for _, p := range list.items {
			if msg := repoPattern(p); msg != "" {
				return list.name + ": " + msg
			}
		}
	}
	return ""
}

// repoPattern checks one worktree-relative slash glob.
func repoPattern(p string) string {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return "a pattern is a worktree-relative path with / between folders."
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return "a pattern may not contain . or .. folders."
		}
		if part != "**" {
			if _, err := path.Match(part, ""); err != nil {
				return "a pattern is not a valid glob."
			}
		}
	}
	return ""
}

func ruleName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r == '.' || r == '_' || r == '-' || r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))
		if !ok {
			return false
		}
	}
	return true
}

func modeID(s string) bool {
	if s == "" || len(s) > maxModeLen {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

func sentence(s string) string {
	return "This session setting is not valid: " + strings.TrimSuffix(s, ".") + "."
}

// aiPolicy is the part of policyState the session keys add (ADR "Phase 7
// configuration keys", Loosens column), on effective values.
type aiPolicy struct {
	agentStart, agentflowMCP bool
	home, unasked            string
	maxRunning               int
	maxDuration              time.Duration
	rules                    map[string]config.AIRule
	repoDisabled             bool
	repoAsk, repoDeny        []string
	// Phase 8: each draft feature's harness, and the draft time limit.
	features      map[string]string
	draftDuration time.Duration
}

func aiPolicyOf(user *config.User, repo *config.Repo) aiPolicy {
	s := user.AI.Sessions
	return aiPolicy{
		agentStart: s.AgentStartOn(), agentflowMCP: s.AgentflowMCPOn(),
		home: s.EffectiveHome(), unasked: s.EffectiveUnasked(),
		maxRunning: s.EffectiveMaxRunning(), maxDuration: s.EffectiveMaxDuration(),
		rules:        user.AI.Permissions.Rules,
		repoDisabled: repo.AI.Sessions.Disabled,
		repoAsk:      repo.AI.Permissions.Ask, repoDeny: repo.AI.Permissions.Deny,
		features:      aiFeaturesOf(user.AI.Features),
		draftDuration: user.AI.Drafts.EffectiveMaxDuration(),
	}
}

func aiFeaturesOf(f config.UserAIFeatures) map[string]string {
	out := map[string]string{}
	for _, id := range f.IDs() {
		out[id] = f.Harness(id)
	}
	return out
}

// aiLoosening says how going from a to b loosens the session policy, or "".
func aiLoosening(a, b aiPolicy) string {
	switch {
	case b.agentStart && !a.agentStart:
		return "Lets agents start ACP sessions again, each after your confirmation."
	case b.home == config.AISessionHomeReal && a.home != config.AISessionHomeReal:
		return "Gives session harnesses your real home folder, with your sign-ins and credentials."
	case b.maxRunning > a.maxRunning:
		return fmt.Sprintf("Lets %d sessions run at once instead of %d.", b.maxRunning, a.maxRunning)
	case b.maxDuration > a.maxDuration:
		return fmt.Sprintf("Lets a session run for %s instead of %s.", shortDuration(b.maxDuration), shortDuration(a.maxDuration))
	case b.unasked == config.AIUnaskedWarn && a.unasked != config.AIUnaskedWarn:
		return "Keeps a session running when its harness edits, runs or fetches something in the worktree without asking."
	case b.agentflowMCP && !a.agentflowMCP:
		return "Passes agentflow's read-only tools to session harnesses."
	}
	for _, name := range slices.Sorted(maps.Keys(b.rules)) {
		r := b.rules[name]
		if r.Decision == config.AIDecisionAllow && a.rules[name] != r {
			return fmt.Sprintf("Adds the session rule %s, which allows %s requests matching %s without asking.", name, r.Kind, r.Match)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(a.rules)) {
		r := a.rules[name]
		if r.Decision != config.AIDecisionDeny && r.Decision != config.AIDecisionAsk {
			continue
		}
		if b.rules[name] != r {
			return fmt.Sprintf("Removes or changes the session rule %s, which made matching %s requests %s.", name, r.Kind, r.Decision)
		}
	}
	// Setting or changing a feature's harness turns on, or redirects,
	// sending repository and task text to a model provider.
	for _, id := range (config.UserAIFeatures{}).IDs() {
		switch h := b.features[id]; {
		case h == "" || h == a.features[id]:
		case a.features[id] == "":
			return fmt.Sprintf("Turns on %s drafts: they send the listed repository and task text to the harness %s and its model provider.", id, h)
		default:
			return fmt.Sprintf("Sends %s drafts to the harness %s instead of %s, and so to its model provider.", id, h, a.features[id])
		}
	}
	if b.draftDuration > a.draftDuration {
		return fmt.Sprintf("Lets a draft run for %s instead of %s.", shortDuration(b.draftDuration), shortDuration(a.draftDuration))
	}
	if a.repoDisabled && !b.repoDisabled {
		return "Lets sessions start in this repository again."
	}
	for _, p := range a.repoAsk {
		if !slices.Contains(b.repoAsk, p) {
			return fmt.Sprintf("Stops always asking for session requests on %s.", p)
		}
	}
	for _, p := range a.repoDeny {
		if !slices.Contains(b.repoDeny, p) {
			return fmt.Sprintf("Stops denying session requests on %s.", p)
		}
	}
	return ""
}
