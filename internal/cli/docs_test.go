package cli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
)

func TestDocsPublishAndSearchCLI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	for _, sub := range []string{".agentflow", "records"} {
		if err := os.Mkdir(sub, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(".agentflow/config.yml", []byte("agentflow: 1\nknowledge_base: {provider: filesystem, settings: {root: records}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "git", "init", "--quiet", "--template=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	output := filepath.Join(t.TempDir(), "cli-output")
	f, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stdout := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = stdout }()
	ref := "filesystem/engineering%2Fsystems/quota"
	if err := run(t.Context(), "docs", []string{"publish", ref, "--title", "Capacity", "--body", "Limit details", "--type", "Policy", "--description", "Quota limits", "--tags", `["gpu","capacity"]`, "--operation-id", "capacity-1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), "docs", []string{"search", "capacity", "--space", "engineering/systems"}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(out), `"status": "applied"`) || !strings.Contains(string(out), "Capacity ["+ref+"]\n  revision ") || !strings.Contains(string(out), "\n  Limit details\n") {
		t.Fatalf("CLI output %q, %v", out, err)
	}
	if err := run(t.Context(), "docs", []string{"search", "capacity", "--space", "engineering/systems", "--json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(int64(len(out)), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	var result struct{ Pages []provider.Document }
	if err := json.NewDecoder(f).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 1 || result.Pages[0].Type != "Policy" || result.Pages[0].Description != "Quota limits" ||
		!slices.Equal(result.Pages[0].Tags, []string{"gpu", "capacity"}) {
		t.Fatalf("published concept metadata = %+v", result.Pages)
	}
}

func TestDocsPublishRejectsInvalidTags(t *testing.T) {
	for _, value := range []string{"", "null", "scalar", `{"gpu":true}`, "[42]", "[null]"} {
		err := runDocs(t.Context(), []string{"publish", "filesystem/engineering/quota", "--tags", value, "--body", "Details"})
		if err == nil || !strings.Contains(err.Error(), "tags") {
			t.Fatalf("--tags %q: %v", value, err)
		}
	}
}
