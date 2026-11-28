package features

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/contextx"
)

// What the commit, pr and ac features share. None of it writes prompt text
// of its own beyond field names and numbers: every part goes through
// ai.Filter and ai.PromptBuilder.

// gitDiff makes a's diffs for the read methods with ai.GitDiff, whose flags
// no git setting overrides.
func gitDiff(a *app.App) func(context.Context, ...string) (string, error) {
	return func(ctx context.Context, args ...string) (string, error) { return ai.GitDiff(ctx, a, args...) }
}

// secretExclusions lists files the read method already took out of a diff.
func secretExclusions(paths []string) []ai.Exclusion {
	var out []ai.Exclusion
	for _, p := range paths {
		out = append(out, ai.Exclusion{Path: p, Reason: ai.ExcludedSecretFile})
	}
	return out
}

// taskText is a task part, or "" when nothing is known about the task.
func taskText(key, title, kind, description string) string {
	var b strings.Builder
	for _, line := range [][2]string{{"Key", key}, {"Title", title}, {"Type", kind}, {"Description", description}} {
		if line[1] == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", line[0], line[1])
	}
	return b.String()
}

// criteriaPart numbers the criteria as the targets do and marks the ticked
// ones, at most ai.MaxCriteria of them.
func criteriaPart(source string, items []contextx.Item) ai.Part {
	var b strings.Builder
	for i, item := range items[:min(len(items), ai.MaxCriteria)] {
		done := ""
		if item.Done {
			done = "[already ticked] "
		}
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, done, item.Text)
	}
	return ai.Part{Kind: ai.PartCriteria, Label: "acceptance criteria", Source: source, Text: b.String(), Cut: len(items) > ai.MaxCriteria}
}

// inputJSON encodes a candidate's input; its values are strings, booleans
// and string lists, which always encode.
func inputJSON(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// ruleMessages are agentflow's words for the convention's findings. The
// convention's own messages quote the drafted type and the repository's list
// of types, and an issue message is shown as agentflow's own words.
var ruleMessages = map[string]string{
	"empty":  "The message is empty.",
	"format": "The subject does not have the form type(scope): summary, with the task key where the repository asks for one.",
	"type":   "The subject's type is not one the repository allows.",
	"length": "The subject is longer than the repository allows.",
	"mood":   `The summary is not in the imperative mood ("add", not "added").`,
	"period": "The subject ends with a period.",
	"case":   "The summary does not start with a lowercase letter.",
}

// ruleIssues are the commit convention's findings on one drafted field.
func ruleIssues(found []commit.Issue, field string) []ai.Issue {
	var out []ai.Issue
	for _, i := range found {
		message, ok := ruleMessages[i.Code]
		if !ok {
			message = "The text does not follow the repository's commit convention."
		}
		out = append(out, ai.Issue{Code: i.Code, Field: field, Message: message})
	}
	return out
}
