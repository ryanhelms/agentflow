package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
)

func TestDocsSearchWithFileSystemProvider(t *testing.T) {
	dir := newRepo(t)
	for path, content := range map[string]string{
		".devflow/config.yml": "devflow: 1\nknowledge_base: {provider: filesystem, settings: {root: handbook}}\n",
		"handbook/quota.md":   "---\nversion: 1\nkind: document\ninstance: filesystem\nscope: engineering\nid: quota\ntitle: Quota design\n---\nAn engineering decision.\n",
	} {
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.DocsSearch(t.Context(), "quota", "engineering")
	if err != nil {
		t.Fatal(err)
	}
	pages := result["pages"].([]provider.Document)
	if len(pages) != 1 || pages[0].Ref.ID != "quota" || pages[0].Title != "Quota design" {
		t.Fatalf("pages = %+v", pages)
	}
}
