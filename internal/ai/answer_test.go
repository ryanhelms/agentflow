package ai_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/ai/aitest"
)

const sid = "ais_0123456789abcdef0123456789abcdef"

func targetsFile(t *testing.T, targets []ai.Target, fixed map[string]any) []byte {
	t.Helper()
	fx, _ := json.Marshal(fixed)
	b, _ := json.Marshal(map[string]any{"feature": "commit", "harness_name": "Drafter", "targets": targets, "fixed": json.RawMessage(fx)})
	return b
}

func finish(t *testing.T, targets []byte, stop, answer string) ai.Draft {
	t.Helper()
	var d ai.Draft
	if err := json.Unmarshal(ai.Finish(sid, targets, stop, answer), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func codes(is []ai.Issue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Code)
	}
	return out
}

// TestDraftAnswers covers every case of "Phase 8 answers".
func TestDraftAnswers(t *testing.T) {
	aitest.Use(t, &aitest.Feature{})
	targets := []ai.Target{{Index: 1, Kind: "criterion", ID: "Tests pass", Label: "Tests pass"}, {Index: 2, Kind: "criterion", ID: "Docs updated", Label: "Docs updated"}}
	tf := targetsFile(t, targets, map[string]any{"paths": []string{"a.go"}, "key": "PLAT-1"})

	t.Run("stop reasons", func(t *testing.T) {
		for _, stop := range []string{"max_tokens", "refusal", "limit_duration", "draft_tool", "answer_too_large"} {
			d := finish(t, tf, stop, `{"message":"feat: x"}`)
			if d.State != ai.DraftFailed || d.Code != stop || len(d.Candidates) != 0 {
				t.Errorf("%s: %+v", stop, d)
			}
		}
		d := finish(t, tf, "cancelled", `{"message":"feat: x"}`)
		if d.State != ai.DraftCancelled || len(d.Candidates) != 0 {
			t.Errorf("cancelled: %+v", d)
		}
	})
	t.Run("one object", func(t *testing.T) {
		ok := []string{
			`{"message":"feat: add x"}`,
			"  \n{\"message\":\"feat: add x\"}\n",
			"Here it is:\n```json\n{\"message\":\"feat: add x\"}\n```\nDone.",
		}
		for _, a := range ok {
			if d := finish(t, tf, "end_turn", a); d.State != ai.DraftReady {
				t.Errorf("%q: %+v", a, d)
			}
		}
		bad := []string{
			"no json at all",
			`{"message":"feat: a"} and {"message":"feat: b"}`,
			"```json\n{\"message\":\"feat: a\"}\n```\n```json\n{\"message\":\"feat: b\"}\n```",
			`{"message": 42}`,
			`["feat: a"]`,
			acptest.DraftAnswer("valid", "nothing"),
		}
		for _, a := range bad {
			d := finish(t, tf, "end_turn", a)
			if d.State != ai.DraftFailed || d.Code != ai.IssueUnparsable || d.Answer == "" {
				t.Errorf("%q: %+v", a, d)
			}
		}
	})
	t.Run("unknown keys change nothing", func(t *testing.T) {
		d := finish(t, tf, "end_turn", `{"message":"feat: add x","operation":"push","paths":["/etc"],"items":[{"target":1,"note":"ok","thread_id":"T"}]}`)
		if d.State != ai.DraftReady {
			t.Fatalf("%+v", d)
		}
		var names []string
		for _, w := range d.Warnings {
			if w.Code == ai.IssueUnknownField {
				names = append(names, w.Field)
			}
		}
		if !slices.Equal(names, []string{"items.thread_id", "operation", "paths"}) {
			t.Errorf("unknown fields %v", names)
		}
		var in struct {
			Paths []string `json:"paths"`
		}
		_ = json.Unmarshal(d.Candidates[0].Input, &in)
		if !slices.Equal(in.Paths, []string{"a.go"}) {
			t.Errorf("the answer changed a fixed field: %v", in.Paths)
		}
	})
	t.Run("fields are cleaned", func(t *testing.T) {
		token := "gh" + "p_" + strings.Repeat("A", 36)
		ans, _ := json.Marshal(map[string]any{
			"message": "feat: add x\u202e\u200b\x1b[31m",
			"title":   "line one\nline two",
			"body":    "see " + token,
			"items":   []map[string]any{{"target": 2, "note": strings.Repeat("n", 300)}},
		})
		d := finish(t, tf, "end_turn", string(ans))
		if d.State != ai.DraftReady {
			t.Fatalf("%+v", d)
		}
		got := codes(d.Warnings)
		for _, want := range []string{ai.IssueHazardRemoved, ai.IssueRedacted, ai.IssueCut} {
			if !slices.Contains(got, want) {
				t.Errorf("warnings %v lack %s", got, want)
			}
		}
		msg := d.Candidates[0].Fields[0].Value
		if msg != "feat: add x" {
			t.Errorf("message %q", msg)
		}
		if body := d.Candidates[0].Fields[1].Value; strings.Contains(body, token) || !strings.Contains(body, "[redacted]") {
			t.Errorf("body %q", body)
		}
		if note := d.Candidates[1].Fields[0].Value; len(note) != 200 {
			t.Errorf("note %d bytes, want the 200 cap", len(note))
		}
	})
	t.Run("targets by index only", func(t *testing.T) {
		d := finish(t, tf, "end_turn", `{"message":"","items":[{"target":2,"note":"done"},{"target":2,"note":"again"},{"target":9,"note":"x"},{"target":0}]}`)
		if len(d.Candidates) != 1 || d.Candidates[0].Target == nil || d.Candidates[0].Target.ID != "Docs updated" {
			t.Fatalf("%+v", d.Candidates)
		}
		if len(d.Issues) != 3 {
			t.Errorf("issues %+v, want three: a duplicate and two out of range", d.Issues)
		}
	})
	t.Run("secrets in the draft", func(t *testing.T) {
		secret := "AI" + "za" + strings.Repeat("k", 35)
		d := finish(t, tf, "end_turn", `{"message":"feat: add `+secret+`"}`)
		if d.State != ai.DraftInvalid || len(d.Candidates) != 1 {
			t.Fatalf("%+v", d)
		}
		c := d.Candidates[0]
		if !slices.Contains(codes(c.Issues), ai.IssueSecretInDraft) || string(c.Input) != "null" || c.Fields[0].Value != "" || c.CLI != nil {
			t.Errorf("the secret's candidate was not emptied: %+v", c)
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), secret) && !strings.Contains(d.Answer, secret) {
			t.Error("the secret is in the candidates")
		}
	})
	t.Run("mentions and links", func(t *testing.T) {
		d := finish(t, tf, "end_turn", `{"message":"feat: x","body":"thanks @alice, see https://example.com"}`)
		got := codes(d.Candidates[0].Warnings)
		if !slices.Contains(got, ai.IssueMention) || !slices.Contains(got, ai.IssueLink) {
			t.Errorf("warnings %v", got)
		}
		d = finish(t, tf, "end_turn", `{"message":"feat: mail a@b.example"}`)
		if len(d.Candidates[0].Warnings) != 0 {
			t.Errorf("a commit message is not posted: %v", d.Candidates[0].Warnings)
		}
	})
	t.Run("invalid keeps the answer", func(t *testing.T) {
		d := finish(t, tf, "end_turn", `{"message":"Added stuff."}`)
		if d.State != ai.DraftInvalid || d.Answer == "" || len(d.Candidates[0].Issues) == 0 {
			t.Errorf("%+v", d)
		}
	})
	t.Run("too large", func(t *testing.T) {
		d := finish(t, tf, "end_turn", strings.Repeat("x", ai.MaxAnswer+1))
		if d.State != ai.DraftFailed || len(d.Answer) > ai.MaxShownAnswer {
			t.Errorf("state %s, answer %d bytes", d.State, len(d.Answer))
		}
	})
	t.Run("no feature", func(t *testing.T) {
		d := finish(t, []byte(`{"feature":"pr"}`), "end_turn", `{"message":"feat: x"}`)
		if d.State != ai.DraftFailed {
			t.Errorf("%+v", d)
		}
	})
}
