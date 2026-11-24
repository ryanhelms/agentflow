package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
)

func TestDocumentEditorUsesCurrentSourceAndDoesNotWriteIt(t *testing.T) {
	s := testServer(t)
	const path = ".agentflow/config.yml"
	base, err := os.ReadFile(filepath.Join(s.repo.Dir, path))
	if err != nil {
		t.Fatal(err)
	}
	source := documentSource{Path: path, Content: string(base), Base: hash(base)}
	w := request(t, s, "POST", "/api/document", s.origin, "session", source)
	var d struct{ Schema config.Field }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil {
		t.Fatalf("describe: %d %s", w.Code, w.Body.String())
	}
	array := d.Schema.Properties["task_management"].Properties["settings"].Properties["labels"]
	if array.Type != "array" || array.Items == nil || array.Items.Type != "string" {
		t.Fatal("task labels did not receive their selected provider's item type")
	}
	edit := map[string]any{"path": path, "content": source.Content, "base_digest": source.Base, "operation": "set",
		"keys": []string{"task_management", "settings", "labels"}, "value": []string{"001", "true", "help wanted"}}
	w = request(t, s, "POST", "/api/document/edit", s.origin, "session", edit)
	var out struct{ Content, Warning string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Warning != "" {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	got, _, err := config.InspectRepo([]byte(out.Content), nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := got.Providers.TaskManagement.Settings.(*config.GitHubIssuesSettings).Labels
	if len(labels) != 3 || labels[0] != "001" || labels[1] != "true" || labels[2] != "help wanted" {
		t.Fatalf("lost typed list values: %v", labels)
	}
	if err := os.WriteFile(filepath.Join(s.repo.Dir, path), append(base, '#'), 0600); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "POST", "/api/document/edit", s.origin, "session", edit)
	if w.Code < 400 {
		t.Fatal("accepted edit from an outdated source")
	}
	if b, err := os.ReadFile(filepath.Join(s.repo.Dir, path)); err != nil || string(b) != source.Content+"#" {
		t.Fatal("typed edit wrote to the worktree")
	}
}

func TestDocumentEditorRejectsUnscopedSources(t *testing.T) {
	s := testServer(t)
	in := documentSource{Path: ".git/config", Content: "agentflow: 1", Base: "unrelated"}
	for _, tc := range []struct{ route, origin, key string }{
		{"/api/document", s.origin, "session"},
		{"/api/document", s.origin, ""},
		{"/api/document/edit", "https://example.invalid", "session"},
	} {
		w := request(t, s, "POST", tc.route, tc.origin, tc.key, in)
		if w.Code < 400 {
			t.Errorf("%s unexpectedly succeeded", tc.route)
		}
	}
}
