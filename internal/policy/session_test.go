package policy

import (
	"errors"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// Only NewSessionGate makes an AI session's gate: NewGate refuses the
// reserved prefix, as it refuses a plugin's.
func TestNewGateRefusesSessionClient(t *testing.T) {
	for _, client := range []string{"ai-session:ais_0123", "ai-session:"} {
		if _, err := NewGate(t.TempDir(), config.Autonomy{}, client); !errors.Is(err, ErrReservedSessionClient) {
			t.Errorf("NewGate(%q): %v, want ErrReservedSessionClient", client, err)
		}
	}
	for _, id := range []string{"", "a:b", "a b", "a/b", `a\b`, "a\nb"} {
		if _, err := NewSessionGate(t.TempDir(), config.Autonomy{}, id); err == nil {
			t.Errorf("NewSessionGate(%q) accepted the ID", id)
		}
	}
	g, err := NewSessionGate(t.TempDir(), config.Autonomy{}, "ais_0123")
	if err != nil {
		t.Fatal(err)
	}
	if g.Client != "ai-session:ais_0123" || !IsSessionClient(g.Client) {
		t.Fatalf("client %q", g.Client)
	}
}

// A session's gate refuses every operation that is not a read, under every
// autonomy level, auto included, and cannot approve or cancel an approval.
func TestSessionGateIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	for _, level := range []config.Level{config.Auto, config.Confirm, config.Suggest} {
		g, err := NewSessionGate(dir, config.Autonomy{Local: level, Remote: level}, "ais_0123")
		if err != nil {
			t.Fatal(err)
		}
		if d, err := g.Check(Read, "context", nil, "", false); err != nil || d.Outcome != Proceed {
			t.Fatalf("%s: a read: %+v %v", level, d, err)
		}
		for _, class := range []Class{Local, Remote} {
			_, err := g.Check(class, "commit", map[string]any{"a": 1}, "", false)
			if !errors.Is(err, ErrSessionReadOnly) || ErrorCode(err) != "session_read_only" {
				t.Errorf("%s %s: %v, want session_read_only", level, class, err)
			}
		}
	}
	person, err := NewGate(dir, config.Autonomy{Local: config.Confirm}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	d, err := person.Check(Local, "commit", map[string]any{"a": 1}, "", false)
	if err != nil || d.Approval == nil {
		t.Fatalf("person: %+v %v", d, err)
	}
	session, err := NewSessionGate(dir, config.Autonomy{Local: config.Confirm}, "ais_0123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Approve(d.Approval.OperationID, d.Token); !errors.Is(err, ErrSessionReadOnly) {
		t.Fatalf("a session approved: %v", err)
	}
	if err := session.Cancel(d.Approval.OperationID, d.Token); !errors.Is(err, ErrSessionReadOnly) {
		t.Fatalf("a session cancelled: %v", err)
	}
}

// Allowing what a session asks for is a person's decision: the never-list
// finds agentflow ai respond --allow however the binary is invoked.
func TestForbiddenAlwaysBlocksSessionAllow(t *testing.T) {
	for _, cmd := range []string{
		"agentflow ai respond ais_1 ask_1 --allow",
		"agentflow ai respond --allow ais_1 ask_1",
		"agentflow ai respond ais_1 ask_1 -allow",
		"agentflow ai respond ais_1 ask_1 --allow=true",
		"agentflow ai respond ais_1 ask_1 --allow=1",
		"agentflow ai respond ais_1 ask_1 --allow=bogus",
		"agentflow ai respond ais_1 ask_1 --json --allow",
		".agentflow/bin/agentflow ai respond ais_1 ask_1 --allow",
		`"$HOME/.cache/agentflow/current/agentflow" ai respond ais_1 ask_1 --allow`,
		"/home/me/.claude/plugins/cache/nscale/agentflow/0.60.0/bin/launch ai respond ais_1 ask_1 --allow",
		"go run ./cmd/agentflow ai respond ais_1 ask_1 --allow",
		"agentflow --native-client codex ai respond ais_1 ask_1 --allow",
		`sh -c "agentflow ai respond ais_1 ask_1 --allow"`,
		"script -q /dev/null agentflow ai respond ais_1 ask_1 --allow",
		"echo y | agentflow ai respond ais_1 ask_1 --allow",
		"env -u CLAUDECODE agentflow ai respond ais_1 ask_1 --allow",
		"AGENTFLOW_ACP_SESSION= agentflow ai respond ais_1 ask_1 --allow",
	} {
		if reason := ForbiddenAlways(cmd); !strings.Contains(reason, "AI session asks for is a human decision") {
			t.Errorf("%q: %q, want it blocked", cmd, reason)
		}
	}
	for _, cmd := range []string{
		"agentflow ai respond ais_1 ask_1 --reject",
		"agentflow ai respond ais_1 ask_1 --allow=false",
		"agentflow ai respond ais_1 ask_1 --reject -- --allow",
		"agentflow ai status --json",
		"agentflow ai cancel ais_1 --yes",
		`echo "agentflow ai respond ais_1 ask_1 --allow"`,
		`grep -rn "ai respond --allow" docs`,
		`git commit -m "docs: say agentflow ai respond --allow needs a terminal"`,
		"codex ai respond --allow",
	} {
		if reason := ForbiddenAlways(cmd); reason != "" {
			t.Errorf("%q: blocked (%s), want it allowed", cmd, reason)
		}
	}
	// The plugin entry keeps its own message.
	if reason := ForbiddenAlways("agentflow plugin add ./x"); !strings.Contains(reason, "plugins is a human decision") {
		t.Fatalf("plugin entry: %q", reason)
	}
}
