package config

import (
	"cmp"
	"slices"
	"sync"
)

// The settings field metadata table: one FieldMeta for each field of the
// catalog (ADR docs/knowledge/engineering/decisions/event-driven-platform.md,
// section "Phase 4 Field metadata", lane 4.1). The table lives here, not in
// struct tags, so no configuration type changes and config.Field stays as it
// is (ADR phase 4 decision 6).
//
// An entry is keyed by layer, path pattern and provider. The path is the
// field's path (FieldPath grammar; a "*" segment matches any key or index).
// An entry with no provider describes the field for every provider that has
// it; an entry with a provider wins over it. Entries are listed in the order
// the Settings page shows them: a field's Order is its position in its group.
//
// Descriptions say what a setting does and never name a value. Only entries
// that need them carry extra metadata: a stepper has a minimum, a duration
// has limits where the code checks some, and a list or object kv-table has
// columns.
//
// TestEveryConfigFieldHasControl keeps the table and the compiled fields
// together: a new field needs an entry, and an entry for a removed field
// fails.

// Group IDs. Each provider role's settings form the group "providers.<role>".
const (
	groupProviders    = "providers"
	groupAutonomy     = "autonomy"
	groupPolicy       = "policy"
	groupCommit       = "commit"
	groupWorktrees    = "worktrees"
	groupIntegrations = "integrations"
	groupUpdates      = "updates"
	groupInit         = "init"
	groupAI           = "ai"
	groupPlugins      = "plugins"
)

func roleGroup(role string) string { return groupProviders + "." + role }

// minSubjectMax is the smallest commit.subject_max that commit.ValidatePRTitle
// can work with. A pull request title is checked against subject_max minus
// commit.SquashSuffixReserve (8), and the shortest conventional title under
// the default types is "ci: a" (5 characters). A smaller value leaves no
// title that can pass, so a control never needs to offer it. config cannot
// import commit (commit imports config), so the value is written out;
// TestStepperMinimumsAreAcceptedByTheCode checks it against both.
const minSubjectMax = 13

// levelChoices are the autonomy levels, loosest last. The compiled Level type
// has no `allowed` tag, so the catalog adds them to the schema.
var levelChoices = []string{string(Suggest), string(Confirm), string(Auto)}

// metaEntry is one table row.
type metaEntry struct {
	layer    FieldLayer
	path     string
	provider string
	keys     []string // path, parsed
	meta     FieldMeta
	columns  map[string]FieldMeta

	// Schema refinements for choices the compiled types do not carry: enum
	// values for the field, suggestions for its list items, enum values for
	// its map values and suggestions for its map keys.
	enum, itemSuggestions, valueEnum, keySuggestions []string
	// columnEnum sets enum values on properties of a kv-table value that is
	// an object (a select column), by property name.
	columnEnum map[string][]string
}

func (e *metaEntry) describe() string {
	id := FieldID(e.layer, e.path)
	if e.provider != "" {
		return id + " (provider " + e.provider + ")"
	}
	return id
}

// apply sets a catalog field's metadata and schema refinements. Columns take
// their group and applies from the field when they have none.
func (e *metaEntry) apply(f *CatalogField) {
	f.Meta = e.meta.clone()
	if len(e.columns) > 0 {
		f.Columns = make(map[string]FieldMeta, len(e.columns))
		for name, c := range e.columns {
			c = c.clone()
			c.Group = cmp.Or(c.Group, f.Meta.Group)
			c.Applies = cmp.Or(c.Applies, f.Meta.Applies)
			f.Columns[name] = c
		}
	}
	s := &f.Schema
	if len(e.enum) > 0 && len(s.Enum) == 0 {
		s.Enum = slices.Clone(e.enum)
	}
	if len(e.keySuggestions) > 0 {
		s.KeySuggestions = slices.Clone(e.keySuggestions)
	}
	if len(e.valueEnum) > 0 && s.AdditionalProperties != nil && len(s.AdditionalProperties.Enum) == 0 {
		v := *s.AdditionalProperties
		v.Enum = slices.Clone(e.valueEnum)
		s.AdditionalProperties = &v
	}
	if len(e.columnEnum) > 0 && s.AdditionalProperties != nil && s.AdditionalProperties.Properties != nil {
		v := *s.AdditionalProperties
		v.Properties = make(map[string]Field, len(s.AdditionalProperties.Properties))
		for name, p := range s.AdditionalProperties.Properties {
			if values, ok := e.columnEnum[name]; ok && len(p.Enum) == 0 {
				p.Enum = slices.Clone(values)
			}
			v.Properties[name] = p
		}
		s.AdditionalProperties = &v
	}
	if len(e.itemSuggestions) > 0 && s.Items != nil {
		item := *s.Items
		item.Suggestions = slices.Clone(e.itemSuggestions)
		s.Items = &item
	}
}

// clone copies the pointers of a FieldMeta, so a caller cannot change the table.
func (m FieldMeta) clone() FieldMeta {
	if m.Min != nil {
		v := *m.Min
		m.Min = &v
	}
	if m.Max != nil {
		v := *m.Max
		m.Max = &v
	}
	if m.Picker != nil {
		p := *m.Picker
		m.Picker = &p
	}
	return m
}

var (
	metaTableOnce sync.Once
	metaTableData []metaEntry
)

// metaTable returns the table, built once. Callers must not change it.
func metaTable() []metaEntry {
	metaTableOnce.Do(func() { metaTableData = buildMetaTable() })
	return metaTableData
}

type tableBuilder struct {
	entries []metaEntry
	counts  map[string]int
}

type entryOption func(*metaEntry)

func optEnum(values ...string) entryOption          { return func(e *metaEntry) { e.enum = values } }
func optItems(values ...string) entryOption         { return func(e *metaEntry) { e.itemSuggestions = values } }
func optValues(values ...string) entryOption        { return func(e *metaEntry) { e.valueEnum = values } }
func optKeys(values ...string) entryOption          { return func(e *metaEntry) { e.keySuggestions = values } }
func optColumns(c map[string]FieldMeta) entryOption { return func(e *metaEntry) { e.columns = c } }
func optColumnEnum(c map[string][]string) entryOption {
	return func(e *metaEntry) { e.columnEnum = c }
}

// add appends an entry. A field's Order is 10 times its position in its group.
func (b *tableBuilder) add(layer FieldLayer, path, provider string, m FieldMeta, opts ...entryOption) {
	b.counts[m.Group]++
	m.Order = 10 * b.counts[m.Group]
	keys, _ := ParseFieldPath(path) // an invalid path matches nothing: the coverage test reports it
	e := metaEntry{layer: layer, path: path, provider: provider, keys: keys, meta: m}
	for _, opt := range opts {
		opt(&e)
	}
	b.entries = append(b.entries, e)
}

type metaOption func(*FieldMeta)

// ctl builds a FieldMeta. Fields apply live unless a withApplies says otherwise.
func ctl(group string, w Widget, label, description string, opts ...metaOption) FieldMeta {
	m := FieldMeta{Label: label, Description: description, Group: group, Widget: w, Applies: AppliesLive}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// col builds a column of a list or kv-table. It takes its group and applies
// from the field when the catalog is built.
func col(w Widget, label, description string) FieldMeta {
	return FieldMeta{Label: label, Description: description, Widget: w}
}

func withApplies(a Applies) metaOption    { return func(m *FieldMeta) { m.Applies = a } }
func withLoosens(l Loosening) metaOption  { return func(m *FieldMeta) { m.Loosens = l } }
func withAdvanced() metaOption            { return func(m *FieldMeta) { m.Advanced = true } }
func withPlaceholder(s string) metaOption { return func(m *FieldMeta) { m.Placeholder = s } }
func withPicker(p Picker) metaOption      { return func(m *FieldMeta) { m.Picker = &p } }
func withStepper(minimum, step int) metaOption {
	return func(m *FieldMeta) { m.Min, m.Step = &minimum, step }
}
func withDurationLimits(minimum, maximum string) metaOption {
	return func(m *FieldMeta) { m.MinDuration, m.MaxDuration = minimum, maximum }
}

// dir and file are file-picker settings. base is "repo" or "home".
func dir(base string) metaOption { return withPicker(Picker{Kind: PickDir, Base: base}) }
func file(glob string) metaOption {
	return withPicker(Picker{Kind: PickFile, Base: "repo", Glob: glob})
}

func buildMetaTable() []metaEntry {
	b := &tableBuilder{counts: map[string]int{}}
	b.personal()
	b.repository()
	b.providerSelections()
	b.codeHosting()
	b.taskManagement()
	b.knowledgeBase()
	b.devops()
	b.secops()
	b.notifications()
	return b.entries
}

// personal lists the fields of ~/.agentflow/config.yml.
func (b *tableBuilder) personal() {
	const l = FieldPersonal
	b.add(l, "autonomy.local", "", ctl(groupAutonomy, WidgetSelect, "Local actions",
		"Highest autonomy for local actions such as commit, in every repository.", withLoosens(LoosensLevel)),
		optEnum(levelChoices...))
	b.add(l, "autonomy.remote", "", ctl(groupAutonomy, WidgetSelect, "Remote actions",
		"Highest autonomy for remote actions such as push and opening a pull request, in every repository.", withLoosens(LoosensLevel)),
		optEnum(levelChoices...))
	b.add(l, "autonomy.actions", "", ctl(groupAutonomy, WidgetKVTable, "Action overrides",
		"Sets the autonomy level for single actions. An override replaces the local or remote level for that action.", withLoosens(LoosensLevel)),
		optKeys(AutonomyActions()...), optValues(levelChoices...))
	b.add(l, "approval_window", "", ctl(groupAutonomy, WidgetDuration, "Approval window",
		"How long a confirmation stays valid after agentflow shows its summary. A repository maximum can shorten it.",
		withLoosens(LoosensLonger), withDurationLimits(MinApprovalWindow.String(), MaxApprovalWindow.String()), withPlaceholder("5m")))

	b.add(l, "worktree.root", "", ctl(groupWorktrees, WidgetFilePicker, "Personal worktree root",
		"Directory that holds new task worktrees, as root/repository/branch, for repositories that set no root of their own.",
		withApplies(AppliesNewWorktrees), dir("home")))
	b.add(l, "worktree.checkouts", "", ctl(groupWorktrees, WidgetKVTable, "Primary checkouts",
		"Maps a repository name to the directory of its primary checkout, for checkouts outside the default location.",
		withApplies(AppliesNewWorktrees), withAdvanced()))

	b.add(l, "atlassian.email", "", ctl(groupIntegrations, WidgetText, "Atlassian e-mail",
		"E-mail address of your Atlassian account, used to sign in to Jira and Confluence.", withPlaceholder("you@example.com")))

	b.add(l, "update.auto", "", ctl(groupUpdates, WidgetToggle, "Update automatically",
		"Check for and install new agentflow releases without asking. Turn off to update by hand.", withApplies(AppliesNextRun)))

	b.add(l, "init.auto", "", ctl(groupInit, WidgetSelect, "Set up new repositories",
		"What the AI-client plugins do in a repository that has no agentflow configuration: set it up, only suggest setup, or do nothing. Applies to the listed organisations only.",
		withApplies(AppliesNextRun)), optEnum("auto", "ask", "off"))
	b.add(l, "init.orgs", "", ctl(groupInit, WidgetChips, "Organisations",
		"GitHub owners whose repositories the plugins may set up automatically.", withApplies(AppliesNextRun)))

	b.aiHarnesses()
	b.aiSessions()
}

// aiHarnesses lists the personal ai.harnesses fields (ADR "Phase 6
// configuration keys"). The repository layer has no ai keys.
func (b *tableBuilder) aiHarnesses() {
	const l = FieldPersonal
	b.add(l, "ai.harnesses.auto_probe", "", ctl(groupAI, WidgetToggle, "Probe harnesses automatically",
		"Lets agentflow start an installed harness for a few seconds to read its version and check that it speaks ACP, when the harness appears or changes, and lets agents ask for a probe. When off, agentflow only looks at files on your PATH until you ask for a probe.",
		withLoosens(LoosensEnable)))
	b.add(l, "ai.harnesses.sign_in_check", "", ctl(groupAI, WidgetToggle, "Check sign-in",
		"Runs a harness's own sign-in status command, such as `claude auth status`, with your real home folder, to show whether you are signed in. agentflow keeps only the answer (signed in, signed out or unknown), never the command's output. When off, agentflow never runs these commands.",
		withLoosens(LoosensEnable)))
	b.add(l, "ai.harnesses.poll_interval", "", ctl(groupAI, WidgetDuration, "How often to look for harnesses",
		"How often the console daemon checks your PATH for harnesses that were installed, changed or removed.",
		withDurationLimits(MinHarnessPollInterval.String(), MaxHarnessPollInterval.String()), withPlaceholder(DefaultHarnessPollInterval.String())))
	b.add(l, "ai.harnesses.probe_timeout", "", ctl(groupAI, WidgetDuration, "Probe time limit",
		"How long a harness has to answer the ACP initialize request before the probe gives up.",
		withApplies(AppliesNextRun), withDurationLimits(MinHarnessProbeTimeout.String(), MaxHarnessProbeTimeout.String()),
		withPlaceholder(DefaultHarnessProbeTimeout.String())))
	b.add(l, "ai.harnesses.disabled", "", ctl(groupAI, WidgetChips, "Harnesses to ignore",
		"Harness IDs agentflow never lists or starts.", withLoosens(LoosensShrink)))
	b.add(l, "ai.harnesses.overrides", "", ctl(groupAI, WidgetKVTable, "Harness overrides",
		"Changes how agentflow finds or starts a harness, or adds one that is not in the ACP registry."),
		optColumnEnum(map[string][]string{
			"kind":        {"native", "adapter"},
			"status_from": {"self", "base"},
		}),
		optColumns(map[string]FieldMeta{
			"status_from":    col(WidgetSelect, "Sign-in check runs", "Which program runs the sign-in status command: the harness itself, or the CLI an adapter bridges. Empty means no sign-in check."),
			"status_args":    col(WidgetChips, "Sign-in check arguments", "Arguments of the sign-in status command."),
			"credential_env": col(WidgetChips, "Sign-in variables", "Environment variables that sign this harness in; agentflow checks only that one is set."),
			"name":           col(WidgetText, "Name", "Name shown for the harness."),
			"kind":           col(WidgetSelect, "Kind", "Whether the program speaks ACP itself, or is an adapter for another CLI."),
			"command":        col(WidgetText, "Command", "A command name on your PATH, or an absolute path."),
			"args":           col(WidgetChips, "ACP arguments", "Arguments that start the harness's ACP server on standard input and output."),
			"version_args":   col(WidgetChips, "Version arguments", "Arguments that print the version. Empty means --version."),
			"no_version":     col(WidgetToggle, "Never ask for a version", "Never runs a version command for this harness."),
			"base_name":      col(WidgetText, "Base CLI name", "Name of the CLI an adapter bridges."),
			"base_commands":  col(WidgetChips, "Base CLI commands", "Command names of the CLI an adapter bridges, looked up on your PATH."),
			"min_version":    col(WidgetText, "Oldest version", "The oldest version agentflow accepts, as X.Y.Z."),
			"status_signed_in": withAdvancedCol(col(WidgetChips, "Signed-in exit codes",
				"Exit codes of the sign-in status command that mean signed in. Empty means 0.")),
			"status_signed_out": withAdvancedCol(col(WidgetChips, "Signed-out exit codes",
				"Exit codes of the sign-in status command that mean signed out. Empty means 1.")),
			"status_signed_out_line": withAdvancedCol(col(WidgetText, "Signed-out line",
				"When set, a signed-out answer also needs this whole line in the command's output.")),
			"config_env": withAdvancedCol(col(WidgetChips, "Sign-in configuration variables",
				"Directory variables, such as CODEX_HOME, passed to the sign-in status command.")),
			"status_source": withAdvancedCol(col(WidgetText, "Sign-in check source",
				"Where the meaning of the sign-in status command is documented. For your own notes.")),
		}))
}

// aiSessions lists the personal ai.sessions and ai.permissions fields (ADR
// "Phase 7 configuration keys"). The repository's three tightening keys are
// in repository.
func (b *tableBuilder) aiSessions() {
	const l = FieldPersonal
	b.add(l, "ai.sessions.agent_start", "", ctl(groupAI, WidgetToggle, "Let agents start sessions",
		"Lets an agent ask to start an ACP session in its own worktree. Each start still needs your confirmation, and the session asks you before it writes a file or runs a command.",
		withLoosens(LoosensEnable)))
	b.add(l, "ai.sessions.home", "", ctl(groupAI, WidgetSelect, "Session home folder",
		"Which home folder a session's harness gets: your real one, where its sign-in lives, or an empty folder of its own, where it may not be signed in.",
		withApplies(AppliesNextRun), withLoosens(LoosensEnable)), optEnum(AISessionHomeReal, AISessionHomeIsolated))
	b.add(l, "ai.sessions.max_running", "", ctl(groupAI, WidgetStepper, "Sessions at once",
		"How many ACP sessions may run at the same time, across all your repositories.",
		withLoosens(LoosensEnable), withStepperRange(MinAIMaxRunning, MaxAIMaxRunning, 1)))
	b.add(l, "ai.sessions.max_duration", "", ctl(groupAI, WidgetDuration, "Session time limit",
		"How long one session may run before agentflow cancels it.",
		withApplies(AppliesNextRun), withLoosens(LoosensEnable),
		withDurationLimits(MinAIMaxDuration.String(), MaxAIMaxDuration.String()), withPlaceholder("30m")))
	b.add(l, "ai.sessions.idle_timeout", "", ctl(groupAI, WidgetDuration, "Idle time limit",
		"How long a session may go without hearing from the harness, while no request waits for you, before agentflow cancels it.",
		withApplies(AppliesNextRun), withDurationLimits(MinAIIdleTimeout.String(), MaxAIIdleTimeout.String()), withPlaceholder("10m")))
	b.add(l, "ai.sessions.ask_timeout", "", ctl(groupAI, WidgetDuration, "Time to answer a request",
		"How long a harness's request waits for your answer before agentflow denies it.",
		withApplies(AppliesNextRun), withDurationLimits(MinAIAskTimeout.String(), MaxAIAskTimeout.String()), withPlaceholder("5m")))
	b.add(l, "ai.sessions.unasked", "", ctl(groupAI, WidgetSelect, "When the harness acts without asking",
		"What agentflow does when a harness edits, runs or fetches something inside the worktree without asking first: record a warning, or cancel the session. Outside the worktree it always cancels.",
		withApplies(AppliesNextRun), withLoosens(LoosensEnable)), optEnum(AIUnaskedWarn, AIUnaskedCancel))
	b.add(l, "ai.sessions.agentflow_mcp", "", ctl(groupAI, WidgetToggle, "Give the harness agentflow's read-only tools",
		"Passes agentflow's own MCP server to the harness with only its read-only tools, such as the task context and the docs search.",
		withApplies(AppliesNextRun), withLoosens(LoosensEnable)))
	b.add(l, "ai.sessions.keep_transcripts", "", ctl(groupAI, WidgetToggle, "Keep session transcripts",
		"Keeps what a session said and did in your agentflow home after it ends. When off, only the session's summary stays."))
	b.add(l, "ai.sessions.keep_days", "", ctl(groupAI, WidgetStepper, "Days to keep sessions",
		"How many days agentflow keeps an ended session before it deletes it.",
		withStepperRange(MinAIKeepDays, MaxAIKeepDays, 1)))
	b.add(l, "ai.sessions.modes", "", ctl(groupAI, WidgetKVTable, "Session mode per harness",
		"The session mode agentflow asks a harness for, by harness ID. Mode IDs are the harness's own.",
		withApplies(AppliesNextRun)))
	b.add(l, "ai.permissions.rules", "", ctl(groupAI, WidgetKVTable, "Session permission rules",
		"Your standing answers to a session's requests: allow, ask or deny reading or writing matching files, running a named program, or a kind of tool. A rule never allows what only you may allow one request at a time.",
		withLoosens(LoosensEnable)),
		optColumnEnum(map[string][]string{
			"kind":     {AIRuleKindFSRead, AIRuleKindFSWrite, AIRuleKindTerminal, AIRuleKindTool},
			"decision": {AIDecisionAllow, AIDecisionAsk, AIDecisionDeny},
		}),
		optColumns(map[string]FieldMeta{
			"kind":     col(WidgetSelect, "Request", "What the rule matches: reading a file, writing a file, starting a program, or a kind of tool."),
			"match":    col(WidgetText, "Match", "A worktree-relative file pattern, a program name pattern, or a tool kind."),
			"decision": col(WidgetSelect, "Answer", "Whether matching requests are allowed, asked of you or denied."),
		}))
}

// withStepperRange is a stepper with both limits.
func withStepperRange(minimum, maximum, step int) metaOption {
	return func(m *FieldMeta) { m.Min, m.Max, m.Step = &minimum, &maximum, step }
}

func withAdvancedCol(m FieldMeta) FieldMeta {
	m.Advanced = true
	return m
}

// repository lists the fields of <worktree>/.agentflow/config.yml that are not
// provider role sections.
func (b *tableBuilder) repository() {
	const l = FieldRepo
	b.add(l, "policy.max_autonomy.local", "", ctl(groupPolicy, WidgetSelect, "Local actions ceiling",
		"Highest autonomy anyone may use for local actions in this repository. A personal level above it has no effect.", withLoosens(LoosensLevel)),
		optEnum(levelChoices...))
	b.add(l, "policy.max_autonomy.remote", "", ctl(groupPolicy, WidgetSelect, "Remote actions ceiling",
		"Highest autonomy anyone may use for remote actions in this repository. A personal level above it has no effect.", withLoosens(LoosensLevel)),
		optEnum(levelChoices...))
	b.add(l, "policy.max_autonomy.actions", "", ctl(groupPolicy, WidgetKVTable, "Action ceilings",
		"Caps the autonomy level for single actions in this repository, below the local or remote ceiling.", withLoosens(LoosensLevel)),
		optKeys(AutonomyActions()...), optValues(levelChoices...))
	b.add(l, "policy.max_approval_window", "", ctl(groupPolicy, WidgetDuration, "Approval window ceiling",
		"Longest time a confirmation may stay valid in this repository. It shortens any longer personal window.",
		withLoosens(LoosensLonger), withDurationLimits(MinApprovalWindow.String(), MaxApprovalWindow.String()), withPlaceholder("5m")))
	b.add(l, "policy.allow_auto_pr_publication", "", ctl(groupPolicy, WidgetToggle, "Automatic pull request publication",
		"Lets an agent open a pull request and push its branch in one step without confirmation, even when the remote ceiling asks for confirmation.",
		withLoosens(LoosensEnable)))
	b.add(l, "policy.coordinator_merge", "", ctl(groupPolicy, WidgetToggle, "Coordinator merge",
		"Lets an agent merge a pull request whose base branch is not main or master. The rest of the never-list still applies.",
		withLoosens(LoosensEnable)))
	b.add(l, "policy.protected_branches", "", ctl(groupPolicy, WidgetBranchPicker, "Protected branches",
		"Branches that changes reach only through a pull request. A pattern with * matches several branches.",
		withLoosens(LoosensShrink), withPicker(Picker{Multiple: true})))
	b.add(l, "policy.test_cmd", "", ctl(groupPolicy, WidgetText, "Test command",
		"Command the pull request readiness check runs to confirm that tests pass."))
	b.add(l, "policy.lint_cmd", "", ctl(groupPolicy, WidgetText, "Lint command",
		"Command the pull request readiness check runs to confirm that lint passes."))

	b.add(l, "commit.types", "", ctl(groupCommit, WidgetChips, "Commit types",
		"Conventional commit types that the commit message check accepts."), optItems(DefaultTypes...))
	b.add(l, "commit.subject_max", "", ctl(groupCommit, WidgetStepper, "Subject length",
		"Longest commit subject, in characters. Pull request titles stay a little shorter so the squash commit fits.",
		withStepper(minSubjectMax, 1)))
	b.add(l, "commit.require_key", "", ctl(groupCommit, WidgetToggle, "Require a task key",
		"Commit subjects must carry a task key, such as a Jira key, that links the commit to its task."))
	b.add(l, "commit.ai_trailer", "", ctl(groupCommit, WidgetToggle, "Keep AI trailers",
		"Keep AI attribution trailers in commit messages. When off, agentflow removes them."))
	b.add(l, "commit.breaking_paths", "", ctl(groupCommit, WidgetChips, "Breaking-change paths",
		"Path patterns whose changes mark a commit as a breaking change. A pattern may use ** to match any number of directories.",
		withAdvanced()))

	b.add(l, "plugins.disabled", "", ctl(groupPlugins, WidgetChips, "Plugins turned off here",
		"IDs of installed plugins whose event handlers do not run for this repository. A repository can turn plugins off for itself, never on.",
		withLoosens(LoosensShrink)))

	b.add(l, "ai.sessions.disabled", "", ctl(groupAI, WidgetToggle, "Turn sessions off in this repository",
		"Stops anyone from starting an ACP session in this repository.", withLoosens(LoosensShrink)))
	b.add(l, "ai.permissions.ask", "", ctl(groupAI, WidgetChips, "Always ask for these paths",
		"Worktree-relative file patterns whose session requests always ask a person, whatever a personal rule says.", withLoosens(LoosensShrink)))
	b.add(l, "ai.permissions.deny", "", ctl(groupAI, WidgetChips, "Always deny these paths",
		"Worktree-relative file patterns whose session requests are always denied.", withLoosens(LoosensShrink)))

	b.add(l, "worktree.root", "", ctl(groupWorktrees, WidgetFilePicker, "Repository worktree root",
		"Directory that holds new task worktrees, as root/repository/branch, for everyone who works in this repository.",
		withApplies(AppliesNewWorktrees), dir("home")))
	b.add(l, "worktree.go_work.use", "", ctl(groupWorktrees, WidgetChips, "go.work modules",
		"Module directories, relative to the worktree root, that the go.work file in each new worktree uses.",
		withApplies(AppliesNewWorktrees), withAdvanced()))
	b.add(l, "worktree.go_work.siblings", "", ctl(groupWorktrees, WidgetKVTable, "go.work sibling modules",
		"Replaces a Go module in each new worktree's go.work with the same module in another repository: its matching worktree, or its primary checkout.",
		withApplies(AppliesNewWorktrees), withAdvanced()),
		optColumns(map[string]FieldMeta{
			"repo": col(WidgetText, "Repository", "Name of the repository that holds the module."),
			"path": col(WidgetText, "Module directory", "Directory of the module inside that repository."),
		}))

	b.add(l, "jira.site", "", ctl(groupIntegrations, WidgetText, "Jira site",
		"Web address of the Jira site. The task provider's own site setting takes precedence.", withPlaceholder(DefaultSite)))
	b.add(l, "jira.projects", "", ctl(groupIntegrations, WidgetChips, "Jira projects",
		"Jira project keys the task provider searches. The task provider's own projects setting takes precedence."))
	b.add(l, "confluence.site", "", ctl(groupIntegrations, WidgetText, "Confluence site",
		"Web address of the Confluence site. Without one, agentflow uses the Jira site.", withPlaceholder(DefaultSite)))
	b.add(l, "flow_map", "", ctl(groupIntegrations, WidgetText, "Flow map",
		"Where the file is that maps events and commit verbs to Jira statuses: a path in the repository, or owner/repository@ref:path.",
		withAdvanced(), withPlaceholder(DefaultFlowMap)))

	b.add(l, "update.channel", "", ctl(groupUpdates, WidgetSelect, "Release channel",
		"Which releases agentflow offers: stable releases only, or beta releases too.", withApplies(AppliesNextRun)),
		optEnum("stable", "beta"))
	b.add(l, "update.check_every", "", ctl(groupUpdates, WidgetDuration, "Check interval",
		"How often agentflow checks for a new release. An invalid or non-positive value uses the default.",
		withApplies(AppliesNextRun), withAdvanced(), withPlaceholder("6h")))
	// Min 0: update.ParseVersion cannot read a negative major version, so no
	// release has one.
	b.add(l, "update.track", "", ctl(groupUpdates, WidgetStepper, "Major version",
		"Major version of agentflow to follow. Without one, agentflow stays on the installed major version.",
		withApplies(AppliesNextRun), withAdvanced(), withStepper(0, 1)))
	b.add(l, "update.pin", "", ctl(groupUpdates, WidgetText, "Pinned version",
		"Exact release to stay on. Without one, agentflow follows its channel.", withApplies(AppliesNextRun), withAdvanced()))
}

// providerSelections lists the provider field of each role.
func (b *tableBuilder) providerSelections() {
	for _, role := range []struct{ role, label, description string }{
		{"code_hosting", "Code hosting provider", "How agentflow finds the code host: detected from the Git remotes, or set to a specific forge."},
		{"task_management", "Task provider", "Where tasks live: Jira, GitHub Issues, Asana, or files. None turns task actions off."},
		{"knowledge_base", "Knowledge provider", "Where documents are searched and published: Confluence, a GitHub wiki, or files. None turns knowledge actions off."},
		{"devops.checks", "Checks provider", "Where CI check results are read from."},
		{"devops.deployments", "Deployments provider", "Where deployment status is read from: GitHub, or a fixture file."},
		{"devops.releases", "Releases provider", "Where published releases are read from: GitHub, or a fixture file."},
		{"secops", "SecOps provider", "Where security findings come from for triage."},
		{"notifications", "Notification provider", "How agentflow sends notifications: Slack, a webhook, or a router that sends each signal to several destinations."},
	} {
		b.add(FieldRepo, role.role+".provider", "", ctl(groupProviders, WidgetProviderPicker, role.label,
			role.description+" A running AI session keeps its old provider until it restarts.",
			withApplies(AppliesRestart), withPicker(Picker{Role: role.role})))
	}
}

func (b *tableBuilder) codeHosting() {
	g := roleGroup("code_hosting")
	b.add(FieldRepo, "code_hosting.settings.hosts", "", ctl(g, WidgetKVTable, "Self-hosted forges",
		"Maps the hostname of a self-hosted code host to its forge type, so agentflow recognises remotes on that host.",
		withPlaceholder("git.example.com")))
	b.add(FieldRepo, "code_hosting.settings.source_remote", "", ctl(g, WidgetText, "Source remote",
		"Git remote that holds your branches. Without one, agentflow detects it."))
	b.add(FieldRepo, "code_hosting.settings.target_remote", "", ctl(g, WidgetText, "Target remote",
		"Git remote that pull requests go to. Without one, agentflow detects it."))
}

func (b *tableBuilder) taskManagement() {
	const role = "task_management"
	g := roleGroup(role)
	s := func(path string) string { return role + ".settings." + path }
	transitions := ctl(g, WidgetKVTable, "Event transitions",
		"Maps events and commit verbs to the state a task moves to: open, closed, or none to leave it alone.")

	b.add(FieldRepo, s("project"), "asana", ctl(g, WidgetText, "Default project",
		"Asana project that bare task IDs and new tasks use, as its GID."))
	b.add(FieldRepo, s("transitions"), "asana", ctl(g, WidgetKVTable, "Section transitions",
		"Maps each Asana project GID to its events and the section GID, completed, or none that each event moves a task to."))

	b.add(FieldRepo, s("site"), "jira", ctl(g, WidgetText, "Site", "Web address of the Jira site.", withPlaceholder(DefaultSite)))
	b.add(FieldRepo, s("projects"), "jira", ctl(g, WidgetChips, "Projects", "Jira project keys that My Work and task search look in."))

	b.add(FieldRepo, s("repository"), "github-issues", ctl(g, WidgetText, "Repository",
		"GitHub repository, as owner/name, that holds the issues. Without one, agentflow uses the detected code host repository.",
		withPlaceholder("owner/name")))
	b.add(FieldRepo, s("labels"), "github-issues", ctl(g, WidgetChips, "Labels", "Labels that agentflow adds to the issues it creates."))
	b.add(FieldRepo, s("search.open_only"), "github-issues", ctl(g, WidgetToggle, "Open issues only",
		"Task search lists open issues only."))
	// Min 0: the field is nonnegative (mergeSetting); zero means the default.
	b.add(FieldRepo, s("search.limit"), "github-issues", ctl(g, WidgetStepper, "Search limit",
		"Most issues one search returns. Zero uses the default.", withStepper(0, 1)))

	b.add(FieldRepo, s("root"), "filesystem", ctl(g, WidgetFilePicker, "Task directory",
		"Directory in the repository that holds the task files.", dir("repo")))
	b.add(FieldRepo, s("repository"), "repository", ctl(g, WidgetText, "Repository",
		"GitHub repository, as owner/name, that holds the task files. Without one, agentflow uses the current repository.",
		withPlaceholder("owner/name")))
	b.add(FieldRepo, s("branch"), "repository", ctl(g, WidgetBranchPicker, "Branch",
		"Branch that holds the task files. Without one, agentflow uses the repository's default branch."))
	b.add(FieldRepo, s("directory"), "repository", ctl(g, WidgetFilePicker, "Task directory",
		"Directory in that repository that holds the task files.", dir("repo")))
	b.add(FieldRepo, s("publication"), "repository", ctl(g, WidgetSelect, "Publication",
		"How task file changes reach the branch: through a pull request, or by pushing."))
	b.add(FieldRepo, s("scope"), "", ctl(g, WidgetText, "Scope",
		"Scope that tasks belong to when a command names none."))
	b.add(FieldRepo, s("transitions"), "", transitions)

	b.add(FieldRepo, "task_management.templates.catalog", "", ctl(g, WidgetFilePicker, "Template catalog",
		"JSON file in the repository that lists the task templates. Required when task templates are used.", file("*.json")))
	b.add(FieldRepo, "task_management.templates.default.name", "", ctl(g, WidgetText, "Default template",
		"Name of the template that new tasks use when none is named."))
	b.add(FieldRepo, "task_management.templates.default.revision", "", ctl(g, WidgetText, "Default template revision",
		"Exact revision of the default template.", withAdvanced()))
}

func (b *tableBuilder) knowledgeBase() {
	const role = "knowledge_base"
	g := roleGroup(role)
	s := func(path string) string { return role + ".settings." + path }
	format := ctl(g, WidgetSelect, "Document format",
		"Encoding of newly published documents: the open knowledge format, or the legacy format.")

	b.add(FieldRepo, s("site"), "confluence", ctl(g, WidgetText, "Site",
		"Web address of the Confluence site.", withPlaceholder(DefaultSite)))
	b.add(FieldRepo, s("publish_spaces"), "confluence", ctl(g, WidgetChips, "Publish spaces",
		"Confluence space keys that documents may be published to."))

	b.add(FieldRepo, s("repository"), "github-wiki", ctl(g, WidgetText, "Repository",
		"GitHub repository, as owner/name, whose wiki holds the documents. Without one, agentflow uses the detected code host repository.",
		withPlaceholder("owner/name")))

	b.add(FieldRepo, s("root"), "filesystem", ctl(g, WidgetFilePicker, "Document directory",
		"Directory in the repository that holds the documents.", dir("repo")))
	b.add(FieldRepo, s("repository"), "repository", ctl(g, WidgetText, "Repository",
		"GitHub repository, as owner/name, that holds the documents. Without one, agentflow uses the current repository.",
		withPlaceholder("owner/name")))
	b.add(FieldRepo, s("branch"), "repository", ctl(g, WidgetBranchPicker, "Branch",
		"Branch that holds the documents. Without one, agentflow uses the repository's default branch."))
	b.add(FieldRepo, s("directory"), "repository", ctl(g, WidgetFilePicker, "Document directory",
		"Directory in that repository that holds the documents.", dir("repo")))
	b.add(FieldRepo, s("publication"), "repository", ctl(g, WidgetSelect, "Publication",
		"How new documents reach the branch: through a pull request, or by pushing."))
	b.add(FieldRepo, s("format"), "", format)
}

func (b *tableBuilder) devops() {
	for _, role := range []struct{ role, noun string }{
		{"devops.checks", "check results"},
		{"devops.deployments", "deployments"},
		{"devops.releases", "releases"},
	} {
		g := roleGroup(role.role)
		b.add(FieldRepo, role.role+".settings.repository", "github", ctl(g, WidgetText, "Repository",
			"GitHub repository, as owner/name, to read "+role.noun+" from. Without one, agentflow uses the detected code host repository.",
			withPlaceholder("owner/name")))
		if role.role != "devops.checks" {
			b.add(FieldRepo, role.role+".settings.path", "fixture", ctl(g, WidgetFilePicker, "Fixture file",
				"JSON file in the repository that stands in for the service, so "+role.noun+" can be read without credentials.", file("*.json")))
		}
	}
}

func (b *tableBuilder) secops() {
	const role = "secops"
	g := roleGroup(role)
	s := func(path string) string { return role + ".settings." + path }
	b.add(FieldRepo, s("repository"), "github", ctl(g, WidgetText, "Repository",
		"GitHub repository, as owner/name, whose findings agentflow reads. Without one, agentflow uses the detected code host repository.",
		withPlaceholder("owner/name")))
	b.add(FieldRepo, s("findings"), "github", ctl(g, WidgetChips, "Finding sources",
		"Which GitHub alerts agentflow reads: Dependabot alerts, code scanning alerts, or both. An empty list reads none."))
	b.add(FieldRepo, s("min_severity"), "github", ctl(g, WidgetSelect, "Minimum severity",
		"Lowest severity of a finding that agentflow reports."))
	b.add(FieldRepo, s("incidents.fixture"), "github", ctl(g, WidgetFilePicker, "Incident fixture",
		"JSON file in the repository that lists incidents, for triage without a service.", file("*.json")))
	b.add(FieldRepo, s("incidents.scope"), "github", ctl(g, WidgetText, "Incident scope",
		"Stable name for the source of the incident fixture. Required with a fixture."))
}

func (b *tableBuilder) notifications() {
	const role = "notifications"
	g := roleGroup(role)
	s := func(path string) string { return role + ".settings." + path }
	credential := ctl(g, WidgetCredentialRef, "Webhook credential",
		"Name of the environment variable that holds the webhook URL. The URL itself never goes in the configuration.")
	destination := ctl(g, WidgetText, "Destination name",
		"Name that identifies this destination in delivery receipts.")

	b.add(FieldRepo, s("destination"), "", destination)
	b.add(FieldRepo, s("credential_ref"), "", credential)
	b.add(FieldRepo, s("native.server"), "slack", ctl(g, WidgetText, "MCP server",
		"Name of the Slack MCP registration used for native delivery."))
	b.add(FieldRepo, s("native.workspace_id"), "slack", ctl(g, WidgetText, "Workspace ID",
		"ID of the Slack workspace to send to."))
	b.add(FieldRepo, s("native.channel_id"), "slack", ctl(g, WidgetText, "Conversation ID",
		"ID of the Slack channel or conversation to send to."))
	b.add(FieldRepo, s("native.actor_id"), "slack", ctl(g, WidgetText, "Required sender",
		"ID of the Slack user who must be signed in for delivery. Without one, any signed-in user may send.", withAdvanced()))

	b.add(FieldRepo, s("header_refs"), "webhook", ctl(g, WidgetKVTable, "Header variables",
		"Maps an HTTP header name to the environment variable that holds its value.", withAdvanced()))
	b.add(FieldRepo, s("rejected_statuses"), "webhook", ctl(g, WidgetChips, "Safe rejection codes",
		"HTTP 4xx status codes, other than 408, that guarantee the receiver rejected the request. Only list codes for which a retry is safe.",
		withAdvanced()))

	b.add(FieldRepo, s("destinations"), "router", ctl(g, WidgetKVTable, "Destinations",
		"Named places the router can deliver to. Each has a provider and either a credential or a native Slack scope."),
		optColumns(map[string]FieldMeta{
			"provider":            col(WidgetSelect, "Provider", "How this destination delivers."),
			"credential_ref":      col(WidgetCredentialRef, "Credential", "Name of the environment variable that holds the webhook URL."),
			"header_refs":         col(WidgetKVTable, "Header variables", "Maps an HTTP header name to the environment variable that holds its value."),
			"rejected_statuses":   col(WidgetChips, "Safe rejection codes", "HTTP 4xx status codes, other than 408, that guarantee the receiver rejected the request."),
			"native.server":       col(WidgetText, "MCP server", "Name of the Slack MCP registration used for native delivery."),
			"native.workspace_id": col(WidgetText, "Workspace ID", "ID of the Slack workspace to send to."),
			"native.channel_id":   col(WidgetText, "Conversation ID", "ID of the Slack channel or conversation to send to."),
			"native.actor_id":     col(WidgetText, "Required sender", "ID of the Slack user who must be signed in for delivery."),
		}))
	b.add(FieldRepo, s("routes"), "router", ctl(g, WidgetKVTable, "Routes",
		"Maps each signal to the destination names that receive it, in delivery order. Without routes, every signal goes to every destination."))
}
