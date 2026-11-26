package manifest

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Display and value limits that the Max* constants do not cover.
const (
	maxNameBytes        = 64
	maxDescriptionBytes = 512
	maxDenyBytes        = 256
	maxIDChars          = 40
	minIDChars          = 2
	maxArgs             = 16
	maxGlobBytes        = 128
	maxInValues         = 64
	maxInputDepth       = 8
)

var (
	idRe         = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
	nameRe       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	payloadKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	semverRe     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
		`(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
)

// reservedIDs are plugin IDs no manifest may use.
var reservedIDs = []string{"agentflow", "builtin", "daemon", "cli", "mcp"}

// eventFields are the envelope fields a condition or a template may read,
// besides payload.<key>.
var eventFields = []string{"type", "repo", "worktree", "source", "correlation_id"}

// AnnotateType is the one event type a plugin may publish.
const AnnotateType = string(bus.TypePluginAnnotationAdded)

// Validate checks a manifest against every rule of the ADR (section
// "Phase 5 manifest"). It returns nil or a *ValidationError that lists
// every problem it found, in a fixed order. It never touches the file
// system: whether exec.command exists in the plugin directory is a check of
// plugin add (install.Plan).
func Validate(m Manifest) error {
	v := &validator{}
	v.manifest(m)
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: v.problems}
}

type validator struct {
	problems []Problem
	settings map[string]bool // declared setting keys, for templates
}

func (v *validator) add(path, code, format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: path, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) manifest(m Manifest) {
	switch {
	case m.Schema == 0:
		v.add("schema", CodeRequired, "schema is required: use %d", SchemaVersion)
	case m.Schema != SchemaVersion:
		v.add("schema", CodeSyntax, "schema %d is not supported: this agentflow reads schema %d", m.Schema, SchemaVersion)
	}
	v.id(m.ID)
	v.text("name", m.Name, maxNameBytes, true, false)
	v.text("description", m.Description, maxDescriptionBytes, false, true)
	if !semverRe.MatchString(m.Version) {
		v.add("version", CodeVersion, "version must be a semantic version without a leading v, such as 0.1.0")
	}
	v.tier(m)
	v.settings = v.settingList(m.Settings)
	v.permissions(m.Permissions)
	v.extensions(m)
}

func (v *validator) id(id string) {
	switch {
	case len(id) < minIDChars || len(id) > maxIDChars || !idRe.MatchString(id):
		v.add("id", CodeID, "id must be %d to %d characters: lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen", minIDChars, maxIDChars)
	case slices.Contains(reservedIDs, id) || strings.HasPrefix(id, "agentflow-"):
		v.add("id", CodeID, "id %q is reserved", id)
	}
}

// text checks display text: required, at most max bytes, valid UTF-8 and no
// control characters, except line breaks and tabs when multiline.
func (v *validator) text(at, s string, max int, required, multiline bool) {
	switch {
	case required && strings.TrimSpace(s) == "":
		v.add(at, CodeRequired, "%s is required", at)
		return
	case len(s) > max:
		v.add(at, CodeLimit, "%s is longer than %d bytes", at, max)
		return
	}
	if !plainText(s, multiline) {
		v.add(at, CodeSyntax, "%s must be plain text%s", at, map[bool]string{true: "", false: " on one line"}[multiline])
	}
}

func plainText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsControl(r) || multiline && (r == '\n' || r == '\t') {
			continue
		}
		return false
	}
	return true
}

func (v *validator) tier(m Manifest) {
	switch m.Tier {
	case TierDeclarative:
		if m.Exec != nil {
			v.add("exec", CodeExec, "a declarative plugin runs no code, so it has no exec")
		}
	case TierProcess:
		v.exec(m.Exec)
	case TierBuiltin:
		v.add("tier", CodeTier, "a builtin plugin is compiled into agentflow and cannot come from a file")
	case "":
		v.add("tier", CodeRequired, "tier is required: declarative or process")
	default:
		v.add("tier", CodeTier, "unknown tier %q: use declarative or process", m.Tier)
	}
}

func (v *validator) exec(e *Exec) {
	if e == nil || strings.TrimSpace(e.Command) == "" {
		v.add("exec.command", CodeExec, "a process plugin needs exec.command: the executable, relative to the plugin directory")
		return
	}
	if err := ValidExecPath(e.Command); err != nil {
		v.add("exec.command", CodeExec, "%v", err)
	}
	if len(e.Args) > maxArgs {
		v.add("exec.args", CodeLimit, "exec.args has more than %d entries", maxArgs)
	}
	for i, a := range e.Args {
		if !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
			v.add(fmt.Sprintf("exec.args[%d]", i), CodeExec, "an argument must be plain text")
		}
	}
}

// ValidExecPath checks exec.command: a slash path inside the plugin
// directory. It is never looked up on PATH.
func ValidExecPath(command string) error {
	switch {
	case !utf8.ValidString(command) || strings.ContainsAny(command, "\x00\\:") || strings.IndexFunc(command, unicode.IsControl) >= 0:
		return fmt.Errorf("exec.command must be a plain relative path with forward slashes")
	case path.IsAbs(command):
		return fmt.Errorf("exec.command must be relative to the plugin directory")
	}
	for _, seg := range strings.Split(command, "/") {
		if seg == ".." {
			return fmt.Errorf("exec.command must stay inside the plugin directory, so it has no .. segment")
		}
	}
	if clean := path.Clean(command); clean == "." || strings.HasSuffix(command, "/") {
		return fmt.Errorf("exec.command must name a file")
	}
	return nil
}

// ---- permissions ----

func (v *validator) permissions(p Permissions) {
	v.list("permissions.subscribe", p.Subscribe, func(i int, s string) {
		at := fmt.Sprintf("permissions.subscribe[%d]", i)
		switch {
		case !validPattern(s):
			v.add(at, CodeSyntax, "%q is not an event pattern: dot-separated lowercase segments, * for one segment, ** last for the rest", s)
		case !Subscribable(s):
			v.add(at, CodePermission, "plugins cannot subscribe to %q: they receive only %s events and %s", s, strings.Join(SubscribableFamilies, ", "), typeList(SubscribableTypes))
		case !matchesKnownType(s):
			v.add(at, CodePermission, "%q matches no event type agentflow knows", s)
		}
	})
	v.list("permissions.publish", p.Publish, func(i int, s string) {
		if !slices.Contains(typeStrings(PublishableTypes), s) {
			v.add(fmt.Sprintf("permissions.publish[%d]", i), CodePermission, "plugins can publish only %s, not %q", typeList(PublishableTypes), s)
		}
	})
	v.list("permissions.invoke", p.Invoke, func(i int, s string) {
		if !slices.Contains(InvocableCommands, s) {
			v.add(fmt.Sprintf("permissions.invoke[%d]", i), CodePermission, "plugins can run only %s, not %q", strings.Join(InvocableCommands, ", "), s)
		}
	})
}

func (v *validator) list(at string, list []string, each func(i int, s string)) {
	if len(list) > MaxPatterns {
		v.add(at, CodeLimit, "%s has more than %d entries", at, MaxPatterns)
		return
	}
	for i, s := range list {
		each(i, s)
	}
}

func validPattern(s string) bool {
	_, err := bus.ParsePattern(s)
	return err == nil
}

// Subscribable reports whether a valid event pattern stays inside what a
// plugin may receive: a type of SubscribableFamilies, or one of
// SubscribableTypes. "**" and patterns with a wildcard family never do.
func Subscribable(pattern string) bool {
	if slices.Contains(typeStrings(SubscribableTypes), pattern) {
		return true
	}
	family, rest, ok := strings.Cut(pattern, ".")
	return ok && rest != "" && slices.Contains(SubscribableFamilies, family)
}

func matchesKnownType(pattern string) bool {
	p := bus.Pattern(pattern)
	for _, info := range bus.Types() {
		if p.Match(info.Type) {
			return true
		}
	}
	return false
}

// Covers reports whether the granted pattern matches every type the wanted
// pattern can match, so a handler's On is allowed by Permissions.Subscribe.
func Covers(granted, wanted string) bool {
	gs, ws := strings.Split(granted, "."), strings.Split(wanted, ".")
	for i, g := range gs {
		if g == "**" && i == len(gs)-1 {
			return len(ws) > i
		}
		if i >= len(ws) || ws[i] == "**" {
			return false
		}
		if g != "*" && g != ws[i] {
			return false
		}
	}
	return len(gs) == len(ws)
}

func covered(granted []string, wanted string) bool {
	return slices.ContainsFunc(granted, func(g string) bool { return Covers(g, wanted) })
}

func typeStrings(types []bus.Type) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	return out
}

func typeList(types []bus.Type) string { return strings.Join(typeStrings(types), ", ") }

// ---- extensions ----

func (v *validator) extensions(m Manifest) {
	e := m.Extensions
	reserved := false
	for _, r := range []struct {
		point Point
		used  bool
	}{
		{PointHarness, e.Harness != nil}, {PointProvider, e.Provider != nil}, {PointSource, e.Source != nil},
		{PointSink, e.Sink != nil}, {PointWidget, e.Widget != nil},
	} {
		if r.used {
			reserved = true
			v.add("extensions."+string(r.point), CodeReserved, "the extension point %q is reserved: no agentflow release implements it yet", r.point)
		}
	}
	if !reserved && len(e.Handler) == 0 && len(e.PermissionRule) == 0 {
		v.add("extensions", CodeRequired, "a plugin adds at least one handler or permission rule")
	}
	if len(e.Handler) > MaxHandlers {
		v.add("extensions.handler", CodeLimit, "a plugin has at most %d handlers", MaxHandlers)
	}
	if len(e.PermissionRule) > MaxRules {
		v.add("extensions.permission_rule", CodeLimit, "a plugin has at most %d permission rules", MaxRules)
	}
	seen := map[string]bool{}
	for i, h := range e.Handler {
		v.handler(fmt.Sprintf("extensions.handler[%d]", i), m, h, seen)
	}
	seen = map[string]bool{}
	for i, r := range e.PermissionRule {
		v.rule(fmt.Sprintf("extensions.permission_rule[%d]", i), m, r, seen)
	}
}

func (v *validator) name(at, name string, seen map[string]bool, what string) {
	switch {
	case !nameRe.MatchString(name) || len(name) > maxIDChars:
		v.add(at+".name", CodeID, "a %s name is lowercase letters, digits and hyphens, starting with a letter, at most %d characters", what, maxIDChars)
	case seen[name]:
		v.add(at+".name", CodeID, "two %ss are named %q", what, name)
	}
	seen[name] = true
}

func (v *validator) handler(at string, m Manifest, h Handler, seen map[string]bool) {
	v.name(at, h.Name, seen, "handler")
	switch {
	case len(h.On) == 0:
		v.add(at+".on", CodeRequired, "a handler lists at least one event pattern in on")
	case len(h.On) > MaxPatterns:
		v.add(at+".on", CodeLimit, "on has more than %d patterns", MaxPatterns)
	}
	for i, p := range h.On {
		if i >= MaxPatterns {
			break
		}
		pat := fmt.Sprintf("%s.on[%d]", at, i)
		switch {
		case !validPattern(p):
			v.add(pat, CodeSyntax, "%q is not an event pattern", p)
		case !Subscribable(p):
			v.add(pat, CodePermission, "plugins cannot subscribe to %q", p)
		case !covered(m.Permissions.Subscribe, p):
			v.add(pat, CodeUngranted, "%q is not covered by permissions.subscribe", p)
		}
	}
	switch m.Tier {
	case TierProcess:
		if len(h.When) > 0 || len(h.Actions) > 0 {
			v.add(at, CodeRule, "a process plugin's handler only declares on: the process decides what to do, so it has no when or actions")
		}
	case TierDeclarative:
		v.declarative(at, m, h)
	}
}

func (v *validator) declarative(at string, m Manifest, h Handler) {
	if len(h.When) > MaxConditions {
		v.add(at+".when", CodeLimit, "when has more than %d conditions", MaxConditions)
	}
	for i, c := range h.When {
		if i >= MaxConditions {
			break
		}
		v.condition(fmt.Sprintf("%s.when[%d]", at, i), c)
	}
	switch {
	case len(h.Actions) == 0:
		v.add(at+".actions", CodeAction, "a declarative handler has at least one action")
	case len(h.Actions) > MaxActions:
		v.add(at+".actions", CodeLimit, "a handler has at most %d actions", MaxActions)
	}
	for i, a := range h.Actions {
		if i >= MaxActions {
			break
		}
		v.action(fmt.Sprintf("%s.actions[%d]", at, i), m, h, a)
	}
}

// ---- conditions ----

func (v *validator) condition(at string, c Condition) {
	if !validEventField(c.Field) {
		v.add(at+".field", CodeCondition, "field must be %s, or payload.<key> with dots for nested keys", strings.Join(eventFields, ", "))
	}
	n := 0
	for _, set := range []bool{c.Equals != nil, c.NotEquals != nil, c.In != nil, c.Exists != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		v.add(at, CodeCondition, "a condition has exactly one of equals, not_equals, in and exists")
		return
	}
	switch {
	case c.Equals != nil && !scalar(c.Equals):
		v.add(at+".equals", CodeCondition, "equals takes text, a number or true or false")
	case c.NotEquals != nil && !scalar(c.NotEquals):
		v.add(at+".not_equals", CodeCondition, "not_equals takes text, a number or true or false")
	case c.In != nil && (len(c.In) == 0 || len(c.In) > maxInValues):
		v.add(at+".in", CodeCondition, "in lists 1 to %d values", maxInValues)
	case c.In != nil && slices.ContainsFunc(c.In, func(x any) bool { return !scalar(x) }):
		v.add(at+".in", CodeCondition, "in takes text, numbers or true and false")
	}
}

func scalar(x any) bool {
	switch x.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	}
	return false
}

// validEventField reports whether f is a field path of the event envelope:
// one of the plain fields, or payload.<key>[.<key>].
func validEventField(f string) bool {
	if slices.Contains(eventFields, f) {
		return true
	}
	rest, ok := strings.CutPrefix(f, "payload.")
	if !ok {
		return false
	}
	for _, k := range strings.Split(rest, ".") {
		if !payloadKeyRe.MatchString(k) {
			return false
		}
	}
	return true
}

// ---- actions ----

func (v *validator) action(at string, m Manifest, h Handler, a Action) {
	switch {
	case a.Invoke != nil && a.Annotate != nil, a.Invoke == nil && a.Annotate == nil:
		v.add(at, CodeAction, "an action is exactly one of invoke and annotate")
	case a.Invoke != nil:
		v.invoke(at+".invoke", m, *a.Invoke)
	default:
		v.annotate(at+".annotate", m, h, *a.Annotate)
	}
}

func (v *validator) invoke(at string, m Manifest, a InvokeAction) {
	switch {
	case a.Command == "":
		v.add(at+".command", CodeRequired, "invoke needs a command")
	case !slices.Contains(InvocableCommands, a.Command):
		v.add(at+".command", CodePermission, "plugins can run only %s, not %q", strings.Join(InvocableCommands, ", "), a.Command)
	case !slices.Contains(m.Permissions.Invoke, a.Command):
		v.add(at+".command", CodeUngranted, "%q is not listed in permissions.invoke", a.Command)
	}
	if _, has := a.Input["confirm_token"]; has {
		v.add(at+".input.confirm_token", CodeAction, "a plugin never supplies a confirm token: a person confirms in the console")
	}
	v.walk(at+".input", a.Input, 0)
}

func (v *validator) annotate(at string, m Manifest, h Handler, a AnnotateAction) {
	if len(h.On) == 0 || slices.ContainsFunc(h.On, func(p string) bool { return !strings.HasPrefix(p, "op.") }) {
		v.add(at, CodeAction, "annotate works only in a handler whose on patterns are all op.* events")
	}
	if !slices.Contains(m.Permissions.Publish, AnnotateType) {
		v.add(at, CodeUngranted, "annotate needs %s in permissions.publish", AnnotateType)
	}
	if strings.TrimSpace(a.Text) == "" {
		v.add(at+".text", CodeRequired, "annotate needs text")
	}
	v.template(at+".text", a.Text)
	v.template(at+".url", a.URL)
	if a.URL != "" && !strings.Contains(a.URL, TemplateOpen) {
		if u, err := url.Parse(a.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			v.add(at+".url", CodeAction, "url must be an https address")
		}
	}
	switch a.Level {
	case "", bus.AnnotationInfo, bus.AnnotationWarning:
	default:
		v.add(at+".level", CodeAction, "level is %s or %s", bus.AnnotationInfo, bus.AnnotationWarning)
	}
}

// walk checks every string of an input value as a template.
func (v *validator) walk(at string, x any, depth int) {
	if depth > maxInputDepth {
		v.add(at, CodeLimit, "input is nested more than %d levels deep", maxInputDepth)
		return
	}
	switch t := x.(type) {
	case string:
		v.template(at, t)
	case []any:
		for i, e := range t {
			v.walk(fmt.Sprintf("%s[%d]", at, i), e, depth+1)
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			v.walk(at+"."+k, t[k], depth+1)
		}
	case map[any]any:
		v.add(at, CodeSyntax, "input keys must be text")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// template checks the placeholders of one string: {{event.<field>}} and
// {{settings.<key>}} with a declared key, nothing else.
func (v *validator) template(at, s string) {
	for {
		start := strings.Index(s, TemplateOpen)
		if start < 0 {
			return
		}
		end := strings.Index(s[start:], TemplateClose)
		if end < 0 {
			v.add(at, CodeTemplate, "a {{ is never closed")
			return
		}
		inner := s[start+len(TemplateOpen) : start+end]
		s = s[start+end+len(TemplateClose):]
		kind, key, _ := strings.Cut(inner, ".")
		switch {
		case kind == "event" && validEventField(key):
		case kind == "settings" && v.settings[key]:
		case kind == "settings":
			v.add(at, CodeTemplate, "{{%s}} names a setting the plugin does not declare", inner)
		default:
			v.add(at, CodeTemplate, "{{%s}} is not a placeholder: use {{event.<field>}} or {{settings.<key>}}, with no spaces", inner)
		}
	}
}

// ---- permission rules ----

func (v *validator) rule(at string, m Manifest, r PermissionRule, seen map[string]bool) {
	v.name(at, r.Name, seen, "permission rule")
	if m.Tier == TierProcess {
		v.add(at, CodeRule, "a process plugin has no permission rules: a rule that crashed would have to fail open or block every command")
	}
	lists := map[string][]string{"operation": r.Match.Operation, "class": r.Match.Class, "action": r.Match.Action,
		"origin": r.Match.Origin, "branch": r.Match.Branch}
	empty := true
	for _, key := range []string{"operation", "class", "action", "origin", "branch"} {
		list := lists[key]
		if len(list) > 0 {
			empty = false
		}
		if len(list) > MaxPatterns {
			v.add(at+".match."+key, CodeLimit, "%s has more than %d entries", key, MaxPatterns)
			continue
		}
		for i, g := range list {
			if g == "" || len(g) > maxGlobBytes || !plainText(g, false) {
				v.add(fmt.Sprintf("%s.match.%s[%d]", at, key, i), CodeRule, "a match entry is a glob of at most %d bytes", maxGlobBytes)
			} else if _, err := path.Match(g, ""); err != nil {
				v.add(fmt.Sprintf("%s.match.%s[%d]", at, key, i), CodeRule, "%q is not a valid glob", g)
			}
		}
	}
	if empty {
		v.add(at+".match", CodeRule, "a rule matches on at least one of operation, class, action, origin and branch")
	}
	switch {
	case strings.TrimSpace(r.Deny) == "":
		v.add(at+".deny", CodeRequired, "a rule needs a deny message")
	case len(r.Deny) > maxDenyBytes:
		v.add(at+".deny", CodeLimit, "deny is longer than %d bytes", maxDenyBytes)
	case !plainText(r.Deny, false):
		v.add(at+".deny", CodeSyntax, "deny must be plain text on one line")
	}
}
