package busaudit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type recorder struct{ actions []string }

func (r *recorder) Audit(_ policy.Class, action string, _ any, result string) {
	r.actions = append(r.actions, action+"="+result)
}

func TestTapRecordsOnlyActions(t *testing.T) {
	r := &recorder{}
	tap := Tap(r)
	ctx := context.Background()
	tap.Tap(ctx, bus.Event{Type: bus.TypeGitHeadChanged})
	tap.Tap(ctx, bus.Event{Type: bus.TypeConfigLayerChanged})
	tap.Tap(ctx, bus.Event{Type: "op.applied"})
	tap.Tap(ctx, bus.Event{Type: "approval.requested"})
	if got := strings.Join(r.actions, ","); got != "op.applied=published,approval.requested=published" {
		t.Fatalf("audited %q", got)
	}
}

func TestTapOnBusRecordsNothingInPhaseOne(t *testing.T) {
	r := &recorder{}
	b := bus.New(bus.Options{Taps: []bus.Tap{Tap(r)}})
	defer b.Close()
	payload, _ := json.Marshal(bus.GitHeadChanged{Ref: "refs/heads/main"})
	if _, err := b.Publish(context.Background(), bus.Event{Type: bus.TypeGitHeadChanged, Source: "test",
		Repo: "0123456789abcdef", Worktree: "main", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(r.actions) != 0 {
		t.Fatalf("audited %v", r.actions)
	}
}

func TestGateWritesAuditLine(t *testing.T) {
	dir := t.TempDir()
	g, err := policy.NewGate(dir, config.Autonomy{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	Tap(g).Tap(context.Background(), bus.Event{ID: "x", Type: "op.applied", Time: time.Now()})
	b, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(b, &line); err != nil {
		t.Fatal(err)
	}
	if line["action"] != "op.applied" || line["result"] != Result || line["class"] != string(policy.Local) {
		t.Fatalf("audit line %s", b)
	}
}
