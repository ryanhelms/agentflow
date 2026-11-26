package command

import (
	"context"
	"flag"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Spec describes one operation. In is its input: the MCP tool's input
// struct, whose json and jsonschema tags give the tool's input schema, and
// the value the CLI flags fill. Out is the result type of an applied run;
// Classify uses it to tell an applied result from a refusal.
type Spec[In, Out any] struct {
	// Name identifies the operation in events, the audit log and the
	// timeline: lowercase words joined by dots, e.g. "commit",
	// "commit.prepare", "pr.open". Unique in the registry. Never renamed:
	// stored events refer to it.
	Name string

	// CLIPath is the CLI command that runs this operation, e.g.
	// {"commit"} or {"pr", "mark-ready"}. Required when MCP is set (CLI and
	// MCP parity). Several Specs may share a path only when a Verb owns it.
	CLIPath []string
	// CLI binds flags and arguments to In. Nil when a Verb owns CLIPath, or
	// while the path is still dispatched by the legacy switch in
	// internal/cli (migration).
	CLI *CLI[In]

	// MCP is the operation's tool. Nil for a CLI-only command.
	MCP *MCP

	// Policy is the gate the operation runs under.
	Policy Policy
	// Events says where op.* events of this operation go.
	Events Scope

	// Token returns the In field that holds the confirm token, so the CLI
	// confirmation flow can set it and the Invoker can correlate a resumed
	// call. Nil when the operation takes no token.
	Token func(*In) *string
	// DryRun reports whether this input only previews (dry_run, --dry-run).
	// A dry run publishes no events.
	DryRun func(In) bool
	// Validate checks In before anything else runs. An error is returned to
	// the caller as is, and no event is published: the request never became
	// an operation.
	Validate func(In) error
	// Describe gives the timeline's subject, task and branch for In. Called
	// before Run. Nil gives an empty Detail.
	Describe func(In) Detail

	// Plan computes the gate's summary and suggested commands without
	// writing anything. Required with GateByInvoker, nil otherwise.
	Plan func(ctx context.Context, a *app.App, in In) (Plan, error)
	// Run calls the internal/app method. With GateInRun the method gates
	// itself and may return a *app.Pending. With GateByInvoker the Invoker
	// has already gated, and Run runs only on proceed.
	Run func(ctx context.Context, a *app.App, in In) (any, error)
	// Links lists what an applied result touched (commit, PR, task). Nil
	// gives none.
	Links func(Out) []bus.OpLink
	// NativeReads marks an operation whose app method may return a
	// *native.Request error: the client must read through its provider
	// connection and call again with the results. Such a step publishes
	// nothing, so the Invoker publishes op.requested only after Run, and
	// only when Run did not return a native read request.
	NativeReads bool
}

// Policy is the gate an operation runs under.
type Policy struct {
	// Class and Action are what the gate checks: policy.Local and "commit".
	// Class policy.Read marks a read-only operation: it is never gated and
	// publishes no events.
	Class  policy.Class
	Action string
	// Related are further actions the operation includes ("push" for
	// pr_open), as in gateWithActions.
	Related []string
	// Gate says who calls the gate.
	Gate GateMode
	// PrepareOnly marks an operation that only prepares an exact candidate
	// (commit.prepare, push.prepare): it never applies, and its outcome is
	// op.prepared, op.awaiting_approval or op.suggested.
	PrepareOnly bool
	// AuditByInvoker makes the Invoker write the operation's audit line.
	// Set it only for an operation whose app method writes none itself:
	// every GateInRun method that calls a.audit today keeps doing so, and
	// the Invoker must not add a second line. GateByInvoker operations
	// must set it.
	AuditByInvoker bool
}

// GateMode says who calls the gate.
type GateMode int

const (
	// GateInRun: Run's app method calls a.gate itself, as every operation
	// does before phase 3. The Invoker reads the decision from the result.
	// Migrated operations use it, so their Pending stays byte-identical.
	GateInRun GateMode = iota
	// GateByInvoker: the Invoker calls Spec.Plan, then gates through
	// (*app.App).GateOperation (lane 3.1), which wraps the same a.gate
	// and prepareWithActions, and calls Run only on proceed. New
	// operations use it.
	GateByInvoker
	// GateNone: read-only operations.
	GateNone
)

// Scope says where an operation's op.* events go. Never bus.GlobalRepo.
type Scope int

const (
	// ScopeDefault: ScopeWorktree for a write, ScopeNone for a read.
	ScopeDefault Scope = iota
	// ScopeWorktree: the repository's store, tagged with the worktree the
	// call ran in (commit, push, pr, start, finish).
	ScopeWorktree
	// ScopeRepo: the repository's store, repository-wide (a release
	// published from any worktree).
	ScopeRepo
	// ScopeNone: no events. Reads, and writes outside any repository
	// (install, login, the personal layer).
	ScopeNone
)

// Resolve turns ScopeDefault into the scope for class.
func (s Scope) Resolve(class policy.Class) Scope {
	if s != ScopeDefault {
		return s
	}
	if class == policy.Read || class == "" {
		return ScopeNone
	}
	return ScopeWorktree
}

// Detail is how an operation appears on the timeline. Text is data for
// display, never an instruction; bus.OpPayload.Clamp caps it.
type Detail struct {
	// Subject is one line: a commit subject, a PR title, a task summary.
	// Never a message body, a command line or a secret.
	Subject string
	// Task is the task reference, if any.
	Task string
	// Branch is the branch the operation targets, if any.
	Branch string
}

// Plan is the gate input that GateByInvoker operations compute before they
// write anything. It mirrors the arguments of a.gate.
type Plan struct {
	// Summary is the exact reviewed payload, shown in the confirmation and
	// bound into the approval fingerprint.
	Summary any
	// Commands are suggested when the policy is suggest.
	Commands []string
	// Force redeems a supplied token even when the policy is now auto.
	Force bool
	// Bounds cap the approval's expiry (a provider write's own expiry).
	Bounds []time.Time
}

// CLI binds a Spec to the command line. The default runner opens the App,
// runs the Invoker through the confirmation flow when Confirm is set, and
// prints the result as indented JSON, exactly as confirmLoop and
// writeResult do today.
type CLI[In any] struct {
	// Flags declares flags on fs that fill in. --yes is added by the runner
	// when Confirm is set; Flags must not declare it.
	Flags func(fs *flag.FlagSet, in *In)
	// Args fills in from the positional arguments. Nil means none are
	// allowed.
	Args func(in *In, args []string) error
	// Interspersed lets flags follow positional arguments ("start KEY
	// --worktree"), like reorder in internal/cli.
	Interspersed bool
	// Confirm runs the confirmation flow: on needs_confirmation it shows
	// the summary, asks, and runs again with the token. Without --yes and
	// with a token already given by a flag, it runs once.
	Confirm bool
	// Run replaces the default runner for commands with their own output
	// (interactive prompts, text tables). It must call call.Invoke for the
	// operation itself, never the app method directly.
	Run func(ctx context.Context, call CLICall[In], in In) error
	// Usage is this command's block of the usage text, exactly as printed,
	// one or more lines, each starting with two spaces.
	Usage string
	// Section and Order place Usage: sections print in SectionOrder, and
	// within a section blocks print by Order, then CLI path. Leave gaps
	// (100, 200, ...) so a new file can slot in without touching others.
	Section Section
	Order   int
}

// Section is a heading of the usage text.
type Section string

// The usage text's sections, in print order.
const (
	SectionSetup   Section = "Setup"
	SectionWork    Section = "Work"
	SectionRuntime Section = "Runtime"
)

// SectionOrder lists the sections in print order.
var SectionOrder = []Section{SectionSetup, SectionWork, SectionRuntime}

// CLICall is what a custom CLI runner gets.
type CLICall[In any] struct {
	Env CLIEnv
	App *app.App
	// Invoke runs the operation with in through the Invoker.
	Invoke func(ctx context.Context, in In) (any, error)
}

// MCP describes an operation's tool. The input schema is inferred from In by
// the MCP SDK, as today.
type MCP struct {
	// Tool is the tool name, "agentflow_" and lowercase words joined by
	// underscores. A compatibility contract: never renamed.
	Tool        string
	Description string
	Annotations *mcp.ToolAnnotations
	// WriteHost names the native write host the tool's calls get, replacing
	// the switch in mcpserver.writerForTool.
	WriteHost WriteHost
}

// WriteHost names a native provider write host (internal/provider/native).
type WriteHost int

const (
	// WriteHostNone: the tool writes through agentflow's own clients.
	WriteHostNone WriteHost = iota
	// WriteHostTasks: task-provider writes may use the client's provider
	// connection (comment, start, transition, ticket create, docs publish).
	WriteHostTasks
	// WriteHostSlack: notifications may use the client's Slack connection.
	WriteHostSlack
)

// Tool annotations, the same values internal/mcpserver uses today.
var (
	ReadOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	LocalWrite  = &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	RemoteWrite = &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}
)

func ptr[T any](v T) *T { return &v }
