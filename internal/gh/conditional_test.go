package gh

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/watch"
)

func conditionalClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("test-token")
	c.API = srv.URL
	return c
}

// The first request has no If-None-Match; sending the returned ETag back
// yields a 304 that leaves the output alone and is not an error.
func TestGetConditionalHTTPETagAndNotModified(t *testing.T) {
	reset := time.Date(2026, 11, 26, 13, 0, 0, 0, time.UTC)
	var seen []string
	c := conditionalClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("If-None-Match"))
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.Header().Set("X-Poll-Interval", "60")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`[{"number":7}]`))
	}))

	var first []struct{ Number int }
	meta, err := c.GetConditional(t.Context(), "/repos/acme/repo/pulls?state=open", "", &first)
	if err != nil || meta.NotModified || meta.ETag != `"v1"` || len(first) != 1 || first[0].Number != 7 {
		t.Fatalf("first = %+v, %+v, %v", first, meta, err)
	}
	if meta.RateLimit.Limit != 5000 || meta.RateLimit.Remaining != 4321 || !meta.RateLimit.Reset.Equal(reset) ||
		meta.PollInterval != time.Minute || meta.RetryAfter != 0 {
		t.Fatalf("meta = %+v", meta)
	}

	second := []struct{ Number int }{{Number: 99}}
	meta, err = c.GetConditional(t.Context(), "/repos/acme/repo/pulls?state=open", meta.ETag, &second)
	if err != nil || !meta.NotModified || meta.ETag != `"v1"` || len(second) != 1 || second[0].Number != 99 {
		t.Fatalf("second = %+v, %+v, %v", second, meta, err)
	}
	if len(seen) != 2 || seen[0] != "" || seen[1] != `"v1"` {
		t.Fatalf("If-None-Match values = %q", seen)
	}
}

// A 304 without an ETag header keeps the ETag the caller sent.
func TestGetConditionalNotModifiedKeepsSentETag(t *testing.T) {
	c := conditionalClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	meta, err := c.GetConditional(t.Context(), "/x", `W/"abc"`, nil)
	if err != nil || !meta.NotModified || meta.ETag != `W/"abc"` {
		t.Fatalf("meta = %+v, %v", meta, err)
	}
}

// A rate-limited response returns the error and the headers that explain
// it, so a poller waits as long as GitHub asked.
func TestGetConditionalRateLimited(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		status int
		header map[string]string
		retry  time.Duration
	}{
		{"primary", http.StatusForbidden, map[string]string{"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "0"}, 0},
		{"secondary", http.StatusForbidden, map[string]string{"Retry-After": "75"}, 75 * time.Second},
		{"too many", http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := conditionalClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			}))
			meta, err := c.GetConditional(t.Context(), "/x", "", nil)
			if !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrNotModified) || meta.NotModified {
				t.Fatalf("err = %v, meta = %+v", err, meta)
			}
			if meta.RetryAfter != tc.retry || !meta.RateLimit.Reset.Equal(reset) {
				t.Fatalf("meta = %+v", meta)
			}
		})
	}
}

func TestGetConditionalErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusForbidden, ErrPermission},
		{http.StatusNotFound, nil},
	} {
		c := conditionalClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		_, err := c.GetConditional(t.Context(), "/x", "", nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != tc.status || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("status %d: err = %v", tc.status, err)
		}
	}
}

func TestRetryAfterForms(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"120", 2 * time.Minute},
		{"-5", 0},
		{"soon", 0},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
	} {
		if got := retryAfter(tc.in, now); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		status int
		header map[string]string
		want   watch.GitHubFailure
	}{
		{http.StatusUnauthorized, nil, watch.FailureAuth},
		{http.StatusForbidden, nil, watch.FailureAuth},
		{http.StatusForbidden, map[string]string{"Retry-After": "10"}, watch.FailureRateLimited},
		{http.StatusTooManyRequests, nil, watch.FailureRateLimited},
		{http.StatusNotFound, nil, watch.FailureNotFound},
		{http.StatusBadGateway, nil, watch.FailureOther},
	} {
		c := conditionalClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range tc.header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tc.status)
		}))
		_, err := c.GetConditional(t.Context(), "/x", "", nil)
		if got := Classify(err); got != tc.want {
			t.Errorf("status %d %v: Classify = %v, want %v", tc.status, tc.header, got, tc.want)
		}
	}
	if Classify(ErrCLIUnavailable) != watch.FailureAuth || Classify(errors.New("dial tcp: refused")) != watch.FailureOther || Classify(nil) != watch.FailureOther {
		t.Error("Classify of sentinel and plain errors")
	}
}
