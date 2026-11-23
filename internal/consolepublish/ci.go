package consolepublish

import (
	"context"
	"net/url"
	"strings"
)

// Only GitHub Actions is supported here. Its marker applies to push and
// pull_request events, including a squash push only if its message has the
// marker too. Skipping a workflow on a protected branch can leave checks
// pending indefinitely. Missing permissions or unrecognized responses fall
// back to the host's normal workflow.
func (a remote) clearRules(ctx context.Context, branch string) bool {
	var ref struct {
		Name      string `json:"name"`
		Protected *bool  `json:"protected"`
	}
	escaped := url.PathEscape(branch)
	if err := a.client.Do(ctx, "GET", a.url+"/branches/"+escaped, nil, &ref); err != nil || ref.Name != branch || ref.Protected == nil || *ref.Protected {
		return false
	}
	var rules []struct{}
	if err := a.client.Do(ctx, "GET", a.url+"/rules/branches/"+escaped+"?per_page=1", nil, &rules); err != nil || rules == nil || len(rules) != 0 {
		return false
	}
	return true
}
func (a remote) quietCI(ctx context.Context, base, head string) bool {
	if !a.clearRules(ctx, base) || !a.clearRules(ctx, head) {
		return false
	}
	var workflows struct {
		Workflows []struct {
			State string `json:"state"`
		} `json:"workflows"`
	}
	if err := a.client.Do(ctx, "GET", a.url+"/actions/workflows?per_page=100", nil, &workflows); err != nil {
		return false
	}
	for _, w := range workflows.Workflows {
		if w.State == "active" {
			return true
		}
	}
	return false
}

func skipDirective(title string) bool {
	lower := strings.ToLower(title)
	for _, marker := range []string{"[skip ci]", "[ci skip]", "[no ci]", "[skip actions]", "[actions skip]", "skip-checks:"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
