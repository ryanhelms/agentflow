package nativemap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/notifications"
)

var fixtureDestination = SlackDestination{
	Name: "release", Instance: "work", WorkspaceID: "T0123456789", ChannelID: "C0123456789",
}

const (
	operation = "repo/rollout/24"
	slackLink = "https://fixture.slack.com/archives/C0123456789/p1790771415332219"
	slackAck  = `{"message_link":"` + slackLink + `","message_context":{
		"channel_id":"C0123456789","message_ts":"1790771415.332219"
	}}`
)

func TestSlackNotificationPolicyAndReplay(t *testing.T) {
	ctx := t.Context()
	store := testReceiptStore(t)
	n := prepareNotification(t, fixtureDestination, "Rollout")
	policyDenied := errors.New("confirmation required")
	var authorized bool
	attempts := 0
	authorize := func(_ context.Context, intent SlackIntent, call Call) error {
		if intent.Destination != fixtureDestination || intent.OperationID != operation || intent.Message != "Rollout\n\n[Runbook](<https://example.test/runbook>)" {
			t.Fatalf("intent changed: %+v", intent)
		}
		assertCall(t, call, Slack, DeliverToSlack, "slack_send_message", "work", `{
			"channel_id":"C0123456789","message":"Rollout\n\n[Runbook](<https://example.test/runbook>)"
		}`)
		// Arguments returned for schema validation cannot mutate the write.
		call.Arguments()[0] = '['
		if !authorized {
			return policyDenied
		}
		return nil
	}
	send := func(_ context.Context, call Call) (json.RawMessage, error) {
		attempts++
		if !json.Valid(call.Arguments()) {
			t.Fatal("policy callback mutated the payload")
		}
		return json.RawMessage(slackAck), nil
	}
	if _, err := n.Deliver(ctx, store, authorize, send); !errors.Is(err, policyDenied) || attempts != 0 {
		t.Fatalf("blocked write = %v, attempts = %d", err, attempts)
	}
	authorized = true
	first, err := n.Deliver(ctx, store, authorize, send)
	if err != nil || first.Status != provider.Applied || first.URL != slackLink || first.ID == "" || attempts != 1 {
		t.Fatalf("first = %+v, %v, attempts = %d", first, err, attempts)
	}
	second, err := n.Deliver(ctx, store, authorize, send)
	if err != nil || second.Status != provider.AlreadyApplied || second.ID != first.ID || attempts != 1 {
		t.Fatalf("replay = %+v, %v, attempts = %d", second, err, attempts)
	}
	changed := fixtureDestination
	changed.ChannelID = "C0123456788"
	for _, modified := range []SlackNotification{
		prepareNotification(t, fixtureDestination, "Different message"),
		prepareNotification(t, changed, "Rollout"),
	} {
		if _, err := modified.Deliver(ctx, store, allowSlack, send); !errors.Is(err, provider.ErrConflict) || attempts != 1 {
			t.Fatalf("changed delivery = %v, attempts = %d", err, attempts)
		}
	}
}

func TestSlackNotificationUncertainOutcomesStayClaimed(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		err  error
	}{
		{"timeout", "", context.DeadlineExceeded},
		{"wrong channel", strings.ReplaceAll(slackAck, "C0123456789", "C0123456788"), nil},
		{"wrong permalink destination", strings.Replace(slackAck, slackLink, "https://example.test/message", 1), nil},
		{"missing receipt", `{"ok":true}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testReceiptStore(t)
			n := prepareNotification(t, fixtureDestination, "Rollout")
			attempts := 0
			send := func(context.Context, Call) (json.RawMessage, error) {
				attempts++
				return json.RawMessage(tc.raw), tc.err
			}
			for range 2 {
				got, err := n.Deliver(t.Context(), store, allowSlack, send)
				if !errors.Is(err, notifications.ErrUnconfirmed) || got.Status != "" || got.ID == "" || attempts != 1 {
					t.Fatalf("unconfirmed = %+v, %v, attempts = %d", got, err, attempts)
				}
			}
		})
	}
}

func TestSlackNotificationConcurrentClaim(t *testing.T) {
	n := prepareNotification(t, fixtureDestination, "Rollout")
	store := testReceiptStore(t)
	called := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := n.Deliver(t.Context(), store, allowSlack, func(context.Context, Call) (json.RawMessage, error) {
			close(called)
			<-resume
			return json.RawMessage(slackAck), nil
		})
		done <- err
	}()
	select {
	case <-called:
	case err := <-done:
		t.Fatalf("first delivery never dispatched: %v", err)
	}
	_, err := n.Deliver(t.Context(), store, allowSlack, func(context.Context, Call) (json.RawMessage, error) {
		t.Error("concurrent delivery dispatched a duplicate")
		return nil, nil
	})
	close(resume)
	if firstErr := <-done; firstErr != nil || !errors.Is(err, notifications.ErrUnconfirmed) {
		t.Fatalf("first = %v, racing call = %v", firstErr, err)
	}
}

func TestSlackNotificationPreservesExistingReceiptNamespace(t *testing.T) {
	store := testReceiptStore(t)
	key := notifications.ReceiptKey{OperationID: operation, Destination: fixtureDestination.Name}
	if _, err := store.Claim(t.Context(), key, sha256.Sum256([]byte("prior webhook fingerprint"))); err != nil {
		t.Fatal(err)
	}
	n := prepareNotification(t, fixtureDestination, "Rollout")
	_, err := n.Deliver(t.Context(), store, allowSlack, func(context.Context, Call) (json.RawMessage, error) {
		t.Error("switched backend dispatched an existing operation")
		return nil, nil
	})
	if !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("switched backend = %v", err)
	}
}

func TestSlackNotificationPreconditions(t *testing.T) {
	n := provider.Notification{Destination: fixtureDestination.Name, Body: "fixture"}
	for _, opts := range []provider.WriteOptions{
		{OperationID: operation, ExpectedRevision: "v1"},
		{OperationID: operation, CheckTask: func(provider.Task) error { return nil }},
	} {
		if _, err := PrepareSlackNotification(fixtureDestination, n, opts); !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("unsupported guard = %v", err)
		}
	}
	if _, err := PrepareSlackNotification(fixtureDestination, n, provider.WriteOptions{}); err == nil {
		t.Fatal("accepted an empty operation id")
	}
	for _, bad := range []string{"#release", "U0123456789", "https://fixture.slack.com"} {
		dest := fixtureDestination
		dest.ChannelID = bad
		if _, err := PrepareSlackNotification(dest, n, provider.WriteOptions{OperationID: operation}); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("invalid destination = %v", err)
		}
	}
	bad := n
	bad.Links = []provider.Link{{URL: "https://name@fixture.test/"}}
	if _, err := PrepareSlackNotification(fixtureDestination, bad, provider.WriteOptions{OperationID: operation}); err == nil {
		t.Fatal("accepted a link with user info")
	}
	bad = n
	bad.Body = strings.Repeat("a", slackMessageLimit+1)
	if _, err := PrepareSlackNotification(fixtureDestination, bad, provider.WriteOptions{OperationID: operation}); err == nil {
		t.Fatal("accepted an overlong message")
	}
	prepared := prepareNotification(t, fixtureDestination, "Rollout")
	send := func(context.Context, Call) (json.RawMessage, error) {
		t.Error("dispatched without required dependencies")
		return nil, nil
	}
	if _, err := prepared.Deliver(t.Context(), nil, allowSlack, send); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("missing store = %v", err)
	}
	if _, err := prepared.Deliver(t.Context(), testReceiptStore(t), nil, send); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("missing policy = %v", err)
	}
}

func testReceiptStore(t *testing.T) notifications.FileStore {
	t.Helper()
	return notifications.FileStore{Dir: filepath.Join(t.TempDir(), "receipts")}
}

func prepareNotification(t *testing.T, dest SlackDestination, title string) SlackNotification {
	t.Helper()
	n, err := PrepareSlackNotification(dest, provider.Notification{
		Destination: dest.Name, Title: title,
		Links: []provider.Link{{Title: "Runbook", URL: "https://example.test/runbook"}},
	}, provider.WriteOptions{OperationID: operation})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func allowSlack(context.Context, SlackIntent, Call) error { return nil }
