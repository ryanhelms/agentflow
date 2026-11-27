package manifest_test

import (
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

const acmeHarness = `schema: 1
id: acme-harness
name: Acme harness
version: 1.0.0
tier: declarative
extensions:
  harness:
    - id: acme-agent
      name: Acme agent
      command: acme
      args: [acp]
`

func TestHarnessOnlyPluginLoads(t *testing.T) {
	m, err := manifest.Load([]byte(acmeHarness))
	if err != nil {
		t.Fatal(err)
	}
	h := m.Extensions.Harness
	if len(h) != 1 || h[0].ID != "acme-agent" || h[0].Command != "acme" || strings.Join(h[0].Args, " ") != "acp" || h[0].Name != "Acme agent" {
		t.Fatalf("harness = %+v", h)
	}
	if manifest.PointPhase[manifest.PointHarness] != 6 {
		t.Fatal("the harness point is not phase 6")
	}
}

func TestHarnessEntryIsStrict(t *testing.T) {
	_, err := manifest.Parse([]byte(strings.Replace(acmeHarness, "      args: [acp]\n", "      args: [acp]\n      shell: true\n", 1)))
	if ps := problems(t, err); len(ps) == 0 || !strings.Contains(ps[0].Message, "unknown key shell") {
		t.Fatalf("problems = %+v", ps)
	}
}

func TestHarnessEntriesRefused(t *testing.T) {
	cases := []struct {
		name, from, to, code, path string
	}{
		{"launcher", "command: acme", "command: npx", harnessdef.CodeLauncher, "extensions.harness[0].command"},
		{"absolute command", "command: acme", "command: /opt/acme/agent", harnessdef.CodeCommand, "extensions.harness[0].command"},
		{"status check", "      args: [acp]\n", "      args: [acp]\n      status_from: self\n", harnessdef.CodeStatusNotAllowed, "extensions.harness[0].status_from"},
		{"credential env", "      args: [acp]\n", "      args: [acp]\n      credential_env: [ACME_KEY]\n", harnessdef.CodeStatusNotAllowed, "extensions.harness[0].status_from"},
		{"bad arg", "args: [acp]", "args: ['$(id)']", harnessdef.CodeArgs, "extensions.harness[0].args"},
		{"bad id", "- id: acme-agent", "- id: Acme", harnessdef.CodeID, "extensions.harness[0].id"},
		{"duplicate id", "      args: [acp]\n", "      args: [acp]\n    - id: acme-agent\n      command: other\n", manifest.CodeID, "extensions.harness[1].id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(acmeHarness, tc.from, tc.to, 1)
			if text == acmeHarness {
				t.Fatal("the replacement did not apply")
			}
			_, err := manifest.Load([]byte(text))
			ps := problems(t, err)
			for _, p := range ps {
				if p.Code == tc.code && p.Path == tc.path {
					return
				}
			}
			t.Fatalf("problems = %+v, want %s at %s", ps, tc.code, tc.path)
		})
	}
}

func TestProcessPluginHarnessRefused(t *testing.T) {
	m, err := manifest.Load([]byte(timelineHints))
	if err != nil {
		t.Fatal(err)
	}
	m.Extensions.Harness = []manifest.HarnessEntry{{ID: "x", Definition: harnessdef.Definition{Command: "x"}}}
	ps := problems(t, manifest.Validate(m))
	for _, p := range ps {
		if p.Code == manifest.CodeRule && p.Path == "extensions.harness" {
			return
		}
	}
	t.Fatalf("problems = %+v", ps)
}
