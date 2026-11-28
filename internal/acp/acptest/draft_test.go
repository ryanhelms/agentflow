package acptest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/acp/acptest"
)

func (s *session) promptText(t *testing.T, id, text string) (acp.PromptResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return acp.Prompt(ctx, s.proc.Conn(), acp.PromptRequest{SessionID: id, Prompt: []acp.ContentBlock{{Type: "text", Text: text}}})
}

// chunks are the agent message chunks the host received, in order.
func (s *session) chunks() []string {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	var out []string
	for _, u := range s.host.updates {
		if u.Update.Kind == acp.UpdateAgentMessageChunk && u.Update.Content != nil {
			out = append(out, u.Update.Content.Text)
		}
	}
	return out
}

func draftPrompt(feature string) string {
	return acptest.DraftPromptPrefix + feature + "\nYou draft text for one operation.\n<<<data abc kind=diff>>>\n+hello\n<<<end abc>>>"
}

func TestDraftModeValidAnswers(t *testing.T) {
	for _, feature := range []string{"commit", "pr", "ticket", "ac", "reply"} {
		t.Run(feature, func(t *testing.T) {
			s := launch(t, acptest.SessionModeScripts["draft"](), nil)
			text := draftPrompt(feature)
			resp, err := s.promptText(t, s.start(t), text)
			if err != nil || resp.StopReason != acp.StopEndTurn {
				t.Fatalf("prompt: %+v %v", resp, err)
			}
			got := s.chunks()
			want := acptest.DraftAnswers[acptest.DraftValid][feature]
			if len(got) != 3 || strings.Join(got, "") != want {
				t.Fatalf("chunks %q, want three joining to %q", got, want)
			}
			for _, c := range got {
				if c == "" {
					t.Errorf("an empty chunk in %q", got)
				}
			}
			// --log records the prompt the fake received.
			entries, err := acptest.ReadLog(s.log)
			if err != nil {
				t.Fatal(err)
			}
			if p := acptest.Prompts(entries); len(p) != 1 || p[0] != text {
				t.Errorf("logged prompts %q", p)
			}
		})
	}
}

func TestDraftModeInvalidAnswers(t *testing.T) {
	for _, feature := range []string{"commit", "pr", "ticket", "ac", "reply"} {
		s := launch(t, acptest.SessionModeScripts["draft-invalid"](), nil)
		if _, err := s.promptText(t, s.start(t), draftPrompt(feature)); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(s.chunks(), "")
		if got != acptest.DraftAnswers[acptest.DraftInvalid][feature] {
			t.Errorf("%s: %q", feature, got)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Errorf("%s: not one JSON object: %v", feature, err)
		}
		if _, ok := m["operation"]; !ok {
			t.Errorf("%s: no unknown key", feature)
		}
		if !strings.Contains(got, "\u202e") || !strings.Contains(got, "ghp_") {
			t.Errorf("%s: no hidden character or token shape", feature)
		}
	}
}

func TestDraftModeUnknownFeature(t *testing.T) {
	for _, text := range []string{draftPrompt("nonsense"), "no first line here"} {
		s := launch(t, acptest.SessionModeScripts["draft"](), nil)
		if _, err := s.promptText(t, s.start(t), text); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(s.chunks(), ""); got != acptest.DraftNoJSON || strings.Contains(got, "{") {
			t.Errorf("%q: played %q", text, got)
		}
	}
}

func TestDraftModeHostile(t *testing.T) {
	s := launch(t, acptest.SessionModeScripts["draft-hostile"](), nil)
	resp, err := s.promptText(t, s.start(t), draftPrompt("commit"))
	if err != nil || resp.StopReason != acp.StopEndTurn {
		t.Fatalf("prompt: %+v %v", resp, err)
	}
	asks := s.asks(t)
	want := []string{acp.MethodReadTextFile, acp.MethodWriteTextFile, acp.MethodTerminalCreate,
		acp.MethodRequestPermission, acp.MethodRequestPermission, acp.MethodRequestPermission, acp.MethodRequestPermission}
	if len(asks) != len(want) {
		t.Fatalf("asks %+v", asks)
	}
	for i, w := range want {
		if asks[i].Asked != w || asks[i].Code == -1 {
			t.Errorf("ask %d: %+v, want an answered %s", i, asks[i], w)
		}
	}
	// Every request came before the tool call, which came before the answer.
	s.host.mu.Lock()
	updates := append([]acp.SessionNotification(nil), s.host.updates...)
	methods := len(s.host.methods)
	s.host.mu.Unlock()
	if methods != 7 {
		t.Errorf("host saw %d requests", methods)
	}
	if len(updates) < 5 || updates[0].Update.ToolCall == nil || updates[0].Update.ToolCall.Kind != acp.ToolRead {
		t.Fatalf("updates %+v", updates)
	}
	if got := strings.Join(s.chunks(), ""); got != acptest.DraftAnswers[acptest.DraftValid]["commit"] {
		t.Errorf("answer %q", got)
	}
}

func TestDraftModeGarbage(t *testing.T) {
	s := launch(t, acptest.SessionModeScripts["draft-garbage"](), nil)
	if _, err := s.promptText(t, s.start(t), draftPrompt("commit")); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(s.chunks(), "")
	if strings.Count(got, "{") != 2 || strings.HasPrefix(got, "{") {
		t.Errorf("garbage %q", got)
	}
}

func TestDraftModeHangCancels(t *testing.T) {
	s := launch(t, acptest.SessionModeScripts["draft-hang"](), nil)
	id := s.start(t)
	done := make(chan acp.PromptResponse, 1)
	go func() {
		resp, _ := s.promptText(t, id, draftPrompt("commit"))
		done <- resp
	}()
	select {
	case <-done:
		t.Fatal("draft-hang ended before session/cancel")
	case <-time.After(300 * time.Millisecond):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := acp.Cancel(ctx, s.proc.Conn(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.StopReason != acp.StopCancelled {
			t.Fatalf("stop %q", r.StopReason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("draft-hang did not end after session/cancel")
	}
}

func TestDraftChunksJoin(t *testing.T) {
	for _, s := range []string{"", "a", "ab", "héllo wörld ✓", acptest.DraftAnswers[acptest.DraftInvalid]["pr"]} {
		c := acptest.DraftChunks(s)
		if len(c) != 3 || strings.Join(c, "") != s {
			t.Errorf("%q: %q", s, c)
		}
	}
	if acptest.DraftFeature("agentflow-draft v1 feature=pr\nrest") != "pr" || acptest.DraftFeature("feature=pr") != "" {
		t.Error("DraftFeature")
	}
}
