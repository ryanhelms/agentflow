package app

import (
	"context"
	"regexp"
	"strings"
)

var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY`)},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"Slack webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/]+`)},
	{"Atlassian API token", regexp.MustCompile(`\bATATT[A-Za-z0-9_\-=]{40,}\b`)},
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`)},
	{"OpenAI key", regexp.MustCompile(`\bsk-(proj-)?[A-Za-z0-9]{32,}\b`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"generic secret assignment", regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*["'][^"'\s]{12,}["']`)},
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
		if strings.Contains(l, "devflow:allow-secret") {
			continue
		}
		for _, p := range secretPatterns {
			if p.re.MatchString(l) {
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
		if p.re.MatchString(text) {
			return true
		}
	}
	return false
}
