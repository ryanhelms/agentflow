package features_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	_ "github.com/ryanhelms/agentflow/internal/ai/features"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// ticketFinish runs the host's finish step on answer, as a draft session
// does: parse, clean, check, make candidates.
func ticketFinish(t *testing.T, id ai.FeatureID, c ai.Context, answer string) ai.Draft {
	t.Helper()
	targets, err := json.Marshal(map[string]any{"feature": id, "harness_name": "Drafter", "rules": c.Rules, "targets": c.Targets, "fixed": c.Fixed})
	if err != nil {
		t.Fatal(err)
	}
	var d ai.Draft
	if err := json.Unmarshal(ai.Finish("ais_test", targets, "end_turn", answer), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func ticketCodes(issues []ai.Issue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Code)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func ticketRefusal(t *testing.T, err error) string {
	t.Helper()
	var r *aisession.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a refusal", err)
	}
	return r.Code
}

func ticketFeature(t *testing.T, id ai.FeatureID) ai.Feature {
	t.Helper()
	f, ok := ai.Lookup(id)
	if !ok {
		t.Fatalf("feature %s is not registered", id)
	}
	if f.ID() != id || f.Task() == "" || !json.Valid([]byte(f.Example())) {
		t.Fatalf("feature %s: task or example", id)
	}
	return f
}

// ticketContext is what Gather would return for a template with these
// sections.
func ticketContext(fixed string, sections ...tasktemplate.Section) ai.Context {
	c := ai.Context{Feature: ai.FeatureTicket, Fixed: json.RawMessage(fixed)}
	if sections != nil {
		c.Rules.Template = &tasktemplate.Template{Ref: tasktemplate.Ref{Name: "bug", Revision: "1"}, Sections: sections,
			Required: []string{"title", "priority"}}
		for i, s := range sections {
			c.Targets = append(c.Targets, ai.Target{Index: i + 1, Kind: "section", ID: s.Heading, Label: s.Heading})
		}
	}
	return c
}

func ticketInput(t *testing.T, c ai.Candidate) map[string]string {
	t.Helper()
	var in map[string]string
	if err := json.Unmarshal(c.Input, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestTicketCandidateWithoutTemplate(t *testing.T) {
	ticketFeature(t, ai.FeatureTicket)
	d := ticketFinish(t, ai.FeatureTicket, ticketContext(`{"project":"eng","type":"Bug","link_to":"PLAT-1"}`),
		`{"summary": "Add a\ngreeting", "description": "- greet the user", "project": "other"}`)
	if d.State != ai.DraftReady || len(d.Candidates) != 1 {
		t.Fatalf("draft %+v", d)
	}
	c := d.Candidates[0]
	if c.Operation != "task.create" || c.Tool != "agentflow_ticket_create" || len(c.Issues) != 0 {
		t.Fatalf("candidate %+v", c)
	}
	// The fixed fields are the request's; the answer cannot set one.
	want := map[string]string{"summary": "Add a greeting", "description": "- greet the user", "project": "eng", "type": "Bug", "link_to": "PLAT-1"}
	if got := ticketInput(t, c); len(got) != len(want) || got["summary"] != want["summary"] || got["description"] != want["description"] ||
		got["project"] != "eng" || got["type"] != "Bug" || got["link_to"] != "PLAT-1" {
		t.Errorf("input %v", got)
	}
	cli := []string{"agentflow", "task", "create", "--summary", "Add a greeting", "--description", "- greet the user", "--project", "eng", "--type", "Bug", "--link-to", "PLAT-1"}
	if !slices.Equal(c.CLI, cli) {
		t.Errorf("cli %v", c.CLI)
	}
	if len(c.Fields) != 2 || c.Fields[0].Name != "summary" || c.Fields[1].Name != "description" {
		t.Errorf("fields %+v", c.Fields)
	}
	if !slices.Equal(ticketCodes(d.Warnings), []string{ai.IssueUnknownField}) {
		t.Errorf("warnings %+v", d.Warnings)
	}
}

func TestTicketCandidateWithTemplate(t *testing.T) {
	sections := []tasktemplate.Section{{Heading: "Why"}, {Heading: "How to test", Body: "Run make check."}, {Heading: "Risk"}}
	fixed := `{"template":"bug","template_revision":"1"}`
	// The answer's order does not matter: the template's does. A section
	// with a default may be left to it.
	d := ticketFinish(t, ai.FeatureTicket, ticketContext(fixed, sections...),
		`{"summary": "Fix quota", "sections": [{"section": 3, "body": "Low."}, {"section": 1, "body": "The quota is wrong."}]}`)
	if d.State != ai.DraftReady {
		t.Fatalf("draft %+v", d)
	}
	c := d.Candidates[0]
	got := ticketInput(t, c)
	want := "## Why\n\nThe quota is wrong.\n\n## How to test\n\nRun make check.\n\n## Risk\n\nLow."
	if got["description"] != want || got["template"] != "bug" || got["template_revision"] != "1" {
		t.Errorf("input %q", got)
	}
	if !slices.Equal(c.CLI[len(c.CLI)-4:], []string{"--template", "bug", "--template-revision", "1"}) {
		t.Errorf("cli %v", c.CLI)
	}
	if !slices.Equal(ticketCodes(c.Warnings), []string{"template_default"}) {
		t.Errorf("warnings %+v", c.Warnings)
	}
}

func TestTicketCandidateIssues(t *testing.T) {
	sections := []tasktemplate.Section{{Heading: "Why"}, {Heading: "Risk"}}
	with, without := ticketContext(`{}`, sections...), ticketContext(`{}`)
	cases := []struct {
		name    string
		c       ai.Context
		answer  string
		issues  []string
		warning string
	}{
		{"missing section", with, `{"summary":"s","sections":[{"section":1,"body":"a"}]}`, []string{"missing_section"}, ""},
		{"empty section", with, `{"summary":"s","sections":[{"section":1,"body":"a"},{"section":2,"body":"  "}]}`, []string{"missing_section"}, ""},
		{"out of range", with, `{"summary":"s","sections":[{"section":1,"body":"a"},{"section":2,"body":"b"},{"section":9,"body":"c"}]}`, []string{"unknown_target"}, ""},
		{"zero index", with, `{"summary":"s","sections":[{"section":0,"body":"c"},{"section":1,"body":"a"},{"section":2,"body":"b"}]}`, []string{"unknown_target"}, ""},
		{"twice", with, `{"summary":"s","sections":[{"section":1,"body":"a"},{"section":1,"body":"b"},{"section":2,"body":"b"}]}`, []string{"duplicate_target"}, ""},
		{"no summary", with, `{"sections":[{"section":1,"body":"a"},{"section":2,"body":"b"}]}`, []string{"missing_field"}, ""},
		// An unclosed fence would swallow the next heading.
		{"swallowed heading", with, `{"summary":"s","sections":[{"section":1,"body":"` + "```" + `\ncode"},{"section":2,"body":"b"}]}`, []string{"invalid_template"}, ""},
		{"description with a template", with, `{"summary":"s","description":"mine","sections":[{"section":1,"body":"a"},{"section":2,"body":"b"}]}`, nil, "unused_field"},
		{"sections without a template", without, `{"summary":"s","description":"d","sections":[{"section":1,"body":"a"}]}`, []string{"unknown_target"}, ""},
		{"no description", without, `{"summary":"s"}`, []string{"missing_field"}, ""},
		{"nothing", without, `{}`, []string{"missing_field"}, ""},
	}
	for _, c := range cases {
		d := ticketFinish(t, ai.FeatureTicket, c.c, c.answer)
		if len(d.Candidates) != 1 {
			t.Fatalf("%s: %+v", c.name, d)
		}
		cand := d.Candidates[0]
		if !slices.Equal(ticketCodes(cand.Issues), c.issues) {
			t.Errorf("%s: issues %+v, want %v", c.name, cand.Issues, c.issues)
		}
		if (d.State == ai.DraftReady) != (c.issues == nil) {
			t.Errorf("%s: state %s", c.name, d.State)
		}
		if c.warning != "" && !slices.Contains(ticketCodes(cand.Warnings), c.warning) {
			t.Errorf("%s: warnings %+v", c.name, cand.Warnings)
		}
		if c.warning != "" && strings.Contains(ticketInput(t, cand)["description"], "mine") {
			t.Errorf("%s: the unused description was kept", c.name)
		}
	}
}

// A summary the finish step did not clean still has to be one line.
func TestTicketSummaryOneLine(t *testing.T) {
	f := ticketFeature(t, ai.FeatureTicket)
	a := f.Answer()
	if err := json.Unmarshal([]byte(`{"summary":"a\nb","description":"d"}`), a); err != nil {
		t.Fatal(err)
	}
	c, _ := f.Candidates(ticketContext(`{}`), a)
	if len(c) != 1 || !slices.Equal(ticketCodes(c[0].Issues), []string{"summary_lines"}) {
		t.Errorf("%+v", c)
	}
}

// The fake agent's answers pass and fail the checks as the contract says.
func TestTicketFakeAnswers(t *testing.T) {
	d := ticketFinish(t, ai.FeatureTicket, ticketContext(`{}`), acptest.DraftAnswers[acptest.DraftValid]["ticket"])
	if d.State != ai.DraftReady {
		t.Errorf("valid: %+v", d)
	}
	d = ticketFinish(t, ai.FeatureTicket, ticketContext(`{}`), acptest.DraftAnswers[acptest.DraftInvalid]["ticket"])
	if d.State != ai.DraftInvalid || !slices.Equal(ticketCodes(d.Candidates[0].Issues), []string{"missing_field", "unknown_target"}) {
		t.Errorf("invalid: %+v", d)
	}
}

// ticketApp is a repository with a filesystem task provider and a template
// catalog; config is the task_management block.
func ticketApp(t *testing.T, config string) *app.App {
	t.Helper()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".agentflow/config.yml": "task_management:\n" + config,
		"catalog.json": `[{"name":"bug","revision":"1","required":["title"],
		  "sections":[{"heading":"Why"},{"heading":"How to test","body":"Run make check."}]}]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := app.Open(context.Background(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTicketGather(t *testing.T) {
	f := ticketFeature(t, ai.FeatureTicket)
	a := ticketApp(t, "  provider: filesystem\n  templates: {catalog: catalog.json, default: {name: bug, revision: '1'}}\n")
	r := ai.Request{Feature: ai.FeatureTicket, Text: "  The quota is wrong.\r\nSecond line.  ", Project: "eng", Type: "Bug", LinkTo: "PLAT-1"}
	c, err := f.Gather(context.Background(), a, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parts) != 2 || c.Parts[0].Kind != ai.PartText || c.Parts[0].Source != "caller" || c.Parts[0].Text != "The quota is wrong.\nSecond line." {
		t.Fatalf("parts %+v", c.Parts)
	}
	tmpl := c.Parts[1]
	if tmpl.Kind != ai.PartTemplate {
		t.Fatalf("parts %+v", c.Parts)
	}
	for _, want := range []string{"bug", "Required fields: title", "Section 1: Why", "Section 2: How to test", "Run make check."} {
		if !strings.Contains(tmpl.Text, want) {
			t.Errorf("the template part lacks %q:\n%s", want, tmpl.Text)
		}
	}
	if len(c.Targets) != 2 || c.Targets[1].Index != 2 || c.Targets[1].Kind != "section" || c.Targets[1].ID != "How to test" || c.Rules.Template == nil {
		t.Errorf("targets %+v", c.Targets)
	}
	// The default template is not written into the candidate's input.
	var fixed map[string]string
	if err := json.Unmarshal(c.Fixed, &fixed); err != nil || len(fixed) != 3 || fixed["project"] != "eng" || fixed["type"] != "Bug" || fixed["link_to"] != "PLAT-1" {
		t.Errorf("fixed %s", c.Fixed)
	}

	// Without a template there is only the text.
	plain := ticketApp(t, "  provider: filesystem\n")
	c, err = f.Gather(context.Background(), plain, ai.Request{Feature: ai.FeatureTicket, Text: "x"})
	if err != nil || len(c.Parts) != 1 || len(c.Targets) != 0 || c.Rules.Template != nil {
		t.Errorf("no template: %+v %v", c, err)
	}

	for want, req := range map[string]struct {
		a    *app.App
		text string
	}{"no_text": {a, " \n "}, "no_task_provider": {ticketApp(t, "  provider: none\n"), "x"}} {
		_, err := f.Gather(context.Background(), req.a, ai.Request{Feature: ai.FeatureTicket, Text: req.text})
		if got := ticketRefusal(t, err); got != want {
			t.Errorf("%s: %s", want, got)
		}
	}
}
