package watch

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

func TestClassifyUser(t *testing.T) {
	u := UserTarget{Home: filepath.FromSlash("/h/.agentflow"), UserHome: filepath.FromSlash("/h")}
	record := "0123456789abcdef0123456789abcdef-op_0123456789abcdef0123456789abcdef.json"
	cases := []struct {
		path   string
		signal UserSignal
		client string
		ok     bool
	}{
		{"/h/.agentflow/config.yml", SignalPersonalConfig, "", true},
		{"/h/.agentflow/providers/jira.yaml", SignalPersonalConfig, "", true},
		{"/h/.agentflow/plugins/installed.json", SignalPersonalConfig, "", true},
		{"/h/.agentflow/plugins/installed.json.lock", 0, "", false},
		{"/h/.agentflow/plugins/.installed-123", 0, "", false},
		{"/h/.agentflow/plugins/deadletter.jsonl", 0, "", false},
		{"/h/.agentflow/plugins/ci-notify/data/plugin.log", 0, "", false},
		{"/h/.agentflow/plugins/ci-notify/installed.json", 0, "", false},
		{"/h/.agentflow/installed.json", 0, "", false},
		{"/h/.agentflow/approvals/" + record, SignalApproval, "", true},
		{"/h/.claude/settings.json", SignalClientSettings, "claude", true},
		{"/h/.codex/config.toml", SignalClientSettings, "codex", true},
		{"/h/.agentflow/approvals/events.jsonl", 0, "", false},
		{"/h/.agentflow/approvals/request-abc", 0, "", false},
		{"/h/.agentflow/confirm.key", 0, "", false},
		{"/h/.agentflow/config.yml.lock", 0, "", false},
		{"/h/.agentflow/providers/nested/x.yml", 0, "", false},
		{"/h/.claude.json", 0, "", false},
		{"/h/.claude/projects/x.json", 0, "", false},
		{"/elsewhere/config.yml", 0, "", false},
	}
	for _, c := range cases {
		s, client, ok := ClassifyUser(u, filepath.FromSlash(c.path))
		if s != c.signal || client != c.client || ok != c.ok {
			t.Errorf("ClassifyUser(%s) = %v %q %v", c.path, s, client, ok)
		}
	}
	dirs := map[string]bool{}
	for _, p := range UserPaths(u) {
		if p.Recursive || dirs[p.Dir] {
			t.Errorf("bad path %+v", p)
		}
		dirs[p.Dir] = true
	}
	if !dirs[filepath.FromSlash("/h/.agentflow/approvals")] || !dirs[filepath.FromSlash("/h/.agentflow/plugins")] || !dirs[filepath.FromSlash("/h/.claude")] {
		t.Errorf("missing directories: %v", dirs)
	}
}

func TestParseApprovalRecord(t *testing.T) {
	scope, id, ok := ParseApprovalRecord("0123456789abcdef0123456789abcdef-op_ffffffffffffffffffffffffffffffff.json")
	if !ok || scope != "0123456789abcdef0123456789abcdef" || id != "op_ffffffffffffffffffffffffffffffff" {
		t.Fatal(scope, id, ok)
	}
	for _, bad := range []string{"events.jsonl", "abc-op_1.json", "0123456789abcdef0123456789abcdef-op_ffffffffffffffffffffffffffffffff.json.tmp"} {
		if _, _, ok := ParseApprovalRecord(bad); ok {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestApprovalTransition(t *testing.T) {
	cases := []struct {
		prev, cur bus.ApprovalState
		want      bus.Type
	}{
		{"", bus.StatePending, bus.TypeApprovalRequested},
		{bus.StatePending, bus.StateApproved, bus.TypeApprovalResolved},
		{bus.StateApproved, bus.StateConsumed, bus.TypeApprovalResolved},
		{bus.StatePending, bus.StateCancelled, bus.TypeApprovalResolved},
		{bus.StatePending, bus.StateExpired, bus.TypeApprovalExpired},
		{bus.StatePending, bus.StatePending, ""},
		{"", "prepared", ""},
		{"prepared", bus.StateConsumed, ""},
		{"", bus.StateConsumed, ""},
		{bus.StateExpired, bus.StateExpired, ""},
		{bus.StateExpired, bus.StateCancelled, ""},
	}
	for _, c := range cases {
		got, ok := ApprovalTransition(c.prev, c.cur)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("ApprovalTransition(%q, %q) = %q, %v", c.prev, c.cur, got, ok)
		}
	}
	for _, c := range []struct {
		prev, cur bus.ApprovalState
		asked     bool
		want      []bus.Type
	}{
		{"", bus.StatePending, true, []bus.Type{bus.TypeApprovalRequested}},
		{"", bus.StateApproved, true, []bus.Type{bus.TypeApprovalRequested, bus.TypeApprovalResolved}},
		{"", bus.StateConsumed, true, []bus.Type{bus.TypeApprovalRequested, bus.TypeApprovalResolved}},
		{"", bus.StateExpired, true, []bus.Type{bus.TypeApprovalRequested, bus.TypeApprovalExpired}},
		{"", bus.StateConsumed, false, nil},
		{"", "prepared", false, nil},
		{bus.StatePending, bus.StateApproved, true, []bus.Type{bus.TypeApprovalResolved}},
		{bus.StateConsumed, bus.StateConsumed, true, nil},
	} {
		if got := ApprovalTransitions(c.prev, c.cur, c.asked); !slices.Equal(got, c.want) {
			t.Errorf("ApprovalTransitions(%q, %q, %v) = %v, want %v", c.prev, c.cur, c.asked, got, c.want)
		}
	}
	now := time.Now()
	if !ObservedExpiry(bus.StatePending, now.Add(-time.Second), now) || ObservedExpiry(bus.StateConsumed, now.Add(-time.Second), now) || ObservedExpiry(bus.StatePending, now.Add(time.Second), now) {
		t.Fatal("ObservedExpiry")
	}
}

func TestNextPoll(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	full := RateLimit{Limit: 5000, Remaining: 4000, Reset: now.Add(time.Hour)}
	cases := []struct {
		name     string
		active   bool
		watched  bool
		meta     GitHubMeta
		failures int
		want     time.Duration
	}{
		{"idle", false, true, GitHubMeta{RateLimit: full}, 0, GitHubIdle},
		{"active", true, true, GitHubMeta{RateLimit: full}, 0, GitHubActive},
		{"dormant", true, false, GitHubMeta{RateLimit: full}, 0, GitHubDormant},
		{"poll interval floor", true, true, GitHubMeta{RateLimit: full, PollInterval: 30 * time.Second}, 0, 30 * time.Second},
		{"retry after", true, true, GitHubMeta{RetryAfter: 90 * time.Second}, 3, 90 * time.Second},
		{"exhausted", true, true, GitHubMeta{RateLimit: RateLimit{Limit: 5000, Remaining: 0, Reset: now.Add(20 * time.Minute)}}, 0, 20 * time.Minute},
		{"reserve", true, true, GitHubMeta{RateLimit: RateLimit{Limit: 5000, Remaining: 10, Reset: now.Add(time.Hour)}}, 0, 6 * time.Minute},
		{"backoff", false, true, GitHubMeta{}, 3, 4 * time.Minute},
		{"backoff cap", false, true, GitHubMeta{}, 20, GitHubMaxBackoff},
	}
	for _, c := range cases {
		if got := NextPoll(c.active, c.watched, c.meta, c.failures, now); got != c.want {
			t.Errorf("%s: NextPoll = %v, want %v", c.name, got, c.want)
		}
	}
}
