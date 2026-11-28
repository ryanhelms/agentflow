package features

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// The ticket feature (ADR "Phase 8 features"): a ticket from the caller's
// plain-text description and the task template. The model writes a summary
// and either a description or the text of each template section, named by
// its number; agentflow writes the headings. Every other task create field
// is the request's.

func init() { ai.Register(ticketFeature{}) }

type ticketFeature struct{}

func (ticketFeature) ID() ai.FeatureID { return ai.FeatureTicket }

type ticketAnswer struct {
	Summary     string          `json:"summary" draft:"max=256,line"`
	Description string          `json:"description,omitempty" draft:"max=32768"`
	Sections    []ticketSection `json:"sections,omitempty"`
}

type ticketSection struct {
	Section int    `json:"section"`
	Body    string `json:"body" draft:"max=8192"`
}

// ticketFixed are the task create fields the request set.
type ticketFixed struct {
	Template         string `json:"template,omitempty"`
	TemplateRevision string `json:"template_revision,omitempty"`
	Project          string `json:"project,omitempty"`
	Type             string `json:"type,omitempty"`
	LinkTo           string `json:"link_to,omitempty"`
}

// ticketInput is the agentflow_ticket_create input of the candidate.
type ticketInput struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	ticketFixed
}

func (ticketFeature) Gather(ctx context.Context, a *app.App, r ai.Request) (ai.Context, error) {
	text := strings.TrimSpace(tasksNewlines(r.Text))
	if text == "" {
		return ai.Context{}, &aisession.Refusal{Code: "no_text", Message: "A ticket draft needs your description.",
			Fix: "Give it with --text on ai draft, text on agentflow_ai_draft, or --description with task create --ai."}
	}
	raw, err := a.TicketDraftContext(ctx, tasktemplate.Ref{Name: r.Template, Revision: r.TemplateRevision})
	if err != nil {
		return ai.Context{}, err
	}
	c := ai.Context{Feature: ai.FeatureTicket, Parts: []ai.Part{{Kind: ai.PartText, Label: "the caller's description", Source: "caller", Text: text}}}
	if t := raw.Template; t != nil {
		c.Rules.Template = t
		c.Parts = append(c.Parts, ai.Part{Kind: ai.PartTemplate, Label: "task template", Source: "git", Text: ticketTemplateText(*t)})
		for i, s := range t.Sections {
			c.Targets = append(c.Targets, ai.Target{Index: i + 1, Kind: "section", ID: s.Heading, Label: aisession.SanitizeLine(s.Heading, 256)})
		}
	}
	c.Fixed, err = json.Marshal(ticketFixed{Template: r.Template, TemplateRevision: r.TemplateRevision, Project: r.Project, Type: r.Type, LinkTo: r.LinkTo})
	return c, err
}

// ticketTemplateText is the template as the prompt shows it: the required
// fields and the numbered sections with their default bodies.
func ticketTemplateText(t tasktemplate.Template) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Template: %s, revision %s\n", t.Name, t.Revision)
	required := "none"
	if len(t.Required) > 0 {
		required = strings.Join(t.Required, ", ")
	}
	fmt.Fprintf(&b, "Required fields: %s\n", required)
	for i, s := range t.Sections {
		fmt.Fprintf(&b, "Section %d: %s\n", i+1, s.Heading)
		if body := strings.TrimSpace(tasksNewlines(s.Body)); body != "" {
			fmt.Fprintf(&b, "Default body of section %d:\n%s\n", i+1, body)
		}
	}
	return b.String()
}

func (ticketFeature) Task() string {
	return `Write one ticket from the description in the data block of kind text. Use only what that description says: invent no requirement, name, number or link.
summary is the ticket's title: one line, at most 256 bytes.
If no numbered section is listed, answer {"summary": "...", "description": "..."}; description is Markdown.
If the data block of kind template lists numbered sections, answer {"summary": "...", "sections": [{"section": 1, "body": "..."}]} instead: one entry per numbered section, each once, by its number, and no description field. A body is Markdown without the section's heading, which agentflow writes. Where the description says nothing about a section, say what is not known; a section with a default body may be left out to keep that default.`
}

func (ticketFeature) Answer() any { return &ticketAnswer{} }

func (ticketFeature) Example() string {
	return `{"summary": "Add a greeting to the demo", "description": "Add a greeting line to the demo output."}`
}

func (ticketFeature) Candidates(c ai.Context, answer any) ([]ai.Candidate, []ai.Issue) {
	ans := answer.(*ticketAnswer)
	var fixed ticketFixed
	_ = json.Unmarshal(c.Fixed, &fixed)
	cand := ai.Candidate{Operation: "task.create", Tool: "agentflow_ticket_create"}
	switch {
	case ans.Summary == "":
		cand.Issues = append(cand.Issues, ai.Issue{Code: "missing_field", Field: "summary", Message: "The answer has no summary."})
	case strings.ContainsAny(ans.Summary, "\r\n"):
		cand.Issues = append(cand.Issues, ai.Issue{Code: "summary_lines", Field: "summary", Message: "The summary is not one line."})
	}
	description := ans.Description
	if t := c.Rules.Template; t != nil && len(t.Sections) > 0 {
		description = ticketDescription(*t, ans, &cand)
	} else {
		if len(ans.Sections) > 0 {
			cand.Issues = append(cand.Issues, ai.Issue{Code: "unknown_target", Field: "sections", Message: "The answer has template sections, and no template section applies."})
		}
		if description == "" {
			cand.Issues = append(cand.Issues, ai.Issue{Code: "missing_field", Field: "description", Message: "The answer has no description."})
		}
	}
	cand.Fields = []ai.Field{{Name: "summary", Label: "Title", Value: ans.Summary}, {Name: "description", Label: "Description", Value: description}}
	cand.Input, _ = json.Marshal(ticketInput{Summary: ans.Summary, Description: description, ticketFixed: fixed})
	cand.CLI = []string{"agentflow", "task", "create", "--summary", ans.Summary, "--description", description}
	for _, f := range [][2]string{{"--template", fixed.Template}, {"--template-revision", fixed.TemplateRevision},
		{"--project", fixed.Project}, {"--type", fixed.Type}, {"--link-to", fixed.LinkTo}} {
		if f[1] != "" {
			cand.CLI = append(cand.CLI, f[0], f[1])
		}
	}
	return []ai.Candidate{cand}, nil
}

// ticketDescription writes the description from the answer's sections, in
// the template's order, and adds to cand what is wrong with them.
func ticketDescription(t tasktemplate.Template, ans *ticketAnswer, cand *ai.Candidate) string {
	before := len(cand.Issues)
	issue := func(code, msg string) {
		cand.Issues = append(cand.Issues, ai.Issue{Code: code, Field: "sections", Message: msg})
	}
	warn := func(code, msg string) {
		cand.Warnings = append(cand.Warnings, ai.Issue{Code: code, Field: "description", Message: msg})
	}
	bodies := make([]string, len(t.Sections))
	given := make([]bool, len(t.Sections))
	for _, s := range ans.Sections {
		switch i := s.Section - 1; {
		case i < 0 || i >= len(t.Sections):
			issue("unknown_target", fmt.Sprintf("The answer names section %d, which the template does not have.", s.Section))
		case given[i]:
			issue("duplicate_target", fmt.Sprintf("The answer gives section %d twice.", s.Section))
		default:
			given[i], bodies[i] = true, strings.TrimSpace(s.Body)
		}
	}
	var blocks []string
	for i, s := range t.Sections {
		body := bodies[i]
		if body == "" {
			// The default is repository text: cleaned like the model's.
			if body = strings.TrimSpace(aisession.Sanitize(tasksNewlines(s.Body), 8<<10)); body == "" {
				issue("missing_section", fmt.Sprintf("Section %d of the template has no text.", i+1))
				continue
			}
			warn("template_default", fmt.Sprintf("Section %d keeps the template's default text.", i+1))
		}
		// The heading stays exact, or task create would not find it.
		if strings.ContainsFunc(s.Heading, func(r rune) bool { return r != '\t' && aisession.Hazard(r) }) {
			warn("hidden_characters", fmt.Sprintf("The template's heading of section %d holds hidden characters.", i+1))
		}
		blocks = append(blocks, "## "+s.Heading+"\n\n"+body)
	}
	description := strings.Join(blocks, "\n\n")
	if ans.Description != "" {
		warn("unused_field", "The answer also had a description. With template sections agentflow writes it from the sections, so it was ignored.")
	}
	// The same check task create makes: a body can hide a later heading.
	shape := tasktemplate.Template{Ref: t.Ref, Sections: t.Sections}
	if len(cand.Issues) == before && shape.Validate(provider.TaskDraft{Description: description}) != nil {
		issue("invalid_template", "A section's text hides a section heading or leaves a section empty (an unclosed code block, or a heading of its own), so task create would refuse it.")
	}
	return description
}

// tasksNewlines writes Windows line endings as newlines, so they do not
// reach the prompt as visible control characters.
func tasksNewlines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
