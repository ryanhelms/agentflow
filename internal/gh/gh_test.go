package gh

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDirectClientKeepsExplicitTokenAndAPI(t *testing.T) {
	t.Setenv("GH_TOKEN", "ignored-env-token")
	client := New("direct-token")
	client.API = "https://fixture.example"
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "fixture.example" || r.Header.Get("Authorization") != "Bearer direct-token" ||
			r.Header.Get("Accept") != "application/vnd.github+json" ||
			r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("unexpected request host or headers")
		}
		w := httptest.NewRecorder()
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"login":"direct-user"}`)
			return w.Result(), nil
		}
		w.Header().Set("X-GitHub-Request-Id", "request-id")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, strings.Repeat("x", 500))
		return w.Result(), nil
	})
	login, err := client.Viewer(t.Context())
	if err != nil || login != "direct-user" {
		t.Fatalf("viewer = %q, %v", login, err)
	}
	err = client.Do(t.Context(), http.MethodGet, "/repos/acme/missing", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || len(apiErr.Body) != 400 ||
		apiErr.Header.Get("X-GitHub-Request-Id") != "request-id" {
		t.Fatalf("unexpected HTTP error: %v", err)
	}
}
