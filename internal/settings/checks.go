package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The check chain (ADR, "Phase 4 diagnostics and the check chain"): a Chain
// of Responsibility whose links run in a fixed order. Each link adds
// Diagnostics; a link skips an edit that an earlier link marked with an
// error.

// edit is one Edit as the chain sees it.
type edit struct {
	Edit
	index  int
	layer  config.FieldLayer
	keys   []string
	field  config.CatalogField
	rest   []string     // keys inside the field
	node   config.Field // the schema at the edit's path
	known  bool
	failed bool
	// before and after are the edit's layer value at its path, in the file
	// as read and once the edit applied to the files so far.
	before, after       json.RawMessage
	beforeSet, afterSet bool
	// prior and next are both layer files just before and just after this
	// edit applied, kept for the policy link of a loosening field.
	prior, next [2]string
	loosens     string
}

// check runs one chain.
type check struct {
	s     *Service
	ctx   context.Context
	cs    ChangeSet
	files files
	ev    evaluated
	edits []*edit
	diags []Diagnostic
	// next holds both files after every edit that passed.
	next     [2]string
	planDiff string
	// rules caches rulePlugins.
	rules *rulePlugins
}

type link func(c *check)

// chain is the order of the ADR's table.
var chain = []link{syntaxLink, catalogLink, schemaLink, rulesLink, referencesLink, secretsLink, filesLink, policyLink, conflictLink, effectLink}

func (c *check) add(sev Severity, e *edit, code, msg string) {
	d := Diagnostic{Severity: sev, Edit: -1, Code: code, Message: msg}
	if e != nil {
		d.ID, d.Edit = e.ID, e.index
		if sev == SeverityError {
			e.failed = true
		}
	}
	c.diags = append(c.diags, d)
}

// live yields the edits no link has failed.
func (c *check) live() []*edit {
	var out []*edit
	for _, e := range c.edits {
		if !e.failed {
			out = append(out, e)
		}
	}
	return out
}

func layerIndex(l config.FieldLayer) int {
	if l == config.FieldPersonal {
		return 0
	}
	return 1
}

// 1. ChangeSet syntax and limits.
func syntaxLink(c *check) {
	if len(c.cs.Edits) > MaxEdits {
		c.add(SeverityError, nil, CodeSyntax, fmt.Sprintf("A change set holds at most %d edits.", MaxEdits))
		for _, e := range c.edits {
			e.failed = true
		}
		return
	}
	if b, _ := json.Marshal(c.cs); len(b) > MaxBytes {
		c.add(SeverityError, nil, CodeSyntax, "The change set exceeds 1 MiB.")
		for _, e := range c.edits {
			e.failed = true
		}
		return
	}
	switch c.cs.Repo.Mode {
	case "", RepoDraft, RepoDirect, RepoWorktree:
	default:
		c.add(SeverityError, nil, CodeSyntax, "The repository mode must be draft, direct or worktree.")
	}
	for _, e := range c.edits {
		layer, keys, err := fieldKeys(e.ID)
		switch {
		case err != nil:
			c.add(SeverityError, e, CodeSyntax, "The field ID must be <layer>:<path>, with personal or repo as the layer.")
			continue
		case containsWildcard(keys):
			c.add(SeverityError, e, CodeSyntax, "A field ID names a concrete path, without *.")
			continue
		}
		e.layer, e.keys = layer, keys
		if layer == config.FieldPersonal && c.files.personalErr != nil {
			c.add(SeverityError, e, CodeConfig, "Cannot change personal settings: "+c.files.personalErr.Error()+".")
			continue
		}
		switch {
		case len(e.Value) > MaxValueBytes || len(e.Expect) > MaxValueBytes:
			c.add(SeverityError, e, CodeSyntax, "A value is at most 64 KiB.")
		case e.Op == OpSet && (len(e.Value) == 0 || !json.Valid(e.Value)):
			c.add(SeverityError, e, CodeSyntax, "A set edit needs a JSON value.")
		case e.Op == OpUnset && len(e.Value) > 0:
			c.add(SeverityError, e, CodeSyntax, "An unset edit takes no value.")
		case e.Op != OpSet && e.Op != OpUnset:
			c.add(SeverityError, e, CodeSyntax, "The op must be set or unset.")
		case len(e.Expect) > 0 && !json.Valid(e.Expect):
			c.add(SeverityError, e, CodeSyntax, "expect must be JSON.")
		}
	}
	for i, a := range c.edits {
		for _, b := range c.edits[:i] {
			if a.layer == "" || b.layer == "" || a.layer != b.layer {
				continue
			}
			if isPrefix(a.keys, b.keys) || isPrefix(b.keys, a.keys) {
				c.add(SeverityError, a, CodeSyntax, fmt.Sprintf("Edit %d changes the same field as edit %d.", a.index, b.index))
				break
			}
		}
	}
}

// 2. Catalog lookup under the provider selected so far.
func catalogLink(c *check) {
	selected := map[string]string{}
	for _, r := range config.ProviderRoles {
		selected[r] = selectedProvider(c.files.repo.Content, r)
	}
	other := map[config.FieldLayer]config.FieldLayer{config.FieldPersonal: config.FieldRepo, config.FieldRepo: config.FieldPersonal}
	for _, e := range c.live() {
		provider := ""
		role, n := "", 0
		if e.layer == config.FieldRepo {
			role, n = roleOf(e.keys)
		}
		if role != "" {
			provider = selected[role]
		}
		f, rest, ok := c.s.Catalog.Lookup(e.ID, provider)
		if !ok {
			alt := config.FieldID(other[e.layer], config.FormatFieldPath(e.keys...))
			if _, _, found := c.s.Catalog.Lookup(alt, ""); found {
				c.add(SeverityError, e, CodeLayer, fmt.Sprintf("This setting belongs to the %s layer: use %s.", other[e.layer], alt))
				continue
			}
			if f2, rest2, found := c.s.Catalog.Lookup(e.ID, ""); found && f2.Provider != "" {
				f, rest, ok = f2, rest2, true
				c.add(SeverityWarning, e, CodeInactive, fmt.Sprintf("%s is a setting of %s, which %s does not select; it has no effect until it does.", f.Path, f.Provider, role))
			}
		}
		if !ok {
			c.add(SeverityError, e, CodeUnknown, "No setting has this ID.")
			continue
		}
		e.field, e.rest, e.known = f, rest, true
		e.node = nodeAt(f.Schema, rest)
		if role != "" && len(e.keys) == n+1 && e.keys[n] == "provider" {
			if e.Op == OpUnset {
				selected[role] = selectedProvider("", role)
			} else {
				var id string
				_ = json.Unmarshal(e.Value, &id)
				selected[role] = id
			}
		}
	}
}

// nodeAt is the schema at keys inside f.
func nodeAt(f config.Field, keys []string) config.Field {
	for _, k := range keys {
		switch {
		case f.Items != nil:
			f = *f.Items
		case f.Properties[k].Type != "":
			f = f.Properties[k]
		case f.AdditionalProperties != nil:
			f = *f.AdditionalProperties
		default:
			return config.Field{}
		}
	}
	return f
}

// 3. Schema: type, enum, nonnegative, required.
func schemaLink(c *check) {
	for _, e := range c.live() {
		if e.Op == OpUnset {
			if e.node.Required && len(e.rest) == 0 {
				c.add(SeverityError, e, CodeRequired, "This setting is required; set a value instead.")
			}
			continue
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(e.Value))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			c.add(SeverityError, e, CodeType, "The value is not JSON.")
			continue
		}
		if code, msg := validate(e.node, v, ""); code != "" {
			c.add(SeverityError, e, code, msg)
		}
	}
}

// validate checks v against schema f. at names the position for messages.
func validate(f config.Field, v any, at string) (string, string) {
	where := func(msg string) string {
		if at == "" {
			return msg
		}
		return at + ": " + msg
	}
	if v == nil {
		if f.Nullable || f.Type == "" {
			return "", ""
		}
		return CodeType, where("null is not allowed here; unset the setting instead.")
	}
	switch f.Type {
	case "":
		return "", ""
	case "boolean":
		if _, ok := v.(bool); !ok {
			return CodeType, where("expected true or false.")
		}
	case "integer":
		n, ok := v.(json.Number)
		i, err := n.Int64()
		if !ok || err != nil {
			return CodeType, where("expected a whole number.")
		}
		if f.Nonnegative && i < 0 {
			return CodeRange, where("expected zero or more.")
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return CodeType, where("expected text.")
		}
		if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
			return CodeEnum, where("expected one of " + strings.Join(f.Enum, ", ") + ".")
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return CodeType, where("expected a list.")
		}
		if f.Items != nil {
			for i, item := range list {
				if code, msg := validate(*f.Items, item, joinAt(at, fmt.Sprint(i))); code != "" {
					return code, msg
				}
			}
		}
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return CodeType, where("expected an object.")
		}
		for k, item := range m {
			child, known := f.Properties[k]
			if !known {
				if f.AdditionalProperties == nil {
					return CodeType, where(fmt.Sprintf("unknown key %q.", k))
				}
				child = *f.AdditionalProperties
			}
			if code, msg := validate(child, item, joinAt(at, k)); code != "" {
				return code, msg
			}
		}
	}
	return "", ""
}

func joinAt(at, k string) string {
	if at == "" {
		return k
	}
	return at + "." + k
}

// 4. Field rules: min/max, duration limits, autonomy action names and
// levels, release-layer constraints.
func rulesLink(c *check) {
	for _, e := range c.live() {
		m := e.field.Meta
		if e.Op == OpUnset {
			continue
		}
		var v any
		_ = json.Unmarshal(e.Value, &v)
		if n, ok := v.(float64); ok && len(e.rest) == 0 && m.Widget == config.WidgetStepper {
			// The control's limits only guide: a value the code accepts
			// stays savable, and the whole-file link refuses what it does not.
			if m.Min != nil && n < float64(*m.Min) || m.Max != nil && n > float64(*m.Max) {
				c.add(SeverityWarning, e, CodeRange, rangeText(m.Min, m.Max))
			}
		}
		if m.Widget == config.WidgetDuration && len(e.rest) == 0 {
			if code, msg := durationRule(v, m); code == CodeType {
				c.add(SeverityError, e, code, msg)
				continue
			} else if code != "" {
				c.add(SeverityWarning, e, code, msg)
			}
		}
		if code, msg := actionRule(e, v); code != "" {
			c.add(SeverityError, e, code, msg)
			continue
		}
		if e.field.Provider != "" && len(e.keys) > 0 {
			role, n := roleOf(e.keys)
			sel := selection(c.ev.cfg, role)
			if sel.Provider == e.field.Provider && len(e.keys) > n+1 {
				if con, ok := sel.Constraints[strings.Join(e.keys[n+1:], ".")]; ok && con.Lock == true {
					c.add(SeverityError, e, CodeLocked, "The release definition locks this setting.")
				}
			}
		}
	}
}

func rangeText(lo, hi *int) string {
	switch {
	case lo != nil && hi != nil:
		return fmt.Sprintf("Expected a number from %d to %d.", *lo, *hi)
	case lo != nil:
		return fmt.Sprintf("Expected %d or more.", *lo)
	default:
		return fmt.Sprintf("Expected %d or less.", *hi)
	}
}

func durationRule(v any, m config.FieldMeta) (string, string) {
	if v == nil {
		return "", ""
	}
	s, ok := v.(string)
	d, err := time.ParseDuration(s)
	if !ok || err != nil {
		return CodeType, "Expected a duration such as 5m or 1h."
	}
	lo, _ := time.ParseDuration(m.MinDuration)
	hi, _ := time.ParseDuration(m.MaxDuration)
	if m.MinDuration != "" && d < lo || m.MaxDuration != "" && d > hi {
		return CodeRange, fmt.Sprintf("Expected a duration from %s to %s.", m.MinDuration, m.MaxDuration)
	}
	return "", ""
}

// actionRule checks autonomy action overrides: known action names and
// levels.
func actionRule(e *edit, v any) (string, string) {
	base := []string{"autonomy", "actions"}
	if e.layer == config.FieldRepo {
		base = []string{"policy", "max_autonomy", "actions"}
	}
	if !isPrefix(base, e.keys) {
		return "", ""
	}
	levels := map[string]any{}
	switch extra := e.keys[len(base):]; len(extra) {
	case 0:
		m, _ := v.(map[string]any)
		levels = m
	case 1:
		levels[extra[0]] = v
	default:
		return CodeReference, "An action override is one level."
	}
	for action, level := range levels {
		if !config.ValidAutonomyAction(action) {
			return CodeReference, fmt.Sprintf("%q is not an autonomy action.", action)
		}
		switch config.Level(fmt.Sprint(level)) {
		case config.Suggest, config.Confirm, config.Auto:
		default:
			return CodeEnum, "Expected suggest, confirm or auto."
		}
	}
	return "", ""
}

// 5. References: environment variables of credential references.
func referencesLink(c *check) {
	for _, e := range c.live() {
		if e.Op == OpUnset || e.field.Meta.Widget != config.WidgetCredentialRef {
			continue
		}
		var name string
		if json.Unmarshal(e.Value, &name) == nil && name != "" && os.Getenv(name) == "" {
			c.add(SeverityWarning, e, CodeCredential, fmt.Sprintf("The environment variable %s is not set here.", name))
		}
	}
}

// 6. Secrets: a value that looks like a credential, outside a credential
// reference.
func secretsLink(c *check) {
	for _, e := range c.live() {
		if e.Op == OpUnset || e.field.Meta.Widget == config.WidgetCredentialRef {
			continue
		}
		if consolecfg.Sensitive(string(e.Value)) || consolecfg.Sensitive(e.field.Path+": "+string(e.Value)) {
			c.add(SeverityError, e, CodeCredential, "This looks like a credential. Store it in an environment variable and reference the variable instead.")
		}
	}
}

// 7. Whole files: apply the edits in order, then the edited files must load.
func filesLink(c *check) {
	c.next = [2]string{c.files.personal.Content, c.files.repo.Content}
	repoEdited := false
	for _, e := range c.edits {
		if !e.failed && e.layer == config.FieldRepo && c.s.Root == "" {
			c.add(SeverityError, e, CodeConfig, errNoRepository.Error()+".")
		}
		if !e.failed && e.layer == config.FieldRepo && !c.files.repo.Exists {
			c.add(SeverityError, e, CodeConfig, "This repository has no .agentflow/config.yml; run agentflow init first.")
		}
	}
	for _, e := range c.edits {
		if e.layer == "" {
			continue
		}
		i := layerIndex(e.layer)
		e.before, e.beforeSet, _ = consolecfg.LayerValue(c.layerContent(e.layer), e.keys)
		if e.failed {
			continue
		}
		e.prior = c.next
		var value json.RawMessage
		if e.Op == OpSet {
			value = e.Value
		}
		next, err := consolecfg.EditLayer(c.next[i], e.keys, value, e.Op == OpUnset)
		if err != nil {
			c.add(SeverityError, e, CodeConfig, safeMessage(err))
			continue
		}
		c.next[i] = next
		e.next = c.next
		e.after, e.afterSet, _ = consolecfg.LayerValue(next, e.keys)
		repoEdited = repoEdited || e.layer == config.FieldRepo
	}
	if c.next[0] != c.files.personal.Content {
		if _, err := parseUser(c.next[0]); err != nil {
			c.add(SeverityError, nil, CodeConfig, PersonalFile+": "+safeMessage(err))
		}
	}
	if repoEdited && c.next[1] != c.files.repo.Content {
		plan, err := consolecfg.Preview(c.ctx, c.s.Root, RepoFile, c.next[1], consolecfg.Digest(c.files.repo.Content))
		if err != nil {
			c.add(SeverityError, nil, CodeConfig, RepoFile+": "+safeMessage(err))
		} else {
			c.planDiff = plan.Diff
		}
	}
}

func (c *check) layerContent(l config.FieldLayer) string {
	if l == config.FieldPersonal {
		return c.files.personal.Content
	}
	return c.files.repo.Content
}

// 8. Policy: a personal level above the repository ceiling; a loosening
// edit.
func policyLink(c *check) {
	for _, e := range c.live() {
		if e.next == ([2]string{}) {
			continue
		}
		if e.field.Meta.Loosens != config.LoosensNone {
			if msg := loosening(e.prior, e.next, c.rulePlugins()); msg != "" {
				e.loosens = msg
				c.add(SeverityInfo, e, CodeLoosens, msg)
			}
		}
		if e.layer == config.FieldPersonal {
			if msg := capped(e.keys, c.next); msg != "" {
				c.add(SeverityWarning, e, CodeCapped, msg)
			}
		}
	}
}

// policyState is the effective policy of both layer files.
type policyState struct {
	levels    map[string]config.Level // "<class>/<action>", action "" for the class
	window    time.Duration
	autoPR    bool
	merge     bool
	protected []string
	// plugins are the plugins the personal layer enables, and disabled the
	// plugins the repository turns off for itself.
	plugins  map[string]bool
	disabled []string
}

func policyOf(files [2]string) policyState {
	user, err := parseUser(files[0])
	if err != nil {
		user = &config.User{}
	}
	repo, _, err := config.ParseRepoCore([]byte(files[1]))
	if err != nil || repo == nil {
		repo, _, _ = config.ParseRepoCore(nil)
	}
	p := policyState{levels: map[string]config.Level{}, autoPR: repo.Policy.AllowAutoPRPublication, merge: repo.Policy.CoordinatorMerge, protected: repo.Policy.ProtectedBranches,
		plugins: map[string]bool{}, disabled: repo.Plugins.Disabled}
	for id, plugin := range user.Plugins {
		p.plugins[id] = plugin.Enabled
	}
	for _, action := range append([]string{""}, config.AutonomyActions()...) {
		for _, class := range []string{"local", "remote"} {
			d := config.ResolveAutonomy(user, repo, class, action, false)
			p.levels[class+"/"+action] = d.Level
			p.window = d.Window
		}
	}
	return p
}

func rank(l config.Level) int {
	switch l {
	case config.Suggest:
		return 0
	case config.Auto:
		return 2
	}
	return 1
}

// rulePlugins says which plugins may carry a permission rule. A rule only
// denies operations, so turning such a plugin off loosens policy.
type rulePlugins struct {
	ids map[string]bool
	// unread is set when the registry cannot be read: any plugin may then
	// have a rule.
	unread bool
}

func (r rulePlugins) may(id string) bool { return r.unread || r.ids[id] }

// rulePlugins reads the registry once per check.
func (c *check) rulePlugins() rulePlugins {
	if c.rules == nil {
		c.rules = &rulePlugins{ids: map[string]bool{}}
		reg, err := manifest.ReadRegistry(c.s.Home)
		c.rules.unread = err != nil
		for _, p := range reg.Plugins {
			if len(p.Manifest.Extensions.PermissionRule) > 0 {
				c.rules.ids[p.ID] = true
			}
		}
	}
	return *c.rules
}

// loosening says how going from before to after loosens policy, or "".
// rules are the plugins that may carry a permission rule.
func loosening(before, after [2]string, rules rulePlugins) string {
	a, b := policyOf(before), policyOf(after)
	keys := make([]string, 0, len(a.levels))
	for k := range a.levels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if rank(b.levels[k]) > rank(a.levels[k]) {
			class, action, _ := strings.Cut(k, "/")
			what := class + " actions"
			if action != "" {
				what = action + " (" + class + ")"
			}
			if b.levels[k] == config.Auto {
				return fmt.Sprintf("Allows %s without confirmation.", what)
			}
			return fmt.Sprintf("Raises %s from %s to %s.", what, a.levels[k], b.levels[k])
		}
	}
	switch {
	case b.window > a.window:
		return fmt.Sprintf("Lengthens the approval window from %s to %s.", shortDuration(a.window), shortDuration(b.window))
	case b.autoPR && !a.autoPR:
		return "Turns on automatic pull request publication."
	case b.merge && !a.merge:
		return "Lets a coordinator agent merge pull requests into branches other than main and master."
	}
	for _, branch := range a.protected {
		if !slices.Contains(b.protected, branch) {
			return fmt.Sprintf("Stops protecting %s.", branch)
		}
	}
	// A plugin is code that runs with the permissions its manifest lists, so
	// turning one on loosens, and so does letting a repository's plugins run
	// again.
	for _, id := range slices.Sorted(maps.Keys(b.plugins)) {
		if b.plugins[id] && !a.plugins[id] {
			return fmt.Sprintf("Turns on the plugin %s, which then runs in the console with the permissions its manifest lists.", id)
		}
	}
	for _, id := range a.disabled {
		if !slices.Contains(b.disabled, id) {
			return fmt.Sprintf("Lets the plugin %s run for this repository again.", id)
		}
	}
	// A permission rule only denies, so turning its plugin off loosens too:
	// for the person, and, conservatively, for a repository.
	why := "which has a permission rule: operations it denies are allowed again"
	if rules.unread {
		why = "which may have a permission rule: the plugin registry cannot be read"
	}
	for _, id := range slices.Sorted(maps.Keys(a.plugins)) {
		if rules.may(id) && a.plugins[id] && !b.plugins[id] {
			return fmt.Sprintf("Turns off the plugin %s, %s.", id, why)
		}
	}
	if !rules.unread {
		why = "which has a permission rule that denies operations"
	}
	for _, id := range b.disabled {
		if rules.may(id) && !slices.Contains(a.disabled, id) {
			return fmt.Sprintf("Turns off for this repository the plugin %s, %s.", id, why)
		}
	}
	return ""
}

func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// capped warns when a personal class level is above its repository ceiling
// in the files after the change.
func capped(keys []string, files [2]string) string {
	if len(keys) != 2 || keys[0] != "autonomy" || keys[1] != "local" && keys[1] != "remote" {
		return ""
	}
	user, err := parseUser(files[0])
	repo, _, rerr := config.ParseRepoCore([]byte(files[1]))
	if err != nil || rerr != nil {
		return ""
	}
	level, ceiling := user.Autonomy.Local, repo.Policy.MaxAutonomy.Local
	if keys[1] == "remote" {
		level, ceiling = user.Autonomy.Remote, repo.Policy.MaxAutonomy.Remote
	}
	if level != "" && rank(level) > rank(ceiling) {
		return fmt.Sprintf("The repository ceiling for %s actions is %s, so %s has no effect here.", keys[1], ceiling, level)
	}
	return ""
}

// 9. Concurrency: expect against the file as read.
func conflictLink(c *check) {
	for _, e := range c.live() {
		if len(e.Expect) == 0 {
			continue
		}
		expectSet := !bytes.Equal(bytes.TrimSpace(e.Expect), []byte("null"))
		if expectSet != e.beforeSet || expectSet && !bytes.Equal(canonical(e.Expect), canonical(e.before)) {
			c.add(SeverityError, e, CodeConflict, "This setting changed since you last read it.")
		}
	}
}

// 10. Effect: a restart or new worktrees needed; a no-op edit.
func effectLink(c *check) {
	for _, e := range c.live() {
		if e.beforeSet == e.afterSet && bytes.Equal(canonical(e.before), canonical(e.after)) {
			c.add(SeverityInfo, e, CodeUnchanged, "The setting already has this value.")
			continue
		}
		switch e.field.Meta.Applies {
		case config.AppliesRestart:
			c.add(SeverityInfo, e, CodeApplies, "Running agent sessions keep the old value until they restart.")
		case config.AppliesNewWorktrees:
			c.add(SeverityInfo, e, CodeApplies, "Applies to worktrees created from now on.")
		}
	}
}
