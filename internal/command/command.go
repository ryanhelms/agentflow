package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Meta is the type-free part of a Spec: what the registry, the usage text,
// the parity test and the timeline need.
type Meta struct {
	Name        string
	CLIPath     []string
	Tool        string // "" without MCP
	Class       policy.Class
	Action      string
	Gate        GateMode
	PrepareOnly bool
	// AuditByInvoker and NativeReads are Policy.AuditByInvoker and
	// Spec.NativeReads.
	AuditByInvoker bool
	NativeReads    bool
	Events         Scope // resolved: never ScopeDefault
	WriteHost      WriteHost
	// OwnCLI is true when the Spec binds its own CLI (Spec.CLI != nil).
	OwnCLI bool
	// Usage, Section and Order come from Spec.CLI.
	Usage   string
	Section Section
	Order   int
}

// Command is a registered Spec with its types erased. Only Spec implements
// it.
type Command interface {
	Meta() Meta
	// AddMCP adds the Spec's tool to srv. wrap runs each call: it picks
	// the App, the native write host and the Origin, and formats the
	// result, as mcpserver.addTool does today. Nothing happens without MCP.
	AddMCP(srv *mcp.Server, wrap MCPWrap, inv Invoker)
	// RunCLI parses args and runs the Spec from the command line. It fails
	// when the Spec binds no CLI of its own.
	RunCLI(ctx context.Context, env CLIEnv, inv Invoker, args []string) error
	spec()
}

// MCPWrap runs one tool call in the MCP server: it loads the current App,
// applies the tool's write host, calls call, and turns its result or error
// into the tool result, appending notices. internal/mcpserver provides it
// from today's addTool body, so tool output stays byte-identical.
type MCPWrap func(ctx context.Context, m Meta, call func(ctx context.Context, a *app.App, o Origin) (any, error)) *mcp.CallToolResult

// CLIEnv is what internal/cli provides to derived commands.
type CLIEnv interface {
	// Open opens the App of the current directory (cli.open).
	Open(ctx context.Context) (*app.App, error)
	// Origin is the CLI's Origin for a: source "cli".
	Origin(a *app.App) Origin
	// Confirm runs fn through the confirmation flow and prints the final
	// result (cli.confirmLoop).
	Confirm(yes bool, fn func(token string) (any, error)) error
	// Print prints a result as indented JSON, or returns err
	// (cli.writeResult).
	Print(v any, err error) error
}

// Meta returns s's metadata.
func (s Spec[In, Out]) Meta() Meta {
	m := Meta{Name: s.Name, CLIPath: append([]string(nil), s.CLIPath...), Class: s.Policy.Class, Action: s.Policy.Action,
		Gate: s.Policy.Gate, PrepareOnly: s.Policy.PrepareOnly, AuditByInvoker: s.Policy.AuditByInvoker, NativeReads: s.NativeReads,
		Events: s.Events.Resolve(s.Policy.Class), OwnCLI: s.CLI != nil}
	if s.Policy.Class == policy.Read && m.Gate == GateInRun {
		m.Gate = GateNone
	}
	if s.MCP != nil {
		m.Tool, m.WriteHost = s.MCP.Tool, s.MCP.WriteHost
	}
	if s.CLI != nil {
		m.Usage, m.Section, m.Order = s.CLI.Usage, s.CLI.Section, s.CLI.Order
	}
	return m
}

func (Spec[In, Out]) spec() {}

// AddMCP implements Command.
func (s Spec[In, Out]) AddMCP(srv *mcp.Server, wrap MCPWrap, inv Invoker) {
	if s.MCP == nil {
		return
	}
	m := s.Meta()
	tool := &mcp.Tool{Name: s.MCP.Tool, Description: s.MCP.Description, Annotations: s.MCP.Annotations}
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		return wrap(ctx, m, func(ctx context.Context, a *app.App, o Origin) (any, error) {
			return Invoke(ctx, inv, a, s, in, o)
		}), nil, nil
	})
}

// ErrNoCLI is returned by RunCLI for a Spec without its own CLI binding.
var ErrNoCLI = errors.New("command has no CLI binding of its own")

// RunCLI implements Command: parse flags and arguments into In, open the App,
// then run the custom runner, or the confirmation flow (Confirm), or print
// the result once.
func (s Spec[In, Out]) RunCLI(ctx context.Context, env CLIEnv, inv Invoker, args []string) error {
	if s.CLI == nil {
		return fmt.Errorf("%s: %w", strings.Join(s.CLIPath, " "), ErrNoCLI)
	}
	fs := flag.NewFlagSet(strings.Join(s.CLIPath, " "), flag.ContinueOnError)
	var in In
	if s.CLI.Flags != nil {
		s.CLI.Flags(fs, &in)
	}
	if s.CLI.Confirm && fs.Lookup("yes") == nil {
		fs.Bool("yes", false, "skip the confirmation prompt")
	}
	pos, err := parseArgs(fs, args, s.CLI.Interspersed)
	if err != nil {
		return err
	}
	switch {
	case s.CLI.Args != nil:
		if err := s.CLI.Args(&in, pos); err != nil {
			return err
		}
	case len(pos) > 0:
		return fmt.Errorf("%s takes no arguments", strings.Join(s.CLIPath, " "))
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	origin := env.Origin(a)
	call := func(ctx context.Context, in In) (any, error) { return Invoke(ctx, inv, a, s, in, origin) }
	if s.CLI.Run != nil {
		return s.CLI.Run(ctx, CLICall[In]{Env: env, App: a, Invoke: call}, in)
	}
	given := s.Token != nil && *s.Token(&in) != ""
	if !s.CLI.Confirm || given || s.Policy.PrepareOnly {
		return env.Print(call(ctx, in))
	}
	return env.Confirm(boolFlag(fs, "yes"), func(token string) (any, error) {
		next := in
		if s.Token != nil {
			*s.Token(&next) = token
		}
		return call(ctx, next)
	})
}

// parseArgs parses args into fs and returns the positional arguments.
// Without interspersed, flags end at the first positional argument, as
// flag.Parse does. With interspersed, flags may follow positional arguments
// ("start KEY --worktree"), a flag that takes a value consumes the next
// argument, and "--" ends the flags: the same rule as parseInterleaved in
// internal/cli, so derived commands parse exactly like the legacy ones.
func parseArgs(fs *flag.FlagSet, args []string, interspersed bool) ([]string, error) {
	if !interspersed {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		return fs.Args(), nil
	}
	var options, positional []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			positional = append(positional, args...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if f := fs.Lookup(name); f != nil && !inline && len(args) > 0 {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
				options = append(options, args[0])
				args = args[1:]
			}
		}
	}
	return positional, fs.Parse(options)
}

// boolFlag returns the value of the boolean flag name, false when fs has no
// such flag.
func boolFlag(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	g, ok := f.Value.(flag.Getter)
	if !ok {
		return false
	}
	v, _ := g.Get().(bool)
	return v
}
