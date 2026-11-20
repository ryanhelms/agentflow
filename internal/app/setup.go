package app

import (
	"context"
	"fmt"

	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/setup"
)

// InitResult is what `devflow init` wrote: repo files, then the release setup.
type InitResult struct {
	Files   *setup.Result      `json:"files"`
	Release *ReleaseInitResult `json:"release,omitempty"`
}

// InitRepo runs `devflow init` for the repository containing dir: it writes the repo
// templates, then detects deployable units and writes the release-please files.
func InitRepo(ctx context.Context, dir, client string, force bool) (*InitResult, error) {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	res := &InitResult{}
	if res.Files, err = setup.Init(ctx, repo.Dir, force); err != nil {
		return res, err
	}
	a, err := Open(ctx, repo.Dir, client)
	if err != nil {
		return res, err
	}
	if res.Release, err = a.ReleaseInit(ctx, "", false); err != nil {
		return res, fmt.Errorf("release setup: %w (fix it, then run devflow release init)", err)
	}
	return res, nil
}

// RemoveRepo runs `devflow remove` for the repository containing dir.
func RemoveRepo(ctx context.Context, dir string, opt setup.RemoveOptions) (*setup.Result, error) {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	return setup.Remove(ctx, repo.Dir, opt)
}
