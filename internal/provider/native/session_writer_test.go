package native

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
