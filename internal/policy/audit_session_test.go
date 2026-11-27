package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

func TestAuditSessionWritesOnlyTheNamedFields(t *testing.T) {
	dir := t.TempDir()
	g, err := NewGate(dir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "ai-host")
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC) }
	long := strings.Repeat("é", 200) // 400 bytes
	g.AuditSession(SessionAudit{Class: Local, Action: "ai.fs.write", Session: "ais_x", Kind: "fs_write", Subject: "src/a\x1b[31m\u202e.go", Decision: "deny", Handler: "never", Code: "agentflow_home", By: "policy"})
	g.AuditSession(SessionAudit{Class: Read, Action: "ai.fs.read", Session: "ais_x", Kind: "fs_read", Subject: long, Decision: "allow", Handler: "gate"})
	b, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &second)
	want := map[string]any{"time": "2026-11-27T09:00:00Z", "client": "ai-host", "class": "local", "action": "ai.fs.write", "session": "ais_x", "kind": "fs_write", "subject": "src/a[31m.go", "decision": "deny", "handler": "never", "code": "agentflow_home", "by": "policy"}
	if len(first) != len(want) {
		t.Errorf("fields = %v", first)
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %v, want %v", k, first[k], v)
		}
	}
	if _, ok := second["code"]; ok {
		t.Error("an empty code was written")
	}
	if s, _ := second["subject"].(string); len(s) > MaxAuditSubject || !strings.HasPrefix(long, s) || len(s) < MaxAuditSubject-1 {
		t.Errorf("subject not clamped at a character boundary: %d bytes", len(s))
	}
}
