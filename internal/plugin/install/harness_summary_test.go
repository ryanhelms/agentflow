package install_test

import (
	"slices"
	"testing"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/plugin/install"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

func TestSummaryListsHarnesses(t *testing.T) {
	m := manifest.Manifest{Tier: manifest.TierDeclarative, Extensions: manifest.Extensions{Harness: []manifest.HarnessEntry{
		{ID: "acme-agent", Definition: harnessdef.Definition{Command: "acme", Args: []string{"acp"}}},
		{ID: "acme-adapter", Definition: harnessdef.Definition{Command: "acme-acp", BaseCommands: []string{"acme"}}},
		{ID: "acme-v", Definition: harnessdef.Definition{Command: "acme", Args: []string{"acp"}, VersionArgs: []string{"-V", "--short"}}},
		{ID: "acme-n", Definition: harnessdef.Definition{Command: "acme", NoVersion: true}},
	}}}
	got := install.DescribeExtensions(m)
	want := []string{
		"harness acme-agent: when you probe it, runs acme acp, and for its version acme --version; found on your PATH, only when you probe it by name (agentflow ai harnesses --probe --id acme-agent)",
		"harness acme-adapter: when you probe it, runs acme-acp, and for its version acme-acp --version (adapter for acme); found on your PATH, only when you probe it by name (agentflow ai harnesses --probe --id acme-adapter)",
		"harness acme-v: when you probe it, runs acme acp, and for its version acme -V --short; found on your PATH, only when you probe it by name (agentflow ai harnesses --probe --id acme-v)",
		"harness acme-n: when you probe it, runs acme, no version command; found on your PATH, only when you probe it by name (agentflow ai harnesses --probe --id acme-n)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
