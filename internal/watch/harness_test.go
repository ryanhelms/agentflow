package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/harness"
)

// fakeHarnessDetector returns the scripted reports in order (the last one again
// after that) and records the options of every pass.
type fakeHarnessDetector struct {
	mu      sync.Mutex
	reports []harness.Report
	opts    []harness.DetectOptions
	err     error
	running int
	overlap bool
	delay   time.Duration
}

func (d *fakeHarnessDetector) Detect(ctx context.Context, o harness.DetectOptions) (harness.Report, error) {
	d.mu.Lock()
	d.running++
	if d.running > 1 {
		d.overlap = true
	}
	d.opts = append(d.opts, o)
	var r harness.Report
	if len(d.reports) > 0 {
		r = d.reports[0]
		if len(d.reports) > 1 {
			d.reports = d.reports[1:]
		}
	}
	err, delay := d.err, d.delay
	d.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	d.mu.Lock()
	d.running--
	d.mu.Unlock()
	return r, err
}

func (d *fakeHarnessDetector) calls() []harness.DetectOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]harness.DetectOptions{}, d.opts...)
}

type harnessRecorder struct {
	mu     sync.Mutex
	events []bus.Event
}

func (r *harnessRecorder) Publish(_ context.Context, e bus.Event) (bus.Event, error) {
	e = e.Normalize(time.Now())
	if err := e.Validate(); err != nil {
		return e, err
	}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	return e, nil
}

func (r *harnessRecorder) take() []bus.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func harnessFound(id string, state harness.State, probe string, version string) harness.Harness {
	return harness.Harness{ID: id, Name: id, Kind: "native", State: state, Sources: []string{"registry", "agentflow"},
		Command: id, Path: "/Users/me/.local/bin/" + id, Args: []string{"--acp"}, Version: version,
		Probe: harness.Probe{Status: probe, Code: map[string]string{"none": "not_probed", "ok": "ok", "cached": "ok"}[probe]},
		Auth:  harness.SignInStatus{Status: "unknown", Code: "no_check"},
		ACP:   &harness.ACPInfo{ProtocolVersion: 1, AgentName: "agent-text-here"}}
}

func harnessReport(hs ...harness.Harness) harness.Report {
	return harness.Report{Harnesses: hs, CheckedAt: time.Now()}
}

func harnessTypes(events []bus.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, string(e.Type))
	}
	return out
}

func TestHarnessDiff(t *testing.T) {
	snap := map[string]harnessView{}
	step := func(rep harness.Report) []bus.Event {
		events, next := harnessDiff(snap, rep, HarnessTriggerPoll)
		snap = next
		return events
	}
	ev := step(harnessReport(harnessFound("gemini", harness.StateNative, "none", "0.71.1"), harness.Harness{ID: "kimi", State: harness.StateNotInstalled}))
	if strings.Join(harnessTypes(ev), ",") != "harness.detected" {
		t.Fatalf("first pass: %v", harnessTypes(ev))
	}
	var p bus.HarnessChanged
	_ = json.Unmarshal(ev[0].Payload, &p)
	if p != (bus.HarnessChanged{Harness: "gemini", Kind: "native", State: "native", ProbeStatus: "none", SignIn: "unknown", Version: "0.71.1", Source: "agentflow", Trigger: "poll"}) {
		t.Errorf("payload %+v", p)
	}
	for _, bad := range []string{"/Users/me", "--acp", "agent-text-here"} {
		if strings.Contains(string(ev[0].Payload), bad) {
			t.Errorf("payload carries %q: %s", bad, ev[0].Payload)
		}
	}
	if ev := step(harnessReport(harnessFound("gemini", harness.StateNative, "none", "0.71.1"))); len(ev) != 0 {
		t.Errorf("no change: %v", harnessTypes(ev))
	}
	ev = step(harnessReport(harnessFound("gemini", harness.StateNative, "ok", "0.71.1")))
	if strings.Join(harnessTypes(ev), ",") != "harness.changed" {
		t.Fatalf("probe status: %v", harnessTypes(ev))
	}
	_ = json.Unmarshal(ev[0].Payload, &p)
	if p.Previous != "native" || p.ProbeStatus != "ok" {
		t.Errorf("payload %+v", p)
	}
	// Reading the same result back from the cache is not a change.
	if ev := step(harnessReport(harnessFound("gemini", harness.StateNative, "cached", "0.71.1"))); len(ev) != 0 {
		t.Errorf("cached: %v", harnessTypes(ev))
	}
	if ev := step(harnessReport(harnessFound("gemini", harness.StateNative, "cached", "0.72.0"))); strings.Join(harnessTypes(ev), ",") != "harness.changed" {
		t.Errorf("version: %v", harnessTypes(ev))
	}
	ev = step(harnessReport(harness.Harness{ID: "gemini", State: harness.StateNotInstalled}))
	if strings.Join(harnessTypes(ev), ",") != "harness.removed" {
		t.Fatalf("removed: %v", harnessTypes(ev))
	}
	var r bus.HarnessRemoved
	_ = json.Unmarshal(ev[0].Payload, &r)
	if r != (bus.HarnessRemoved{Harness: "gemini", Previous: "native", Trigger: "poll"}) {
		t.Errorf("removed payload %+v", r)
	}
	if ev := step(harnessReport(harnessFound("gemini", harness.StateNeedsAuth, "ok", ""))); strings.Join(harnessTypes(ev), ",") != "harness.detected" {
		t.Errorf("back: %v", harnessTypes(ev))
	}
	if ev := step(harnessReport()); strings.Join(harnessTypes(ev), ",") != "harness.removed" {
		t.Errorf("gone from the definitions: %v", harnessTypes(ev))
	}
	for _, e := range ev {
		if e.Repo != bus.GlobalRepo || e.Source != HarnessEventSource {
			t.Errorf("event %+v", e)
		}
	}
}

func TestHarnessSourceRestartPublishesOnlyChanges(t *testing.T) {
	store := harness.NewFileStore(t.TempDir())
	rec := &harnessRecorder{}
	det := &fakeHarnessDetector{reports: []harness.Report{harnessReport(harnessFound("gemini", harness.StateNative, "ok", "0.71.1"), harnessFound("goose", harness.StateNative, "none", "1.0.0"))}}
	src := &HarnessSource{Detector: det, Store: store, Bus: rec}
	src.pass(context.Background(), HarnessTriggerStart)
	if got := harnessTypes(rec.take()); len(got) != 2 {
		t.Fatalf("first start: %v", got)
	}
	// A new daemon with the same store and report publishes nothing.
	det2 := &fakeHarnessDetector{reports: []harness.Report{harnessReport(harnessFound("gemini", harness.StateNative, "cached", "0.71.1"), harnessFound("goose", harness.StateNative, "none", "1.0.0"))}}
	src2 := &HarnessSource{Detector: det2, Store: store, Bus: rec}
	src2.pass(context.Background(), HarnessTriggerStart)
	if got := harnessTypes(rec.take()); len(got) != 0 {
		t.Errorf("restart: %v", got)
	}
	// After the restart a real change still shows.
	det2.reports = []harness.Report{harnessReport(harnessFound("gemini", harness.StateIncompatible, "failed", "0.71.1"))}
	src2.pass(context.Background(), HarnessTriggerPoll)
	if got := strings.Join(harnessTypes(rec.take()), ","); got != "harness.changed,harness.removed" {
		t.Errorf("after restart: %v", got)
	}
}

func TestHarnessSourceAutoProbe(t *testing.T) {
	det := &fakeHarnessDetector{reports: []harness.Report{harnessReport()}}
	auto := false
	src := &HarnessSource{Detector: det, Bus: &harnessRecorder{}, AutoProbe: func() bool { return auto }}
	src.pass(context.Background(), HarnessTriggerPoll)
	auto = true
	src.pass(context.Background(), HarnessTriggerPoll)
	calls := det.calls()
	if len(calls) != 2 || calls[0].Probe || !calls[1].Probe {
		t.Fatalf("calls %+v", calls)
	}
	for _, c := range calls {
		if c.Requester != harness.RequesterDaemon || !c.All {
			t.Errorf("options %+v", c)
		}
	}
	if src2 := (&HarnessSource{Detector: det, Bus: &harnessRecorder{}}); true {
		src2.pass(context.Background(), HarnessTriggerPoll)
		if c := det.calls(); !c[len(c)-1].Probe {
			t.Error("auto_probe unset is not on")
		}
	}
}

func TestHarnessSourceRunTriggers(t *testing.T) {
	det := &fakeHarnessDetector{reports: []harness.Report{harnessReport(harnessFound("gemini", harness.StateNative, "none", "1.0.0"))}, delay: 100 * time.Millisecond}
	rec := &harnessRecorder{}
	src := &HarnessSource{Detector: det, Bus: rec, Join: 300 * time.Millisecond, PollInterval: func() time.Duration { return time.Hour }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx) }()
	harnessWait(t, func() bool { return src.Passes() == 1 })
	if ev := rec.take(); len(ev) != 1 {
		t.Fatalf("start: %v", harnessTypes(ev))
	} else {
		var p bus.HarnessChanged
		_ = json.Unmarshal(ev[0].Payload, &p)
		if p.Trigger != HarnessTriggerStart {
			t.Errorf("trigger %q", p.Trigger)
		}
	}
	// A burst of triggers is one pass, named by the newest.
	det.mu.Lock()
	det.reports = []harness.Report{harnessReport(harnessFound("gemini", harness.StateNative, "ok", "1.0.0"))}
	det.mu.Unlock()
	src.Trigger(HarnessTriggerFocus)
	src.Trigger(HarnessTriggerFocus)
	src.Trigger(HarnessTriggerProbe)
	harnessWait(t, func() bool { return src.Passes() >= 2 })
	time.Sleep(600 * time.Millisecond)
	if n := src.Passes(); n != 2 {
		t.Errorf("passes = %d, want 2", n)
	}
	ev := rec.take()
	if len(ev) != 1 {
		t.Fatalf("burst: %v", harnessTypes(ev))
	}
	var p bus.HarnessChanged
	_ = json.Unmarshal(ev[0].Payload, &p)
	if p.Trigger != HarnessTriggerProbe {
		t.Errorf("trigger %q", p.Trigger)
	}
	// A trigger during a pass runs one more pass after it.
	det.mu.Lock()
	det.delay = 400 * time.Millisecond
	det.mu.Unlock()
	src.Trigger(HarnessTriggerConfig)
	time.Sleep(450 * time.Millisecond) // the pass starts after the join window
	src.Trigger(HarnessTriggerFocus)
	harnessWait(t, func() bool { return src.Passes() >= 4 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	det.mu.Lock()
	defer det.mu.Unlock()
	if det.overlap {
		t.Error("passes overlapped")
	}
}

func TestHarnessSourcePolls(t *testing.T) {
	det := &fakeHarnessDetector{reports: []harness.Report{harnessReport()}}
	src := &HarnessSource{Detector: det, Bus: &harnessRecorder{}, PollInterval: func() time.Duration { return time.Millisecond }}
	if got := src.interval(); got != harness.MinPollInterval {
		t.Errorf("interval clamps to %v", got)
	}
	src.PollInterval = func() time.Duration { return time.Hour }
	if got := src.interval(); got != harness.MaxPollInterval {
		t.Errorf("interval clamps to %v", got)
	}
	src.PollInterval = nil
	if got := src.interval(); got != harness.DefaultPollInterval {
		t.Errorf("default interval %v", got)
	}
}

func TestHarnessSourceErrorKeepsLastReport(t *testing.T) {
	det := &fakeHarnessDetector{reports: []harness.Report{harnessReport(harnessFound("gemini", harness.StateNative, "none", ""))}}
	rec := &harnessRecorder{}
	var errs []error
	src := &HarnessSource{Detector: det, Bus: rec, OnError: func(err error) { errs = append(errs, err) }}
	src.pass(context.Background(), HarnessTriggerStart)
	det.err = errors.New("boom")
	src.pass(context.Background(), HarnessTriggerPoll)
	if last, ok := src.Last(); !ok || len(last.Harnesses) != 1 {
		t.Errorf("last %+v %v", last, ok)
	}
	if len(errs) != 1 || len(rec.take()) != 1 {
		t.Errorf("errs %v", errs)
	}
}

func TestHarnessProbeOutcome(t *testing.T) {
	cases := map[harness.Probe]string{
		{Status: "cached", Code: "ok"}:           "ok",
		{Status: "cached", Code: "timeout"}:      "failed",
		{Status: "cached", Code: "protocol"}:     "failed",
		{Status: "cached", Code: "needs_person"}: "refused",
		{Status: "cached", Code: "not_probed"}:   "none",
		{Status: "ok", Code: "ok"}:               "ok",
		{Status: "none", Code: "not_probed"}:     "none",
	}
	for p, want := range cases {
		if got := HarnessProbeOutcome(p); got != want {
			t.Errorf("%+v: %s, want %s", p, got, want)
		}
	}
}

func harnessWait(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
