package ops_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestConfigApplyPublishesConfirmApplyAndRefusal(t *testing.T) {
	f := newFixture(t)
	t.Chdir(f.dir)
	input := func(v any) string {
		path := filepath.Join(t.TempDir(), "in.json")
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// Inspection is a read: no events.
	em, inv := recording()
	env := &commandtest.Env{App: f.app}
	if err := dispatch(t, env, inv, "config", "autonomy", "--input", input(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	var view struct {
		UserRevision string `json:"user_revision"`
	}
	if err := json.Unmarshal(env.Out.Bytes(), &view); err != nil || view.UserRevision == "" || len(em.Events()) != 0 {
		t.Fatalf("autonomy: %v %s, events %v", err, env.Out.String(), em.Types())
	}
	req := map[string]any{"user_revision": view.UserRevision, "patch": map[string]any{"remote": "suggest"}}
	env = &commandtest.Env{App: f.app}
	if err := dispatch(t, env, inv, "config", "preview_personal", "--input", input(req)); err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Token string `json:"confirm_token"`
	}
	if err := json.Unmarshal(env.Out.Bytes(), &pending); err != nil || pending.Token == "" {
		t.Fatalf("preview_personal: %s", env.Out.String())
	}
	req["confirm_token"] = pending.Token
	if err := dispatch(t, &commandtest.Env{App: f.app}, inv, "config", "save_personal", "--input", input(req)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(em.Types(), []bus.Type{bus.TypeOpRequested, bus.TypeOpApplied}) {
		t.Fatalf("save events %v", em.Types())
	}
	if p, _ := commandtest.Payload(em.Events()[0]); p.Operation != "config.apply" || !p.Resumed {
		t.Errorf("payload %+v", p)
	}
	// A reused token fails.
	em2, inv2 := recording()
	if err := dispatch(t, &commandtest.Env{App: f.app}, inv2, "config", "save_personal", "--input", input(req)); err == nil {
		t.Fatal("a reused token saved again")
	}
	if !slices.Equal(em2.Types(), []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", em2.Types())
	}
}
