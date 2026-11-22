// Package devopsfixture reads local deployment and release records.
package devopsfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

type Deployments []provider.Deployment

var _ provider.Deployments = Deployments(nil)

// LoadDeployments reads records in newest-first order from a repository file.
func LoadDeployments(dir, path string) (Deployments, error) {
	rows, err := load[provider.Deployment](dir, path)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		if strings.TrimSpace(row.Environment) == "" || strings.TrimSpace(row.Revision) == "" {
			return nil, fmt.Errorf("deployment fixture record %d requires environment and revision", i+1)
		}
	}
	return rows, nil
}

func (d Deployments) Deployments(ctx context.Context, _ provider.RepositoryRef, revision string) ([]provider.Deployment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []provider.Deployment
	for _, row := range d {
		if revision == "" || row.Revision == revision {
			out = append(out, row)
		}
	}
	return out, nil
}

type Releases map[string]provider.Release

var _ provider.ReleaseReader = Releases(nil)

func LoadReleases(dir, path string) (Releases, error) {
	rows, err := load[provider.Release](dir, path)
	if err != nil {
		return nil, err
	}
	out := make(Releases, len(rows))
	for i, row := range rows {
		if strings.TrimSpace(row.Tag) == "" || strings.TrimSpace(row.Revision) == "" {
			return nil, fmt.Errorf("release fixture record %d requires tag and revision", i+1)
		}
		if _, exists := out[row.Tag]; exists {
			return nil, fmt.Errorf("release fixture contains duplicate tag %q", row.Tag)
		}
		out[row.Tag] = row
	}
	return out, nil
}

func (r Releases) GetRelease(ctx context.Context, _ provider.RepositoryRef, tag string) (provider.Release, error) {
	if err := ctx.Err(); err != nil {
		return provider.Release{}, err
	}
	out, found := r[tag]
	if !found {
		return provider.Release{}, fmt.Errorf("fixture release %q: %w", tag, provider.ErrNotFound)
	}
	return out, nil
}

func load[T any](dir, path string) ([]T, error) {
	if !filepath.IsLocal(path) {
		return nil, errors.New("set settings.path to a file inside the repository")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open fixture repository: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %q: %w", path, err)
	}
	var rows []T
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode fixture %q: %w", path, err)
	}
	if rows == nil {
		return nil, fmt.Errorf("fixture %q requires a JSON array", path)
	}
	return rows, nil
}
