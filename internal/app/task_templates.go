package app

import (
	"fmt"
	"os"

	"github.com/nscaledev/devflow/internal/tasktemplate"
)

// ResolveTaskTemplate selects an exact ref from the repository's configured catalog.
// On creation, pass a zero ref to use the configured default. A nil result and nil
// error mean no template applies, so the caller can continue its existing task path.
// For an existing task, call only when it has a stored ref and pass that ref here.
func (a *App) ResolveTaskTemplate(ref tasktemplate.Ref) (*tasktemplate.Template, error) {
	settings := a.Cfg.Providers.TaskTemplates
	if ref == (tasktemplate.Ref{}) {
		if settings == nil || settings.Default == nil {
			return nil, nil
		}
		ref = *settings.Default
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, fmt.Errorf("task_management.templates.catalog is required to select a task template")
	}
	path, err := settings.CatalogPath()
	if err != nil {
		return nil, fmt.Errorf("task_management.templates.catalog: %w", err)
	}
	catalog, err := readTaskTemplateCatalog(a.Repo.Dir, path)
	if err != nil {
		return nil, fmt.Errorf("task template catalog %q: %w", settings.Catalog, err)
	}
	template, err := catalog.Select(ref)
	if err != nil {
		return nil, fmt.Errorf("task template catalog %q: %w", settings.Catalog, err)
	}
	return &template, nil
}

func readTaskTemplateCatalog(repo, path string) (tasktemplate.Catalog, error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file")
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return tasktemplate.DecodeCatalog(f)
}
