package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider/native"
)

const (
	slackScopeFixture = "{workspace_id: T0123456789, channel_id: C0123456789}"
	slackActorFixture = "U0123456789"
	slackPermalink    = "https://fixture.slack.com/archives/C0123456789/p1790771415332219"
	slackSendACK      = `{"structuredContent":{"message_link":"` + slackPermalink + `","message_context":{"channel_id":"C0123456789","message_ts":"1790771415.332219"}}}`
)

func TestNotifyNativeSlackConfirmationAndReplay(t *testing.T) {
	dir := notificationFixtureRepo(t, "")
	writeSlackNotificationConfig(t, dir, "notifications: {settings: {destination: team, native: "+slackScopeFixture+"}}")
	a := openSlackNotificationFixture(t, dir, "codex")
	a.Gate.Autonomy.Remote = config.Confirm
	fixture := newSlackNotifyHost(t, a.Home)
	input := NotifyInput{Signal: "delivery", OperationID: "deploy/42", Title: "Rollout", Body: "Deployment finished", DryRun: true}
	ctx := native.WithWriteHost(t.Context(), fixture)

	result, pending, err := a.Notify(ctx, input, "")
	if pending != nil || err != nil || fixture.prepares != 0 || result.Deliveries[0].WorkspaceID != "T0123456789" {
		t.Fatalf("dry run contacted Slack or omitted the configured scope: %+v %+v %v", result, pending, err)
	}
	assertNotificationStatuses(t, result, NotificationPlanned)
	input.DryRun = false
	_, pending, err = a.Notify(ctx, input, "")
	if err != nil || pending == nil || fixture.writes != 0 {
		t.Fatalf("preview: pending = %+v, writes = %d, error = %v", pending, fixture.writes, err)
	}
	summary, err := json.Marshal(pending.Summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{slackActorFixture, `"workspace_id":"T0123456789"`, `"channel_id":"C0123456789"`, `"message":"Rollout\n\nDeployment finished"`, `"connection":"fixture-session"`} {
		if !strings.Contains(string(summary), want) {
			t.Errorf("summary missing %s: %s", want, summary)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Home, "notification-receipts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview touched receipts: %v", err)
	}

	fixture.connection = "changed-session"
	if _, _, err := a.Notify(ctx, input, pending.ConfirmToken); !errors.Is(err, policy.ErrToken) || fixture.writes != 0 {
		t.Fatalf("changed binding was approved: %d writes, %v", fixture.writes, err)
	}
	fixture.connection = "fixture-session"
	fixture.profile = `{"user":{"id":"U1123456789","team_id":"T0123456789"}}`
	if _, _, err := a.Notify(ctx, input, pending.ConfirmToken); !errors.Is(err, policy.ErrToken) || fixture.writes != 0 {
		t.Fatalf("changed actor was approved: %d writes, %v", fixture.writes, err)
	}
	fixture.profile = `{"user":{"id":"U0123456789","team_id":"T0123456789"}}`
	changed := input
	changed.Body = "Different payload"
	if _, _, err := a.Notify(ctx, changed, pending.ConfirmToken); !errors.Is(err, policy.ErrToken) || fixture.writes != 0 {
		t.Fatalf("changed message was approved: %d writes, %v", fixture.writes, err)
	}
	result, _, err = a.Notify(ctx, input, pending.ConfirmToken)
	if err != nil || fixture.prepares != 5 || fixture.writes != 1 {
		t.Fatalf("confirmed send: %+v, %d prepares, %d writes, %v", result, fixture.prepares, fixture.writes, err)
	}
	assertNotificationStatuses(t, result, NotificationDelivered)
	receipt := result.Deliveries[0].ReceiptID
	if receipt == "" || result.Deliveries[0].URL != slackPermalink {
		t.Fatalf("missing validated receipt: %+v", result)
	}

	reopened := openSlackNotificationFixture(t, dir, "codex")
	result, _, err = reopened.Notify(ctx, input, "")
	if err != nil || fixture.writes != 1 || result.Deliveries[0].ReceiptID != receipt {
		t.Fatalf("replay: %+v %d writes %v", result, fixture.writes, err)
	}
	assertNotificationStatuses(t, result, NotificationAlreadyDelivered)
	result, _, err = reopened.Notify(ctx, changed, "")
	if err != nil || fixture.writes != 1 {
		t.Fatalf("conflict replay: %+v %d writes %v", result, fixture.writes, err)
	}
	assertNotificationStatuses(t, result, NotificationConflict)
	writeSlackNotificationConfig(t, dir, "notifications: {settings: {destination: team, credential_ref: SLACK_WEBHOOK_URL}}")
	legacy := openSlackNotificationFixture(t, dir, "cli")
	input.Automation = true
	result, _, err = legacy.notify(t.Context(), input, "", notificationFixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("webhook re-routed an existing native delivery")
		return nil, nil
	}))
	if err != nil || result.Deliveries[0].ReceiptID != receipt {
		t.Fatalf("backend switching did not preserve the receipt: %+v, %v", result, err)
	}
	assertNotificationStatuses(t, result, NotificationConflict)
}

func TestNotifyNativeSlackRequiresVerifiedScope(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*slackNotifyHost)
		wantWrites int
		wantStatus NotificationStatus
	}{
		{"markdown profile and scoped channel list", func(h *slackNotifyHost) {
			h.profile = `{"content":[{"type":"text","text":"{\"result\":\"- **User ID:** ` + slackActorFixture + `\"}"}]}`
			h.channels = []string{
				`{"channels":[{"id":"C1123456789"}],"pagination_info":{"next_cursor":"second"}}`,
				`{"result":"### team\n- **ID:** ` + "`C0123456789`" + `"}`,
			}
		}, 1, NotificationDelivered},
		{"wrong workspace", func(h *slackNotifyHost) {
			h.profile = `{"user":{"id":"U0123456789","team_id":"T1123456789"}}`
		}, 0, NotificationFailed},
		{"wrong actor", func(h *slackNotifyHost) {
			h.profile = `{"user":{"id":"U1123456789","team_id":"T0123456789"}}`
		}, 0, NotificationFailed},
		{"target in wrong workspace", func(h *slackNotifyHost) {
			h.profile = `{"user_id":"U0123456789"}`
			h.channels = []string{`{"team_id":"T1123456789","channels":[{"id":"C0123456789"}]}`}
		}, 0, NotificationFailed},
		{"target absent", func(h *slackNotifyHost) {
			h.profile = `{"user_id":"U0123456789"}`
			h.channels = []string{`{"channels":[]}`}
		}, 0, NotificationFailed},
		{"untrusted origin", func(h *slackNotifyHost) { h.origin = "https://mcp.slack.com.example.invalid" }, 0, NotificationFailed},
		{"native unavailable", func(h *slackNotifyHost) { h.prepareErr = native.ErrUnavailable }, 0, NotificationFailed},
		{"sign in required", func(h *slackNotifyHost) { h.prepareErr = native.ErrSignIn }, 0, NotificationFailed},
		{"profile access denied", func(h *slackNotifyHost) { h.profileErr = native.ErrDenied }, 0, NotificationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := notificationFixtureRepo(t, "")
			writeSlackNotificationConfig(t, dir, `notifications:
  provider: router
  settings:
    destinations:
      team:
        provider: slack
        native: {server: work-slack, workspace_id: T0123456789, channel_id: C0123456789, actor_id: U0123456789}
    routes: {delivery: [team]}
`)
			a := openSlackNotificationFixture(t, dir, "claude")
			fixture := newSlackNotifyHost(t, a.Home)
			test.configure(fixture)
			ctx := native.WithWriteHost(t.Context(), fixture)
			input := NotifyInput{Signal: "delivery", OperationID: "deploy/42", Body: "Release ready"}
			result, pending, err := a.notify(ctx, input, "", notificationFixtureTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("native delivery used a webhook")
				return nil, nil
			}))
			if pending != nil || err != nil || fixture.writes != test.wantWrites || fixture.call.Server != "work-slack" {
				t.Fatalf("notify: %+v %+v, %d writes, %v", result, pending, fixture.writes, err)
			}
			assertNotificationStatuses(t, result, test.wantStatus)
		})
	}
}

func TestNotifyNativeSlackUnconfirmedStopsReplay(t *testing.T) {
	for _, test := range []struct {
		name, ack string
		writeErr  error
	}{
		{"unknown send outcome", "", errors.New("fixture-secret")},
		{"ambiguous capability error", "", errors.Join(native.ErrUnavailable, native.ErrWriteUncertain)},
		{"MCP error", `{"isError":true,"structuredContent":{"fixture-secret":1}}`, nil},
		{"wrong channel", strings.ReplaceAll(slackSendACK, "C0123456789", "C1123456789"), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := notificationFixtureRepo(t, "")
			writeSlackNotificationConfig(t, dir, "notifications: {settings: {native: "+slackScopeFixture+"}}")
			a := openSlackNotificationFixture(t, dir, "codex")
			fixture := newSlackNotifyHost(t, a.Home)
			fixture.ack, fixture.writeErr = test.ack, test.writeErr
			ctx := native.WithWriteHost(t.Context(), fixture)
			input := NotifyInput{Signal: "delivery", OperationID: "deploy/42", Body: "Release ready"}
			for range 2 {
				result, pending, err := a.Notify(ctx, input, "")
				if pending != nil || err != nil || result.Deliveries[0].ReceiptID == "" || strings.Contains(result.Deliveries[0].Detail, "fixture-secret") {
					t.Fatalf("notify: %+v, %+v, %v", result, pending, err)
				}
				assertNotificationStatuses(t, result, NotificationUnconfirmed)
			}
			if fixture.writes != 1 {
				t.Fatalf("ambiguous operation sent %d times", fixture.writes)
			}
		})
	}
}

func TestNotifyNativeSlackRetriesOnlyKnownPredispatchFailure(t *testing.T) {
	dir := notificationFixtureRepo(t, "")
	writeSlackNotificationConfig(t, dir, "notifications: {settings: {native: "+slackScopeFixture+"}}")
	a := openSlackNotificationFixture(t, dir, "codex")
	fixture := newSlackNotifyHost(t, a.Home)
	fixture.writeErr = native.ErrUnavailable
	ctx := native.WithWriteHost(t.Context(), fixture)
	input := NotifyInput{Signal: "delivery", OperationID: "deploy/42", Body: "Release ready"}
	result, pending, err := a.Notify(ctx, input, "")
	if pending != nil || err != nil || fixture.writes != 1 || result.Deliveries[0].ReceiptID == "" {
		t.Fatalf("predispatch failure: %+v, %+v, %v", result, pending, err)
	}
	assertNotificationStatuses(t, result, NotificationFailed)
	firstID := result.Deliveries[0].ReceiptID
	fixture.writeErr = nil
	result, pending, err = a.Notify(ctx, input, "")
	if pending != nil || err != nil || fixture.writes != 2 || result.Deliveries[0].ReceiptID != firstID {
		t.Fatalf("retry: %+v, %+v, %v", result, pending, err)
	}
	assertNotificationStatuses(t, result, NotificationDelivered)
}

func TestSlackIncomingWebhookAutomationOnly(t *testing.T) {
	dir := notificationFixtureRepo(t, "")
	writeSlackNotificationConfig(t, dir, "notifications: {settings: {credential_ref: SLACK_WEBHOOK_URL}}")
	input := NotifyInput{Signal: "delivery", OperationID: "deploy/42", Body: "Release ready"}
	writes := 0
	transport := notificationFixtureTransport(func(*http.Request) (*http.Response, error) {
		writes++
		return fixtureResponse(http.StatusOK), nil
	})
	t.Setenv("GITHUB_ACTIONS", "")
	cli := openSlackNotificationFixture(t, dir, "cli")
	result, _, err := cli.notify(t.Context(), input, "", transport)
	if err != nil || writes != 0 {
		t.Fatalf("interactive webhook = %+v, %d writes, %v", result, writes, err)
	}
	assertNotificationStatuses(t, result, NotificationFailed)
	t.Setenv("GITHUB_ACTIONS", "true")
	claude := openSlackNotificationFixture(t, dir, "claude")
	result, _, err = claude.notify(t.Context(), input, "", transport)
	if err != nil || writes != 0 {
		t.Fatalf("interactive MCP webhook = %+v, %d writes, %v", result, writes, err)
	}
	assertNotificationStatuses(t, result, NotificationFailed)
	result, _, err = cli.notify(t.Context(), input, "", transport)
	if err != nil || writes != 1 {
		t.Fatalf("GitHub Actions webhook = %+v, %d writes, %v", result, writes, err)
	}
	assertNotificationStatuses(t, result, NotificationDelivered)
}

func writeSlackNotificationConfig(t *testing.T, dir, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".devflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openSlackNotificationFixture(t *testing.T, dir, client string) *App {
	t.Helper()
	a, err := Open(t.Context(), dir, client)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type slackNotifyHost struct {
	t                                      *testing.T
	home, origin, connection, profile, ack string
	prepareErr, profileErr, writeErr       error
	call                                   native.Call
	channels                               []string
	prepares, writes                       int
}

func newSlackNotifyHost(t *testing.T, home string) *slackNotifyHost {
	return &slackNotifyHost{t: t, home: home, origin: "https://mcp.slack.com", connection: "fixture-session", ack: slackSendACK,
		profile: `{"user":{"id":"U0123456789","team_id":"T0123456789"}}`}
}

func (h *slackNotifyHost) Prepare(_ context.Context, call native.Call) (native.WriteBinding, error) {
	h.t.Helper()
	h.call, h.prepares = call, h.prepares+1
	if call.Provider != "slack" || call.Tool != "slack_send_message" || call.Arguments["channel_id"] != "C0123456789" || len(call.Arguments) != 2 {
		h.t.Fatalf("unexpected slack send: %+v", call)
	}
	return native.WriteBinding{Provider: call.Provider, Server: call.Server, Tool: call.Tool, Origin: h.origin, Connection: h.connection}, h.prepareErr
}

func (h *slackNotifyHost) ReadBound(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	h.t.Helper()
	if binding.Connection != h.connection || call.Server != h.call.Server || call.Provider != h.call.Provider {
		h.t.Fatalf("identity read was unbound: %+v, %+v", binding, call)
	}
	switch call.Tool {
	case "slack_read_user_profile":
		if len(call.Arguments) != 0 {
			h.t.Fatalf("profile did not target calling actor: %+v", call)
		}
		return json.RawMessage(h.profile), h.profileErr
	case "slack_list_user_channels":
		if call.Arguments["team_id"] != "T0123456789" || call.Arguments["exclude_archived"] != true {
			h.t.Fatalf("unexpected channel scope read: %+v", call)
		}
		if h.channels == nil {
			return json.RawMessage(`{"team_id":"T0123456789","channels":[{"id":"C0123456789"}]}`), nil
		}
		if len(h.channels) == 0 {
			h.t.Fatalf("unexpected extra channel scope read: %+v", call)
		}
		if len(h.channels) == 1 && strings.Contains(h.channels[0], "###") && call.Arguments["cursor"] != "second" {
			h.t.Fatalf("missing cursor for the second page: %+v", call)
		}
		raw := h.channels[0]
		h.channels = h.channels[1:]
		return json.RawMessage(raw), nil
	default:
		return nil, fmt.Errorf("unexpected tool %s", call.Tool)
	}
}

func (h *slackNotifyHost) Write(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	h.t.Helper()
	h.writes++
	if binding.Connection != h.connection || !reflect.DeepEqual(call, h.call) {
		h.t.Fatalf("different call dispatched: %+v, %+v", binding, call)
	}
	claims, err := filepath.Glob(filepath.Join(h.home, "notification-receipts", "*.claim"))
	if err != nil || len(claims) != h.writes {
		h.t.Fatalf("write before durable claim: %v, %v", claims, err)
	}
	return json.RawMessage(h.ack), h.writeErr
}
