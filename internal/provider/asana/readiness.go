package asana

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"
)

func FromEnvironment(cfg Config) (*Adapter, error) {
	token := os.Getenv("ASANA_ACCESS_TOKEN")
	if token == "" {
		return nil, errors.New("set ASANA_ACCESS_TOKEN in the environment running devflow")
	}
	return New(&http.Client{Timeout: 15 * time.Second}, token, cfg)
}

// CheckAccess probes project task listing and verifies the membership of each
// configured destination section. Empty projects are valid.
func (a *Adapter) CheckAccess(ctx context.Context) error {
	for _, project := range slices.Sorted(maps.Keys(a.sections)) {
		_, err := request[[]resource](ctx, a, http.MethodGet, "/projects/"+url.PathEscape(project)+"/tasks", url.Values{"limit": {"1"}, "opt_fields": {"gid"}}, nil)
		if err != nil {
			return fmt.Errorf("read tasks in asana project %s: %w", project, err)
		}
		for _, section := range slices.Sorted(maps.Keys(a.sections[project])) {
			if err := a.checkSection(ctx, project, section); err != nil {
				return fmt.Errorf("check section %s in project %s: %w", section, project, err)
			}
		}
	}
	return nil
}
