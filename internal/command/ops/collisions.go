package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type collisionsIn struct {
	Key string `json:"key,omitempty" jsonschema:"task key or qualified reference; defaults to the branch task"`
}

var collisionsSpec = command.Spec[collisionsIn, any]{
	Name:    "collisions",
	CLIPath: []string{"collisions"},
	MCP: &command.MCP{Tool: "agentflow_collisions", Description: "Other people's PRs on this ticket and open PRs touching the same files.",
		Annotations: command.ReadOnly},
	Policy: command.Policy{Class: policy.Read},
	Run: func(ctx context.Context, a *app.App, in collisionsIn) (any, error) {
		key := in.Key
		if key == "" {
			key = a.CurrentKey(ctx)
		}
		return a.Collisions(ctx, key)
	},
}

func init() {
	command.Register(collisionsSpec)
	// A Verb, not a Spec's CLI: the flag --json only changes how the result
	// prints, and a Spec's CLI keeps flag values out of reach of its runner.
	command.RegisterVerb(command.Verb{
		Path:    []string{"collisions"},
		Usage:   "  collisions [KEY] [--json]      other PRs on this task or touching your files",
		Section: command.SectionWork,
		Order:   3400,
		Run:     runCollisions,
	})
}

// runCollisions prints the other open PRs on the task or on the same files.
func runCollisions(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("collisions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print raw JSON")
	pos, err := parseInterleavedArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: agentflow collisions [KEY] [--json]")
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	var in collisionsIn
	if len(pos) == 1 {
		in.Key = pos[0]
	}
	out, err := command.Invoke(ctx, inv, a, collisionsSpec, in, env.Origin(a))
	if err != nil {
		return err
	}
	if *asJSON {
		return env.Print(out, nil)
	}
	c, _ := out.(*app.Collisions)
	if c == nil {
		return errors.New("collisions returned nothing")
	}
	if c.MyPR != "" {
		fmt.Println("Your PR:", c.MyPR)
	}
	if len(c.OthersOnTicket) == 0 && len(c.FileOverlaps) == 0 {
		fmt.Println("No other open PRs on this task or your files.")
		return nil
	}
	for _, p := range c.OthersOnTicket {
		fmt.Println("  same task:", p)
	}
	for _, f := range c.FileOverlaps {
		fmt.Println("  same files:", f)
	}
	return nil
}
