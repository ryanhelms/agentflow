package app

import (
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// MCP configuration reload (lane 2.6). The contract is in the ADR, section
// "Phase 2, MCP config reload". The MCP server follows its repository's
// store and the global store as store.RoleReader, never as a writer, and
// rebuilds its App when NeedsReload reports an event. It also compares
// the configuration files' fingerprint before each tool call, because no
// event arrives while no daemon runs the watcher.

// ReloadDebounce coalesces a burst of configuration events (an editor's
// save writes several times) into one reload.
const ReloadDebounce = 250 * time.Millisecond

// ReloadTopics are the topics an MCP server for worktree follows.
func ReloadTopics(repo bus.RepoID, worktree bus.WorktreeID) []bus.Topic {
	return []bus.Topic{
		{Repo: repo, Worktree: worktree, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
	}
}

// NeedsReload reports whether e changes the configuration an MCP server
// for worktree of repo runs with: the repository layer of that worktree,
// or the personal layer. Client settings files do not: the client reads
// them itself and restarts the server when they change.
func NeedsReload(e bus.Event, repo bus.RepoID, worktree bus.WorktreeID) bool {
	if e.Type != bus.TypeConfigLayerChanged {
		return false
	}
	var p bus.ConfigLayerChanged
	if json.Unmarshal(e.Payload, &p) != nil {
		return true // unreadable payload: reloading is always safe
	}
	switch p.Layer {
	case bus.LayerPersonal:
		return e.Repo == bus.GlobalRepo
	case bus.LayerRepo:
		return e.Repo == repo && (e.Worktree == "" || e.Worktree == worktree)
	}
	return false
}
