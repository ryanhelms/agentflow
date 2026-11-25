package app

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/managedcli"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

func TestExplicitAtlassianCredentialsKeepHeadlessRoute(t *testing.T) {
	const site = "https://sample.atlassian.net"
	t.Setenv("JIRA_USER", "fixture")
	t.Setenv("JIRA_TOKEN", "fixture")
	t.Setenv("CONFLUENCE_USER", "")
	t.Setenv("CONFLUENCE_TOKEN", "")

	for _, name := range []string{"jira", "confluence"} {
		if useAtlassianCLI(t.Context(), site, name) {
			t.Errorf("%s replaced the explicit service account with CLI auth", name)
		}
		if !useAtlassianCLI(native.WithHost(t.Context(), native.Client{}), site, name) {
			t.Errorf("%s interactive path did not offer CLI fallback", name)
		}
	}
}

func TestConfluenceMCPResultsPreserveRevisionAndScope(t *testing.T) {
	pages, err := decodeConfluencePages([]byte(`{"results":[{"content":{"id":"123","title":"Fixture","version":{"number":4},"space":{"key":"APP"}}}]}`), "https://sample.atlassian.net", "APP")
	if err != nil || len(pages) != 1 || pages[0].Revision != "4" || pages[0].Space != "APP" {
		t.Fatalf("mapped results %+v: %v", pages, err)
	}
	if _, err := decodeConfluencePages([]byte(`{"results":[{"content":{"id":"123","title":"Fixture"}}]}`), "https://sample.atlassian.net", "APP"); err == nil {
		t.Fatal("scoped query accepted a provider hit without a space")
	}
}

func TestStandaloneGitHubContextPreparesPinnedCLI(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX executable fixture")
	}
	a, _ := issueApp(t)
	a.Client, a.tasks = "cli", nil
	ctx := t.Context()
	const raw = `#!/bin/sh
case "$*" in
  --version) printf '%s\n' 'gh version 1.2.3';;
  "api --hostname github.com --method GET "*"-- repos/org/repo/issues/42")
    printf 'HTTP/2.0 200 OK\r\n\r\n%s' '{"number":42,"title":"Managed issue","state":"open"}';;
  *) exit 64;;
esac
`
	source := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(source, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("gh:\n- platform: {os: %s, arch: %s}\n  version: 1.2.3\n  source: %s\n  sha256: %x\n",
		runtime.GOOS, runtime.GOARCH, (&url.URL{Scheme: "file", Path: source}).String(), sha256.Sum256([]byte(raw)))
	if err := os.WriteFile(filepath.Join(a.Home, "companions.yml"), []byte(pin), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CI", "true")
	headless, err := a.Context(ctx, "org/repo#42")
	if err != nil || !strings.Contains(headless.Text, "pinned gh executable") {
		t.Fatalf("headless context: %+v, %v", headless, err)
	}
	t.Setenv("CI", "")
	if got := managedcli.ReadInstallRepo(a.standaloneReadContext(native.WithHost(ctx, native.Client{}))); got != "" {
		t.Fatalf("MCP chose the standalone installer: %s", got)
	}
	interactive, err := a.Context(ctx, "org/repo#42")
	if err != nil || interactive.Summary != "Managed issue" || len(interactive.Warnings) != 0 {
		t.Fatalf("interactive context: %+v, %v", interactive, err)
	}
}
