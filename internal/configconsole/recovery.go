package configconsole

import (
	"context"
	"errors"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

// Recovery is only for the configuration tools. Their write paths independently
// load the current policy and reject a write when it cannot be resolved.
func Recovery(ctx context.Context, dir, client string, cause error) (*app.App, error) {
	var invalid *app.ConfigLoadError
	if !errors.As(cause, &invalid) {
		return nil, cause
	}
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	if client == "" {
		client = "cli"
	}
	return &app.App{Repo: repo, Client: client}, nil
}
