package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/version"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// The harness source in the daemon (lane 6.2; ADR "Phase 6 daemon
// source"). It starts from this runtimeExtension, runs passes at start,
// every poll interval, on a personal config.layer.changed, on a console
// focus check and after a console probe, and publishes harness.* in
// global. The harnesses projection reads its last Report through
// currentHarnesses.

func init() {
	runtimeExtensions = append(runtimeExtensions, func() runtimeExtension { return &harnessesExtension{} })
	// Every repository's projector follows the source's global events for
	// the harnesses projection. Streams gain no global harness topic: the
	// projection sends its state in each repository's own frames.
	globalTopics = append(globalTopics,
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHarnessDetected)},
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHarnessChanged)},
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHarnessRemoved)},
	)
}

// HarnessOptions are the harness source's dependencies.
type HarnessOptions struct {
	// Detector runs a pass. Nil starts no source: the projection then has
	// detector: false and the console endpoints do nothing.
	Detector harness.Detector
	// Store keeps the published snapshot.
	Store harness.Store
	// AutoProbe and PollInterval read personal:ai.harnesses.auto_probe and
	// poll_interval; nil means on and DefaultPollInterval.
	AutoProbe    func() bool
	PollInterval func() time.Duration
}

// newHarnessOptions gives the source's production dependencies. workDirs
// lists the directories of every repository the daemon serves; the
// Detector skips PATH entries inside any of their worktrees. In a test
// binary it gives no Detector unless a test replaces it, so no daemon test
// reaches detection by accident ("Phase 6 test guard").
var newHarnessOptions = func(workDirs func() []string) HarnessOptions {
	if testing.Testing() {
		return HarnessOptions{}
	}
	return productionHarnessOptions(workDirs)
}

// productionHarnessOptions is harness.Production over the agentflow home:
// cache.json, the ACP prober and the sign-in check, with the daemon's
// served repositories as the work directories. auto_probe and
// poll_interval are read from the personal config.yml on every pass, so a
// change applies without a restart. Without a usable home it starts no
// source.
func productionHarnessOptions(workDirs func() []string) HarnessOptions {
	home, err := config.HomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentflow: harness detection is off:", err)
		return HarnessOptions{}
	}
	d, store, err := harness.Production(home, version.Version, workDirs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentflow: harness detection is off:", err)
		return HarnessOptions{}
	}
	return HarnessOptions{
		Detector:     d,
		Store:        store,
		AutoProbe:    func() bool { return harness.ReadSettings(home).AutoProbe },
		PollInterval: func() time.Duration { return harness.ReadSettings(home).PollInterval },
	}
}

type harnessWorkDirsKey struct{}

// withHarnessWorkDirs gives the runtime's harness source the daemon's
// registered worktrees (the daemon's index, which it reads under its own
// lock), so the first pass already skips the repositories registered
// before any of them is attached.
func withHarnessWorkDirs(ctx context.Context, dirs func() []string) context.Context {
	return context.WithValue(ctx, harnessWorkDirsKey{}, dirs)
}

// harnessWorkDirs is every served directory: the daemon's registered
// worktrees and every worktree attached to the runtime.
func (rt *eventRuntime) harnessWorkDirs() []string {
	var out []string
	if f, ok := rt.ctx.Value(harnessWorkDirsKey{}).(func() []string); ok && f != nil {
		out = append(out, f()...)
	}
	rt.mu.Lock()
	repos := make([]*repoEvents, 0, len(rt.repos))
	for _, r := range rt.repos {
		repos = append(repos, r)
	}
	rt.mu.Unlock()
	for _, r := range repos {
		r.mu.Lock()
		for _, w := range r.worktrees {
			if w.Root != "" {
				out = append(out, w.Root)
			}
		}
		r.mu.Unlock()
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// harnessesRuntime is what the projection and the endpoints read.
type harnessesRuntime struct {
	source *watch.HarnessSource
	// detector is the source's Detector; console probes run under it
	// (app.WithHarnessDetector), so they follow the daemon's PATH rules.
	detector harness.Detector
	// ctx is the source's context: console probes run under it, not under
	// the request's, which ends with the 202 answer.
	ctx context.Context
}

var harnesses struct {
	mu      sync.Mutex
	rt      harnessesRuntime
	probing map[string]int // ID -> console probes running for it
}

func currentHarnesses() harnessesRuntime {
	harnesses.mu.Lock()
	defer harnesses.mu.Unlock()
	return harnesses.rt
}

func setHarnesses(rt harnessesRuntime) {
	harnesses.mu.Lock()
	harnesses.rt = rt
	harnesses.mu.Unlock()
}

// markProbing adds ids to the probing set and returns a function that
// removes them again.
func markProbing(ids []string) func() {
	harnesses.mu.Lock()
	if harnesses.probing == nil {
		harnesses.probing = map[string]int{}
	}
	for _, id := range ids {
		harnesses.probing[id]++
	}
	harnesses.mu.Unlock()
	return func() {
		harnesses.mu.Lock()
		for _, id := range ids {
			if harnesses.probing[id]--; harnesses.probing[id] <= 0 {
				delete(harnesses.probing, id)
			}
		}
		harnesses.mu.Unlock()
	}
}

func probingIDs() []string {
	harnesses.mu.Lock()
	defer harnesses.mu.Unlock()
	out := make([]string, 0, len(harnesses.probing))
	for id := range harnesses.probing {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

type harnessesExtension struct {
	source *watch.HarnessSource
	cancel context.CancelFunc
	done   chan struct{}
}

func (x *harnessesExtension) start(rt *eventRuntime) error {
	o := newHarnessOptions(rt.harnessWorkDirs)
	if o.Detector == nil {
		setHarnesses(harnessesRuntime{})
		return nil // no detector in this build: no source runs
	}
	src := &watch.HarnessSource{Detector: o.Detector, Store: o.Store, Bus: rt.bus, AutoProbe: o.AutoProbe, PollInterval: o.PollInterval}
	sub, err := rt.bus.Subscribe(bus.SubscribeOptions{Name: "harness.detector"},
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)})
	if err != nil {
		return err
	}
	x.source = src
	ctx, cancel := context.WithCancel(rt.ctx)
	setHarnesses(harnessesRuntime{source: src, detector: o.Detector, ctx: ctx})
	x.cancel, x.done = cancel, make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = src.Run(ctx) }()
	go func() {
		defer wg.Done()
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				if personalLayerChange(e) {
					src.Trigger(watch.HarnessTriggerConfig)
				}
			}
		}
	}()
	go func() { wg.Wait(); close(x.done) }()
	return nil
}

// personalLayerChange reports a change of the personal configuration
// layer, which holds the harness overrides and the plugin registry.
func personalLayerChange(e bus.Event) bool {
	if e.Type != bus.TypeConfigLayerChanged {
		return false
	}
	var p bus.ConfigLayerChanged
	return json.Unmarshal(e.Payload, &p) == nil && p.Layer == bus.LayerPersonal
}

func (x *harnessesExtension) attached(*repoEvents, registeredWorktree) {}

func (x *harnessesExtension) stop() {
	if x.source == nil {
		setHarnesses(harnessesRuntime{})
		return
	}
	x.cancel()
	<-x.done
	setHarnesses(harnessesRuntime{})
}
