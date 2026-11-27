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
	}}}
	got := install.DescribeExtensions(m)
	want := []string{
		"harness acme-agent: starts acme acp, found on your PATH",
		"harness acme-adapter: starts acme-acp (adapter for acme), found on your PATH",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
