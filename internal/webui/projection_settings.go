package webui

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/settings"
)

// The settings projection (lane 4.2; ADR, "Phase 4 settings projection").
// One SettingsState per worktree, computed from the layer files, the field
// catalog and the undo journal, never from event payloads: an event only
// says which worktrees to compute again.

func init() {
	projectionExtensions = append(projectionExtensions, func(r *repoEvents) Projection {
		return &settingsProjection{repo: r}
	})
}

type settingsProjection struct{ repo *repoEvents }

func (p *settingsProjection) Name() string { return ProjectionSettings }

func (p *settingsProjection) Topics() []bus.Topic {
	return []bus.Topic{
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeGitHeadChanged)},
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeGHPullUpdated)},
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeOpApplied)},
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeOpFailed)},
	}
}

func (p *settingsProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo == bus.GlobalRepo {
		if e.Type == bus.TypeConfigLayerChanged {
			return p.AllKeys()
		}
		return nil
	}
	if e.Repo != p.repo.id || !bus.MatchAny(p.Topics(), e) {
		return nil
	}
	if e.Type == bus.TypeOpApplied || e.Type == bus.TypeOpFailed {
		var op bus.OpPayload
		if json.Unmarshal(e.Payload, &op) != nil || !strings.HasPrefix(op.Operation, "config.") {
			return nil
		}
		// The journal is per repository: every worktree's last save changed.
		return p.AllKeys()
	}
	if e.Worktree != "" && e.Type == bus.TypeGitHeadChanged {
		if _, ok := p.repo.worktree(e.Worktree); ok {
			return []ProjectionKey{{Repo: p.repo.id, Worktree: e.Worktree}}
		}
		return nil
	}
	return p.AllKeys()
}

func (p *settingsProjection) AllKeys() []ProjectionKey {
	ids := p.repo.worktreeIDs()
	keys := make([]ProjectionKey, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, ProjectionKey{Repo: p.repo.id, Worktree: id})
	}
	return keys
}

func (p *settingsProjection) State(ctx context.Context, key ProjectionKey) (json.RawMessage, error) {
	w, ok := p.repo.worktree(key.Worktree)
	if key.Repo != p.repo.id || !ok || !w.available() {
		return jsonNull, nil
	}
	st, err := settingsStateOf(ctx, w.Root)
	if err != nil {
		return nil, err
	}
	return json.Marshal(st)
}

// settingsStateOf computes the SettingsState of the worktree at root.
func settingsStateOf(ctx context.Context, root string) (SettingsState, error) {
	home, err := config.HomeDir()
	if err != nil {
		return SettingsState{}, err
	}
	svc := &settings.Service{Root: root, Repo: &gitx.Repo{Dir: root}, Home: home, Catalog: consolecfg.RepoCatalog(root),
		Publisher: settings.DefaultPublisher}
	st, err := svc.State(ctx)
	if err != nil {
		return SettingsState{}, err
	}
	return SettingsState{Revisions: st.Revisions, CatalogRevision: st.CatalogRevision, Values: st.Values, Problems: st.Problems,
		LastSave: st.LastSave, Branch: st.Branch, Protected: st.Protected}, nil
}
