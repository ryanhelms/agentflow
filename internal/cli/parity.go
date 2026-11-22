package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/gh"
)

// The commands in this file give the MCP tools that had no terminal
// equivalent a CLI command over the same internal/app method.

// parseInterleaved parses flags that may appear before, between or after
// positional arguments. Everything after the flag terminator is literal,
// including a comment body or checklist item spelled "--yes".
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
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

const acUsage = "usage: devflow ac [list] [KEY] [--json] | devflow ac done|undo ITEM [--key KEY] [--evidence E] [--post] [--yes]"

func runAC(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		switch args[0] {
		case "list", "done", "undo":
			sub, args = args[0], args[1:]
		}
	}
	fs := flag.NewFlagSet("ac "+sub, flag.ContinueOnError)
	if sub == "list" {
		asJSON := fs.Bool("json", false, "print the checklist as JSON")
		pos, err := parseInterleaved(fs, args)
		if err != nil {
			return err
		}
		if len(pos) > 1 {
			return errors.New(acUsage)
		}
		key := ""
		if len(pos) == 1 {
			key = pos[0]
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		cl, err := a.Checklist(ctx, key)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(cl)
		}
		d, n := cl.Progress()
		fmt.Printf("%s · %d/%d acceptance criteria\n", cl.Key, d, n)
		for _, i := range cl.Items {
			mark := "[ ]"
			if i.Done {
				mark = "[x]"
			}
			line := "  " + mark + " " + i.Text
			if i.Evidence != "" {
				line += "  (" + i.Evidence + ")"
			}
			fmt.Println(line)
		}
		return nil
	}
	key := fs.String("key", "", "task key; defaults to the branch task")
	evidence := fs.String("evidence", "", "commit SHA or test name that proves it")
	post := fs.Bool("post", false, "also post a progress comment to the task")
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	item := strings.Join(pos, " ")
	if item == "" {
		return errors.New(acUsage)
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	done := sub == "done"
	return confirmLoop(*yes, func(tok string) (any, error) {
		return a.UpdateChecklist(ctx, *key, item, done, *evidence, *post, tok)
	})
}

func runScope(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scope", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: devflow scope [--json]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	d, err := a.ScopeCheck(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(d)
	}
	fmt.Println(d.Summary)
	for _, f := range d.Files {
		fmt.Println("  file:", f)
	}
	for _, t := range d.TODOs {
		fmt.Println("  todo:", t)
	}
	return nil
}

func runComment(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("comment", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	key, text := "", ""
	switch len(pos) {
	case 1:
		text = pos[0]
	case 2:
		key, text = pos[0], pos[1]
	default:
		return errors.New("usage: devflow comment [KEY] TEXT [--yes]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		ctx = app.WithJiraCommentCLI(ctx)
	}
	return confirmLoop(*yes, func(tok string) (any, error) { return a.Comment(ctx, key, text, tok) })
}

func runReview(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "reply" {
		return runReviewReply(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "threads" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print unresolved review threads as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: devflow review [threads] [--json] | devflow review reply THREAD_ID BODY [--yes]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	out, err := a.ReviewThreads(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out)
	}
	threads, _ := out["threads"].([]gh.Thread)
	if out["pr"] == nil {
		fmt.Println("no open PR for this branch")
		return nil
	}
	fmt.Printf("%v · %d unresolved\n", out["pr"], len(threads))
	for _, t := range threads {
		fmt.Printf("\n%s:%d  @%s  thread %s\n%s\n", t.Path, t.Line, t.Author, t.ID, indent(t.Body))
	}
	return nil
}

func runReviewReply(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review reply", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: devflow review reply THREAD_ID BODY [--yes]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	return confirmLoop(*yes, func(tok string) (any, error) { return a.ReviewReply(ctx, pos[0], pos[1], tok) })
}

func runEOD(ctx context.Context, args []string) error {
	const usage = "usage: devflow eod post KEY=NOTE [KEY=NOTE...] [--yes]"
	if len(args) == 0 || args[0] != "post" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("eod post", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New(usage)
	}
	notes := make(map[string]string, len(pos))
	for _, p := range pos {
		key, note, ok := strings.Cut(p, "=")
		key, note = strings.TrimSpace(key), strings.TrimSpace(note)
		if !ok || key == "" || note == "" {
			return fmt.Errorf("%q is not KEY=NOTE; %s", p, usage)
		}
		notes[key] = note
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	return confirmLoop(*yes, func(tok string) (any, error) { return a.PostEOD(ctx, notes, tok) })
}

func runCollisions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("collisions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print raw JSON")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: devflow collisions [KEY] [--json]")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	key := a.CurrentKey(ctx)
	if len(pos) == 1 {
		key = pos[0]
	}
	c, err := a.Collisions(ctx, key)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c)
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
