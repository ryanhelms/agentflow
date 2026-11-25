package event

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Replay the existing workflow entry point with only the Jira transition
// executable replaced. The fixture records the script arguments, comment
// and dry-run environment that the reusable workflow relies on.
func TestLegacyHandleEventFixtures(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("legacy shell replay requires %s: %v", tool, err)
		}
	}
	dir := t.TempDir()
	for _, name := range []string{"handle-event.sh", "lib.sh"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/usr/bin/env bash
set -euo pipefail
test "$#" -eq 2
printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$DRY_RUN" "$TRANSITION_COMMENT"
`
	if err := os.WriteFile(filepath.Join(dir, "transition.sh"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}

	const (
		prURL    = "https://github.com/acme/widget/pull/57"
		prOpened = "OPS-3\tpr.opened\t1\tdevflow: pr.opened — " + prURL + "\n" +
			"PLAT-12\tpr.opened\t1\tdevflow: pr.opened — " + prURL + "\n"
		prMerged = "OPS-3\tpr.merged\t1\tdevflow: pr.merged — " + prURL + "\n" +
			"PLAT-12\tpr.merged\t1\tdevflow: pr.merged — " + prURL + "\n"
	)
	for _, tt := range []struct {
		fixture, event, want string
	}{
		{"pr-opened", "pull_request", prOpened},
		{"pr-reopened", "pull_request", prOpened},
		{"pr-ready", "pull_request_target", prOpened},
		{"pr-merged", "pull_request", prMerged},
		{"pr-draft", "pull_request", ""},
		{"pr-closed", "pull_request", ""},
		{"pr-merged-other-branch", "pull_request", ""},
		{"pr-skipped-marker", "pull_request", ""},
		{"pr-skipped-trailer", "pull_request", ""},
		{"pr-no-task-ref", "pull_request", prOpened},
		{"pr-fork", "pull_request_target", prOpened},
		{"branch-created", "create", "PLAT-12\tbranch.created\t1\tdevflow: branch.created — https://github.com/acme/widget/tree/work/PLAT-12\n"},
		{"push", "push", "PLAT-12\t#review\t1\tdevflow: #review — https://github.com/acme/widget/commit/abc\n" +
			"OPS-3\t#block\t1\tBlocked: Waiting on access\n" +
			"OPS-3\t#start\t1\tdevflow: #start — https://github.com/acme/widget/commit/def\n"},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("testdata", tt.fixture+".json"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(dir, "handle-event.sh"))
			cmd.Env = append(os.Environ(), "LC_ALL=C", "GITHUB_EVENT_NAME="+tt.event, "GITHUB_EVENT_PATH="+path, "DRY_RUN=1", "BASH_ENV=")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || string(out) != tt.want {
				t.Fatalf("legacy transitions = %q, %v, want %q\nstderr: %s", out, err, tt.want, &stderr)
			}
		})
	}
}
