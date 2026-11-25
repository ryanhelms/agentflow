package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	slackFixtureURL = "https://slack.example.invalid/slack-fixture-secret"
	hookFixtureURL  = "https://hook.example.invalid/hook-fixture-secret"
	fixtureBody     = "*Production deploy failed* <https://github.example.invalid/acme/widget/actions/runs/42|run>"
)

func TestNotificationFanOutReplaysDefiniteRejectionOnly(t *testing.T) {
	dir := notificationFixtureRepo(t, "notifications.yml")
	setOrigin := func(url string) {
		t.Helper()
		if err := exec.Command("git", "-C", dir, "config", "remote.origin.url", url).Run(); err != nil {
			t.Fatal(err)
		}
	}
	setOrigin("https://github.example.invalid/acme/widget.git")
	input := NotifyInput{Signal: "delivery", OperationID: "widget/42/default", Body: fixtureBody}
	var requests []string
	transport := notificationFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		switch req.URL.String() {
		case slackFixtureURL:
			checkNotificationPayload(t, req, "notification-slack.json")
			return fixtureResponse(http.StatusOK), nil
		case hookFixtureURL:
			checkNotificationPayload(t, req, "notification-webhook.json")
			if req.Header.Get("Authorization") != "Bearer authorization-fixture-secret" {
				t.Error("webhook authorization missing")
			}
			if len(requests) == 2 {
				return fixtureResponse(http.StatusTooManyRequests), nil
			}
			return fixtureResponse(http.StatusNoContent), nil
		default:
			t.Fatal("unexpected notification destination")
			return nil, nil
		}
	})
	first := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, first, NotificationDelivered, NotificationRejected)
	if !first.HasFailures() || !strings.Contains(first.Deliveries[1].Detail, "HTTP 429") {
		t.Fatalf("partial failure not reported: %+v", first)
	}
	setOrigin("git@github.example.invalid:acme/widget.git")
	second := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, second, NotificationAlreadyDelivered, NotificationDelivered)
	third := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, third, NotificationAlreadyDelivered, NotificationAlreadyDelivered)
	if want := []string{slackFixtureURL, hookFixtureURL, hookFixtureURL}; !slices.Equal(requests, want) {
		t.Fatalf("made %d requests with unexpected routing", len(requests))
	}
	for i, delivery := range third.Deliveries {
		if delivery.ReceiptID == "" || delivery.ReceiptID != first.Deliveries[i].ReceiptID {
			t.Fatalf("receipt did not survive replay: %+v", delivery)
		}
	}
	input.Body = "Changed body with the same ID"
	changed := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, changed, NotificationConflict, NotificationConflict)
	if len(requests) != 3 {
		t.Fatal("mismatched payload was delivered")
	}
}

func TestNotificationTimeoutRemainsUnconfirmed(t *testing.T) {
	dir := notificationFixtureRepo(t, "notifications.yml")
	input := NotifyInput{Signal: "delivery", OperationID: "run/43", Body: fixtureBody}
	var requests []string
	transport := notificationFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		if req.URL.String() == slackFixtureURL {
			return nil, fmt.Errorf("%s: %w", slackFixtureURL, context.DeadlineExceeded)
		}
		return fixtureResponse(http.StatusOK), nil
	})
	first := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, first, NotificationUnconfirmed, NotificationDelivered)
	second := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, second, NotificationUnconfirmed, NotificationAlreadyDelivered)
	if len(requests) != 2 || first.Deliveries[0].ReceiptID == "" || first.Deliveries[0].ReceiptID != second.Deliveries[0].ReceiptID {
		t.Fatal("timeout did not retain its receipt or replay repeated a write")
	}
	data, err := json.Marshal([]NotificationResult{first, second})
	if err != nil || strings.Contains(string(data), "fixture-secret") {
		t.Fatal("notification result exposes a credential")
	}
	entries, err := os.ReadDir(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "notification-receipts"))
	if err != nil || len(entries) != 3 {
		t.Fatalf("receipt directory: %v, %v", entries, err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "notification-receipts", entry.Name()))
		if err != nil || strings.Contains(string(data), "fixture-secret") || strings.Contains(string(data), fixtureBody) {
			t.Fatal("receipt contains notification credentials or the message")
		}
	}
}

func TestNotificationIndependentConfigurationFailure(t *testing.T) {
	dir := notificationFixtureRepo(t, "notifications.yml")
	t.Setenv("SLACK_WEBHOOK_URL", "http://invalid endpoint/fixture-secret")
	input := NotifyInput{Signal: "delivery", OperationID: "run/45", Body: fixtureBody}
	calls := 0
	transport := notificationFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != hookFixtureURL {
			t.Fatal("unexpected destination")
		}
		return fixtureResponse(http.StatusOK), nil
	})
	result := runNotificationFixture(t, dir, input, transport)
	assertNotificationStatuses(t, result, NotificationFailed, NotificationDelivered)
	if calls != 1 || strings.Contains(result.Deliveries[0].Detail, "fixture-secret") {
		t.Fatal("destination failure affected another destination or exposed a credential")
	}
	input.Signal = "unrouted"
	result = runNotificationFixture(t, dir, input, transport)
	if calls != 1 || len(result.Deliveries) != 0 || result.Reason == "" || result.HasFailures() {
		t.Fatalf("unrouted signal = %+v, calls = %d", result, calls)
	}
}

func TestNotificationLegacySlackAndUnconfiguredRoutes(t *testing.T) {
	dir := notificationFixtureRepo(t, "")
	input := NotifyInput{Signal: "delivery", OperationID: "run/44", Body: fixtureBody}
	calls := 0
	transport := notificationFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != slackFixtureURL {
			t.Fatal("legacy signal used an unexpected endpoint")
		}
		checkNotificationPayload(t, req, "notification-slack.json")
		return fixtureResponse(http.StatusOK), nil
	})
	input.DryRun = true
	assertNotificationStatuses(t, runNotificationFixture(t, dir, input, transport), NotificationPlanned)
	if _, err := os.Stat(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "notification-receipts")); !os.IsNotExist(err) {
		t.Fatalf("preview touched receipt store: %v", err)
	}
	input.DryRun = false
	assertNotificationStatuses(t, runNotificationFixture(t, dir, input, transport), NotificationDelivered)
	t.Setenv("SLACK_WEBHOOK_URL", "")
	assertNotificationStatuses(t, runNotificationFixture(t, dir, input, transport), NotificationSkipped)
	if calls != 1 {
		t.Fatalf("legacy Slack POST count = %d, want 1", calls)
	}
}

func notificationFixtureRepo(t *testing.T, fixture string) string {
	t.Helper()
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("SLACK_WEBHOOK_URL", slackFixtureURL)
	t.Setenv("WEBHOOK_URL", hookFixtureURL)
	t.Setenv("FIXTURE_WEBHOOK_AUTH", "Bearer authorization-fixture-secret")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if b, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, b)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("autonomy: {remote: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fixture != "" {
		data, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runNotificationFixture(t *testing.T, dir string, input NotifyInput, transport http.RoundTripper) NotificationResult {
	t.Helper()
	a, err := Open(t.Context(), dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	input.Automation = true
	result, pending, err := a.notify(t.Context(), input, "", transport)
	if pending != nil || err != nil {
		t.Fatalf("notify: %+v, %v", pending, err)
	}
	return result
}

func assertNotificationStatuses(t *testing.T, result NotificationResult, want ...NotificationStatus) {
	t.Helper()
	var statuses []NotificationStatus
	for _, delivery := range result.Deliveries {
		statuses = append(statuses, delivery.Status)
	}
	if !slices.Equal(statuses, want) {
		t.Fatalf("delivery outcomes = %+v, want %v", result.Deliveries, want)
	}
}

func checkNotificationPayload(t *testing.T, req *http.Request, fixture string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected map[string]string
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(req.Body).Decode(&actual); err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("request did not match %s: %+v, %v", fixture, actual, err)
	}
	if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		t.Fatal("expected a JSON POST")
	}
}

type notificationFixtureTransport func(*http.Request) (*http.Response, error)

func (fn notificationFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	return fn(req)
}

func fixtureResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("response-fixture-secret"))}
}
