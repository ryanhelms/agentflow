package app

import (
	"strings"
	"testing"
)

func TestSlackWebhookSecretDetection(t *testing.T) {
	const scheme = "https://"
	const host = "hooks.slack.com"
	const servicePath = "/services/WORKSPACE/CHANNEL/fixture"
	webhook := scheme + host + servicePath
	for _, tt := range []struct {
		name, text string
		want       bool
	}{
		{"assignment", `webhook_url="` + webhook + `"`, true},
		{"prose", "See (" + webhook + ").", true},
		{"query", "See " + webhook + "?source=fixture", true},
		{"lookalike domain", scheme + host + ".example.invalid" + servicePath, false},
		{"userinfo", scheme + "user@" + host + servicePath, false},
		{"insecure scheme", "http://" + host + servicePath, false},
		{"lookalike path", scheme + host + servicePath + "/unexpected.txt", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsSecret(tt.text); got != tt.want {
				t.Fatalf("ContainsSecret = %v; want %v", got, tt.want)
			}
			hits := scanDiff("+++ b/settings.yml\n+" + tt.text)
			if (len(hits) == 1) != tt.want {
				t.Fatalf("got %d hits; want a hit: %v", len(hits), tt.want)
			}
			if tt.want && (hits[0].Kind != "Slack webhook" || strings.Contains(hits[0].Line, servicePath)) {
				t.Fatal("expected a redacted Slack webhook finding")
			}
		})
	}
}
