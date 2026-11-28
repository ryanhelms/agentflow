package ops

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
)

// --ai on commit, pr and ac (ADR "Phase 8 commands"): ai draft, then the
// command's own path with the drafted text. It is for a person at a
// terminal, asks before it uses a draft, and takes no --yes.

func aiUsage(message string) error {
	return &app.AIRefusal{Refusal: &aisession.Refusal{Code: ai.CodeUsage, Message: message,
		Fix: "Run the command with --ai alone, or without --ai and with your own text."}}
}

// draftForPerson makes a draft of in for a person at a terminal and prints
// it. Anyone else is refused before anything is read or sent. A nil draft
// and no error means there is nothing to use.
func draftForPerson(ctx context.Context, env command.CLIEnv, inv command.Invoker, in aiDraftIn) (*ai.Draft, error) {
	ctx = cliAIRequester(ctx)
	if !isPerson(ctx) {
		return nil, &app.AIRefusal{Refusal: &aisession.Refusal{Code: ai.CodeNeedsPerson,
			Message: "--ai is for a person at a terminal.",
			Fix:     "Run agentflow ai draft " + in.Feature + " (or agentflow_ai_draft) and pass the candidate's fields to the command."}}
	}
	a, err := env.Open(ctx)
	if err != nil {
		return nil, err
	}
	origin := env.Origin(a)
	in.foreground = true
	out, err := confirmDraft(false, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return command.Invoke(ctx, inv, a, aiDraftSpec, next, origin)
	})
	if err != nil || out == nil {
		return nil, err
	}
	var d ai.Draft
	switch v := out.(type) {
	case ai.Draft:
		d = v
	case AIDraftRunning:
		if !v.foreground {
			return nil, env.Print(v, nil)
		}
		if d, err = hostDraft(ctx, a, v.Session); err != nil {
			return nil, err
		}
	default:
		return nil, env.Print(out, nil)
	}
	// An invalid or failed draft prints its issues and is an error.
	if err := printDraftResult(os.Stdout, d); err != nil {
		return nil, err
	}
	return &d, nil
}

// usable are the candidates of operation without an issue.
func usable(d *ai.Draft, operation string) []ai.Candidate {
	var out []ai.Candidate
	for _, c := range d.Candidates {
		if c.Operation == operation && len(c.Issues) == 0 {
			out = append(out, c)
		}
	}
	return out
}

// draftInput decodes the first usable candidate of operation.
func draftInput(d *ai.Draft, operation string, in any) error {
	c := usable(d, operation)
	if len(c) == 0 {
		return errors.New("the draft has no usable candidate; run the command with your own text")
	}
	return json.Unmarshal(c[0].Input, in)
}

// askYes asks a yes or no question. It reads no further than the line, so
// each of several questions gets its own answer.
func askYes(prompt string) bool {
	fmt.Print(prompt)
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil {
			break
		}
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(line))), "y")
}

var errDraftDeclined = errors.New("cancelled: the draft was not used")

// flagsGiven lists which of names were set on fs.
func flagsGiven(fs *flag.FlagSet, names ...string) []string {
	var out []string
	fs.Visit(func(f *flag.Flag) {
		if slices.Contains(names, f.Name) {
			out = append(out, "--"+f.Name)
		}
	})
	return out
}

// commitWithAI is `commit --ai`: draft the message, then `commit -m MSG`
// with the same --path, or only prepare it.
func commitWithAI(ctx context.Context, env command.CLIEnv, inv command.Invoker, fs *flag.FlagSet, paths []string, prepare bool) error {
	if bad := flagsGiven(fs, "m", "yes", "confirm-token", "split", "dry-run"); len(bad) > 0 || fs.NArg() > 0 {
		return aiUsage("commit --ai drafts the message; it takes --path and --prepare only.")
	}
	d, err := draftForPerson(ctx, env, inv, aiDraftIn{Feature: string(ai.FeatureCommit), Paths: paths})
	if err != nil || d == nil {
		return err
	}
	var in commitPrepareIn
	if err := draftInput(d, "commit.prepare", &in); err != nil {
		return err
	}
	args := []string{"-m", in.Message}
	for _, p := range paths {
		args = append(args, "--path", p)
	}
	if prepare {
		return runCommit(ctx, env, inv, append(args, "--prepare"))
	}
	if !askYes("Use this draft? [y/N]: ") {
		return errDraftDeclined
	}
	return runCommit(ctx, env, inv, args)
}

// prWithAI is `pr --ai`: draft the title and body, then `pr` with them.
// --draft, --reviewer, --label and --run keep their meaning.
func prWithAI(ctx context.Context, env command.CLIEnv, inv command.Invoker, fs *flag.FlagSet, reviewers, labels []string) error {
	prepare, run := len(flagsGiven(fs, "prepare")) > 0, len(flagsGiven(fs, "run")) > 0
	if bad := flagsGiven(fs, "title", "body", "yes", "confirm-token", "check"); len(bad) > 0 || (prepare && run) || fs.NArg() > 0 {
		return aiUsage("pr --ai drafts the title and body; it takes --prepare, --draft, --reviewer, --label and --run only, and not --prepare with --run.")
	}
	var pass []string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "prepare", "draft", "run":
			pass = append(pass, "--"+f.Name+"="+f.Value.String())
		}
	})
	for _, r := range reviewers {
		pass = append(pass, "--reviewer", r)
	}
	for _, l := range labels {
		pass = append(pass, "--label", l)
	}
	d, err := draftForPerson(ctx, env, inv, aiDraftIn{Feature: string(ai.FeaturePR)})
	if err != nil || d == nil {
		return err
	}
	var in app.PROpenInput
	if err := draftInput(d, "pr.prepare", &in); err != nil {
		return err
	}
	args := []string{"--title", in.Title}
	if in.Body != "" {
		args = append(args, "--body", in.Body)
	}
	if !prepare && !askYes("Use this draft? [y/N]: ") {
		return errDraftDeclined
	}
	return runPR(ctx, env, inv, append(args, pass...))
}

// wantsAI reports whether args ask for --ai, before any "--".
func wantsAI(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "--ai", "-ai", "--ai=true", "-ai=true":
			return true
		}
	}
	return false
}

// acWithAI is `ac --ai [KEY]`: draft the checks, then ask before each tick
// and run `ac done` for it. It never posts to the task.
func acWithAI(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	fs := flag.NewFlagSet("ac --ai", flag.ContinueOnError)
	fs.Bool("ai", false, "draft criteria checks with your harness")
	fs.Bool("yes", false, "refused with --ai: every tick is asked")
	pos, err := taskParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(flagsGiven(fs, "yes")) > 0 || len(pos) > 1 {
		return aiUsage("ac --ai takes a task key only, and asks before each tick.")
	}
	in := aiDraftIn{Feature: string(ai.FeatureAC)}
	if len(pos) == 1 {
		in.Key = pos[0]
	}
	d, err := draftForPerson(ctx, env, inv, in)
	if err != nil || d == nil {
		return err
	}
	for _, c := range usable(d, "ac.update") {
		var update acUpdateIn
		if err := json.Unmarshal(c.Input, &update); err != nil {
			return err
		}
		number := "?"
		if c.Target != nil {
			number = strconv.Itoa(c.Target.Index)
		}
		fmt.Printf("\nCriterion %s: %s\nEvidence (the model's words): %s\n", number, visible(update.Item), visible(update.Evidence))
		if !askYes("Tick this criterion? [y/N]: ") {
			continue
		}
		tick := []string{"--key", update.Key, "--evidence", update.Evidence, "--", update.Item}
		if err := runACUpdate(ctx, env, inv, "done", tick); err != nil {
			return err
		}
	}
	return nil
}
