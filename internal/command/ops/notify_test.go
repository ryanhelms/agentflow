package ops_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

const notifyConfig = "notifications: {provider: webhook, settings: {destination: audit}}\n"

// webhook answers the notification webhook and counts what it receives.
func webhook(t *testing.T) *int {
	t.Helper()
	t.Setenv("WEBHOOK_URL", "https://hooks.example.invalid/audit")
	posts := new(int)
	old := http.DefaultTransport
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && r.URL.Host == "hooks.example.invalid" {
			*posts++
			return httptest.NewRecorder().Result(), nil
		}
		return httptest.NewRecorder().Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return posts
}

func TestNotifyPublishesConfirmApplyAndBlocked(t *testing.T) {
	r := newOpsRepo(t, notifyConfig, confirmEverything)
	posts := webhook(t)
	quietStdout(t)

	// A preview publishes nothing and delivers nothing.
	em, env, err := r.cli(t, false, "notify", "delivery", "--operation-id", "run/1", "--body", "done", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	wantOpTypes(t, em)
	if *posts != 0 || !strings.Contains(env.Out.String(), `"dry_run": true`) {
		t.Fatalf("preview: posts %d, output %q", *posts, env.Out.String())
	}

	// --yes confirms and delivers in one command: asked, then resumed and applied.
	em, env, err = r.cli(t, false, "notify", "delivery", "--operation-id", "run/1", "--body", "secret body", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantOpConfirmFlow(t, em, "notify", "notify", "remote")
	wantWorktreeScope(t, evs, false)
	if p, _ := commandtest.Payload(evs[0]); p.Subject != "notify delivery" {
		t.Errorf("subject %q", p.Subject)
	}
	noTokensInEvents(t, em)
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), "secret body") {
			t.Errorf("payload carries the notification body: %s", ev.Payload)
		}
	}
	var result app.NotificationResult
	if json.Unmarshal(env.Out.Bytes(), &result) != nil || len(result.Deliveries) != 1 || result.Deliveries[0].Status != app.NotificationDelivered || *posts != 1 {
		t.Fatalf("delivery: posts %d, output %q", *posts, env.Out.String())
	}

	// A "no" at the prompt leaves it asking.
	em, _, err = r.cli(t, false, "notify", "delivery", "--operation-id", "run/2", "--body", "later")
	if err == nil {
		t.Fatal("declined prompt succeeded")
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)

	// A forged token is refused by the gate.
	em, out, isErr := r.tool(t, "notify", map[string]any{"signal": "delivery", "operation_id": "run/3", "body": "x", "confirm_token": "cf_forged"})
	if !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
}

func TestNotifyBlockedByAutonomyPublishesSuggested(t *testing.T) {
	r := newOpsRepo(t, notifyConfig, "autonomy: {remote: suggest}\n")
	webhook(t)
	quietStdout(t)
	em, env, err := r.cli(t, true, "notify", "delivery", "--operation-id", "run/1", "--body", "failed", "--yes")
	if err == nil || err.Error() != "notifications are blocked by remote autonomy" {
		t.Fatalf("blocked notification: %v", err)
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpSuggested)
	// The command still prints the planned deliveries with the reason.
	var result app.NotificationResult
	if json.Unmarshal(env.Out.Bytes(), &result) != nil || result.Reason == "" || len(result.Deliveries) != 1 || result.Deliveries[0].Status != app.NotificationPlanned {
		t.Fatalf("blocked output %q", env.Out.String())
	}
}

func TestNotifyUsageErrorsMatchTheLegacyCommand(t *testing.T) {
	r := newOpsRepo(t, notifyConfig, "")
	for _, args := range [][]string{{"notify"}, {"notify", "--operation-id", "x"}, {"notify", "a", "b"}} {
		if _, _, err := r.cli(t, false, args...); err == nil || !strings.HasPrefix(err.Error(), "usage: agentflow notify SIGNAL --operation-id ID") {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
	if _, _, err := r.cli(t, false, "notify", "delivery", "--links", "{}"); err == nil || err.Error() != "--links must be a JSON array of links" {
		t.Errorf("bad links: %v", err)
	}
}
