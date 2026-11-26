package configconsole

import (
	"context"
	"errors"
	"strings"

	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/consolepublish"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

// PublicationPlan is what publishing one repository file through the console
// publication flow would bind: the same destination, source baseline, head,
// title and CI rules as agentflow_config_apply (Review). Settings saves
// (internal/settings) bind it into their one approval.
type PublicationPlan struct {
	Destination string            `json:"destination"`
	Baseline    map[string]string `json:"baseline"`
	Head        string            `json:"head"`
	Title       string            `json:"title"`
	CI          string            `json:"ci"`
}

// PlanPublication checks a candidate for path as Review does before it gates,
// without gating: the candidate previews, the title is a valid conventional
// title under the repository's commit rules, and a publication destination
// and source baseline resolve.
func PlanPublication(ctx context.Context, repo *gitx.Repo, path, original, content, title string) (PublicationPlan, error) {
	plan, err := consolecfg.Preview(ctx, repo.Dir, path, content, consolecfg.Digest(original))
	if err != nil {
		return PublicationPlan{}, err
	}
	if err := checkTitle(ctx, repo, title); err != nil {
		return PublicationPlan{}, err
	}
	target, err := consolepublish.Destination(ctx, repo.Dir)
	if err != nil {
		return PublicationPlan{}, err
	}
	baseline, err := consolepublish.SourceBaseline(repo.Dir, plan.Sources, path, original)
	if err != nil {
		return PublicationPlan{}, err
	}
	head, err := repo.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return PublicationPlan{}, errors.New("repository has no commit to review")
	}
	return PublicationPlan{Destination: target, Baseline: baseline, Head: strings.TrimSpace(head), Title: title, CI: ciImpact(repo.Dir, path, "")}, nil
}

// checkTitle applies publicationGate's title rules.
func checkTitle(ctx context.Context, repo *gitx.Repo, title string) error {
	cfg, _, err := config.LoadRepoCore(repo.Dir)
	if err != nil {
		return errors.New("cannot read the current repository approval policy")
	}
	message := commit.Parse(title)
	if strings.ContainsAny(title, "\r\n") || !message.Conventional || message.Exempt {
		return errors.New("enter a conventional commit title")
	}
	if issues := commit.Validate(title, cfg.Commit, gitx.KeyFromBranch(repo.Branch(ctx))); len(issues) > 0 {
		return errors.New(issues[0].Message)
	}
	return nil
}

// Publish publishes a planned candidate through consolepublish.PublishTo, the
// flow agentflow_config_apply uses. It never touches the worktree.
func Publish(ctx context.Context, repo *gitx.Repo, p PublicationPlan, path, original, content string, draft bool, taskRef string, publishTo Publisher) (consolepublish.Result, error) {
	if publishTo == nil {
		publishTo = consolepublish.PublishTo
	}
	var task []string
	if taskRef != "" {
		task = []string{taskRef}
	}
	return publishTo(ctx, repo.Dir, p.Destination, path, original, content, p.Title, draft, p.Baseline, task...)
}
