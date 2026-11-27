package webui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/harness"
)

// The harnesses projection (lane 6.2; ADR "Phase 6 harnesses projection").
// One HarnessesState per repository, the same in every repository: the
// daemon harness source's last Report, read on every harness.* and global
// config.layer.changed event, never from event payloads.

func init() {
	projectionExtensions = append(projectionExtensions, func(r *repoEvents) Projection {
		return &harnessesProjection{repo: r, runtime: currentHarnesses, probing: probingIDs}
	})
}

type harnessesProjection struct {
	repo    *repoEvents
	runtime func() harnessesRuntime
	probing func() []string
}

func (p *harnessesProjection) Name() string { return ProjectionHarnesses }

func (p *harnessesProjection) Topics() []bus.Topic {
	return []bus.Topic{
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHarnessDetected)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHarnessChanged)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHarnessRemoved)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
	}
}

func (p *harnessesProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo == bus.GlobalRepo && bus.MatchAny(p.Topics(), e) {
		return p.AllKeys()
	}
	return nil
}

func (p *harnessesProjection) AllKeys() []ProjectionKey { return []ProjectionKey{{Repo: p.repo.id}} }

func (p *harnessesProjection) State(_ context.Context, key ProjectionKey) (json.RawMessage, error) {
	if key.Repo != p.repo.id || key.Worktree != "" {
		return jsonNull, nil
	}
	return json.Marshal(harnessesStateOf(p.runtime(), p.probing(), osHome()))
}

// harnessesStateOf builds the state from the source's last Report: found
// harnesses only, paths under home shown as ~/..., and the IDs being
// probed that the list has.
func harnessesStateOf(rt harnessesRuntime, probing []string, home string) HarnessesState {
	st := HarnessesState{Detector: rt.source != nil, Harnesses: []harness.Harness{}}
	if rt.source == nil {
		return st
	}
	rep, ok := rt.source.Last()
	if !ok {
		return st
	}
	st.Registry, st.AutoProbe, st.CheckedAt, st.Problems = rep.Registry, rep.AutoProbe, rep.CheckedAt, rep.Problems
	for _, h := range rep.Harnesses {
		if h.State == harness.StateNotInstalled || h.State == "" {
			continue
		}
		h.Path = tildePath(h.Path, home)
		if h.Base != nil {
			b := *h.Base
			b.Path = tildePath(b.Path, home)
			h.Base = &b
		}
		st.Harnesses = append(st.Harnesses, h)
	}
	for _, d := range rep.SkippedPath {
		d.Dir = tildePath(d.Dir, home)
		st.SkippedPath = append(st.SkippedPath, d)
	}
	for _, id := range probing {
		if slices.ContainsFunc(st.Harnesses, func(h harness.Harness) bool { return h.ID == id }) {
			st.Probing = append(st.Probing, id)
		}
	}
	return st
}

// osHome is the OS account's home, "" when unknown.
func osHome() string {
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return ""
	}
	return filepath.Clean(h)
}

// tildePath shows a path under home as "~/...". A path already shown that
// way, or outside home, is unchanged.
func tildePath(p, home string) string {
	if p == "" || home == "" || strings.HasPrefix(p, "~") {
		return p
	}
	clean := filepath.Clean(p)
	if clean == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(clean, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return p
}
