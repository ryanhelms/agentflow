package webui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/version"
)

// The plugin host in the daemon (ADR, "Phase 5"). The host starts from
// this runtimeExtension and reloads on every configuration layer change
// (the registry file is reported as a personal-layer change) and every
// newly attached worktree. The plugins projection reaches it through
// currentPlugins.

func init() {
	runtimeExtensions = append(runtimeExtensions, func() runtimeExtension { return &pluginsExtension{} })
	// Every repository's projector also follows the host's global events,
	// for the plugins projection. Streams gain no global plugin topic: the
	// projection sends its state in the repository's own frames.
	globalTopics = append(globalTopics,
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginLifecycleChanged)},
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginDeliveryFailed)},
	)
}

// newPluginOptions gives the host's production dependencies; tests
// replace it.
var newPluginOptions = func() plugin.Options {
	return plugin.DefaultOptions(plugin.Options{Version: version.Version})
}

// pluginsRuntime is what the plugins projection reads.
type pluginsRuntime struct {
	host   *plugin.Host
	source manifest.Source
	layers plugin.Layers
}

var plugins struct {
	mu sync.Mutex
	rt pluginsRuntime
}

func currentPlugins() pluginsRuntime {
	plugins.mu.Lock()
	defer plugins.mu.Unlock()
	return plugins.rt
}

func setPlugins(rt pluginsRuntime) {
	plugins.mu.Lock()
	plugins.rt = rt
	plugins.mu.Unlock()
}

type pluginsExtension struct {
	host   *plugin.Host
	reload chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

func (x *pluginsExtension) start(rt *eventRuntime) error {
	o := newPluginOptions()
	if o.Source == nil || o.Layers == nil {
		setPlugins(pluginsRuntime{source: o.Source, layers: o.Layers})
		return nil // no registry reader in this build: no plugin runs
	}
	home := o.Home
	if home == "" {
		h, err := config.HomeDir()
		if err != nil {
			return fmt.Errorf("plugin host: %w", err)
		}
		home = h
	}
	o.Home, o.Bus, o.Served = home, rt.bus, func() []plugin.Worktree { return servedWorktrees(rt) }
	sub, err := rt.bus.Subscribe(bus.SubscribeOptions{Name: "plugin.host"}, bus.Topic{Repo: bus.Any, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)})
	if err != nil {
		return fmt.Errorf("plugin host: %w", err)
	}
	x.host = plugin.NewHost(o)
	setPlugins(pluginsRuntime{host: x.host, source: o.Source, layers: o.Layers})
	ctx, cancel := context.WithCancel(rt.ctx)
	x.cancel, x.done, x.reload = cancel, make(chan struct{}), make(chan struct{}, 1)
	if err := x.host.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "agentflow: plugin host:", err)
	}
	go func() {
		defer close(x.done)
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-sub.Events():
				if !ok {
					return
				}
			case <-x.reload:
			}
			if err := x.host.Reload(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "agentflow: plugin host:", err)
			}
		}
	}()
	return nil
}

func (x *pluginsExtension) attached(*repoEvents, registeredWorktree) {
	if x.reload == nil {
		return
	}
	select {
	case x.reload <- struct{}{}:
	default:
	}
}

func (x *pluginsExtension) stop() {
	if x.host == nil {
		setPlugins(pluginsRuntime{})
		return
	}
	x.host.Stop()
	x.cancel()
	<-x.done
	setPlugins(pluginsRuntime{})
}

// servedWorktrees lists every worktree the runtime serves. A worktree
// whose git directory is the common one is its repository's main
// worktree.
func servedWorktrees(rt *eventRuntime) []plugin.Worktree {
	rt.mu.Lock()
	repos := make([]*repoEvents, 0, len(rt.byID))
	for _, r := range rt.byID {
		repos = append(repos, r)
	}
	rt.mu.Unlock()
	var out []plugin.Worktree
	for _, r := range repos {
		for _, id := range r.worktreeIDs() {
			w, ok := r.worktree(id)
			if !ok {
				continue
			}
			out = append(out, plugin.Worktree{Repo: r.id, ID: id, Root: w.Root, Main: w.GitDir == w.Common})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].ID < out[j].ID
	})
	return out
}
