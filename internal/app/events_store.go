package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
)

// OpenEventStore opens the Message Store of the repository whose common git
// dir is commonDir, in role. A reader gets an error wrapping fs.ErrNotExist
// when the repository has no store yet.
//
// Swap point: until the file store of phase 1 lane 1.2 lands, this is
// interimEventStore, an in-memory store under the repository's real
// repo-id. Replace interimEventStore with store.Open on this line.
var OpenEventStore = interimEventStore

// interimEventStore keeps events in memory, so only the process that opened
// it sees them. It reads or creates the same repo-id file store.Open uses,
// so repository IDs stay stable across the swap.
func interimEventStore(_ context.Context, commonDir string, role store.Role) (store.Store, error) {
	id, err := EventRepoID(commonDir, role != store.RoleReader)
	if err != nil {
		return nil, err
	}
	return storetest.New(id), nil
}

// EventRepoID reads the repository's bus.RepoID from its store directory.
// With create, a missing ID is created once with O_EXCL.
func EventRepoID(commonDir string, create bool) (bus.RepoID, error) {
	path := filepath.Join(store.Path(commonDir), store.RepoIDFile)
	for range 2 {
		raw, err := os.ReadFile(path)
		if err == nil {
			id := bus.RepoID(strings.TrimSpace(string(raw)))
			if !id.Valid() || id == bus.GlobalRepo {
				return "", fmt.Errorf("invalid event repository ID in %s", path)
			}
			return id, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || !create {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue // another process created it first
		}
		if err != nil {
			return "", err
		}
		var b [8]byte
		_, _ = rand.Read(b[:])
		_, werr := f.WriteString(hex.EncodeToString(b[:]) + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", werr
		}
	}
	return EventRepoID(commonDir, false)
}
