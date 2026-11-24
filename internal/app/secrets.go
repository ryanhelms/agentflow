package app

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

var secretPatterns = []struct {
	name  string
	match func(string) bool
}{
	{"private key", regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY`).MatchString},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`).MatchString},
	{"GitHub token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{60,}\b`).MatchString},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`).MatchString},
	{"Slack webhook", containsSlackWebhook},
	{"Atlassian API token", regexp.MustCompile(`\bATATT[A-Za-z0-9_\-=]{40,}\b`).MatchString},
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`).MatchString},
	{"OpenAI key", regexp.MustCompile(`\bsk-(proj-)?[A-Za-z0-9]{32,}\b`).MatchString},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`).MatchString},
	{"generic secret assignment", regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*["'][^"'\s]{12,}["']`).MatchString},
}

var (
	httpsCandidate    = regexp.MustCompile(`(?i)\bhttps://[^\s"'<>()[\]\{},;]+`)
	slackServicesPath = regexp.MustCompile(`^/services/[A-Za-z0-9]+(?:/[A-Za-z0-9]+)*/?$`)
)

func containsSlackWebhook(text string) bool {
	for _, candidate := range httpsCandidate.FindAllString(text, -1) {
		u, err := url.Parse(strings.TrimRight(candidate, ".!"))
		if err == nil && u.Scheme == "https" && u.User == nil &&
			strings.EqualFold(u.Host, "hooks.slack.com") && slackServicesPath.MatchString(u.EscapedPath()) {
			return true
		}
	}
	return false
}

// SecretHit is a likely secret in an added line.
type SecretHit struct {
	File string `json:"file"`
	Kind string `json:"kind"`
	Line string `json:"line"`
}

// ScanSecrets scans added lines in the staged diff.
func ScanSecrets(ctx context.Context, a *App) []SecretHit {
	diff, err := a.Repo.Git(ctx, "diff", "--cached", "--no-color", "--unified=0")
	if err != nil {
		return nil
	}
	return scanDiff(diff)
}

func scanDiff(diff string) []SecretHit {
	var hits []SecretHit
	file := ""
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+++ b/") {
			file = strings.TrimPrefix(l, "+++ b/")
			continue
		}
		if !strings.HasPrefix(l, "+") || strings.HasPrefix(l, "+++") {
			continue
		}
		if strings.Contains(l, "agentflow:allow-secret") {
			continue
		}
		for _, p := range secretPatterns {
			if p.match(l) {
				line := strings.TrimSpace(l[1:])
				if len(line) > 12 {
					line = line[:12] + "…" // never echo the full secret
				}
				hits = append(hits, SecretHit{File: file, Kind: p.name, Line: line})
				break
			}
		}
	}
	return hits
}

// ContainsSecret checks text before it is sent to the browser or reviewed for publication.
// A commit's allow-secret annotation does not grant permission to display a credential.
func ContainsSecret(text string) bool {
	for _, p := range secretPatterns {
		if p.match(text) {
			return true
		}
	}
	return false
}
