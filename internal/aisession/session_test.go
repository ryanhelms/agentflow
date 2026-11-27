package aisession_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
)

func TestSessionRunsOneTurn(t *testing.T) {
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		{Update: aisessiontest.Message("Hel")},
		{Update: aisessiontest.Message("lo")},
		{Call: &aisessiontest.Call{Method: acp.MethodReadTextFile, Params: aisessiontest.Params(map[string]any{"sessionId": "$SESSION", "path": "$CWD/README.md"})}},
		{Call: &aisessiontest.Call{Method: acp.MethodReadTextFile, Params: aisessiontest.Params(map[string]any{"sessionId": "$SESSION", "path": "/etc/hosts"})}},
	}}, aisessiontest.Settings{AgentStart: true})
	s := r.run(t, person(context.Background()), "say hello")
	if s.State != aisession.StateEnded || s.Code != aisession.CodeEndTurn {
		t.Fatalf("session = %s %s", s.State, s.Code)
	}
	log := aisessiontest.ReadLog(t, r.agent.Log)
	reads := aisessiontest.Calls(log, acp.MethodReadTextFile)
	if len(reads) != 2 {
		t.Fatalf("reads = %+v", reads)
	}
	if reads[0].ErrorCode != 0 || string(reads[0].Result) != `{"content":"hello\n"}` {
		t.Errorf("README read = %+v", reads[0])
	}
	if reads[1].ErrorCode != acp.CodeDenied || reads[1].ErrorData != aisession.VerdictOutsideWorktree {
		t.Errorf("/etc/hosts read = %+v", reads[1])
	}
	items, _, err := r.m.Items(context.Background(), s.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, it := range items {
		if it.Kind == aisession.ItemMessage {
			text += it.Text
		}
	}
	if text != "Hello" {
		t.Errorf("message chunks join to %q", text)
	}
	_ = filepath.Join
	_ = time.Second
}
