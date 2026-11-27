package webui

import (
	"context"
	"encoding/json"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/bus"
)

// The ai projection (lane 7.2; ADR "Phase 7 AI projection"). One AIState
// per repository: the sessions of this repository in the daemon home's
// session store, read on every ai.session.* event of the repository, never
// from event payloads. Asks come only from the store.

const (
	aiMaxSessions  = 20
	aiMaxItems     = 200
	aiMaxItemBytes = 256 << 10
	aiMaxToolCalls = 50
)

func init() {
	projectionExtensions = append(projectionExtensions, func(r *repoEvents) Projection {
		return &aiProjection{repo: r, runtime: currentAI}
	})
}

type aiProjection struct {
	repo    *repoEvents
	runtime func() AIOptions
}

func (p *aiProjection) Name() string { return ProjectionAI }

func (p *aiProjection) Topics() []bus.Topic {
	return []bus.Topic{{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern("ai.session.**")}}
}

func (p *aiProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo == p.repo.id && bus.MatchAny(p.Topics(), e) {
		return p.AllKeys()
	}
	return nil
}

func (p *aiProjection) AllKeys() []ProjectionKey { return []ProjectionKey{{Repo: p.repo.id}} }

func (p *aiProjection) State(_ context.Context, key ProjectionKey) (json.RawMessage, error) {
	if key.Repo != p.repo.id || key.Worktree != "" {
		return jsonNull, nil
	}
	return json.Marshal(aiStateOf(p.runtime(), string(p.repo.id), osHome()))
}

// aiStateOf builds the state of repository repo from the store. A store
// that cannot be read gives no sessions.
func aiStateOf(o AIOptions, repo, home string) AIState {
	st := AIState{Sessions: []AISession{}, MaxRunning: aisession.DefaultMaxRunning}
	if o.MaxRunning != nil {
		st.MaxRunning = o.MaxRunning()
	}
	if o.Store == nil {
		return st
	}
	all, err := o.Store.List()
	if err != nil {
		return st
	}
	for _, s := range all {
		if s.State != aisession.StateEnded {
			st.Running++
		}
		if s.Repo != repo || len(st.Sessions) >= aiMaxSessions {
			continue
		}
		st.Sessions = append(st.Sessions, aiSessionOf(o.Store, s, home))
	}
	return st
}

func aiSessionOf(store aisession.Store, s aisession.Session, home string) AISession {
	out := AISession{
		ID: string(s.ID), Harness: s.Harness, HarnessName: s.HarnessName,
		Worktree: tildePath(s.Worktree, home), WorktreeID: s.WorktreeID, Branch: s.Branch,
		State: string(s.State), Code: s.Code, StartedBy: s.StartedBy.Kind, Client: s.StartedBy.Client,
		Ceiling: s.Ceiling, Home: s.Home, CreatedAt: s.CreatedAt, EndedAt: s.EndedAt,
		Items: []AIItem{}, ToolCalls: []AIToolCall{}, ChangedFiles: s.ChangedFiles, Counters: s.Counters,
	}
	if u := s.Usage; u != nil {
		out.Usage = &AIUsage{Used: u.Used, Size: u.Size, CostAmount: u.CostAmount, CostCurrency: u.CostCurrency}
	}
	if first, err := store.Items(s.ID, 0, 1); err == nil && len(first) == 1 && first[0].Kind == aisession.ItemPrompt {
		out.Prompt = aisession.Clamp(first[0].Text, aisession.MaxPrompt)
	}
	items, truncated, _ := store.Tail(s.ID, aiMaxItems, aiMaxItemBytes)
	out.Truncated = truncated
	tools := map[string]int{} // ID -> index in out.ToolCalls
	terms := map[string]int{}
	for _, it := range items {
		out.Items = append(out.Items, AIItem{Seq: it.Seq, Time: it.Time, Kind: it.Kind, Text: it.Text, ToolCall: it.ToolCall})
		switch it.Kind {
		case aisession.ItemTool:
			var tc AIToolCall
			if json.Unmarshal(it.Data, &tc) != nil || tc.ID == "" {
				continue
			}
			if i, ok := tools[tc.ID]; ok {
				out.ToolCalls[i] = tc
			} else {
				tools[tc.ID] = len(out.ToolCalls)
				out.ToolCalls = append(out.ToolCalls, tc)
			}
		case aisession.ItemPlan:
			var plan struct {
				Entries []AIPlanEntry `json:"entries"`
			}
			if json.Unmarshal(it.Data, &plan) == nil {
				out.Plan = plan.Entries
			}
		case aisession.ItemUsage:
			if out.Usage != nil {
				var u struct {
					Reported bool `json:"cost_reported"`
				}
				if json.Unmarshal(it.Data, &u) == nil {
					out.Usage.CostReported = u.Reported
				}
			}
		case aisession.ItemTerminal:
			var t AITerminal
			if json.Unmarshal(it.Data, &t) != nil || t.ID == "" {
				continue // a denied terminal has no ID
			}
			if i, ok := terms[t.ID]; ok {
				out.Terminals[i] = t
			} else {
				terms[t.ID] = len(out.Terminals)
				out.Terminals = append(out.Terminals, t)
			}
		}
	}
	if n := len(out.ToolCalls); n > aiMaxToolCalls {
		out.ToolCalls = out.ToolCalls[n-aiMaxToolCalls:]
	}
	if s.State != aisession.StateEnded {
		asks, _ := store.Asks(s.ID)
		for _, a := range asks {
			out.Asks = append(out.Asks, aiAskOf(a))
		}
	}
	return out
}

func aiAskOf(a aisession.AskRecord) AIAsk {
	ask := AIAsk{
		ID: a.ID, Kind: string(a.Kind), ToolKind: string(a.ToolKind), Title: a.Title, Paths: a.Paths,
		Line: a.Line, Cwd: a.Cwd, EnvNames: a.EnvNames, Reason: a.Reason, CanAllow: a.CanAllow,
		ExpiresAt: a.ExpiresAt, Fingerprint: a.Fingerprint,
	}
	for _, o := range a.Options {
		ask.Options = append(ask.Options, AIOption{ID: o.OptionID, Name: o.Name, Kind: string(o.Kind)})
	}
	if d := a.Diff; d != nil {
		ask.Diff = &AIDiff{Path: d.Path, OldText: d.OldText, NewText: d.NewText, Cut: d.Cut}
	}
	return ask
}
