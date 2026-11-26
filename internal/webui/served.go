package webui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus/store"
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
	registry, err := globalRegistryDir()
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

// servedIndex is the daemon's worktree index in registry.
func servedIndex(registry string) (string, error) {
	prefix, err := hostPrefix(registry)
	if err != nil {
		return "", err
	}
	return prefix + ".repos.json", nil
}

// hostPrefix is the path prefix of the daemon's files for this host in
// registry. It mirrors daemonPrefix, which also creates the registry; a read
// must not, so it cannot call it. A test keeps the two in step.
func hostPrefix(registry string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(hostname))
	return filepath.Join(registry, fmt.Sprintf("%x", hash[:12])), nil
}

// GlobalStoreDir returns the directory of this host's global event store, and
// whether it exists. The global store holds the events that belong to no
// repository: the personal configuration layer and user-level client
// settings. Unlike the daemon's globalStoreDir, it never creates the registry
// or the directory, so a read leaves nothing behind on a machine where the
// console never ran. store.OpenGlobal checks that the directory is private
// and owned by this OS user.
func GlobalStoreDir() (dir string, ok bool, err error) {
	registry, err := globalRegistryDir()
	if err != nil {
		return "", false, err
	}
	info, err := os.Lstat(registry)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || !info.IsDir() || !privateMode(registry, info) {
		return "", false, errors.New("console registry requires a private user-owned directory")
	}
	prefix, err := hostPrefix(registry)
	if err != nil {
		return "", false, err
	}
	dir = prefix + globalStoreSuffix
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	return dir, true, nil
}

// OpenGlobalEventStore opens this host's global event store in role. It is
// app.OpenGlobalEventStore: `agentflow events` and the MCP server read it as
// store.RoleReader. When the daemon has not created the store, the error
// wraps fs.ErrNotExist, which callers read as "no global events".
func OpenGlobalEventStore(ctx context.Context, role store.Role) (store.Store, error) {
	dir, ok, err := GlobalStoreDir()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("global event store: %w", fs.ErrNotExist)
	}
	return store.OpenGlobal(ctx, dir, role)
}

// internal/app cannot import this package (this package imports it), so the
// opener it reads the global store with is wired here.
func init() { app.OpenGlobalEventStore = OpenGlobalEventStore }
