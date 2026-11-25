package native

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type testHost func(context.Context, Call) (json.RawMessage, error)

func (f testHost) Read(ctx context.Context, call Call) (json.RawMessage, error) {
	return f(ctx, call)
}

func TestReadFallbackRequiresConnectorFailure(t *testing.T) {
	network := errors.New("provider network failure")
	for _, tc := range []struct {
		name     string
		err      error
		payload  string
		want     string
		fallback int
	}{
		{"mcp takes precedence", nil, `"mcp"`, "mcp", 0},
		{"missing capability", ErrUnavailable, "", "cli", 1},
		{"sign-in", ErrSignIn, "", "cli", 1},
		{"connection", ErrConnection, "", "cli", 1},
		{"combined denial", errors.Join(ErrUnavailable, ErrDenied), "", "", 0},
		{"denied", ErrDenied, "", "", 0},
		{"network", network, "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx := WithHost(t.Context(), testHost(func(_ context.Context, _ Call) (json.RawMessage, error) {
				return json.RawMessage(tc.payload), tc.err
			}))
			got, err := Read(ctx, Call{Provider: "jira", Server: "atlassian", Tool: "getJiraIssue"}, func(v json.RawMessage) (string, error) {
				var result string
				err := Decode(v, &result)
				return result, err
			}, func(reason error) (string, error) {
				if !errors.Is(reason, tc.err) {
					t.Fatalf("fallback reason: %v", reason)
				}
				calls++
				return "cli", nil
			})
			wantErr := tc.err
			if tc.fallback != 0 {
				wantErr = nil
			}
			if got != tc.want || !errors.Is(err, wantErr) || calls != tc.fallback {
				t.Fatalf("read = %q, %v; fallback count %d", got, err, calls)
			}
		})
	}
}

func TestDecodeRawProviderContent(t *testing.T) {
	var raw struct {
		Content struct {
			ID string `json:"id"`
		} `json:"content"`
	}
	if err := Decode(json.RawMessage(`{"content":{"id":"123"}}`), &raw); err != nil || raw.Content.ID != "123" {
		t.Fatalf("decoded raw provider object %+v: %v", raw, err)
	}
}
