package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/webui"
)

func runEvents(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	since := fs.String("since", "", "resume after this cursor (printed at the end of each page)")
	limit := fs.Int("limit", 0, fmt.Sprintf("most events to print (default %d, at most %d); ignored with --follow", app.DefaultEventsLimit, app.MaxEventsLimit))
	follow := fs.Bool("follow", false, "keep printing new events until interrupted; starts the console daemon when needed")
	asJSON := fs.Bool("json", false, "print JSON: one page, or with --follow one {event, cursor} object per line")
	var q app.EventsQuery
	fs.Func("type", "event type pattern such as git.** (repeatable)", func(v string) error { q.Types = append(q.Types, v); return nil })
	fs.Func("topic", "topic REPO/WORKTREE/PATTERN; * as REPO means this repository (repeatable)", func(v string) error { q.Topics = append(q.Topics, v); return nil })
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: agentflow events [--since CURSOR] [--type PATTERN] [--topic T] [--limit N] [--follow] [--json]")
	}
	q.Since, q.Limit = *since, *limit
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if *follow {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		dir := a.Repo.Dir
		ensure := func(ctx context.Context) error { return webui.EnsureDaemon(ctx, dir) }
		return a.FollowEvents(ctx, q, ensure, func(e bus.Event, cursor string) error {
			if *asJSON {
				return json.NewEncoder(os.Stdout).Encode(struct {
					Event  bus.Event `json:"event"`
					Cursor string    `json:"cursor"`
				}{e, cursor})
			}
			printEvent(os.Stdout, e)
			return nil
		})
	}
	page, err := a.Events(ctx, q)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(page)
	}
	for _, e := range page.Events {
		printEvent(os.Stdout, e)
	}
	if len(page.Events) == 0 {
		fmt.Println("No events.")
	}
	fmt.Printf("cursor: %s\n", page.Cursor)
	if page.More {
		fmt.Printf("More events: agentflow events --since '%s'\n", page.Cursor)
	}
	return nil
}

func printEvent(w io.Writer, e bus.Event) {
	worktree := string(e.Worktree)
	if worktree == "" {
		worktree = "-"
	}
	fmt.Fprintf(w, "%s %6d %-22s %-20s %s %s\n", e.Time.Format("2006-01-02T15:04:05Z"), e.Seq, e.Type, worktree, e.Source, e.Payload)
}
