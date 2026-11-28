package features_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/config"
)

// candidates decodes raw into the feature's answer and returns what
// Candidates makes of it, as ai.Finish does after cleaning the text.
func candidates(t *testing.T, id ai.FeatureID, c ai.Context, raw string) ([]ai.Candidate, []ai.Issue) {
	t.Helper()
	f, ok := ai.Lookup(id)
	if !ok {
		t.Fatalf("%s is not registered", id)
	}
	ans := f.Answer()
	if err := json.Unmarshal([]byte(raw), ans); err != nil {
		t.Fatal(err)
	}
	return f.Candidates(c, ans)
}

func codes(issues []ai.Issue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Code)
	}
	return out
}

func rules(requireKey bool, hint string) ai.Rules {
	return ai.Rules{Commit: config.Commit{Types: config.DefaultTypes, SubjectMax: 72, RequireKey: &requireKey}, KeyHint: hint}
}

// Every example is a valid answer of its own feature, and every feature has
// constant instructions.
func TestExamplesAreValid(t *testing.T) {
	c := ai.Context{Rules: rules(false, ""), Targets: []ai.Target{{Index: 1, Kind: "criterion", ID: "one"}}}
	for _, id := range []ai.FeatureID{ai.FeatureCommit, ai.FeaturePR, ai.FeatureAC} {
		f, ok := ai.Lookup(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		if strings.TrimSpace(f.Task()) == "" || f.ID() != id {
			t.Errorf("%s: no task, or the wrong ID", id)
		}
		cands, issues := candidates(t, id, c, f.Example())
		if len(cands) != 1 || len(cands[0].Issues) != 0 || len(issues) != 0 {
			t.Errorf("%s: %+v %+v", id, cands, issues)
		}
	}
}

func TestCommitCandidates(t *testing.T) {
	c := ai.Context{Rules: rules(true, "PLAT-1")}
	cands, _ := candidates(t, ai.FeatureCommit, c, `{"message": "feat(PLAT-1): add quota\n\nWhy.\n\nCo-authored-by: Claude <noreply@anthropic.com>\n"}`)
	if len(cands) != 1 || len(cands[0].Issues) != 0 {
		t.Fatalf("%+v", cands)
	}
	if got := cands[0].Fields[0]; got.Name != "message" || got.Value != "feat(PLAT-1): add quota\n\nWhy." {
		t.Errorf("field %+v", got)
	}
	if string(cands[0].Input) != `{"message":"feat(PLAT-1): add quota\n\nWhy."}` {
		t.Errorf("input %s", cands[0].Input)
	}
	// The repository allows the trailer.
	allowed := c
	allowed.Rules.Commit.AITrailer = true
	cands, _ = candidates(t, ai.FeatureCommit, allowed, `{"message": "feat(PLAT-1): add quota\n\nCo-authored-by: Claude <noreply@anthropic.com>"}`)
	if !strings.Contains(cands[0].Fields[0].Value, "Co-authored-by") {
		t.Errorf("trailer removed: %+v", cands[0].Fields)
	}
	for name, raw := range map[string]string{
		"format": `{"message": "Added the quota"}`,
		"empty":  `{"message": ""}`,
		"type":   `{"message": "nope(PLAT-1): add quota"}`,
	} {
		cands, _ = candidates(t, ai.FeatureCommit, c, raw)
		if len(cands) != 1 || !slices.Contains(codes(cands[0].Issues), name) {
			t.Errorf("%s: %+v", name, cands)
		}
	}
}

func TestPRCandidates(t *testing.T) {
	c := ai.Context{Rules: rules(false, "")}
	cands, _ := candidates(t, ai.FeaturePR, c, `{"title": "feat: add quota", "body": "Why."}`)
	if len(cands) != 1 || len(cands[0].Issues) != 0 || string(cands[0].Input) != `{"body":"Why.","title":"feat: add quota"}` {
		t.Fatalf("%+v %s", cands, cands[0].Input)
	}
	if got := []string{cands[0].Fields[0].Name, cands[0].Fields[1].Name}; !slices.Equal(got, []string{"title", "body"}) {
		t.Errorf("fields %v", got)
	}
	// A title leaves room for the squash suffix.
	cands, _ = candidates(t, ai.FeaturePR, c, `{"title": "feat: `+strings.Repeat("x", 62)+`", "body": "Why."}`)
	if !slices.Contains(codes(cands[0].Issues), "length") {
		t.Errorf("long title: %+v", cands[0].Issues)
	}
}

func TestACCandidates(t *testing.T) {
	c := ai.Context{Fixed: json.RawMessage(`{"key":"PLAT-1"}`), Targets: []ai.Target{
		{Index: 1, Kind: "criterion", ID: "first"},
		{Index: 2, Kind: "criterion", ID: "second", Done: true},
		{Index: 3, Kind: "criterion", ID: "third"},
		{Index: 4, Kind: "criterion", ID: "fourth"},
		{Index: 5, Kind: "criterion", ID: "-n is accepted"},
		{Index: 6, Kind: "criterion", ID: "sixth"},
	}}
	cands, issues := candidates(t, ai.FeatureAC, c, `{"checks": [
		{"criterion": 1, "status": "met", "evidence": "a_test.go"},
		{"criterion": 2, "status": "met", "evidence": "x"},
		{"criterion": 3, "status": "not_met", "evidence": "x"},
		{"criterion": 4, "status": "unclear"},
		{"criterion": 5, "status": "met"},
		{"criterion": 6, "status": "yes"},
		{"criterion": 0, "status": "met"},
		{"criterion": 99, "status": "met"}
	]}`)
	if len(cands) != 2 {
		t.Fatalf("candidates %+v", cands)
	}
	if string(cands[0].Input) != `{"done":true,"evidence":"a_test.go","item":"first","key":"PLAT-1"}` || cands[0].Target == nil || cands[0].Target.Index != 1 {
		t.Errorf("first %s %+v", cands[0].Input, cands[0].Target)
	}
	// No evidence is not sent, and an item like a flag stays an item.
	if want := []string{"agentflow", "ac", "done", "--key", "PLAT-1", "--", "-n is accepted"}; !slices.Equal(cands[1].CLI, want) {
		t.Errorf("cli %q", cands[1].CLI)
	}
	want := []string{"already_ticked", "not_met", "unclear", "unknown_status", "unknown_target", "unknown_target"}
	if !slices.Equal(codes(issues), want) {
		t.Errorf("issues %v, want %v", codes(issues), want)
	}

	// A criterion named twice gets no candidate.
	cands, issues = candidates(t, ai.FeatureAC, c, `{"checks": [{"criterion": 1, "status": "met"}, {"criterion": 1, "status": "not_met"}]}`)
	if len(cands) != 0 || !slices.Equal(codes(issues), []string{"duplicate_target"}) {
		t.Errorf("duplicate: %+v %v", cands, codes(issues))
	}
	if _, issues = candidates(t, ai.FeatureAC, c, `{"checks": []}`); !slices.Equal(codes(issues), []string{"no_checks"}) {
		t.Errorf("no checks: %v", codes(issues))
	}
}

// An issue message is shown as agentflow's own words, so nothing the model
// or the repository wrote is in one.
func TestDeliveryIssueMessagesHoldNoDraftedText(t *testing.T) {
	c := ai.Context{Rules: rules(true, "ZZKEY-1"), Fixed: json.RawMessage(`{"key":"ZZKEY-1"}`), Targets: []ai.Target{
		{Index: 1, Kind: "criterion", ID: "zzcriterion one", Label: "zzcriterion one"},
		{Index: 2, Kind: "criterion", ID: "zzcriterion two", Label: "zzcriterion two", Done: true},
		{Index: 3, Kind: "criterion", ID: "zzcriterion three"},
		{Index: 4, Kind: "criterion", ID: "zzcriterion four"},
	}}
	c.Rules.Commit.Types = []string{"feat", "zzrepotype"}
	for _, draft := range []struct {
		id  ai.FeatureID
		raw string
	}{
		{ai.FeatureCommit, `{"message": "zzmodel(PLAT-1): add quota"}`},
		{ai.FeatureCommit, `{"message": "feat(PLAT-1): Zzadded the quota."}`},
		{ai.FeatureCommit, `{"message": "zzmodel words only"}`},
		{ai.FeatureCommit, `{"message": "feat(PLAT-1): add ` + strings.Repeat("zz", 40) + `"}`},
		{ai.FeaturePR, `{"title": "zzmodel(PLAT-1): add quota", "body": "zzbody"}`},
		{ai.FeaturePR, `{"title": "feat(PLAT-1): add ` + strings.Repeat("zz", 40) + `"}`},
		{ai.FeatureAC, `{"checks": [
			{"criterion": 1, "status": "not_met", "evidence": "zzevidence"},
			{"criterion": 2, "status": "met", "evidence": "zzevidence"},
			{"criterion": 3, "status": "zzstatus", "evidence": "zzevidence"},
			{"criterion": 4, "status": "unclear", "evidence": "zzevidence"},
			{"criterion": 987, "status": "met", "evidence": "zzevidence"}]}`},
	} {
		cands, issues := candidates(t, draft.id, c, draft.raw)
		for _, cand := range cands {
			issues = append(issues, cand.Issues...)
		}
		if len(issues) == 0 {
			t.Errorf("%s %s: no issue to check", draft.id, draft.raw)
		}
		for _, is := range issues {
			if m := strings.ToLower(is.Message); m == "" || strings.Contains(m, "zz") || strings.Contains(m, "987") {
				t.Errorf("%s %s: message %q", draft.id, is.Code, is.Message)
			}
		}
	}
}

// The CLI reads flags anywhere, so drafted text is only ever a flag's value
// or comes after "--".
func TestDeliveryCLIWordsCannotBecomeFlags(t *testing.T) {
	c := ai.Context{Rules: rules(false, ""), Fixed: json.RawMessage(`{"key":"PLAT-1"}`), Targets: []ai.Target{
		{Index: 1, Kind: "criterion", ID: "--post"},
		{Index: 2, Kind: "criterion", ID: "ordinary words"},
	}}
	cands, _ := candidates(t, ai.FeatureAC, c, `{"checks": [{"criterion": 1, "status": "met", "evidence": "--yes"}, {"criterion": 2, "status": "met", "evidence": "-x"}]}`)
	if len(cands) != 2 {
		t.Fatalf("%+v", cands)
	}
	for i, want := range [][]string{
		{"agentflow", "ac", "done", "--key", "PLAT-1", "--evidence", "--yes", "--", "--post"},
		{"agentflow", "ac", "done", "--key", "PLAT-1", "--evidence", "-x", "--", "ordinary words"},
	} {
		if !slices.Equal(cands[i].CLI, want) {
			t.Errorf("cli %q, want %q", cands[i].CLI, want)
		}
	}
	cands, _ = candidates(t, ai.FeatureCommit, c, `{"message": "--yes"}`)
	if want := []string{"agentflow", "commit", "--prepare", "-m", "--yes"}; !slices.Equal(cands[0].CLI, want) {
		t.Errorf("commit cli %q", cands[0].CLI)
	}
	cands, _ = candidates(t, ai.FeaturePR, c, `{"title": "--yes", "body": "--run"}`)
	if want := []string{"agentflow", "pr", "--prepare", "--title", "--yes", "--body", "--run"}; !slices.Equal(cands[0].CLI, want) {
		t.Errorf("pr cli %q", cands[0].CLI)
	}
}
