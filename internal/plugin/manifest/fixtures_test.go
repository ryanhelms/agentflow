package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// ciNotify is the declarative sample of the ADR.
const ciNotify = `schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
description: Sends a notification when the checks of a branch you work on start failing.
tier: declarative
permissions:
  subscribe: [gh.ci.updated]
  invoke: [notify]
extensions:
  handler:
    - name: ci-failed
      on: [gh.ci.updated]
      when:
        - {field: payload.state, equals: failure}
        - {field: payload.previous, not_equals: failure}
      actions:
        - invoke:
            command: notify
            input:
              signal: "{{settings.signal}}"
              operation_id: "ci-notify-{{event.payload.head_sha}}"
              title: "CI failed on {{event.payload.head_ref}}"
              body: "Failed checks: {{event.payload.failed}}"
settings:
  - key: signal
    type: string
    default: ci.failed
    label: Notification signal
    description: The notification signal to send. Route it in the notifications provider settings.
`

// timelineHints is the process sample of the ADR.
const timelineHints = `schema: 1
id: timeline-hints
name: Timeline hints
version: 0.1.0
description: Adds a hint to the timeline when an operation fails.
tier: process
exec:
  command: timeline-hints
permissions:
  subscribe: [op.failed]
  publish: [plugin.annotation.added]
extensions:
  handler:
    - name: failed-operations
      on: [op.failed]
`

func mustLoad(t *testing.T, text string) manifest.Manifest {
	t.Helper()
	m, err := manifest.Load([]byte(text))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

// problems returns the problems of a ValidationError, or nil.
func problems(t *testing.T, err error) []manifest.Problem {
	t.Helper()
	if err == nil {
		return nil
	}
	var ve *manifest.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *ValidationError", err)
	}
	return ve.Problems
}

// has reports whether ps holds a problem with code at path.
func has(ps []manifest.Problem, code, path string) bool {
	return slices.ContainsFunc(ps, func(p manifest.Problem) bool { return p.Code == code && p.Path == path })
}

func dump(ps []manifest.Problem) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString("\n  " + p.Code + " " + p.Path + ": " + p.Message)
	}
	return b.String()
}

// tree writes files (path to content) under a new directory. A name that
// ends in "*" is written executable.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, "*") {
			name, mode = strings.TrimSuffix(name, "*"), 0o755
		}
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
