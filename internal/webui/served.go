package webui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// ServedRepos lists the common git dirs of the repositories the console
// daemon serves, from the daemon's registry index, sorted and without
// duplicates. Each holds an event store under <common-git-dir>/agentflow/events.
//
// It only reads. Without a registry or an index (the console never ran) it
// returns none. The index must be a regular file in the private registry, as
// the daemon itself requires. The caller is the terminal user who owns every
// repository listed, so the wider view crosses no trust boundary (ADR "Phase
// 2 multi-repository agentflow events").
func ServedRepos(ctx context.Context) ([]string, error) {
	registry, err := registryDir()
	if err != nil {
		return nil, err
	}
	return servedRepos(ctx, registry)
}

func servedRepos(ctx context.Context, registry string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(registry)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || !privateMode(registry, info) {
		return nil, errors.New("console registry requires a private user-owned directory")
	}
	index, err := servedIndex(registry)
	if err != nil {
		return nil, err
	}
	info, err = os.Lstat(index)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || !privateMode(index, info) {
		return nil, errors.New("invalid private worktree registry")
	}
	raw, err := os.ReadFile(index)
	if err != nil {
		return nil, err
	}
	var entries []registeredWorktree
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("read local console registry: %w", err)
	}
	var common []string
	for _, r := range entries {
		if !filepath.IsAbs(r.Common) {
			return nil, errors.New("invalid worktree in local console registry")
		}
		common = append(common, r.Common)
	}
	slices.Sort(common)
	return slices.Compact(common), nil
}

// servedIndex is the daemon's worktree index in registry. It mirrors
// daemonPrefix, which also creates the registry; a read must not, so it
// cannot call it. A test keeps the two in step.
func servedIndex(registry string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(hostname))
	return filepath.Join(registry, fmt.Sprintf("%x", hash[:12])) + ".repos.json", nil
}
