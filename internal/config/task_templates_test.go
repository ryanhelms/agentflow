package config

import (
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

func TestTaskTemplateSettings(t *testing.T) {
	legacy := loadConfig(t, "jira: {projects: [OPS]}\ntask_management: {provider: jira, settings: {site: old}}")
	if legacy.Providers.TaskTemplates != nil || legacy.Providers.TaskManagement.Settings.(*JiraSettings).Site != "old" {
		t.Fatalf("legacy configuration changed: %+v", legacy.Providers)
	}
	for _, id := range []string{"jira", "github-issues", "asana", "filesystem", "repository", "none"} {
		t.Run(id, func(t *testing.T) {
			cfg := loadConfig(t, "task_management:\n  provider: "+id+`
  templates:
    catalog: .agentflow/task-templates.json
    default: {name: professional, revision: '01'}
`)
			settings := cfg.Providers.TaskTemplates
			want := tasktemplate.Ref{Name: "professional", Revision: "01"}
			if cfg.Providers.TaskManagement.Provider != id || settings == nil ||
				settings.Catalog != ".agentflow/task-templates.json" || settings.Default == nil || *settings.Default != want {
				t.Fatalf("unexpected provider or template settings: %+v, %+v", cfg.Providers.TaskManagement, settings)
			}
		})
	}
	for _, optional := range []string{"", ", default: null"} {
		cfg := loadConfig(t, "task_management: {templates: {catalog: tasks.json"+optional+"}}")
		if cfg.Providers.TaskTemplates.Default != nil {
			t.Fatalf("unexpected default: %+v", cfg.Providers.TaskTemplates.Default)
		}
	}
}

func TestInvalidTaskTemplateSettings(t *testing.T) {
	for _, tc := range []struct{ templates, field string }{
		{"[]", ""},
		{"null", ""},
		{"{}", ".catalog"},
		{"{catalog: false}", ".catalog"},
		{"{catalog: ' '}", ".catalog"},
		{"{catalog: .}", ".catalog"},
		{"{catalog: /tasks.json}", ".catalog"},
		{"{catalog: ./tasks.json}", ".catalog"},
		{"{catalog: dir/../tasks.json}", ".catalog"},
		{`{catalog: 'C:\tasks.json'}`, ".catalog"},
		{"{catalog: https://example.org/tasks.json}", ".catalog"},
		{"{catalog: tasks.json, unexpected: true}", ".unexpected"},
		{"{catalog: tasks.json, default: {name: professional}}", ".default"},
		{"{catalog: tasks.json, default: {revision: '1'}}", ".default"},
		{"{catalog: tasks.json, default: {name: professional, revision: ' '}}", ".default"},
		{"{catalog: tasks.json, default: {name: professional, revision: 1}}", ".default.revision"},
		{"{catalog: tasks.json, default: {name: professional, rev: '1'}}", ".default.rev"},
	} {
		t.Run(tc.templates, func(t *testing.T) {
			root := writeConfig(t, "task_management: {templates: "+tc.templates+"}")
			cfg, found, err := LoadRepo(root)
			if cfg != nil || !found || err == nil || !strings.Contains(err.Error(), "task_management.templates"+tc.field+":") {
				t.Fatalf("LoadRepo = %v, %v, %v, want error at templates%s", cfg, found, err, tc.field)
			}
		})
	}
}
