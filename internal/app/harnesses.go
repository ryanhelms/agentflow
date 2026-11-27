package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/version"
)

// The AI harness operations (ADR "Phase 6 commands"). Both are reads: they
// write nothing but the Detector's own cache under the agentflow home, and
// what a probe may run is the Detector's rule ("Phase 6 who may probe").

// ErrNoHarnessDetector is returned when no Detector can be built.
var ErrNoHarnessDetector = errors.New("harness detection is not available in this agentflow build")

// NewHarnessDetector builds the Detector the harness operations of a use:
// harness.Production over a's agentflow home, with PATH entries inside any
// worktree of a's repository skipped. Tests replace it with a fake.
var NewHarnessDetector = func(a *App) (harness.Detector, error) {
	dir := ""
	if a.Repo != nil {
		dir = a.Repo.Dir
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("harness detection: %w", err)
		}
		dir = wd
	}
	d, _, err := harness.Production(a.Home, version.Version, func() []string { return []string{dir} })
	if err != nil {
		return nil, err
	}
	return d, nil
}

type harnessDetectorKey struct{}

// WithHarnessDetector makes the harness operations under ctx use d instead
// of NewHarnessDetector. The console daemon sets it for a console probe, so
// the probe runs under the daemon's Detector: its PATH rules skip every
// repository the daemon serves, and it shares the daemon's prober and its
// limits. Like the requester, it is never part of an operation's input.
func WithHarnessDetector(ctx context.Context, d harness.Detector) context.Context {
	return context.WithValue(ctx, harnessDetectorKey{}, d)
}

type harnessRequesterKey struct{}

// WithHarnessRequester records on ctx who asks for the harness operations
// run under it. The requester is never part of an operation's input, so a
// caller cannot choose it: only the surface that knows who is asking sets
// it. The CLI sets harness.RequesterPerson with a terminal on standard input
// and no agent environment; the console's Probe handler sets it for the
// person who clicked. Without it, every operation is an agent's.
func WithHarnessRequester(ctx context.Context, requester string) context.Context {
	return context.WithValue(ctx, harnessRequesterKey{}, requester)
}

// harnessRequester is the requester set on ctx: agent unless a surface said
// person or daemon.
func harnessRequester(ctx context.Context) string {
	switch r, _ := ctx.Value(harnessRequesterKey{}).(string); r {
	case harness.RequesterPerson, harness.RequesterDaemon:
		return r
	}
	return harness.RequesterAgent
}

// HarnessQuery selects what Harnesses reports.
type HarnessQuery struct {
	All   bool     // include harnesses that are not installed
	IDs   []string // only these
	Probe bool     // probe now, if the requester may
}

// Harnesses detects the ACP harnesses installed on this machine.
func (a *App) Harnesses(ctx context.Context, q HarnessQuery) (harness.Report, error) {
	d, _ := ctx.Value(harnessDetectorKey{}).(harness.Detector)
	if d == nil {
		var err error
		if d, err = NewHarnessDetector(a); err != nil {
			return harness.Report{}, err
		}
	}
	r, err := d.Detect(ctx, harness.DetectOptions{All: q.All, IDs: q.IDs, Probe: q.Probe, Requester: harnessRequester(ctx)})
	if err != nil {
		return harness.Report{}, err
	}
	if r.Harnesses == nil {
		r.Harnesses = []harness.Harness{}
	}
	return r, nil
}

// Statuses of a DoctorCheck.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// DoctorCheck is one check of ai doctor.
type DoctorCheck struct {
	ID      string       `json:"id"`
	Status  string       `json:"status"` // ok, warn, fail
	Message string       `json:"message"`
	Fix     *harness.Fix `json:"fix,omitempty"`
}

// DoctorReport is the result of ai doctor: its checks, in a fixed order, and
// the detection pass they read.
type DoctorReport struct {
	Checks []DoctorCheck  `json:"checks"`
	Report harness.Report `json:"report"`
}

// Failed reports whether any check failed.
func (r DoctorReport) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			return true
		}
	}
	return false
}

// HarnessDoctorQuery is the input of HarnessDoctor.
type HarnessDoctorQuery struct {
	Probe bool
	// DaemonRunning says whether the console daemon runs, without starting
	// it. The caller provides it: this package cannot import the console.
	DaemonRunning func(context.Context) (bool, error)
}

// registryMaxAge is how old the pinned registry copy may be before doctor
// warns.
const registryMaxAge = 120 * 24 * time.Hour

// HarnessDoctor checks harness detection and says how to fix what is wrong.
func (a *App) HarnessDoctor(ctx context.Context, q HarnessDoctorQuery) (DoctorReport, error) {
	r, err := a.Harnesses(ctx, HarnessQuery{Probe: q.Probe})
	if err != nil {
		return DoctorReport{}, err
	}
	return DoctorReport{Report: r, Checks: []DoctorCheck{
		registryCheck(r),
		homeCheck(r),
		pathCheck(r),
		definitionsCheck(r),
		cacheCheck(a.Home),
		daemonCheck(ctx, q.DaemonRunning),
		autoProbeCheck(r),
		signInCheck(r),
		readyCheck(r),
	}}, nil
}

func passed(id, message string) DoctorCheck {
	return DoctorCheck{ID: id, Status: CheckOK, Message: message}
}

func warned(id, message string, fix harness.Fix) DoctorCheck {
	return DoctorCheck{ID: id, Status: CheckWarn, Message: message, Fix: &fix}
}

func failed(id, message string, fix harness.Fix) DoctorCheck {
	return DoctorCheck{ID: id, Status: CheckFail, Message: message, Fix: &fix}
}

func registryCheck(r harness.Report) DoctorCheck {
	commit := r.Registry.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	message := fmt.Sprintf("ACP registry copy %s from %s, %d agents.", commit, r.Registry.CommittedAt.UTC().Format("2006-01-02"), r.Registry.Agents)
	now := r.CheckedAt
	if now.IsZero() {
		now = time.Now()
	}
	if now.Sub(r.Registry.CommittedAt) > registryMaxAge {
		return warned("registry", message+" It is more than 120 days old.", harness.Fix{Text: "Update agentflow; the registry copy is refreshed with releases."})
	}
	return passed("registry", message)
}

func homeCheck(r harness.Report) DoctorCheck {
	for _, p := range r.Problems {
		if p.Code == harness.ProblemHomeUnsafe {
			message := p.Message
			if message == "" {
				message = "The agentflow home is not safe."
			}
			return failed("home", message, harness.Fix{Text: "agentflow ignores personal overrides and plugin harnesses because the agentflow home is not safe. Use a home outside any repository, owned by you and not writable by others."})
		}
	}
	return passed("home", "The agentflow home is not refused.")
}

func pathCheck(r harness.Report) DoctorCheck {
	var entries, fixes []string
	for _, s := range r.SkippedPath {
		if s.Reason == harness.SkipMissing {
			continue
		}
		dir := s.Dir
		if dir == "" {
			dir = `""`
		}
		entries = append(entries, fmt.Sprintf("%s (%s)", dir, s.Reason))
		fixes = append(fixes, skippedFix(dir, s.Reason))
	}
	if len(entries) == 0 {
		return passed("path", "No PATH entry was skipped.")
	}
	return warned("path", fmt.Sprintf("Skipped on your PATH: %s.", strings.Join(entries, ", ")), harness.Fix{Text: strings.Join(fixes, "\n")})
}

// skippedFix is what to do about one skipped PATH entry or file.
func skippedFix(dir, reason string) string {
	switch reason {
	case harness.SkipInsideRepository:
		return dir + " is inside a git working tree, so agentflow ignores it. Name the harness by absolute path in your overrides if you trust it."
	case harness.SkipNodeModules:
		return dir + " is under a project's node_modules, so agentflow ignores it. Name the harness by absolute path in your overrides if you trust it."
	case harness.SkipRelative:
		return dir + " is not an absolute directory, so agentflow ignores it. Put absolute directories on your PATH."
	case harness.SkipUnsafePermissions:
		return dir + " is writable by others or owned by another user, so agentflow ignores it. Make you or root its owner and remove write access for group and others."
	case harness.SkipShim:
		return dir + " is a Windows command script, which agentflow does not start. Point the override at the program's `.exe`."
	}
	return fmt.Sprintf("agentflow ignores %s (%s).", dir, reason)
}

// definitionsCheck fails when a definition from the person or a plugin was
// refused: it is left out, so what they asked for does not happen.
func definitionsCheck(r harness.Report) DoctorCheck {
	var fixes []string
	for _, p := range r.Problems {
		plugin, fromPlugin := strings.CutPrefix(p.Layer, harness.SourcePluginPrefix)
		if p.Code == harness.ProblemHomeUnsafe || (p.Layer != harness.SourcePersonal && !fromPlugin) {
			continue
		}
		edit := "Edit personal:ai.harnesses.overrides." + p.ID + "."
		if fromPlugin {
			edit = "Update plugin " + plugin + ", or turn it off with plugins." + plugin + ".enabled."
		}
		fixes = append(fixes, strings.TrimSuffix(p.Message, ".")+". "+edit)
	}
	if len(fixes) == 0 {
		return passed("definitions", "No personal or plugin harness definition was refused.")
	}
	return failed("definitions", fmt.Sprintf("Personal or plugin harness definitions refused: %d.", len(fixes)), harness.Fix{Text: strings.Join(fixes, "\n")})
}

// cacheCheck looks at the probe cache file without interpreting it.
func cacheCheck(home string) DoctorCheck {
	fix := harness.Fix{Text: "agentflow rebuilds it on the next probe."}
	unreadable := warned("cache", "The probe cache cannot be read.", fix)
	dir := filepath.Join(home, "harnesses")
	file := filepath.Join(dir, "cache.json")
	info, err := os.Lstat(file)
	if errors.Is(err, fs.ErrNotExist) {
		return passed("cache", "No probe results are cached.")
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > harness.DefaultMaxCacheLen {
		return unreadable
	}
	if runtime.GOOS != "windows" {
		parent, err := os.Stat(dir)
		if err != nil {
			return unreadable
		}
		if info.Mode().Perm()&0o077 != 0 || parent.Mode().Perm()&0o077 != 0 {
			return warned("cache", "The probe cache has the wrong mode: other users can reach it.", fix)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil || !json.Valid(b) {
		return unreadable
	}
	return passed("cache", "The probe cache can be read.")
}

func daemonCheck(ctx context.Context, running func(context.Context) (bool, error)) DoctorCheck {
	fix := harness.Fix{Text: "Run `agentflow ui` for live detection.", Command: "agentflow ui"}
	if running == nil {
		return warned("daemon", "agentflow could not tell whether a console daemon runs.", fix)
	}
	up, err := running(ctx)
	switch {
	case err != nil:
		return warned("daemon", "agentflow could not tell whether a console daemon runs.", fix)
	case !up:
		return warned("daemon", "No console daemon runs.", fix)
	}
	return passed("daemon", "The console daemon runs.")
}

// harnessFound reports whether h's own command was found, so there is
// something to probe.
func harnessFound(h harness.Harness) bool {
	switch h.State {
	case harness.StateNative, harness.StateAdapterInstalled, harness.StateNeedsAuth, harness.StateIncompatible:
		return true
	}
	return false
}

func harnessReady(h harness.Harness) bool {
	if h.State != harness.StateNative && h.State != harness.StateAdapterInstalled {
		return false
	}
	return h.Probe.Status == harness.ProbeOK || (h.Probe.Status == harness.ProbeCached && h.Probe.Code == harness.CodeOK)
}

func harnessLabel(h harness.Harness) string {
	if h.Name == "" || h.Name == h.ID {
		return h.ID
	}
	return fmt.Sprintf("%s (%s)", h.Name, h.ID)
}

func autoProbeCheck(r harness.Report) DoctorCheck {
	if r.AutoProbe {
		return passed("auto_probe", "Automatic probes are on.")
	}
	var unprobed []string
	for _, h := range r.Harnesses {
		never := h.Probe.Status == harness.ProbeNone || (h.Probe.Status == harness.ProbeRefused && h.Probe.Code == harness.CodeNeedsPerson)
		if harnessFound(h) && never {
			unprobed = append(unprobed, h.ID)
		}
	}
	if len(unprobed) == 0 {
		return passed("auto_probe", "Automatic probes are off; no found harness is waiting for one.")
	}
	return warned("auto_probe", fmt.Sprintf("Automatic probes are off and these are not probed: %s.", strings.Join(unprobed, ", ")),
		harness.Fix{Text: "Run `agentflow ai harnesses --probe`, or turn on `ai.harnesses.auto_probe`.", Command: "agentflow ai harnesses --probe"})
}

func signInCheck(r harness.Report) DoctorCheck {
	var waiting []string
	var fix *harness.Fix
	off := false
	for _, h := range r.Harnesses {
		if h.State == harness.StateNeedsAuth {
			waiting = append(waiting, harnessLabel(h))
			if fix == nil && h.Fix != nil {
				fix = h.Fix
			}
		}
		// Code off: a status command was not run because sign_in_check is off.
		off = off || h.Auth.Code == harness.CodeOff
	}
	switch {
	case len(waiting) > 0:
		if fix == nil {
			fix = &harness.Fix{Text: "Sign in, then probe again."}
		}
		return warned("sign_in", fmt.Sprintf("Needs sign-in: %s.", strings.Join(waiting, ", ")), *fix)
	case off:
		return warned("sign_in", "Sign-in checks are off.", harness.Fix{Text: "Sign-in checks are off, so agentflow cannot tell whether you are signed in."})
	}
	return passed("sign_in", "No harness is waiting for sign-in.")
}

func readyCheck(r harness.Report) DoctorCheck {
	var ready []string
	var byName []harness.Harness
	var fix *harness.Fix
	found := false
	for _, h := range r.Harnesses {
		if h.State == harness.StateNotInstalled {
			continue
		}
		found = true
		if harnessReady(h) {
			ready = append(ready, h.ID)
		}
		if h.Probe.Status == harness.ProbeRefused && h.Probe.Code == harness.CodePluginLaunch {
			byName = append(byName, h)
		}
		if fix == nil && h.Fix != nil {
			fix = h.Fix
		}
	}
	if len(byName) > 0 {
		// A probe of every harness (doctor --probe too) skips a harness a
		// plugin chose how to start: name each one, with the probe that
		// starts it.
		var commands, ids []string
		for _, h := range byName {
			ids = append(ids, h.ID)
			command := "`" + harness.ProbeCommand + " --id " + h.ID + "`"
			if launch := harness.ProbeLaunch(h); launch != "" {
				command += " (runs " + launch + ")"
			}
			commands = append(commands, command)
		}
		message := "No harness is ready."
		if len(ready) > 0 {
			message = fmt.Sprintf("Ready: %s.", strings.Join(ready, ", "))
		}
		message += fmt.Sprintf(" A plugin chose how these start, so they are probed only by name: %s.", strings.Join(ids, ", "))
		return warned("harnesses", message, harness.Fix{
			Text:    "Probe each one by name if you trust it: " + strings.Join(commands, ", ") + ".",
			Command: harness.ProbeCommand + " --id " + ids[0],
		})
	}
	switch {
	case len(ready) > 0:
		return passed("harnesses", fmt.Sprintf("Ready: %s.", strings.Join(ready, ", ")))
	case !found:
		return warned("harnesses", "No ACP harnesses found on your PATH.",
			harness.Fix{Text: "See the harnesses agentflow knows and how to install them.", Command: "agentflow ai harnesses --all"})
	case fix == nil:
		fix = &harness.Fix{Text: "Probe the harnesses to confirm they speak ACP.", Command: "agentflow ai harnesses --probe"}
	}
	return warned("harnesses", "No harness is ready.", *fix)
}
