package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
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

// ReloadRetry is how often FollowConfigEvents looks again for event stores
// that do not exist yet, so a daemon that starts later is picked up. Tests
// shorten it.
var ReloadRetry = 2 * time.Second

// ConfigFingerprint summarises the files an App's configuration is built
// from: .agentflow/config.yml and .agentflow/providers/*.y{a,}ml in the
// repository, and the same two in the user's agentflow home (the legacy
// .devflow names too). Each file contributes its size, modification time
// and file identity, so an editor's replace-by-rename shows up even when
// the size and the time do not change. An absent file contributes a marker,
// so adding and removing a file changes the result too.
//
// The MCP server compares it before each tool call. That keeps the server
// current when no daemon runs the watcher and so no event arrives.
func ConfigFingerprint(repoDir string) (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	files := func(paths ...string) {
		for _, p := range paths {
			fmt.Fprintf(h, "%s\x00%s\n", p, fileState(p))
		}
	}
	for _, dir := range []string{".agentflow", ".devflow"} {
		files(filepath.Join(repoDir, dir, "config.yml"))
		files(providerFiles(filepath.Join(repoDir, dir, "providers"))...)
	}
	files(filepath.Join(home, "config.yml"))
	files(providerFiles(filepath.Join(home, "providers"))...)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// providerFiles lists the provider definition files in dir, as the loader
// does: regular files ending in .yml or .yaml.
func providerFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{filepath.Join(dir, "*") + " " + stateOf(err)}
	}
	var out []string
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func stateOf(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "absent"
	}
	return "unreadable"
}

func fileState(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return stateOf(err)
	}
	return fmt.Sprintf("%d %d %s", info.Size(), info.ModTime().UnixNano(), fileID(info))
}

// Reload returns a new App for the same repository and client, built from
// the configuration on disk by Open's rules: the repository and personal
// settings and a gate that keeps the prompt owner and the journal and
// resolves operations against the new policy (Gate.WithConfiguration).
//
// It changes nothing: a keeps its configuration, so a call that started
// with a finishes with it, and the caller decides when to switch. When the
// files do not load, the error is a *ConfigLoadError and the caller keeps a.
// A session in configuration recovery mode (no gate) has nothing to reload.
func (a *App) Reload(ctx context.Context) (*App, error) {
	if a.Gate == nil {
		return nil, errors.New("this session is in configuration recovery mode; restart it after repairing the configuration")
	}
	cfg, found, err := config.LoadRepo(a.Repo.Dir)
	if err != nil {
		return nil, &ConfigLoadError{err}
	}
	user, err := config.LoadUser()
	if err != nil {
		return nil, &ConfigLoadError{err}
	}
	gate := a.Gate.WithConfiguration(a.Repo.GitDir(ctx), user, cfg)
	return &App{Repo: a.Repo, Cfg: cfg, HasCfg: found, User: user, Gate: gate, Client: a.Client, Home: a.Home}, nil
}

// FollowConfigEvents calls changed, after ReloadDebounce, whenever an event
// says the configuration of worktree changed: its repository layer or the
// personal layer (NeedsReload). It follows this repository's store and the
// global store as readers, from their current end, until ctx ends. It never
// starts the daemon, and while no store exists it waits for one.
//
// changed runs on its own goroutine and may overlap itself when events keep
// arriving; it must be safe for that.
func (a *App) FollowConfigEvents(ctx context.Context, worktree bus.WorktreeID, changed func()) {
	retry := ReloadRetry
	for ctx.Err() == nil {
		m, err := a.openMembers(ctx, nil)
		if err == nil {
			err = a.followConfig(ctx, retry, m, worktree, changed)
			m.close()
		}
		if err == nil && ctx.Err() == nil {
			continue // a store appeared: follow it at once
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// followConfig follows m until ctx ends, a store appears that m lacks (it
// returns nil) or the stores fail (it returns the error).
func (a *App) followConfig(ctx context.Context, retry time.Duration, m *eventMembers, worktree bus.WorktreeID, changed func()) error {
	fctx, cancel := context.WithCancel(ctx)
	var watching sync.WaitGroup
	defer func() {
		cancel()
		watching.Wait()
	}()
	watching.Go(func() {
		tick := time.NewTicker(retry)
		defer tick.Stop()
		for {
			select {
			case <-fctx.Done():
				return
			case <-tick.C:
			}
			if now, err := a.openMembers(fctx, nil); err == nil {
				same := slices.Equal(now.repos(), m.repos())
				now.close()
				if !same {
					cancel()
					return
				}
			}
		}
	})
	if len(m.stores) == 0 {
		<-fctx.Done()
		return nil
	}
	head, err := m.reader().Head(fctx)
	if err != nil {
		return err
	}
	follower, err := m.follower()
	if err != nil {
		return err
	}
	topics := ReloadTopics(m.current, worktree)
	var timer *time.Timer
	defer func() {
		if timer != nil && ctx.Err() != nil {
			timer.Stop()
		}
	}()
	for e, err := range follower.Follow(fctx, head) {
		if err != nil {
			if fctx.Err() != nil {
				return nil
			}
			return err
		}
		if !bus.MatchAny(topics, e) || !NeedsReload(e, m.current, worktree) {
			continue
		}
		// A burst (an editor's save writes several times) becomes one call.
		if timer == nil {
			timer = time.AfterFunc(ReloadDebounce, changed)
		} else {
			timer.Reset(ReloadDebounce)
		}
	}
	return nil
}
