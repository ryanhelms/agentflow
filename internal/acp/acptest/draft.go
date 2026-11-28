package acptest

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/acp"
)

// Phase 8 draft steps and modes (ADR "Phase 8 fake ACP agent").

// DraftStep plays a draft answer: the fake reads the feature from the
// prompt's first line ("agentflow-draft v1 feature=<id>") and sends
// DraftAnswers[Answers][feature] as three agent_message_chunk updates. An
// unknown feature, or a prompt without that line, plays DraftNoJSON.
type DraftStep struct {
	Answers string `json:"answers"` // valid, invalid
}

// Answer sets for DraftStep.Answers.
const (
	DraftValid   = "valid"
	DraftInvalid = "invalid"
)

// DraftNoJSON is what a draft step plays for a feature it does not know:
// a sentence with no JSON in it.
const DraftNoJSON = "I cannot help with that feature, sorry."

// DraftPromptPrefix starts the first line of every draft prompt.
const DraftPromptPrefix = "agentflow-draft v1 feature="

// fakeToken is a GitHub-token-shaped placeholder (not a credential), built
// at run time so no token-shaped literal sits in the source.
var fakeToken = "gh" + "p_" + strings.Repeat("A", 36)

// hidden are characters a person cannot see: a right-to-left override and
// a zero-width space.
const hidden = "\u202e\u200b"

// DraftAnswers maps an answer set and a feature to the agent message text
// the fake sends. The valid answers pass each feature's checks; the
// invalid ones fail them and also carry an unknown key ("operation"),
// hidden characters and a token shape in a field.
var DraftAnswers = map[string]map[string]string{
	DraftValid: {
		"commit": `{"message": "feat: add notes\n\nAdd notes.txt with a greeting."}`,
		"pr":     "Here is the draft.\n\n```json\n" + `{"title": "feat: add notes", "body": "Adds notes.txt."}` + "\n```\n",
		"ticket": `{"summary": "Add a greeting to the demo", "description": "Add a greeting line to the demo output."}`,
		"ac":     `{"checks": [{"criterion": 1, "status": "met", "evidence": "notes.txt is added"}]}`,
		"reply":  `{"replies": [{"thread": 1, "body": "Fixed in the latest commit."}]}`,
	},
	DraftInvalid: {
		"commit": `{"message": "Added the notes.` + hidden + ` ` + fakeToken + `", "operation": "push"}`,
		"pr":     `{"title": "Added notes` + hidden + `", "body": "Token ` + fakeToken + `", "operation": "push"}`,
		"ticket": `{"summary": "x` + hidden + `", "sections": [{"section": 9, "body": "Token ` + fakeToken + `"}], "operation": "push"}`,
		"ac":     `{"checks": [{"criterion": 99, "status": "met", "evidence": "done` + hidden + ` ` + fakeToken + `"}], "operation": "push"}`,
		"reply":  `{"replies": [{"thread": 99, "body": "Fixed` + hidden + ` ` + fakeToken + `"}], "operation": "push"}`,
	},
}

// DraftFeature is the feature named on prompt's first line, or "".
func DraftFeature(prompt string) string {
	line, _, _ := strings.Cut(prompt, "\n")
	f, ok := strings.CutPrefix(strings.TrimSpace(line), DraftPromptPrefix)
	if !ok {
		return ""
	}
	return strings.TrimSpace(f)
}

// DraftAnswer is the text a draft step with answers plays for prompt.
func DraftAnswer(answers, prompt string) string {
	if a, ok := DraftAnswers[answers][DraftFeature(prompt)]; ok {
		return a
	}
	return DraftNoJSON
}

// DraftChunks splits s into three pieces of about equal size at rune
// boundaries; joined they are s.
func DraftChunks(s string) []string {
	cut := func(i int) int {
		for i < len(s) && !utf8.RuneStart(s[i]) {
			i++
		}
		return i
	}
	a, b := cut(len(s)/3), cut(2*len(s)/3)
	return []string{s[:a], s[a:b], s[b:]}
}

func init() {
	SessionModeScripts["draft"] = Draft
	SessionModeScripts["draft-invalid"] = DraftInvalidAnswer
	SessionModeScripts["draft-hostile"] = DraftHostile
	SessionModeScripts["draft-garbage"] = DraftGarbage
	SessionModeScripts["draft-hang"] = DraftHang
}

// Draft is --mode=draft: a valid answer for the feature, end_turn.
func Draft() *SessionScript {
	return &SessionScript{Steps: []Step{{Draft: &DraftStep{Answers: DraftValid}}, {Stop: acp.StopEndTurn}}}
}

// DraftInvalidAnswer is --mode=draft-invalid: an answer that fails the
// feature's checks, with an unknown key, hidden characters and a token
// shape, end_turn.
func DraftInvalidAnswer() *SessionScript {
	return &SessionScript{Steps: []Step{{Draft: &DraftStep{Answers: DraftInvalid}}, {Stop: acp.StopEndTurn}}}
}

// DraftHostile is --mode=draft-hostile: a read outside the folder, a
// write, a terminal and permission requests for read, edit, execute and
// fetch, each waiting for its answer; then a read tool call it ran itself
// (no request); then a valid answer, end_turn.
func DraftHostile() *SessionScript {
	steps := []Step{
		askRead("/etc/hosts"),
		askWrite("{cwd}/x.txt", "pwned\n"),
		askTerminal("echo", "hi"),
		askPermission("perm-read", "Read a file", acp.ToolRead, "{cwd}/notes.txt"),
		askPermission("perm-edit", "Edit a file", acp.ToolEdit, "{cwd}/notes.txt"),
		askPermission("perm-execute", "Run a command", acp.ToolExecute, "{cwd}"),
		askPermission("perm-fetch", "Fetch a page", acp.ToolFetch, "{cwd}"),
		{ToolUnasked: JSON(map[string]any{
			"toolCallId": "own-read", "title": "Read notes.txt", "kind": acp.ToolRead,
			"locations": []map[string]any{{"path": "{cwd}/notes.txt"}},
		})},
		{Draft: &DraftStep{Answers: DraftValid}},
		{Stop: acp.StopEndTurn},
	}
	return &SessionScript{Steps: steps}
}

// DraftGarbage is --mode=draft-garbage: prose with two JSON objects,
// end_turn.
func DraftGarbage() *SessionScript {
	return &SessionScript{Steps: []Step{
		chunk(`Here you go: {"message": "feat: a"} and also {"message": "feat: b"}.`),
		{Stop: acp.StopEndTurn},
	}}
}

// DraftHang is --mode=draft-hang: nothing until session/cancel.
func DraftHang() *SessionScript {
	return &SessionScript{Steps: []Step{{Sleep: "24h"}, {Stop: acp.StopEndTurn}}}
}

// Prompts returns the text of every session/prompt the log recorded (Run
// logs each message the client sends with its params), so a test can
// assert what was sent and what was not.
func Prompts(entries []LogEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Method != acp.MethodSessionPrompt {
			continue
		}
		var req struct {
			Prompt []acp.ContentBlock `json:"prompt"`
		}
		if json.Unmarshal(e.Params, &req) != nil {
			continue
		}
		var b strings.Builder
		for _, c := range req.Prompt {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		out = append(out, b.String())
	}
	return out
}
