package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/contextx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// scopeIn is the tool's empty input.
type scopeIn struct{}

// scopeCheck is typed because the `scope` verb reaches it through
// command.Invoke: the verb parses --json, which has no tool input (a field
// for it would change the tool's input schema).
var scopeCheck = command.Spec[scopeIn, any]{
	Name:    "scope.check",
	CLIPath: []string{"scope"},
	MCP: &command.MCP{Tool: "agentflow_scope_check", Description: "Find changes and new TODOs that look outside the current ticket.",
		Annotations: command.ReadOnly},
	Policy: command.Policy{Class: policy.Read},
	Run: func(ctx context.Context, a *app.App, _ scopeIn) (any, error) {
		return a.ScopeCheck(ctx)
	},
}

func init() {
	command.Register(scopeCheck)
	command.RegisterVerb(command.Verb{
		Path:    []string{"scope"},
		Usage:   "  scope [--json]                 changes and new TODOs that look outside the ticket",
		Section: command.SectionWork,
		Order:   3200,
		Run:     runScope,
	})
}

// runScope is `agentflow scope`.
func runScope(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("scope", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: agentflow scope [--json]")
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	out, err := command.Invoke(ctx, inv, a, scopeCheck, scopeIn{}, env.Origin(a))
	if err != nil {
		return err
	}
	if *asJSON {
		return env.Print(out, nil)
	}
	d, _ := out.(*contextx.Drift)
	if d == nil {
		return errors.New("expected a scope check")
	}
	fmt.Fprintln(os.Stdout, d.Summary)
	for _, f := range d.Files {
		fmt.Fprintln(os.Stdout, "  file:", f)
	}
	for _, t := range d.TODOs {
		fmt.Fprintln(os.Stdout, "  todo:", t)
	}
	return nil
}
