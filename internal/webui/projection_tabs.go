package webui

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// tabsProjection keeps one TabsState per worktree, read from the clone's
// tab hub whenever it publishes ui.tabs.changed.
type tabsProjection struct{ repo *repoEvents }

func (p *tabsProjection) Name() string { return ProjectionTabs }

func (p *tabsProjection) Topics() []bus.Topic {
	return []bus.Topic{{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeUITabsChanged)}}
}

func (p *tabsProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo != p.repo.id || !bus.MatchAny(p.Topics(), e) {
		return nil
	}
	return p.AllKeys()
}

func (p *tabsProjection) AllKeys() []ProjectionKey {
	ids := p.repo.worktreeIDs()
	keys := make([]ProjectionKey, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, ProjectionKey{Repo: p.repo.id, Worktree: id})
	}
	return keys
}

func (p *tabsProjection) State(_ context.Context, key ProjectionKey) (json.RawMessage, error) {
	if _, ok := p.repo.worktree(key.Worktree); key.Repo != p.repo.id || !ok {
		return jsonNull, nil
	}
	st := TabsState{Tabs: []TabView{}}
	if h := p.repo.tabHub(); h != nil {
		st = h.tabsState("/w/" + string(key.Worktree) + "/")
	}
	return json.Marshal(st)
}

// tabsState is the TabsState of the tabs showing the worktree at path.
func (h *tabHub) tabsState(path string) TabsState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := TabsState{Tabs: []TabView{}}
	for _, tab := range h.tabs {
		if tab.path != path {
			continue
		}
		st.Tabs = append(st.Tabs, TabView{Tab: tab.id, Attached: tab.attachedAt.UTC()})
		if tab.nav != nil && st.Navigation == nil {
			nav := *tab.nav
			st.Navigation = &nav
		}
	}
	sort.SliceStable(st.Tabs, func(i, j int) bool {
		if !st.Tabs[i].Attached.Equal(st.Tabs[j].Attached) {
			return st.Tabs[i].Attached.Before(st.Tabs[j].Attached)
		}
		return strings.Compare(st.Tabs[i].Tab, st.Tabs[j].Tab) < 0
	})
	return st
}
