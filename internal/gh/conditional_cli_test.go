//go:build darwin || linux

package gh

import (
	"errors"
	"testing"
	"time"
)

// The gh CLI reports a 304 as a failed process. GetConditional turns it
// into NotModified, and sends the ETag as an If-None-Match header.
func TestGetConditionalCLI(t *testing.T) {
	client := mockCLI(t, "")
	fixtureResponse(t, "200 OK", "ETag: \"v1\"\r\nX-RateLimit-Limit: 5000\r\nX-RateLimit-Remaining: 4000\r\nX-RateLimit-Reset: 1900000000\r\n",
		`[{"number":3}]`, "0")
	var prs []struct{ Number int }
	meta, err := client.GetConditional(t.Context(), "/repos/acme/repo/pulls?state=open&per_page=50", "", &prs)
	if err != nil || meta.NotModified || meta.ETag != `"v1"` || len(prs) != 1 || prs[0].Number != 3 ||
		meta.RateLimit.Limit != 5000 || meta.RateLimit.Remaining != 4000 || meta.RateLimit.Reset.Unix() != 1900000000 {
		t.Fatalf("first = %+v, %+v, %v", prs, meta, err)
	}
	assertCLIArgs(t, client, []string{"api", "--hostname", "github.com", "--method", "GET", "--include"},
		[]string{"Accept: application/vnd.github+json", "X-Github-Api-Version: 2022-11-28"},
		"repos/acme/repo/pulls?state=open&per_page=50")

	fixtureResponse(t, "304 Not Modified", "ETag: \"v1\"\r\nX-RateLimit-Remaining: 3999\r\n", "", "1")
	prs = []struct{ Number int }{{Number: 99}}
	meta, err = client.GetConditional(t.Context(), "/repos/acme/repo/pulls?state=open&per_page=50", meta.ETag, &prs)
	if err != nil || !meta.NotModified || meta.ETag != `"v1"` || meta.RateLimit.Remaining != 3999 || len(prs) != 1 || prs[0].Number != 99 {
		t.Fatalf("second = %+v, %+v, %v", prs, meta, err)
	}
	assertCLIArgs(t, client, []string{"api", "--hostname", "github.com", "--method", "GET", "--include"},
		[]string{"Accept: application/vnd.github+json", "X-Github-Api-Version: 2022-11-28", `If-None-Match: "v1"`},
		"repos/acme/repo/pulls?state=open&per_page=50")
}

func TestGetConditionalCLIRateLimited(t *testing.T) {
	client := mockCLI(t, "")
	fixtureResponse(t, "403 Forbidden", "Retry-After: 45\r\nX-RateLimit-Remaining: 0\r\nX-RateLimit-Reset: 1900000000\r\n",
		`{"message":"You have exceeded a secondary rate limit"}`, "1")
	meta, err := client.GetConditional(t.Context(), "/repos/acme/repo/pulls", `"v1"`, nil)
	if !errors.Is(err, ErrRateLimited) || meta.RetryAfter != 45*time.Second || meta.RateLimit.Reset.Unix() != 1900000000 {
		t.Fatalf("meta = %+v, err = %v", meta, err)
	}
}
