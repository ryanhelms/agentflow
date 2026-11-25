package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/tasktemplate"
)

func TestResolveTaskTemplate(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`task_management:
  provider: filesystem
  templates:
    catalog: .agentflow/templates.json
    default: {name: professional, revision: '01'}
`)
	if err := os.WriteFile(filepath.Join(root, ".agentflow", "config.yml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	c, _, err := config.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: c, Repo: &gitx.Repo{Dir: root}}
	if got, err := a.ResolveTaskTemplate(tasktemplate.Ref{}); got != nil || err == nil {
		t.Fatalf("missing catalog: got %v, %v", got, err)
	}
	catalog, err := os.ReadFile("../tasktemplate/testdata/professional.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agentflow", "templates.json"), catalog, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ref  tasktemplate.Ref
		want string
	}{
		{tasktemplate.Ref{}, "01"},
		{tasktemplate.Ref{Name: "professional", Revision: "1"}, "1"},
	} {
		got, err := a.ResolveTaskTemplate(tc.ref)
		want := tasktemplate.Ref{Name: "professional", Revision: tc.want}
		if err != nil || got == nil || got.Ref != want {
			t.Fatalf("resolve %v = %v, %v, want %v", tc.ref, got, err, want)
		}
	}
	for _, ref := range []tasktemplate.Ref{
		{Name: "professional"},
		{Name: "professional", Revision: "001"},
	} {
		if got, err := a.ResolveTaskTemplate(ref); got != nil || err == nil {
			t.Fatalf("resolve invalid ref %v = %v, %v", ref, got, err)
		}
	}
}

func TestResolveTaskTemplateWithoutSelection(t *testing.T) {
	a := &App{Cfg: &config.Repo{}}
	if got, err := a.ResolveTaskTemplate(tasktemplate.Ref{}); got != nil || err != nil {
		t.Fatalf("no config: got %v, %v", got, err)
	}
	ref := tasktemplate.Ref{Name: "professional", Revision: "1"}
	if got, err := a.ResolveTaskTemplate(ref); got != nil || err == nil {
		t.Fatalf("explicit ref without catalog: got %v, %v", got, err)
	}
	a.Cfg.Providers.TaskTemplates = &config.TaskTemplateSettings{Catalog: "missing.json"}
	if got, err := a.ResolveTaskTemplate(tasktemplate.Ref{}); got != nil || err != nil {
		t.Fatalf("no default: got %v, %v", got, err)
	}
}

func TestTaskTemplateCatalogStaysInRepository(t *testing.T) {
	root := t.TempDir()
	ref := tasktemplate.Ref{Name: "work", Revision: "1"}
	settings := &config.TaskTemplateSettings{Catalog: "catalog.json"}
	a := &App{Cfg: &config.Repo{Providers: config.Providers{TaskTemplates: settings}}, Repo: &gitx.Repo{Dir: root}}
	if err := os.Mkdir(filepath.Join(root, settings.Catalog), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := a.ResolveTaskTemplate(ref); got != nil || err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory resolved: %v, %v", got, err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation requires privileges on Windows")
	}
	outside := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "catalog.json"), []byte(`[{"name":"work","revision":"1"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	settings.Catalog = "escape/catalog.json"
	if got, err := a.ResolveTaskTemplate(ref); got != nil || err == nil {
		t.Fatalf("symlink escape resolved: %v, %v", got, err)
	}
}
