package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/settings"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// The settings commands (ADR, "Phase 4 commands"): config.fields,
// config.preview, config.set and config.undo, the config set Verb, and the
// console's settings backend. The service is internal/settings.

// configFieldsIn is settings.FieldsRequest with the tool's descriptions.
type configFieldsIn struct {
	Group    string   `json:"group,omitempty" jsonschema:"only fields of this group (and its subgroups), such as autonomy or providers.task_management"`
	IDs      []string `json:"ids,omitempty" jsonschema:"only these field IDs, such as personal:autonomy.local"`
	Query    string   `json:"query,omitempty" jsonschema:"only fields whose ID, label or description contains this text"`
	Modified bool     `json:"modified,omitempty" jsonschema:"only fields set in a layer file"`
}

func (in configFieldsIn) request() settings.FieldsRequest {
	return settings.FieldsRequest{Group: in.Group, IDs: in.IDs, Query: in.Query, Modified: in.Modified}
}

// configSetIn is a ChangeSet with the call's confirm token.
type configSetIn struct {
	settings.ChangeSet
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// configUndoIn is an UndoRequest with the call's confirm token.
type configUndoIn struct {
	settings.UndoRequest
	ConfirmToken string `json:"confirm_token,omitempty"`
}

//nolint:gosec // G101: tool input description text, not a credential
const tokenText = "token from a needs_confirmation response, after the user agreed to the exact summary"

// changeSetSchema builds the input schema of a ChangeSet tool: Edit.value
// and expect accept any JSON value, which the inferred schema cannot say.
func changeSetSchema[T any](token bool) func() (*jsonschema.Schema, error) {
	return func() (*jsonschema.Schema, error) {
		s, err := jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}})
		if err != nil {
			return nil, err
		}
		edits := s.Properties["edits"]
		if edits == nil || edits.Items == nil {
			return nil, errors.New("change set schema has no edits")
		}
		s.Required = []string{"edits"}
		edits.Description = "edits in order, saved together or not at all; select a provider before its settings"
		item := edits.Items
		item.Required = []string{"id", "op"}
		describe(item, "id", "field ID <layer>:<path>, layer personal or repo, e.g. personal:autonomy.local; see agentflow_config_fields")
		describe(item, "op", "set writes value; unset removes the path so the default or the other layer applies")
		item.Properties["op"].Enum = []any{"set", "unset"}
		describe(item, "value", "the new value as JSON, for set")
		describe(item, "expect", "the layer value you last read: the edit conflicts when the file now differs; null means not set; omit for no check")
		describe(s, "base", "the layer revisions you last read (informational)")
		base := s.Properties["base"]
		base.Required = nil
		describe(s, "repo", "how repository edits are saved: draft pull request (default), direct, or the worktree file")
		repo := s.Properties["repo"]
		repo.Required = nil
		repo.Properties["mode"].Enum = []any{"draft", "direct", "worktree"}
		describe(repo, "title", "conventional title for draft and direct; empty takes the preview's suggestion")
		describe(repo, "task_ref", "optional task reference for the publication")
		if token {
			describe(s, "confirm_token", tokenText)
		}
		return s, nil
	}
}

func undoSchema() (*jsonschema.Schema, error) {
	s, err := jsonschema.For[configUndoIn](nil)
	if err != nil {
		return nil, err
	}
	s.Required = nil
	describe(s, "skip_conflicts", "revert the fields that did not change since the save and leave the others")
	describe(s, "repo", "how an inverse repository change is saved; default: the save's own mode")
	repo := s.Properties["repo"]
	repo.Required = nil
	repo.Properties["mode"].Enum = []any{"draft", "direct", "worktree"}
	describe(s, "confirm_token", tokenText)
	return s, nil
}

func describe(s *jsonschema.Schema, prop, text string) {
	if p := s.Properties[prop]; p != nil {
		p.Description = text
	}
}

var (
	configFieldsSpec = command.Spec[configFieldsIn, any]{
		Name:    "config.fields",
		CLIPath: []string{"config", "fields"},
		MCP: &command.MCP{Tool: "agentflow_config_fields", Annotations: command.ReadOnly,
			Description: "List Agentflow settings fields of both configuration layers (personal and repo) with their metadata, current layer value, effective value and source. Filter by group, IDs, text or modified fields. Use the IDs with agentflow_config_preview and agentflow_config_set."},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in configFieldsIn) (any, error) {
			return settings.New(a).Fields(ctx, in.request())
		},
	}

	configPreviewSpec = command.Spec[settings.ChangeSet, any]{
		Name:    "config.preview",
		CLIPath: []string{"config", "set"},
		MCP: &command.MCP{Tool: "agentflow_config_preview", Annotations: command.ReadOnly, InputSchema: changeSetSchema[settings.ChangeSet](false),
			Description: "Check and preview a change set of settings edits across the personal and repository layers without writing anything: diagnostics, field changes, effects, the repository diff, loosening edits that need confirmation and the repository plan."},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in settings.ChangeSet) (any, error) {
			return settings.New(a).Preview(ctx, in)
		},
	}

	configSetSpec = command.Spec[configSetIn, settings.SaveResult]{
		Name:    "config.set",
		CLIPath: []string{"config", "set"},
		MCP: &command.MCP{Tool: "agentflow_config_set", Annotations: command.RemoteWrite, InputSchema: changeSetSchema[configSetIn](true),
			Description: "Save a change set of settings edits as one change: all layers or none. Every save needs an explicit confirmation, even under auto: the first call returns needs_confirmation with a summary that lists loosening edits first; after the user agrees, call again with the same edits and confirm_token. Personal edits apply at once; repository edits open a draft pull request by default, or apply directly, or write the worktree file (mode worktree, refused on a protected branch)."},
		// Run audits itself: the class and action depend on the edits.
		Policy:   command.Policy{Class: policy.Remote, Action: "console.settings"},
		Events:   command.ScopeWorktree,
		Token:    func(in *configSetIn) *string { return &in.ConfirmToken },
		Describe: func(in configSetIn) command.Detail { return command.Detail{Subject: describeEdits("save", in.Edits)} },
		Run: func(ctx context.Context, a *app.App, in configSetIn) (any, error) {
			return settings.New(a).Save(ctx, in.ChangeSet, in.ConfirmToken, a.GateOperation)
		},
		Links: saveLinks,
	}

	configUndoSpec = command.Spec[configUndoIn, settings.SaveResult]{
		Name:    "config.undo",
		CLIPath: []string{"config", "undo"},
		MCP: &command.MCP{Tool: "agentflow_config_undo", Annotations: command.RemoteWrite, InputSchema: undoSchema,
			Description: "Reverse the last settings save in this repository through the same checks and confirmation as agentflow_config_set; a second undo redoes it. Fields changed since the save conflict unless skip_conflicts. Fields whose pull request is still open cannot be undone: close the pull request instead."},
		// Run audits itself, like config.set.
		Policy:   command.Policy{Class: policy.Remote, Action: "console.settings"},
		Events:   command.ScopeWorktree,
		Token:    func(in *configUndoIn) *string { return &in.ConfirmToken },
		Describe: func(configUndoIn) command.Detail { return command.Detail{Subject: "undo the last settings save"} },
		Run: func(ctx context.Context, a *app.App, in configUndoIn) (any, error) {
			return settings.New(a).Undo(ctx, in.UndoRequest, in.ConfirmToken, a.GateOperation)
		},
		Links: saveLinks,
	}
)

func init() {
	for _, schema := range []func() (*jsonschema.Schema, error){changeSetSchema[settings.ChangeSet](false), changeSetSchema[configSetIn](true), undoSchema} {
		if _, err := schema(); err != nil {
			panic(fmt.Sprintf("settings tool schema: %v", err))
		}
	}
	command.Register(configFieldsSpec)
	command.Register(configPreviewSpec)
	command.Register(configSetSpec)
	command.Register(configUndoSpec)
	command.RegisterVerb(command.Verb{
		Path:    []string{"config", "fields"},
		Usage:   "  config fields [--group G] [--search Q] [--modified] [--json]\n                                 list settings with their values and sources",
		Section: command.SectionWork,
		Order:   3710,
		Run:     runConfigFields,
	})
	command.RegisterVerb(command.Verb{
		Path:    []string{"config", "undo"},
		Usage:   "  config undo [--skip-conflicts] [--mode M] [--title T] [--yes] [--json]\n                                 reverse the last settings save; a second undo redoes it",
		Section: command.SectionWork,
		Order:   3730,
		Run:     runConfigUndo,
	})
	command.RegisterVerb(command.Verb{
		Path:    []string{"config", "set"},
		Usage:   "  config set [--dry-run] [--unset KEY]... [--mode draft|direct|worktree] [--title T] [--task REF] [--yes] [--json] KEY=VALUE...\n                                 change settings in both layers as one confirmed save",
		Section: command.SectionWork,
		Order:   3720,
		Run:     runConfigSet,
	})
	webui.RegisterSettingsBackend(consoleSettings{})
}

func describeEdits(verb string, edits []settings.Edit) string {
	ids := make([]string, 0, len(edits))
	for _, e := range edits {
		ids = append(ids, e.ID)
	}
	return fmt.Sprintf("%s %d settings: %s", verb, len(edits), strings.Join(ids, ", "))
}

func saveLinks(r settings.SaveResult) []bus.OpLink {
	if r.Publication == nil || r.Publication.URL == "" {
		return nil
	}
	id := ""
	if r.Publication.Number > 0 {
		id = strconv.Itoa(r.Publication.Number)
	}
	return []bus.OpLink{{Kind: bus.OpLinkPull, ID: id, URL: r.Publication.URL}}
}

// ---- command line ----

// runConfigSet is `config set`: KEY=VALUE edits in argument order, then the
// --unset edits; --dry-run previews.
func runConfigSet(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "preview only")
	var unset []string
	fs.Func("unset", "remove KEY from its layer (repeatable)", func(k string) error {
		unset = append(unset, k)
		return nil
	})
	mode := fs.String("mode", "", "draft (default), direct or worktree for repository edits")
	title := fs.String("title", "", "conventional title of the pull request")
	task := fs.String("task", "", "task reference for the publication")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := interleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 && len(unset) == 0 {
		return errors.New("usage: agentflow config set [--dry-run] [--unset KEY]... KEY=VALUE")
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	cat := settings.New(a).Catalog
	cs := settings.ChangeSet{Repo: settings.RepoTarget{Mode: settings.RepoMode(*mode), Title: *title, TaskRef: *task}}
	for _, arg := range pos {
		e, err := settings.ParseAssignment(cat, arg)
		if err != nil {
			return err
		}
		cs.Edits = append(cs.Edits, e)
	}
	for _, k := range unset {
		e, err := settings.ParseUnset(cat, k)
		if err != nil {
			return err
		}
		cs.Edits = append(cs.Edits, e)
	}
	origin := env.Origin(a)
	if *dry {
		out, err := command.Invoke(ctx, inv, a, configPreviewSpec, cs, origin)
		if err != nil || *asJSON {
			return env.Print(out, err)
		}
		pv, _ := out.(settings.Preview)
		printPreview(os.Stdout, pv)
		return nil
	}
	call := func(token string) (any, error) {
		return command.Invoke(ctx, inv, a, configSetSpec, configSetIn{ChangeSet: cs, ConfirmToken: token}, origin)
	}
	if *asJSON {
		return env.Confirm(*yes, call)
	}
	return confirmText(*yes, call)
}

func runConfigFields(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("config fields", flag.ContinueOnError)
	var in configFieldsIn
	fs.StringVar(&in.Group, "group", "", "only fields of this group")
	fs.StringVar(&in.Query, "search", "", "only fields whose ID, label or description contains this text")
	fs.BoolVar(&in.Modified, "modified", false, "only fields set in a layer file")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("config fields takes no arguments")
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	out, err := command.Invoke(ctx, inv, a, configFieldsSpec, in, env.Origin(a))
	if err != nil || *asJSON {
		return env.Print(out, err)
	}
	view, _ := out.(settings.View)
	for _, v := range view.Values {
		id := v.ID
		if v.Provider != "" {
			id += " [" + v.Provider + "]"
		}
		state := ""
		if !v.Active {
			state = ", inactive"
		}
		fmt.Printf("%s = %s (%s%s)\n", id, valueText(v.Effective), v.Source, state)
	}
	for _, d := range view.Problems {
		fmt.Printf("%s %s: %s\n", d.Severity, d.Code, d.Message)
	}
	return nil
}

func runConfigUndo(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("config undo", flag.ContinueOnError)
	var in configUndoIn
	fs.BoolVar(&in.SkipConflicts, "skip-conflicts", false, "revert only the fields that did not change since the save")
	mode := fs.String("mode", "", "draft, direct or worktree for an inverse repository change")
	fs.StringVar(&in.Repo.Title, "title", "", "conventional title of an inverse pull request")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("config undo takes no arguments")
	}
	in.Repo.Mode = settings.RepoMode(*mode)
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	origin := env.Origin(a)
	call := func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return command.Invoke(ctx, inv, a, configUndoSpec, next, origin)
	}
	if *asJSON {
		return env.Confirm(*yes, call)
	}
	return confirmText(*yes, call)
}

// interleaved parses flags anywhere among the positional arguments.
func interleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// confirmText is the confirmation flow of the CLI (cli.confirmLoop) with
// text output: on needs_confirmation it shows the summary, asks (unless
// yes) and runs again with the token, then prints one line per change.
func confirmText(yes bool, call func(token string) (any, error)) error {
	out, err := call("")
	if err != nil {
		return err
	}
	if p, ok := out.(*app.Pending); ok {
		switch p.Status {
		case "suggest":
			fmt.Println(p.Reason)
			for _, c := range p.Commands {
				fmt.Println("  " + c)
			}
			return nil
		case "needs_confirmation":
			if !yes {
				release, err := p.OpenPrompt()
				if err != nil {
					return err
				}
				defer release()
			}
			printSummary(os.Stderr, p)
			if !yes && !strings.HasPrefix(strings.ToLower(ask(os.Stdin, "Proceed? [y/N]: ")), "y") {
				return errors.Join(errors.New("cancelled"), p.Cancel())
			}
			if out, err = call(p.ConfirmToken); err != nil {
				return err
			}
		default:
			raw, _ := json.MarshalIndent(p, "", "  ")
			fmt.Println(string(raw))
			return nil
		}
	}
	switch r := out.(type) {
	case settings.SaveResult:
		printResult(os.Stdout, r)
	case settings.PartialResult:
		printResult(os.Stdout, r.SaveResult)
		return errors.New("saved in part: " + r.Error)
	case settings.Preview:
		printPreview(os.Stdout, r)
		return fmt.Errorf("not saved: %s", r.Status)
	default:
		raw, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(raw))
	}
	return nil
}

func ask(in io.Reader, prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	return strings.TrimSpace(line)
}

func printSummary(w io.Writer, p *app.Pending) {
	if p.Policy != nil {
		fmt.Fprintf(w, "Agentflow %s: %s (%s)\n", p.Policy.Action, p.Policy.Level, p.Policy.Source)
	}
	fmt.Fprintln(w, p.Reason)
	raw, _ := json.MarshalIndent(p.Summary, "", "  ")
	fmt.Fprintln(w, string(raw))
}

func valueText(v settings.Value) string {
	switch {
	case v.Redacted:
		return "(redacted)"
	case !v.Set:
		return "(unset)"
	}
	return string(v.JSON)
}

func printChanges(w io.Writer, changes []settings.FieldChange) {
	for _, c := range changes {
		fmt.Fprintf(w, "%s: %s → %s\n", c.ID, valueText(c.Before), valueText(c.After))
	}
}

func printPreview(w io.Writer, pv settings.Preview) {
	printChanges(w, pv.Changes)
	for _, d := range pv.Diagnostics {
		where := d.ID
		if where == "" {
			where = "change set"
		}
		fmt.Fprintf(w, "%s %s %s: %s\n", d.Severity, d.Code, where, d.Message)
	}
	if pv.Repo != nil {
		parts := []string{string(pv.Repo.Mode)}
		for _, p := range []string{pv.Repo.Destination, pv.Repo.Branch, pv.Repo.Title} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		fmt.Fprintf(w, "repository: %s\n", strings.Join(parts, " "))
	}
	fmt.Fprintf(w, "status: %s\n", pv.Status)
}

func printResult(w io.Writer, r settings.SaveResult) {
	printChanges(w, r.Changes)
	if r.Publication != nil && r.Publication.URL != "" {
		fmt.Fprintf(w, "pull request: %s (%s)\n", r.Publication.URL, r.Publication.State)
	}
	if r.Error != "" {
		fmt.Fprintln(w, r.Error)
	}
	if r.Restart {
		fmt.Fprintln(w, "Restart running agent sessions to use the provider changes.")
	}
	fmt.Fprintf(w, "status: %s\n", r.Status)
}

// ---- console ----

// consoleSettings runs the settings commands for the console daemon through
// its own Invoker, with origin daemon: a browser save is an op.* operation on
// the timeline like any other.
type consoleSettings struct{}

const consoleClient = "repository-console"

func (consoleSettings) open(ctx context.Context, call webui.SettingsCall) (*app.App, command.Invoker, command.Origin, error) {
	a, err := app.Open(ctx, call.Dir, consoleClient)
	if err != nil {
		return nil, nil, command.Origin{}, err
	}
	var source command.EmitterSource
	if call.Publish != nil {
		source = func(context.Context, *app.App) (command.Emitter, error) { return daemonEmitter{call}, nil }
	}
	inv := command.NewInvoker(command.Options{Emitters: source, Auditor: command.AuditLog{}})
	return a, inv, command.Origin{Source: "daemon", Client: consoleClient}, nil
}

func (c consoleSettings) Catalog(ctx context.Context, call webui.SettingsCall) (any, error) {
	a, inv, o, err := c.open(ctx, call)
	if err != nil {
		return nil, err
	}
	out, err := command.Invoke(ctx, inv, a, configFieldsSpec, configFieldsIn{}, o)
	if v, ok := out.(settings.View); ok {
		v.Values = []settings.FieldValue{} // values come from the stream
		return v, err
	}
	return out, err
}

func (c consoleSettings) Preview(ctx context.Context, call webui.SettingsCall, cs settings.ChangeSet) (any, error) {
	a, inv, o, err := c.open(ctx, call)
	if err != nil {
		return nil, err
	}
	return command.Invoke(ctx, inv, a, configPreviewSpec, cs, o)
}

func (c consoleSettings) Save(ctx context.Context, call webui.SettingsCall, cs settings.ChangeSet, token string) (any, error) {
	a, inv, o, err := c.open(ctx, call)
	if err != nil {
		return nil, err
	}
	return command.Invoke(ctx, inv, a, configSetSpec, configSetIn{ChangeSet: cs, ConfirmToken: token}, o)
}

func (c consoleSettings) Undo(ctx context.Context, call webui.SettingsCall, req settings.UndoRequest, token string) (any, error) {
	a, inv, o, err := c.open(ctx, call)
	if err != nil {
		return nil, err
	}
	return command.Invoke(ctx, inv, a, configUndoSpec, configUndoIn{UndoRequest: req, ConfirmToken: token}, o)
}

// daemonEmitter publishes on the daemon's bus.
type daemonEmitter struct{ call webui.SettingsCall }

func (d daemonEmitter) Emit(ctx context.Context, e bus.Event, worktree bool) error {
	e.Repo = d.call.Repo
	if worktree {
		e.Worktree = d.call.Worktree
	}
	return d.call.Publish(ctx, e)
}
