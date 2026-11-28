package features

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/commit"
)

// The pr feature: a title and body for the branch's pull request. Its
// candidate is the input of pr.prepare.

func init() { ai.Register(prFeature{}) }

type prFeature struct{}

type prAnswer struct {
	Title string `json:"title" draft:"max=256,line"`
	Body  string `json:"body" draft:"max=32768"`
}

func (prFeature) ID() ai.FeatureID { return ai.FeaturePR }

func (prFeature) Gather(ctx context.Context, a *app.App, _ ai.Request) (ai.Context, error) {
	d, err := a.PRDraftContext(ctx)
	if err != nil {
		return ai.Context{}, err
	}
	parts := []ai.Part{
		{Kind: ai.PartCommits, Label: "commits on the branch", Source: "git", Text: d.Commits},
		{Kind: ai.PartDiffStat, Label: "diff statistics", Source: "git", Text: d.Stat},
		{Kind: ai.PartDiff, Label: "changes against the base", Source: "git", Text: d.Diff, Excluded: secretExclusions(d.Withheld)},
	}
	if text := taskText(d.Task.Key, d.Task.Title, d.Task.Type, d.Task.Description); text != "" {
		parts = append(parts, ai.Part{Kind: ai.PartTask, Label: "the branch's task", Source: d.Source, Text: text})
	}
	if len(d.Criteria) > 0 {
		parts = append(parts, criteriaPart(d.Source, d.Criteria))
	}
	parts = append(parts, ai.Part{Kind: ai.PartRules, Label: "pull request title rules", Source: "git", Text: d.Rules})
	if d.Template != "" {
		parts = append(parts, ai.Part{Kind: ai.PartTemplate, Label: "default pull request body", Source: "git", Text: d.Template})
	}
	return ai.Context{Parts: parts, Rules: ai.Rules{Commit: d.Commit, KeyHint: d.KeyHint}}, nil
}

// PartCap is the feature table's: 16 KiB of commits, 96 KiB of diff and
// 8 KiB of task.
func (prFeature) PartCap(p ai.Part) (total, perFile int) {
	switch p.Kind {
	case ai.PartCommits:
		return 16 << 10, 0
	case ai.PartDiff:
		return 96 << 10, 0
	case ai.PartTask:
		return 8 << 10, 0
	}
	return 0, 0
}

func (prFeature) Task() string {
	return "Write the pull request title and body for the branch's changes in the data blocks. " +
		"The block of kind rules gives the format the title has to meet. " +
		"The block of kind template is the default body: keep its headings, links and checklists, and fill in its sections from the changes. " +
		"Describe only what the data shows, and do not claim that tests ran."
}

func (prFeature) Answer() any { return &prAnswer{} }

func (prFeature) Example() string {
	return `{"title": "feat: add a quota check", "body": "## Why\n\nRequests over the tenant limit reached the scheduler.\n\n## What changed\n\n- reject them first"}`
}

func (prFeature) Candidates(c ai.Context, answer any) ([]ai.Candidate, []ai.Issue) {
	ans, ok := answer.(*prAnswer)
	if !ok {
		return nil, nil
	}
	input := map[string]any{"title": ans.Title}
	cli := []string{"agentflow", "pr", "--prepare", "--title", ans.Title}
	if ans.Body != "" {
		input["body"] = ans.Body
		cli = append(cli, "--body", ans.Body)
	}
	return []ai.Candidate{{
		Operation: "pr.prepare", Tool: "agentflow_pr_prepare", Input: inputJSON(input), CLI: cli,
		Fields: []ai.Field{{Name: "title", Label: "Title", Value: ans.Title}, {Name: "body", Label: "Body", Value: ans.Body}},
		Issues: ruleIssues(commit.ValidatePRTitle(ans.Title, c.Rules.Commit, c.Rules.KeyHint), "title"),
	}}, nil
}
