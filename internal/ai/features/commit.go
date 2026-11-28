package features

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/commit"
)

// The commit feature: a commit message for the staged or selected changes.
// Its candidate is the input of commit.prepare.

func init() { ai.Register(commitFeature{}) }

type commitFeature struct{}

type commitAnswer struct {
	Message string `json:"message" draft:"max=8192"`
}

func (commitFeature) ID() ai.FeatureID { return ai.FeatureCommit }

func (commitFeature) Gather(ctx context.Context, a *app.App, r ai.Request) (ai.Context, error) {
	d, err := a.CommitDraftContext(ctx, r.Paths)
	if err != nil {
		return ai.Context{}, err
	}
	changes := "staged changes"
	if len(r.Paths) > 0 {
		changes = "changes of the selected paths"
	}
	parts := []ai.Part{
		{Kind: ai.PartFiles, Label: "changed files", Source: "git", Text: d.Files},
		{Kind: ai.PartDiffStat, Label: "diff statistics", Source: "git", Text: d.Stat},
		{Kind: ai.PartDiff, Label: changes, Source: "git", Text: d.Diff, Excluded: secretExclusions(d.Withheld)},
		{Kind: ai.PartRules, Label: "commit message rules", Source: "git", Text: d.Rules},
	}
	if text := taskText(d.Task.Key, d.Task.Title, d.Task.Type, ""); text != "" {
		parts = append(parts, ai.Part{Kind: ai.PartTask, Label: "the branch's task", Source: d.Source, Text: text})
	}
	if d.Subjects != "" {
		parts = append(parts, ai.Part{Kind: ai.PartCommits, Label: "recent commit subjects", Source: "git", Text: d.Subjects})
	}
	var fixed json.RawMessage
	if len(r.Paths) > 0 {
		if fixed, err = json.Marshal(map[string]any{"paths": r.Paths}); err != nil {
			return ai.Context{}, err
		}
	}
	return ai.Context{Parts: parts, Rules: ai.Rules{Commit: d.Commit, KeyHint: d.KeyHint}, Fixed: fixed}, nil
}

// PartCap is the feature table's: 96 KiB of diff, 24 KiB a file, and 4 KiB
// of task.
func (commitFeature) PartCap(p ai.Part) (total, perFile int) {
	switch p.Kind {
	case ai.PartDiff:
		return 96 << 10, 24 << 10
	case ai.PartTask:
		return 4 << 10, 0
	}
	return 0, 0
}

func (commitFeature) Task() string {
	return "Write the git commit message for the changes in the data blocks. " +
		"The block of kind rules gives the format the message has to meet. " +
		"The first line is the subject; when it helps, add a body after a blank line that says why the change is made. " +
		"Describe only what the diff changes. Do not add attribution trailers."
}

func (commitFeature) Answer() any { return &commitAnswer{} }

func (commitFeature) Example() string {
	return `{"message": "feat: add a quota check\n\nReject requests over the tenant limit before they are scheduled."}`
}

func (commitFeature) Candidates(c ai.Context, answer any) ([]ai.Candidate, []ai.Issue) {
	ans, ok := answer.(*commitAnswer)
	if !ok {
		return nil, nil
	}
	var fixed struct {
		Paths []string `json:"paths"`
	}
	_ = json.Unmarshal(c.Fixed, &fixed)
	message := ans.Message
	if !c.Rules.Commit.AITrailer {
		message = commit.StripAITrailers(message)
	}
	message = strings.TrimSpace(message)
	input := map[string]any{"message": message}
	cli := []string{"agentflow", "commit", "--prepare", "-m", message}
	if len(fixed.Paths) > 0 {
		input["paths"] = fixed.Paths
		for _, p := range fixed.Paths {
			cli = append(cli, "--path", p)
		}
	}
	return []ai.Candidate{{
		Operation: "commit.prepare", Tool: "agentflow_commit_prepare", Input: inputJSON(input), CLI: cli,
		Fields: []ai.Field{{Name: "message", Label: "Commit message", Value: message}},
		Issues: ruleIssues(commit.Validate(message, c.Rules.Commit, c.Rules.KeyHint), "message"),
	}}, nil
}
