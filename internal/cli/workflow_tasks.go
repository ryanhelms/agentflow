package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/event"
)

func runTaskKeys(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	rng := fs.String("range", "", "revision range, e.g. v1.2.0..HEAD")
	shipped := fs.Bool("shipped", false, "sort keys and skip opted-out commits")
	path := fs.String("path", "", "limit shipped commits to a unit path")
	name := fs.Bool("provider", false, "print the configured task provider")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	if *name {
		fmt.Println(a.Cfg.Providers.TaskManagement.Provider)
		return nil
	}
	var keys []string
	if *shipped {
		keys, err = a.ShippedKeys(ctx, *rng, *path)
	} else {
		keys, err = a.Keys(ctx, *rng)
	}
	for _, k := range keys {
		fmt.Println(k)
	}
	return err
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validateWorkflowPR(ctx context.Context) error {
	var e struct {
		Number      int
		PullRequest struct {
			Title, Body string
			Head, Base  struct{ SHA string }
		} `json:"pull_request"`
	}
	raw, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("decode pull_request: %w", err)
	}
	pr := e.PullRequest
	if e.Number < 1 || !shaPattern.MatchString(pr.Head.SHA) || !shaPattern.MatchString(pr.Base.SHA) {
		return errors.New("expected a pull_request event with a number and base/head commit SHAs")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	commits, err := a.Repo.Git(ctx, "rev-list", "--no-merges", pr.Base.SHA+".."+pr.Head.SHA, "--")
	if err != nil {
		return err
	}
	var problems []error
	skip := event.OptedOutText(pr.Body)
	if err = a.ValidateWorkflowMessage(ctx, fmt.Sprintf("%s (#%d)\n\n%s", pr.Title, e.Number, pr.Body), true, skip); err != nil {
		problems = append(problems, fmt.Errorf("PR title or task reference: %w", err))
	}
	for _, sha := range strings.Fields(commits) {
		msg, err := a.Repo.Git(ctx, "show", "-s", "--format=%B", sha, "--")
		if err != nil {
			return err
		}
		if err := a.ValidateWorkflowMessage(ctx, msg, false, skip); err != nil {
			problems = append(problems, fmt.Errorf("commit %.7s: %w", sha, err))
		}
	}
	return errors.Join(problems...)
}
