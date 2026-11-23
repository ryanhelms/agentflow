package native

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

type writeLeaseFixture struct {
	writes int
}

func (*writeLeaseFixture) Origin() string   { return "https://mcp.atlassian.com" }
func (*writeLeaseFixture) Check(Call) error { return nil }
func (*writeLeaseFixture) Close()           {}

func (s *writeLeaseFixture) Call(_ context.Context, call Call) (json.RawMessage, error) {
	if call.Tool == "createJiraIssueRemoteIssueLink" {
		s.writes++
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func TestExpiredWriteLeaseCannotDispatch(t *testing.T) {
	for _, removed := range []bool{false, true} {
		name := "ended"
		if removed {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			session := &writeLeaseFixture{}
			writer := NewSessionWriter("fixture", func(context.Context, Call) (WriteSession, error) {
				return session, nil
			})
			t.Cleanup(writer.Close)
			write := Call{Provider: "jira", Server: "atlassian", Tool: "createJiraIssueRemoteIssueLink"}
			read := Call{Provider: "jira", Server: "atlassian", Tool: "getJiraIssue"}
			binding, err := writer.Prepare(t.Context(), write)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.ReadBound(t.Context(), binding, read); err != nil {
				t.Fatal(err)
			}
			key, err := writeKey(write)
			if err != nil {
				t.Fatal(err)
			}
			writer.mu.Lock()
			lease := writer.leases[key]
			if removed {
				writer.drop(key, lease)
			} else {
				lease.cancel()
			}
			writer.mu.Unlock()

			_, err = writer.Write(t.Context(), binding, write)
			if !errors.Is(err, ErrDenied) || !errors.Is(err, ErrWriteNotDispatched) || session.writes != 0 {
				t.Fatalf("expired approval = %v, writes = %d", err, session.writes)
			}
			fresh, err := writer.Prepare(t.Context(), write)
			if err != nil || fresh == binding {
				t.Fatalf("fresh approval = %+v, err = %v", fresh, err)
			}
			if _, err := writer.Write(t.Context(), fresh, write); err != nil || session.writes != 1 {
				t.Fatalf("fresh approval = %v, writes = %d", err, session.writes)
			}
		})
	}
}

func TestUnresolvableInputSchemaDoesNotPermitFallback(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(`{"$ref":"#/$defs/missing"}`), &schema); err != nil {
		t.Fatal(err)
	}
	err := ValidateInputSchema(&schema, map[string]any{"issueIdOrKey": "DEV-1"})
	if !errors.Is(err, ErrDenied) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("unresolvable schema = %v", err)
	}
}

func TestRoutedSessionWriterPreservesProviderAndOwner(t *testing.T) {
	session := &writeLeaseFixture{}
	routes := map[string]*SessionWriter{
		"jira": NewSessionWriter("devflow", func(_ context.Context, call Call) (WriteSession, error) {
			if call.Provider != "jira" {
				t.Fatal("wrong provider reached the Jira session")
			}
			return session, nil
		}),
		"asana": NewSessionWriter("another-owner", func(context.Context, Call) (WriteSession, error) {
			t.Fatal("another enrollment owner was selected")
			return nil, ErrDenied
		}),
	}
	writer := NewRoutedSessionWriter("devflow", routes)
	t.Cleanup(writer.Close)
	delete(routes, "jira")
	call := Call{Provider: "jira", Server: "atlassian", Tool: "createJiraIssueRemoteIssueLink"}
	binding, err := writer.Prepare(t.Context(), call)
	if err != nil || binding.Owner != "devflow" {
		t.Fatalf("prepare Jira: %+v %v", binding, err)
	}
	if _, err := writer.Write(t.Context(), binding, call); err != nil || session.writes != 1 {
		t.Fatalf("write Jira: %v, writes: %d", err, session.writes)
	}
	if _, err := writer.Write(t.Context(), binding, call); !errors.Is(err, ErrDenied) || session.writes != 1 {
		t.Fatalf("Jira replay: %v, writes: %d", err, session.writes)
	}
	if _, err := writer.Prepare(t.Context(), Call{Provider: "asana", Server: "asana", Tool: "add_comment"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Asana owner mismatch: %v", err)
	}
	if _, err := writer.Prepare(t.Context(), Call{Provider: "slack", Server: "slack", Tool: "slack_send_message"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfigured Slack route: %v", err)
	}
}

func TestConfiguredReviewWindowBindsNativeConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := &writeLeaseFixture{}
		writer := NewSessionWriter("fixture", func(context.Context, Call) (WriteSession, error) { return session, nil })
		defer writer.Close()
		ctx := WithWriteHost(t.Context(), writer)
		call := Call{Provider: "jira", Server: "atlassian", Tool: "createJiraIssueRemoteIssueLink"}
		ConfigureApprovalWindow(ctx, 15*time.Minute)
		binding, err := PrepareWrite(ctx, call)
		if err != nil || time.Until(binding.ExpiresAt) != 15*time.Minute {
			t.Fatalf("initial deadline %v, err %v", binding.ExpiresAt, err)
		}
		ConfigureApprovalWindow(ctx, time.Minute)
		time.Sleep(6 * time.Minute)
		same, err := PrepareWrite(ctx, call)
		if err != nil || same != binding {
			t.Fatalf("pending connection changed: %v, err %v", same, err)
		}
		time.Sleep(9 * time.Minute)
		synctest.Wait()
		if _, err = Write(ctx, binding, call); !errors.Is(err, ErrWriteNotDispatched) || session.writes != 0 {
			t.Fatalf("expired connection dispatched: %v", err)
		}
		fresh, err := PrepareWrite(ctx, call)
		if err != nil || fresh.Connection == binding.Connection || time.Until(fresh.ExpiresAt) != time.Minute {
			t.Fatalf("fresh deadline %v, err %v", fresh.ExpiresAt, err)
		}
		if _, err = Write(ctx, fresh, call); err != nil || session.writes != 1 {
			t.Fatalf("new connection could not dispatch: %v", err)
		}
	})
}
