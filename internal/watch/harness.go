package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/harness"
)

// The daemon's harness source (lane 6.2; ADR "Phase 6 daemon source"): a
// Polling Consumer of harness.Detector, plus an event-driven wake on the
// console's focus checks, personal configuration changes and console
// probes. Each pass compares the Report with the last published snapshot
// and publishes harness.detected, harness.changed and harness.removed in
// bus.GlobalRepo. Payloads are codes and clamped versions only.

// Harness triggers (HarnessChanged.Trigger).
const (
	HarnessTriggerStart  = "start"
	HarnessTriggerPoll   = "poll"
	HarnessTriggerFocus  = "focus"
	HarnessTriggerConfig = "config"
	HarnessTriggerProbe  = "probe"
)

// HarnessEventSource is the Source of every harness.* event.
const HarnessEventSource = "harness.detector"

// HarnessJoinWindow joins triggers: a pass never starts within this long
// after the previous one ended, and every trigger in between runs as one
// pass.
const HarnessJoinWindow = 2 * time.Second

// HarnessSource runs detection passes. Set Detector, Store and Bus; the
// rest have defaults.
type HarnessSource struct {
	Detector harness.Detector
	// Store keeps the published snapshot, so a restart publishes only real
	// changes.
	Store harness.Store
	Bus   bus.Publisher
	// AutoProbe is the effective personal:ai.harnesses.auto_probe; nil
	// means on. A pass probes (changed identities only) only while it is
	// on; the Detector applies sign_in_check.
	AutoProbe func() bool
	// PollInterval is personal:ai.harnesses.poll_interval, clamped to
	// MinPollInterval..MaxPollInterval; nil means DefaultPollInterval.
	PollInterval func() time.Duration
	// Join replaces HarnessJoinWindow (tests).
	Join time.Duration
	// OnError reports a failed pass; nil writes to stderr.
	OnError func(error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu       sync.Mutex
	last     harness.Report
	have     bool
	wake     chan struct{}
	pending  string
	snapshot map[string]harnessView // what was last published, by ID
	seeded   bool
	passes   int
}

// harnessView is what a published event says about one harness. Probe is
// "" when it comes from the stored snapshot (which has no probe status).
type harnessView struct {
	State   harness.State
	Code    string
	Version string
	Probe   string
}

func (s *HarnessSource) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
}

func (s *HarnessSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Last returns the Report of the last successful pass.
func (s *HarnessSource) Last() (harness.Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.have
}

// Passes counts finished passes (tests and the console's rate limit).
func (s *HarnessSource) Passes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passes
}

// Trigger asks for a pass (focus, config or probe). Triggers that arrive
// while a pass runs, or within the join window after it, run one more pass
// together; the newest trigger names it.
func (s *HarnessSource) Trigger(trigger string) {
	s.init()
	s.mu.Lock()
	s.pending = trigger
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *HarnessSource) interval() time.Duration {
	d := harness.DefaultPollInterval
	if s.PollInterval != nil {
		d = s.PollInterval()
	}
	switch {
	case d <= 0:
		return harness.DefaultPollInterval
	case d < harness.MinPollInterval:
		return harness.MinPollInterval
	case d > harness.MaxPollInterval:
		return harness.MaxPollInterval
	}
	return d
}

func (s *HarnessSource) join() time.Duration {
	if s.Join > 0 {
		return s.Join
	}
	return HarnessJoinWindow
}

// Run passes at start, every poll interval and on every Trigger, never two
// at once, until ctx ends.
func (s *HarnessSource) Run(ctx context.Context) error {
	s.init()
	s.pass(ctx, HarnessTriggerStart)
	ended := s.now()
	for {
		poll := time.NewTimer(s.interval())
		select {
		case <-ctx.Done():
			poll.Stop()
			return nil
		case <-poll.C:
			s.pass(ctx, HarnessTriggerPoll)
		case <-s.wake:
			poll.Stop()
			if wait := s.join() - s.now().Sub(ended); wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					t.Stop()
					return nil
				case <-t.C:
				}
			}
			s.mu.Lock()
			trigger := s.pending
			s.pending = ""
			s.mu.Unlock()
			// A wake sent while waiting is part of this pass.
			select {
			case <-s.wake:
			default:
			}
			if trigger == "" {
				trigger = HarnessTriggerFocus
			}
			s.pass(ctx, trigger)
		}
		ended = s.now()
	}
}

// pass runs one detection and publishes what changed.
func (s *HarnessSource) pass(ctx context.Context, trigger string) {
	if ctx.Err() != nil || s.Detector == nil {
		return
	}
	auto := s.AutoProbe == nil || s.AutoProbe()
	rep, err := s.Detector.Detect(ctx, harness.DetectOptions{All: true, Probe: auto, Requester: harness.RequesterDaemon})
	if err != nil {
		if ctx.Err() == nil {
			s.reportError(fmt.Errorf("harness detection: %w", err))
		}
		return
	}
	s.seed()
	events, next := harnessDiff(s.snapshotCopy(), rep, trigger)

	s.mu.Lock()
	s.last, s.have = rep, true
	s.passes++
	s.mu.Unlock()

	// The report is in place before the events, so the projection reads
	// the new state for them.
	for _, e := range events {
		e.Time = s.now().UTC()
		if _, err := s.Bus.Publish(ctx, e); err != nil && ctx.Err() == nil {
			s.reportError(fmt.Errorf("harness events: %w", err))
		}
	}
	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	if len(events) > 0 && s.Store != nil {
		stored := map[string]harness.PublishedState{}
		for id, v := range next {
			stored[id] = harness.PublishedState{State: v.State, Code: v.Code, Version: v.Version}
		}
		if err := s.Store.SetPublished(stored); err != nil {
			s.reportError(fmt.Errorf("harness snapshot: %w", err))
		}
	}
}

// seed loads the stored snapshot once, so the first pass after a restart
// publishes only real changes.
func (s *HarnessSource) seed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seeded {
		return
	}
	s.seeded = true
	s.snapshot = map[string]harnessView{}
	if s.Store == nil {
		return
	}
	stored, err := s.Store.Published()
	if err != nil {
		return
	}
	for id, p := range stored {
		if p.State != "" && p.State != harness.StateNotInstalled {
			s.snapshot[id] = harnessView{State: p.State, Code: p.Code, Version: p.Version}
		}
	}
}

func (s *HarnessSource) snapshotCopy() map[string]harnessView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]harnessView, len(s.snapshot))
	for k, v := range s.snapshot {
		out[k] = v
	}
	return out
}

func (s *HarnessSource) reportError(err error) {
	if s.OnError != nil {
		s.OnError(err)
		return
	}
	var w io.Writer = os.Stderr
	fmt.Fprintln(w, "agentflow:", err)
}

// harnessDiff compares the snapshot with a report and returns the events
// and the new snapshot (found harnesses only).
func harnessDiff(prev map[string]harnessView, rep harness.Report, trigger string) ([]bus.Event, map[string]harnessView) {
	next := map[string]harnessView{}
	var events []bus.Event
	for _, h := range rep.Harnesses {
		if h.State == harness.StateNotInstalled || h.State == "" || !harnessIDRule.MatchString(h.ID) {
			continue
		}
		cur := harnessView{State: h.State, Code: harnessCode(h.Code), Version: harnessVersion(h.Version), Probe: HarnessProbeOutcome(h.Probe)}
		next[h.ID] = cur
		old, known := prev[h.ID]
		payload := bus.HarnessChanged{Harness: h.ID, Kind: string(h.Kind), State: string(cur.State), Code: cur.Code,
			ProbeStatus: cur.Probe, SignIn: harnessCode(h.Auth.Status), Version: cur.Version, Source: harnessSource(h.Sources), Trigger: trigger}
		switch {
		case !known:
			events = append(events, harnessEvent(bus.TypeHarnessDetected, payload))
		case old.State != cur.State || old.Code != cur.Code || old.Version != cur.Version || (old.Probe != "" && old.Probe != cur.Probe):
			payload.Previous = string(old.State)
			events = append(events, harnessEvent(bus.TypeHarnessChanged, payload))
		}
	}
	var gone []string
	for id := range prev {
		if _, ok := next[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		events = append(events, harnessEvent(bus.TypeHarnessRemoved, bus.HarnessRemoved{Harness: id, Previous: string(prev[id].State), Trigger: trigger}))
	}
	return events, next
}

// HarnessProbeOutcome is a probe status with "cached" replaced by the
// status the cached result had, so a pass that reads the cache after a
// probe is not a change.
func HarnessProbeOutcome(p harness.Probe) string {
	if p.Status != harness.ProbeCached {
		return p.Status
	}
	switch p.Code {
	case harness.CodeOK:
		return harness.ProbeOK
	case harness.CodeNotProbed, "":
		return harness.ProbeNone
	case harness.CodeNeedsPerson, harness.CodeShim, harness.CodeTestGuard:
		return harness.ProbeRefused
	}
	return harness.ProbeFailed
}

func harnessEvent(t bus.Type, payload any) bus.Event {
	b, _ := json.Marshal(payload)
	return bus.Event{Type: t, Source: HarnessEventSource, Repo: bus.GlobalRepo, Payload: b}
}

var (
	harnessIDRule      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	harnessCodeRule    = regexp.MustCompile(`^[a-z_]{0,32}$`)
	harnessVersionRule = regexp.MustCompile(`^[0-9A-Za-z.+-]{0,64}$`)
	harnessSourceRule  = regexp.MustCompile(`^(registry|agentflow|personal|plugin:[a-z0-9][a-z0-9._-]{0,63})$`)
)

func harnessCode(s string) string {
	if harnessCodeRule.MatchString(s) {
		return s
	}
	return ""
}

func harnessVersion(s string) string {
	if harnessVersionRule.MatchString(s) {
		return s
	}
	return ""
}

// harnessSource is the highest layer that shaped the definition.
func harnessSource(sources []string) string {
	for i := len(sources) - 1; i >= 0; i-- {
		if harnessSourceRule.MatchString(sources[i]) {
			return sources[i]
		}
	}
	return harness.SourceRegistry
}
