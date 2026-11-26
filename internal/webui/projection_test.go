package webui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// TestStreamFrameJSON pins the SSE data shapes from the ADR.
func TestStreamFrameJSON(t *testing.T) {
	state := ProjectionState{Projection: "overview", Repo: "0123456789abcdef", Worktree: "main", State: json.RawMessage(`{"branch":"main"}`)}
	gone := ProjectionState{Projection: "overview", Repo: "0123456789abcdef", Worktree: "old-1a2b3c4d", State: json.RawMessage(`null`)}
	event := bus.Event{ID: "0190d6a2-0000-7000-8000-000000000000", Type: bus.TypeGitHeadChanged, Source: "watch.git",
		Repo: "0123456789abcdef", Worktree: "main", Seq: 42, Time: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC),
		SchemaVersion: 1, Payload: json.RawMessage(`{"ref":"refs/heads/main","oid":"abc","previous_ref":"","previous_oid":""}`)}
	cases := []struct {
		frame any
		want  string
	}{
		{SnapshotFrame{Cursor: "v1:0123456789abcdef=42", States: []ProjectionState{state}},
			`{"cursor":"v1:0123456789abcdef=42","states":[{"projection":"overview","repo":"0123456789abcdef","worktree":"main","state":{"branch":"main"}}]}`},
		{DeltaFrame{Event: event, States: []ProjectionState{gone}},
			`{"event":{"id":"0190d6a2-0000-7000-8000-000000000000","type":"git.head.changed","source":"watch.git","repo":"0123456789abcdef","worktree":"main","seq":42,"time":"2026-11-26T10:00:00Z","schema_version":1,"payload":{"ref":"refs/heads/main","oid":"abc","previous_ref":"","previous_oid":""}},"states":[{"projection":"overview","repo":"0123456789abcdef","worktree":"old-1a2b3c4d","state":null}]}`},
		{ResyncFrame{Reason: ResyncOverflow}, `{"reason":"overflow"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.frame)
		if err != nil || string(b) != c.want {
			t.Errorf("Marshal(%T) =\n%s\nwant\n%s", c.frame, b, c.want)
		}
	}
}
