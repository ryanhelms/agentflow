package features

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
)

// The ac feature: which acceptance criteria the branch meets. Each met
// criterion that is not ticked becomes the input of ac.update; the harness
// names a criterion only by the number agentflow gave it, and no candidate
// ever posts to the task.

func init() { ai.Register(acFeature{}) }

type acFeature struct{}

type acAnswer struct {
	Checks []acCheck `json:"checks"`
}

type acCheck struct {
	Criterion int    `json:"criterion"`
	Status    string `json:"status" draft:"max=16,line"`
	Evidence  string `json:"evidence" draft:"max=200,line"`
}

const (
	acBranchLabel = "changes on the branch"
	acStagedLabel = "staged changes"
)

func (acFeature) ID() ai.FeatureID { return ai.FeatureAC }

func (acFeature) Gather(ctx context.Context, a *app.App, r ai.Request) (ai.Context, error) {
	d, err := a.ACDraftContext(ctx, gitDiff(a), r.Key)
	if err != nil {
		return ai.Context{}, err
	}
	parts := []ai.Part{criteriaPart(d.Source, d.Criteria)}
	if d.Branch.Diff != "" {
		parts = append(parts, ai.Part{Kind: ai.PartDiff, Label: acBranchLabel, Source: "git", Text: d.Branch.Diff, Excluded: secretExclusions(d.Branch.Withheld)})
	}
	if d.Staged.Diff != "" {
		parts = append(parts, ai.Part{Kind: ai.PartDiff, Label: acStagedLabel, Source: "git", Text: d.Staged.Diff, Excluded: secretExclusions(d.Staged.Withheld)})
	}
	if d.Commits != "" {
		parts = append(parts, ai.Part{Kind: ai.PartCommits, Label: "commits on the branch", Source: "git", Text: d.Commits})
	}
	var targets []ai.Target
	for i, item := range d.Criteria[:min(len(d.Criteria), ai.MaxCriteria)] {
		targets = append(targets, ai.Target{Index: i + 1, Kind: "criterion", ID: item.Text, Label: aisession.SanitizeLine(item.Text, 512), Done: item.Done})
	}
	fixed, err := json.Marshal(map[string]any{"key": d.Key})
	if err != nil {
		return ai.Context{}, err
	}
	return ai.Context{Parts: parts, Targets: targets, Fixed: fixed}, nil
}

// PartCap is the feature table's: 64 KiB of the branch's changes and 32 KiB
// of the staged ones.
func (acFeature) PartCap(p ai.Part) (total, perFile int) {
	if p.Kind != ai.PartDiff {
		return 0, 0
	}
	if p.Label == acStagedLabel {
		return 32 << 10, 0
	}
	return 64 << 10, 0
}

func (acFeature) Task() string {
	return "Judge each numbered target, an acceptance criterion, against the changes in the data blocks. " +
		`status is "met", "not_met" or "unclear". Say "met" only when the changes themselves show it; when in doubt say "unclear". ` +
		"evidence is one line that names the file, test or commit that shows it. Give each criterion once."
}

func (acFeature) Answer() any { return &acAnswer{} }

func (acFeature) Example() string {
	return `{"checks": [{"criterion": 1, "status": "met", "evidence": "TestQuotaLimit in quota_test.go"}]}`
}

func (acFeature) Candidates(c ai.Context, answer any) ([]ai.Candidate, []ai.Issue) {
	ans, ok := answer.(*acAnswer)
	if !ok {
		return nil, nil
	}
	var fixed struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(c.Fixed, &fixed)
	var out []ai.Candidate
	var issues []ai.Issue
	// A message is agentflow's own words: it may hold a target's number, and
	// nothing else the answer or the task says.
	issue := func(code, message string, args ...any) {
		issues = append(issues, ai.Issue{Code: code, Message: fmt.Sprintf(message, args...)})
	}
	named := map[int]int{}
	for _, check := range ans.Checks {
		named[check.Criterion]++
	}
	for _, check := range ans.Checks {
		n := check.Criterion
		if n < 1 || n > len(c.Targets) {
			issue("unknown_target", "The answer named a criterion agentflow did not give.")
			continue
		}
		if times := named[n]; times != 1 {
			// An answer that names a criterion twice says nothing safe about it.
			if times > 1 {
				issue("duplicate_target", "The answer named criterion %d more than once, so it is not offered.", n)
				named[n] = 0
			}
			continue
		}
		target := c.Targets[n-1]
		switch strings.ToLower(check.Status) {
		case "met":
			if target.Done {
				issue("already_ticked", "Criterion %d is already ticked.", n)
				continue
			}
		case "not_met":
			issue("not_met", "Criterion %d is not met, according to the draft.", n)
			continue
		case "unclear":
			issue("unclear", "The draft could not tell whether criterion %d is met.", n)
			continue
		default:
			issue("unknown_status", "The answer gave criterion %d a status that is not met, not_met or unclear.", n)
			continue
		}
		input := map[string]any{"item": target.ID, "done": true}
		cli := []string{"agentflow", "ac", "done", "--key", fixed.Key}
		if fixed.Key != "" {
			input["key"] = fixed.Key
		}
		if check.Evidence != "" {
			input["evidence"] = check.Evidence
			cli = append(cli, "--evidence", check.Evidence)
		}
		// The CLI reads flags anywhere before "--".
		cli = append(cli, "--", target.ID)
		out = append(out, ai.Candidate{
			Operation: "ac.update", Tool: "agentflow_ac_update", Input: inputJSON(input), CLI: cli, Target: &target,
			Fields: []ai.Field{{Name: "evidence", Label: "Evidence", Value: check.Evidence}},
		})
	}
	if len(ans.Checks) == 0 {
		issue("no_checks", "The answer judged no criterion.")
	}
	return out, issues
}
