package atlassian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/auth"
)

const adf = `{"type":"doc","version":1,"content":[
 {"type":"paragraph","content":[{"type":"text","text":"Add a quota API."}]},
 {"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Acceptance criteria"}]},
 {"type":"bulletList","content":[
   {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"POST validates limits"}]}]},
   {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"429 when exceeded"}]}]}
 ]}
]}`

func TestADFText(t *testing.T) {
	got := ADFText(json.RawMessage(adf))
	for _, want := range []string{"Add a quota API.", "Acceptance criteria", "- POST validates limits", "- 429 when exceeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestTransitionsAndRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if u, p, _ := r.BasicAuth(); u != "dev@nscale.com" || p != "tok" {
			t.Errorf("auth = %q %q", u, p)
		}
		_, _ = w.Write([]byte(`{"transitions":[{"id":"31","name":"Start review","to":{"name":"In Review","statusCategory":{"key":"indeterminate"}}}]}`))
	}))
	defer srv.Close()
	c := New(auth.Atlassian{Site: srv.URL, Email: "dev@nscale.com", Token: "tok"})
	ts, err := c.Transitions(t.Context(), "PLAT-1")
	if err != nil || len(ts) != 1 || ts[0].To.Name != "In Review" || calls != 2 {
		t.Fatalf("ts=%+v err=%v calls=%d", ts, err, calls)
	}
}
