package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	defer func() { _ = req.Body.Close() }()
	return fn(req)
}

func response(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("response-secret"))}
}

type storedReceipt struct {
	fingerprint [sha256.Size]byte
	state       ReceiptState
}

type testStore struct {
	mu        sync.Mutex
	records   map[ReceiptKey]storedReceipt
	finishErr error
}

func (s *testStore) Claim(_ context.Context, key ReceiptKey, fingerprint [sha256.Size]byte) (ReceiptState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.records[key]; ok {
		if record.fingerprint != fingerprint {
			return "", errors.Join(provider.ErrConflict, errors.New("store-secret"))
		}
		if record.state != Retryable {
			return record.state, nil
		}
	}
	if s.records == nil {
		s.records = make(map[ReceiptKey]storedReceipt)
	}
	s.records[key] = storedReceipt{fingerprint, Unconfirmed}
	return Claimed, nil
}

func (s *testStore) Finish(ctx context.Context, key ReceiptKey, state ReceiptState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finishErr != nil {
		return s.finishErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record := s.records[key]
	record.state = state
	s.records[key] = record
	return nil
}

func TestFanOutAndReplay(t *testing.T) {
	const slackURL = "https://slack.example.invalid/hooks/slack-secret"
	const webhookURL = "https://webhook.example.invalid/notify?token=url-secret"
	configs := map[string]DestinationConfig{
		"team": {Kind: Slack, URL: slackURL},
		"audit": {
			Kind: Webhook, URL: webhookURL, RejectedStatuses: []int{429},
			Headers: http.Header{"Authorization": {"Bearer header-secret"}},
		},
	}
	store := &testStore{}
	var requests []string
	link := provider.Link{Title: "run | test", URL: "https://ci.example.invalid/1?a=1&b=2"}
	note := provider.Notification{Title: "Deployment", Body: "*Failed*", Links: []provider.Link{link}}
	options := provider.WriteOptions{OperationID: "deploy-7"}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		if req.Method != "POST" || req.Header.Get("Content-Type") != "application/json" || req.GetBody != nil {
			t.Errorf("expected one non-rewindable JSON POST, got %s %s", req.Method, req.Header.Get("Content-Type"))
		}
		switch req.URL.String() {
		case slackURL:
			var data map[string]string
			want := map[string]string{"text": "Deployment\n*Failed*\n<https://ci.example.invalid/1?a=1&amp;b=2|run   test>"}
			if err := json.NewDecoder(req.Body).Decode(&data); err != nil || !reflect.DeepEqual(data, want) {
				t.Errorf("Slack payload = %#v, error = %v", data, err)
			}
			return response(http.StatusOK), nil
		case webhookURL:
			if req.Header.Get("Authorization") != "Bearer header-secret" {
				t.Error("generic authorization header missing")
			}
			var data struct {
				Destination string          `json:"destination"`
				Title       string          `json:"title"`
				Body        string          `json:"body"`
				Links       []provider.Link `json:"links"`
			}
			if err := json.NewDecoder(req.Body).Decode(&data); err != nil || data.Destination != "audit" ||
				data.Title != note.Title || data.Body != note.Body || !reflect.DeepEqual(data.Links, note.Links) {
				t.Errorf("generic payload = %#v, error = %v", data, err)
			}
			if len(requests) == 2 {
				return response(http.StatusTooManyRequests), nil
			}
			return response(http.StatusNoContent), nil
		default:
			t.Error("unexpected endpoint")
			return response(http.StatusBadRequest), nil
		}
	})
	router := mustRouter(t, configs, store, transport)
	first := router.FanOut(t.Context(), []string{"team", "missing", "audit", "team", "audit"}, note, options)
	if len(first) != 3 ||
		first[0].Destination != "team" || first[0].Receipt.Status != provider.Applied || first[0].Err != nil ||
		!errors.Is(first[1].Err, provider.ErrNotFound) ||
		first[2].Destination != "audit" || !errors.Is(first[2].Err, ErrRejected) {
		t.Fatalf("first fan-out = %+v", first)
	}

	// A new router uses the same receipt store.
	router = mustRouter(t, configs, store, transport)
	second := router.FanOut(t.Context(), []string{"team", "audit"}, note, options)
	if len(second) != 2 ||
		second[0].Receipt.Status != provider.AlreadyApplied || second[0].Err != nil ||
		second[1].Receipt.Status != provider.Applied || second[1].Err != nil {
		t.Fatalf("replayed fan-out = %+v", second)
	}
	if first[0].Receipt.ID == "" || first[2].Receipt.ID == "" || first[0].Receipt.ID == first[2].Receipt.ID ||
		first[0].Receipt.ID != second[0].Receipt.ID || first[2].Receipt.ID != second[1].Receipt.ID {
		t.Fatal("receipt IDs must be distinct per destination and stable on replay")
	}
	changed := note
	changed.Body = "new content"
	changed.Destination = "team"
	if _, err := router.Deliver(t.Context(), changed, options); !errors.Is(err, provider.ErrConflict) || strings.Contains(err.Error(), "store-secret") {
		t.Fatalf("changed-replay error = %v", err)
	}
	if want := []string{slackURL, webhookURL, webhookURL}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %v", requests)
	}
}

func TestAmbiguousDeliveryNeverReposts(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{name: "transport error"},
		{name: "redirect", status: http.StatusFound},
		{name: "temporary redirect", status: http.StatusTemporaryRedirect},
		{name: "timeout response", status: http.StatusRequestTimeout},
		{name: "server error", status: http.StatusServiceUnavailable},
		{name: "generic client error without rejection guarantee", status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := mustRouter(t, map[string]DestinationConfig{"d": {
				Kind: Webhook, URL: "https://webhook.example.invalid/path-secret",
				Headers: http.Header{"Authorization": {"header-secret"}},
			}}, &testStore{}, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				if tc.status == 0 {
					return nil, errors.New("transport-secret")
				}
				resp := response(tc.status)
				resp.Header = http.Header{"Location": {"https://redirect.example.invalid/redirect-secret"}}
				return resp, nil
			}))
			note := provider.Notification{Destination: "d"}
			options := provider.WriteOptions{OperationID: "op"}
			for range 2 {
				receipt, err := router.Deliver(t.Context(), note, options)
				if !errors.Is(err, ErrUnconfirmed) || receipt.ID == "" || receipt.Status != "" ||
					strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "Authorization") {
					t.Fatalf("receipt = %+v, error = %v", receipt, err)
				}
			}
			if calls != 1 {
				t.Fatalf("POST attempts = %d, want 1", calls)
			}
		})
	}
}

func TestConcurrentReplayDoesNotRepost(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	router := mustRouter(t, map[string]DestinationConfig{"team": {Kind: Slack, URL: "https://slack.example.invalid"}},
		&testStore{}, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			select {
			case <-started:
				t.Error("unexpected additional POST")
				return response(http.StatusBadRequest), nil
			default:
				close(started)
			}
			<-release
			return response(http.StatusOK), nil
		}))
	note := provider.Notification{Destination: "team"}
	opts := provider.WriteOptions{OperationID: "op"}
	done := make(chan error, 1)
	go func() {
		_, err := router.Deliver(t.Context(), note, opts)
		done <- err
	}()
	<-started
	_, replayErr := router.Deliver(t.Context(), note, opts)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(replayErr, ErrUnconfirmed) {
		t.Fatalf("concurrent replay error = %v", replayErr)
	}
	receipt, err := router.Deliver(t.Context(), note, opts)
	if err != nil || receipt.Status != provider.AlreadyApplied {
		t.Fatalf("receipt = %+v, error = %v", receipt, err)
	}
}

func TestStoreFinalizationFailureHoldsClaim(t *testing.T) {
	calls := 0
	store := &testStore{finishErr: errors.New("store-secret")}
	router := mustRouter(t, map[string]DestinationConfig{"team": {Kind: Slack, URL: "https://slack.example.invalid"}},
		store, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusOK), nil
		}))
	note := provider.Notification{Destination: "team"}
	opts := provider.WriteOptions{OperationID: "op"}
	receipt, err := router.Deliver(t.Context(), note, opts)
	if !errors.Is(err, ErrReceiptStore) || receipt.Status != provider.Applied || strings.Contains(err.Error(), "store-secret") {
		t.Fatalf("receipt = %+v, error = %v", receipt, err)
	}
	if _, err := router.Deliver(t.Context(), note, opts); !errors.Is(err, ErrUnconfirmed) || calls != 1 {
		t.Fatalf("replay error = %v, POST attempts = %d", err, calls)
	}
}

func TestSlackRejectionAndCancellationAfterAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	router := mustRouter(t, map[string]DestinationConfig{"team": {Kind: Slack, URL: "https://slack.example.invalid"}},
		&testStore{}, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return response(http.StatusTooManyRequests), nil
			}
			cancel()
			return response(http.StatusOK), nil
		}))
	note := provider.Notification{Destination: "team"}
	opts := provider.WriteOptions{OperationID: "op"}
	if _, err := router.Deliver(ctx, note, opts); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Slack rejection error = %v", err)
	}
	if receipt, err := router.Deliver(ctx, note, opts); err != nil || receipt.Status != provider.Applied {
		t.Fatalf("successful retry receipt = %+v, error = %v", receipt, err)
	}
	if receipt, err := router.Deliver(t.Context(), note, opts); err != nil || receipt.Status != provider.AlreadyApplied || calls != 2 {
		t.Fatalf("confirmed replay receipt = %+v, error = %v, POST attempts = %d", receipt, err, calls)
	}
}

func TestConfigurationAndWriteOptions(t *testing.T) {
	for _, cfg := range []DestinationConfig{
		{Kind: Slack, URL: "://path-secret"},
		{Kind: Slack, URL: "https://user:password-secret@example.invalid"},
		{Kind: Slack, URL: "https://example.invalid#fragment-secret"},
		{Kind: Slack, URL: "https://example.invalid", Headers: http.Header{"Authorization": {"header-secret\r\nInjection"}}},
		{Kind: Slack, URL: "https://example.invalid", RejectedStatuses: []int{408}},
		{Kind: Webhook, URL: "https://example.invalid", RejectedStatuses: []int{500}},
	} {
		if _, err := NewRouter(map[string]DestinationConfig{"d": cfg}, nil, nil); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("config error = %v", err)
		}
	}
	calls := 0
	router := mustRouter(t, map[string]DestinationConfig{"d": {Kind: Webhook, URL: "https://example.invalid"}},
		nil, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusAccepted), nil
		}))
	note := provider.Notification{Destination: "d"}
	if _, err := router.Deliver(t.Context(), note, provider.WriteOptions{ExpectedRevision: "rev"}); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("revision error = %v", err)
	}
	if _, err := router.Deliver(t.Context(), note, provider.WriteOptions{OperationID: "op"}); !errors.Is(err, ErrReceiptStore) || calls != 0 {
		t.Errorf("missing store error = %v, POST attempts = %d", err, calls)
	}
	if receipt, err := router.Deliver(t.Context(), note, provider.WriteOptions{}); err != nil || receipt.Status != provider.Applied || calls != 1 {
		t.Errorf("one-shot receipt = %+v, error = %v, POST attempts = %d", receipt, err, calls)
	}
}

func mustRouter(t *testing.T, config map[string]DestinationConfig, store ReceiptStore, transport http.RoundTripper) *Router {
	t.Helper()
	router, err := NewRouter(config, store, transport)
	if err != nil {
		t.Fatal(err)
	}
	return router
}
