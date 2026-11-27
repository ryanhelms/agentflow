package webui

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/harness"
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

// newHarnessOptions gives the source's production dependencies. In a test
// binary it gives no Detector unless a test replaces it, so no daemon test
// reaches detection by accident ("Phase 6 test guard").
var newHarnessOptions = func() HarnessOptions {
	if testing.Testing() {
		return HarnessOptions{}
	}
	return productionHarnessOptions()
}

// productionHarnessOptions gives no Detector until lane 6.1's Detector and
// configuration keys merge; then it returns harness.Detector over
// NewProber, NewSignInCheck and NewFileStore of the agentflow home.
func productionHarnessOptions() HarnessOptions { return HarnessOptions{} }

// harnessesRuntime is what the projection and the endpoints read.
type harnessesRuntime struct {
	source *watch.HarnessSource
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
	o := newHarnessOptions()
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
	setHarnesses(harnessesRuntime{source: src, ctx: ctx})
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
