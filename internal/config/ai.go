package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// UserAI is the personal ai section: ACP harness detection (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 6 configuration keys"), ACP sessions ("Phase 7 configuration
// keys") and AI drafts ("Phase 8 configuration keys"). A repository has
// only the three tightening keys of RepoAI: it can set no draft key.
type UserAI struct {
	Harnesses   UserHarnesses     `yaml:"harnesses,omitempty"`
	Sessions    UserAISessions    `yaml:"sessions,omitempty"`
	Permissions UserAIPermissions `yaml:"permissions,omitempty"`
	Features    UserAIFeatures    `yaml:"features,omitempty"` // phase 8
	Drafts      UserAIDrafts      `yaml:"drafts,omitempty"`   // phase 8
}

// UserAIFeatures routes each draft feature to a harness. A feature with
// no harness is off: it sends nothing anywhere. Fixed fields, not a map, so
// each feature has its own labelled control; a later feature adds a field.
type UserAIFeatures struct {
	Commit UserAIFeature `yaml:"commit,omitempty"`
	PR     UserAIFeature `yaml:"pr,omitempty"`
	Ticket UserAIFeature `yaml:"ticket,omitempty"`
	AC     UserAIFeature `yaml:"ac,omitempty"`
	Reply  UserAIFeature `yaml:"reply,omitempty"`
}

// UserAIFeature is one draft feature's setting.
type UserAIFeature struct {
	Harness string `yaml:"harness,omitempty"` // a harness ID
}

// UserAIDrafts configures AI drafts. Every key is optional.
type UserAIDrafts struct {
	MaxDuration *time.Duration `yaml:"max_duration,omitempty"`
}

// AI draft feature IDs, in their fixed order (UserAIFeatures.IDs).
const (
	AIFeatureCommit = "commit"
	AIFeaturePR     = "pr"
	AIFeatureTicket = "ticket"
	AIFeatureAC     = "ac"
	AIFeatureReply  = "reply"
)

// Draft limits ("Phase 8 configuration keys").
const (
	DefaultAIDraftDuration = 3 * time.Minute
	MinAIDraftDuration     = 30 * time.Second
	MaxAIDraftDuration     = 15 * time.Minute
)

// IDs lists the draft feature IDs in their fixed order.
func (f UserAIFeatures) IDs() []string {
	return []string{AIFeatureCommit, AIFeaturePR, AIFeatureTicket, AIFeatureAC, AIFeatureReply}
}

// field returns the setting of feature id, or nil for an unknown ID.
func (f *UserAIFeatures) field(id string) *UserAIFeature {
	switch id {
	case AIFeatureCommit:
		return &f.Commit
	case AIFeaturePR:
		return &f.PR
	case AIFeatureTicket:
		return &f.Ticket
	case AIFeatureAC:
		return &f.AC
	case AIFeatureReply:
		return &f.Reply
	}
	return nil
}

// Harness is the harness ID feature id is routed to, or "" when the
// feature is off or unknown.
func (f UserAIFeatures) Harness(id string) string {
	if p := f.field(id); p != nil {
		return p.Harness
	}
	return ""
}

// SetHarness routes feature id to harness (empty turns it off). It reports
// false for an unknown feature ID.
func (f *UserAIFeatures) SetHarness(id, harness string) bool {
	p := f.field(id)
	if p == nil {
		return false
	}
	p.Harness = harness
	return true
}

// EffectiveMaxDuration is max_duration, defaulted and clamped.
func (d UserAIDrafts) EffectiveMaxDuration() time.Duration {
	return clampDuration(d.MaxDuration, DefaultAIDraftDuration, MinAIDraftDuration, MaxAIDraftDuration)
}

// UserAISessions configures ACP sessions. Every key is optional.
type UserAISessions struct {
	AgentStart      *bool             `yaml:"agent_start,omitempty"` // nil means on
	Home            string            `yaml:"home,omitempty"`        // real (default), isolated
	MaxRunning      *int              `yaml:"max_running,omitempty"`
	MaxDuration     *time.Duration    `yaml:"max_duration,omitempty"`
	IdleTimeout     *time.Duration    `yaml:"idle_timeout,omitempty"`
	AskTimeout      *time.Duration    `yaml:"ask_timeout,omitempty"`
	Unasked         string            `yaml:"unasked,omitempty"`          // warn (default), cancel
	AgentflowMCP    *bool             `yaml:"agentflow_mcp,omitempty"`    // nil means on
	KeepTranscripts *bool             `yaml:"keep_transcripts,omitempty"` // nil means on
	KeepDays        *int              `yaml:"keep_days,omitempty"`
	Modes           map[string]string `yaml:"modes,omitempty"` // harness ID -> session mode ID
}

// UserAIPermissions are the person's standing answers to session requests.
type UserAIPermissions struct {
	Rules map[string]AIRule `yaml:"rules,omitempty"`
}

// AIRule matches one request kind. Match is a worktree-relative slash glob
// for fs_read and fs_write, a program name glob (the resolved base name) for
// terminal, and a tool kind for tool.
type AIRule struct {
	Kind     string `yaml:"kind"`     // fs_read, fs_write, terminal, tool
	Match    string `yaml:"match"`    //
	Decision string `yaml:"decision"` // allow, ask, deny
}

// RepoAI is the repository ai section: only tightening keys.
type RepoAI struct {
	Sessions struct {
		Disabled bool `yaml:"disabled,omitempty"`
	} `yaml:"sessions,omitempty"`
	Permissions struct {
		Ask  []string `yaml:"ask,omitempty"`
		Deny []string `yaml:"deny,omitempty"`
	} `yaml:"permissions,omitempty"`
}

// Session defaults and limits ("Phase 7 configuration keys", "Phase 7
// limits").
const (
	AISessionHomeReal     = "real"
	AISessionHomeIsolated = "isolated"
	AIUnaskedWarn         = "warn"
	AIUnaskedCancel       = "cancel"
	DefaultAIMaxRunning   = 2
	MinAIMaxRunning       = 1
	MaxAIMaxRunning       = 4
	DefaultAIMaxDuration  = 30 * time.Minute
	MinAIMaxDuration      = time.Minute
	MaxAIMaxDuration      = 4 * time.Hour
	DefaultAIIdleTimeout  = 10 * time.Minute
	MinAIIdleTimeout      = time.Minute
	MaxAIIdleTimeout      = time.Hour
	DefaultAIAskTimeout   = 5 * time.Minute
	MinAIAskTimeout       = 30 * time.Second
	MaxAIAskTimeout       = 30 * time.Minute
	DefaultAIKeepDays     = 7
	MinAIKeepDays         = 1
	MaxAIKeepDays         = 90
	MaxAIModes            = 64
	MaxAIRules            = 64
	MaxAIRepoPaths        = 64
	AIRuleKindFSRead      = "fs_read"
	AIRuleKindFSWrite     = "fs_write"
	AIRuleKindTerminal    = "terminal"
	AIRuleKindTool        = "tool"
	AIDecisionAllow       = "allow"
	AIDecisionAsk         = "ask"
	AIDecisionDeny        = "deny"
)

// AgentStartOn reports the effective agent_start: nil means on.
func (s UserAISessions) AgentStartOn() bool { return s.AgentStart == nil || *s.AgentStart }

// AgentflowMCPOn reports the effective agentflow_mcp: nil means on.
func (s UserAISessions) AgentflowMCPOn() bool { return s.AgentflowMCP == nil || *s.AgentflowMCP }

// KeepTranscriptsOn reports the effective keep_transcripts: nil means on.
func (s UserAISessions) KeepTranscriptsOn() bool {
	return s.KeepTranscripts == nil || *s.KeepTranscripts
}

// EffectiveHome is real unless home is isolated.
func (s UserAISessions) EffectiveHome() string {
	if s.Home == AISessionHomeIsolated {
		return AISessionHomeIsolated
	}
	return AISessionHomeReal
}

// EffectiveUnasked is warn unless unasked is cancel.
func (s UserAISessions) EffectiveUnasked() string {
	if s.Unasked == AIUnaskedCancel {
		return AIUnaskedCancel
	}
	return AIUnaskedWarn
}

// EffectiveMaxRunning is max_running, defaulted and clamped.
func (s UserAISessions) EffectiveMaxRunning() int {
	return clampInt(s.MaxRunning, DefaultAIMaxRunning, MinAIMaxRunning, MaxAIMaxRunning)
}

// EffectiveKeepDays is keep_days, defaulted and clamped.
func (s UserAISessions) EffectiveKeepDays() int {
	return clampInt(s.KeepDays, DefaultAIKeepDays, MinAIKeepDays, MaxAIKeepDays)
}

// EffectiveMaxDuration is max_duration, defaulted and clamped.
func (s UserAISessions) EffectiveMaxDuration() time.Duration {
	return clampDuration(s.MaxDuration, DefaultAIMaxDuration, MinAIMaxDuration, MaxAIMaxDuration)
}

// EffectiveIdleTimeout is idle_timeout, defaulted and clamped.
func (s UserAISessions) EffectiveIdleTimeout() time.Duration {
	return clampDuration(s.IdleTimeout, DefaultAIIdleTimeout, MinAIIdleTimeout, MaxAIIdleTimeout)
}

// EffectiveAskTimeout is ask_timeout, defaulted and clamped.
func (s UserAISessions) EffectiveAskTimeout() time.Duration {
	return clampDuration(s.AskTimeout, DefaultAIAskTimeout, MinAIAskTimeout, MaxAIAskTimeout)
}

func clampInt(v *int, def, lo, hi int) int {
	if v == nil {
		return def
	}
	return min(max(*v, lo), hi)
}

// UserHarnesses configures harness detection.
type UserHarnesses struct {
	AutoProbe    *bool                            `yaml:"auto_probe,omitempty"`    // nil means on
	SignInCheck  *bool                            `yaml:"sign_in_check,omitempty"` // nil means on
	PollInterval *time.Duration                   `yaml:"poll_interval,omitempty"`
	ProbeTimeout *time.Duration                   `yaml:"probe_timeout,omitempty"`
	Disabled     []string                         `yaml:"disabled,omitempty"`
	Overrides    map[string]harnessdef.Definition `yaml:"overrides,omitempty"`
}

// Harness detection defaults and limits.
const (
	DefaultHarnessPollInterval = 30 * time.Second
	MinHarnessPollInterval     = 10 * time.Second
	MaxHarnessPollInterval     = 10 * time.Minute
	DefaultHarnessProbeTimeout = 5 * time.Second
	MinHarnessProbeTimeout     = 1 * time.Second
	MaxHarnessProbeTimeout     = 30 * time.Second
	MaxHarnessesDisabled       = 64
	MaxHarnessOverrides        = 64
)

// AutoProbeOn reports the effective auto_probe: nil means on.
func (h UserHarnesses) AutoProbeOn() bool { return h.AutoProbe == nil || *h.AutoProbe }

// SignInCheckOn reports the effective sign_in_check: nil means on.
func (h UserHarnesses) SignInCheckOn() bool { return h.SignInCheck == nil || *h.SignInCheck }

// EffectivePollInterval is poll_interval, defaulted and clamped to its limits.
func (h UserHarnesses) EffectivePollInterval() time.Duration {
	return clampDuration(h.PollInterval, DefaultHarnessPollInterval, MinHarnessPollInterval, MaxHarnessPollInterval)
}

// EffectiveProbeTimeout is probe_timeout, defaulted and clamped to its limits.
func (h UserHarnesses) EffectiveProbeTimeout() time.Duration {
	return clampDuration(h.ProbeTimeout, DefaultHarnessProbeTimeout, MinHarnessProbeTimeout, MaxHarnessProbeTimeout)
}

func clampDuration(v *time.Duration, def, lo, hi time.Duration) time.Duration {
	if v == nil || *v <= 0 {
		return def
	}
	return min(max(*v, lo), hi)
}

// ValidateAI checks the ai section's limits: the durations and the list
// sizes. Harness definitions are checked by harnessdef.Validate in the
// settings check chain and reported by detection; a bad override never
// stops agentflow from loading.
func ValidateAI(a UserAI) error {
	h := a.Harnesses
	for _, d := range []struct {
		name   string
		v      *time.Duration
		lo, hi time.Duration
	}{
		{"ai.harnesses.poll_interval", h.PollInterval, MinHarnessPollInterval, MaxHarnessPollInterval},
		{"ai.harnesses.probe_timeout", h.ProbeTimeout, MinHarnessProbeTimeout, MaxHarnessProbeTimeout},
	} {
		if d.v != nil && (*d.v < d.lo || *d.v > d.hi) {
			return fmt.Errorf("%s must be from %s to %s", d.name, d.lo, d.hi)
		}
	}
	if len(h.Disabled) > MaxHarnessesDisabled {
		return fmt.Errorf("ai.harnesses.disabled has more than %d items", MaxHarnessesDisabled)
	}
	if len(h.Overrides) > MaxHarnessOverrides {
		return fmt.Errorf("ai.harnesses.overrides has more than %d entries", MaxHarnessOverrides)
	}
	return validateAISessions(a)
}

// validateAISessions checks the sessions and permissions limits. Rule
// contents (kind, decision, glob) are checked by ValidateAIRule, in the
// settings check chain; agentflow never loosens on a rule it cannot read:
// the session chain ignores an invalid rule.
func validateAISessions(a UserAI) error {
	s := a.Sessions
	for _, d := range []struct {
		name   string
		v      *time.Duration
		lo, hi time.Duration
	}{
		{"ai.sessions.max_duration", s.MaxDuration, MinAIMaxDuration, MaxAIMaxDuration},
		{"ai.sessions.idle_timeout", s.IdleTimeout, MinAIIdleTimeout, MaxAIIdleTimeout},
		{"ai.sessions.ask_timeout", s.AskTimeout, MinAIAskTimeout, MaxAIAskTimeout},
	} {
		if d.v != nil && (*d.v < d.lo || *d.v > d.hi) {
			return fmt.Errorf("%s must be from %s to %s", d.name, d.lo, d.hi)
		}
	}
	for _, d := range []struct {
		name   string
		v      *int
		lo, hi int
	}{
		{"ai.sessions.max_running", s.MaxRunning, MinAIMaxRunning, MaxAIMaxRunning},
		{"ai.sessions.keep_days", s.KeepDays, MinAIKeepDays, MaxAIKeepDays},
	} {
		if d.v != nil && (*d.v < d.lo || *d.v > d.hi) {
			return fmt.Errorf("%s must be from %d to %d", d.name, d.lo, d.hi)
		}
	}
	switch s.Home {
	case "", AISessionHomeReal, AISessionHomeIsolated:
	default:
		return fmt.Errorf("ai.sessions.home must be real or isolated")
	}
	switch s.Unasked {
	case "", AIUnaskedWarn, AIUnaskedCancel:
	default:
		return fmt.Errorf("ai.sessions.unasked must be warn or cancel")
	}
	if len(s.Modes) > MaxAIModes {
		return fmt.Errorf("ai.sessions.modes has more than %d entries", MaxAIModes)
	}
	if len(a.Permissions.Rules) > MaxAIRules {
		return fmt.Errorf("ai.permissions.rules has more than %d entries", MaxAIRules)
	}
	return validateAIDrafts(a)
}

// validateAIDrafts checks the draft time limit. Feature harness IDs are
// checked by the settings check chain (the phase 6 ID rule) and by the
// draft itself: an ID no harness has refuses the draft, it never stops
// agentflow from loading.
func validateAIDrafts(a UserAI) error {
	if d := a.Drafts.MaxDuration; d != nil && (*d < MinAIDraftDuration || *d > MaxAIDraftDuration) {
		return fmt.Errorf("ai.drafts.max_duration must be from %s to %s", MinAIDraftDuration, MaxAIDraftDuration)
	}
	return nil
}

// ValidateAIRule checks one session permission rule: a known kind and
// decision, a glob that compiles, and no allow for a person-only tool kind
// (execute, switch_mode, other).
func ValidateAIRule(name string, r AIRule) error {
	switch r.Kind {
	case AIRuleKindFSRead, AIRuleKindFSWrite, AIRuleKindTerminal, AIRuleKindTool:
	default:
		return fmt.Errorf("ai.permissions.rules.%s: kind must be fs_read, fs_write, terminal or tool", name)
	}
	switch r.Decision {
	case AIDecisionAllow, AIDecisionAsk, AIDecisionDeny:
	default:
		return fmt.Errorf("ai.permissions.rules.%s: decision must be allow, ask or deny", name)
	}
	if r.Match == "" {
		return fmt.Errorf("ai.permissions.rules.%s: match is empty", name)
	}
	if _, err := path.Match(r.Match, ""); err != nil {
		return fmt.Errorf("ai.permissions.rules.%s: match is not a valid glob", name)
	}
	if r.Kind == AIRuleKindTool && r.Decision == AIDecisionAllow {
		switch r.Match {
		case "execute", "switch_mode", "other":
			return fmt.Errorf("ai.permissions.rules.%s: only a person may allow tool %s, one request at a time", name, r.Match)
		}
		if strings.ContainsAny(r.Match, "*?[") {
			return fmt.Errorf("ai.permissions.rules.%s: an allow rule for tool must name one tool kind", name)
		}
	}
	return nil
}

// AccountAgentflowHome is the agentflow home of the OS account's own home
// folder (~/.agentflow, or a legacy ~/.devflow when only that exists),
// whatever AGENTFLOW_HOME, DEVFLOW_HOME or HOME say.
func AccountAgentflowHome() (string, error) {
	home, err := accountHome()
	if err != nil {
		return "", err
	}
	return defaultHome(home)
}

// LoadUserAt reads <home>/config.yml the way LoadUser reads the default
// home's, for a caller that was given its agentflow home (an ACP session).
// A missing file gives the defaults.
func LoadUserAt(home string) (*User, error) {
	u := &User{}
	b, err := os.ReadFile(filepath.Join(home, "config.yml"))
	if err == nil {
		if err := yaml.Unmarshal(b, u); err != nil {
			return nil, fmt.Errorf("config.yml: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := validateAutonomy(u.Autonomy, "autonomy", u.ApprovalWindow, "approval_window"); err != nil {
		return nil, fmt.Errorf("config.yml: %w", err)
	}
	if err := ValidateAI(u.AI); err != nil {
		return nil, fmt.Errorf("config.yml: %w", err)
	}
	if u.Autonomy.Local == "" {
		u.Autonomy.defaultLocal = true
		u.Autonomy.Local = Confirm
	}
	if u.Autonomy.Remote == "" {
		u.Autonomy.defaultRemote = true
		u.Autonomy.Remote = Confirm
	}
	return u, nil
}
