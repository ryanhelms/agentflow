package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// TaskTemplateSettings applies to task management independently of the provider.
// Omitting the templates object leaves Providers.TaskTemplates nil.
type TaskTemplateSettings struct {
	Catalog string            `yaml:"catalog"`
	Default *tasktemplate.Ref `yaml:"default"`
}

// CatalogPath returns the configured repository-relative path in native form.
func (s TaskTemplateSettings) CatalogPath() (string, error) {
	path, err := filepath.Localize(s.Catalog)
	if err != nil || path == "." || strings.TrimSpace(s.Catalog) == "" ||
		strings.ContainsAny(s.Catalog, `\:`) || strings.ContainsFunc(s.Catalog, unicode.IsControl) {
		return "", fmt.Errorf("expected a slash-separated repository-relative file path")
	}
	return path, nil
}

func parseTaskTemplates(value any) (*TaskTemplateSettings, error) {
	const path = "task_management.templates"
	var settings TaskTemplateSettings
	if err := applySettings(&settings, value, path); err != nil {
		return nil, err
	}
	if _, err := settings.CatalogPath(); err != nil {
		return nil, fmt.Errorf("%s.catalog: %w", path, err)
	}
	if settings.Default != nil {
		if err := settings.Default.Validate(); err != nil {
			return nil, fmt.Errorf("%s.default: %w", path, err)
		}
	}
	return &settings, nil
}
