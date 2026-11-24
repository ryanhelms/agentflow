package update

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/gh"
)

func TestLatestStableReleaseConstraints(t *testing.T) {
	tag := "v1.2.3"
	policy := "{}"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
		case strings.HasSuffix(r.URL.Path, "/versions.json"):
			_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(policy))})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := gh.New("")
	client.API = srv.URL
	u := Updater{GH: client, Dir: t.TempDir()}
	check := func(want string) {
		t.Helper()
		rel, err := u.LatestStable(context.Background())
		if want == "" {
			if err != nil || rel.Version().String() != "1.2.3" {
				t.Fatalf("release %v, error %v", rel, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %s, got %v", want, err)
		}
	}
	check("")
	u.Opts.Pin = "0.5.0"
	check("update.pin")
	u.Opts.Pin = ""
	track := 0
	u.Opts.Track = &track
	check("update.track")
	u.Opts.Track = nil
	policy = "{\"blocked\":[\"1.2.3\"]}"
	check("blocked")
	tag = "v1.3.0-beta.1"
	check("unexpected")
}
