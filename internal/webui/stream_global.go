package webui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// globalRegistryDir locates the console registry that holds the global
// store. It is registryDir, from os/user and never AGENTFLOW_HOME or $HOME;
// tests point it at a private temporary directory.
var globalRegistryDir = registryDir

// globalStoreSuffix follows the daemon prefix in the global store's
// directory name: <registry>/<host-hash>.events (ADR, "Phase 2 scoping").
const globalStoreSuffix = ".events"

// globalStoreDir returns this host's global store directory, creating it
// when it is missing. Like the rest of the registry it must be a private
// directory owned by this OS user: daemonPrefix checks the registry, and
// this checks the store directory itself (owner and mode on Unix, owner and
// DACL on Windows).
func globalStoreDir() (string, error) {
	registry, err := globalRegistryDir()
	if err != nil {
		return "", err
	}
	prefix, err := daemonPrefix(registry)
	if err != nil {
		return "", err
	}
	dir := prefix + globalStoreSuffix
	if err := makePrivateDir(dir); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create global event store: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !privateMode(dir, info) {
		return "", fmt.Errorf("%w: %s", store.ErrNotPrivate, dir)
	}
	return dir, nil
}

// openDaemonGlobalStore opens this host's global store as store.RoleOwner.
// Only the daemon calls it, once, from startEventRuntime.
func openDaemonGlobalStore(ctx context.Context) (store.Store, error) {
	dir, err := globalStoreDir()
	if err != nil {
		return nil, err
	}
	return store.OpenGlobal(ctx, dir, store.RoleOwner)
}
