package bus

import "sort"

// Type is a dot-separated event type name: "<family>.<subject>.<verb>".
// Segments are lowercase letters, digits and underscores.
type Type string

// Scope says which envelope location fields an event type needs.
type Scope int

const (
	// ScopeWorktree events need Repo and Worktree.
	ScopeWorktree Scope = iota + 1
	// ScopeRepo events need Repo and leave Worktree empty.
	ScopeRepo
	// ScopeAny events need Repo and may set Worktree.
	ScopeAny
)

// TypeInfo describes one registered event type.
type TypeInfo struct {
	Type  Type
	Scope Scope
	// Transient events are delivered to subscribers but never stored and
	// never replayed. Their Seq stays zero.
	Transient bool
	// Phase is the program phase that introduced the type.
	Phase int
	// Global says whether the type may, or must, use GlobalRepo. Events in
	// GlobalRepo never carry a Worktree.
	Global GlobalRule
}

// GlobalRule says where a type's events may live.
type GlobalRule int

const (
	// GlobalNever: the event belongs to one repository (the zero value).
	GlobalNever GlobalRule = iota
	// GlobalAllowed: the event belongs to a repository or to GlobalRepo,
	// for example config.layer.changed (layer "repo" or "personal").
	GlobalAllowed
	// GlobalOnly: the event always belongs to GlobalRepo.
	GlobalOnly
)

// Phase 1 event types. Payload shapes are in the ADR, section "Event types".
// New types are added by amending the ADR first, then this list.
const (
	// TypeGitHeadChanged: HEAD moved or switched branch in one worktree.
	TypeGitHeadChanged Type = "git.head.changed"
	// TypeGitIndexChanged: the staging area of one worktree changed.
	TypeGitIndexChanged Type = "git.index.changed"
	// TypeGitRefsChanged: branches, tags or remote refs changed (refs/ or
	// packed-refs). Repository-wide.
	TypeGitRefsChanged Type = "git.refs.changed"
	// TypeGitWorktreesChanged: a worktree was added, removed or locked.
	// Repository-wide.
	TypeGitWorktreesChanged Type = "git.worktrees.changed"
	// TypeGitOperationChanged: a rebase, merge, cherry-pick, revert or
	// bisect started or finished in one worktree.
	TypeGitOperationChanged Type = "git.operation.changed"
	// TypeConfigLayerChanged: a file of one configuration layer changed.
	TypeConfigLayerChanged Type = "config.layer.changed"
	// TypeStreamOverflow: a subscriber fell behind and events were dropped.
	// Transient, delivered only to the subscriber that overflowed.
	TypeStreamOverflow Type = "stream.overflow"
)

// Reserved families. Their concrete types are added by the ADR amendment of
// the phase that introduces them; until then Publish rejects them. The
// phase 2 families (approval, hook, commit, gh, client, ui) are registered
// in types_phase2.go, the phase 3 family (op) in types_phase3.go and the
// phase 5 family (plugin) in types_phase5.go.
var ReservedFamilies = []string{
	"harness", // phase 6: harness detected, changed, removed
	"ai",      // phase 7: ai.session.*
}

var registry = map[Type]TypeInfo{
	TypeGitHeadChanged:      {Type: TypeGitHeadChanged, Scope: ScopeWorktree, Phase: 1},
	TypeGitIndexChanged:     {Type: TypeGitIndexChanged, Scope: ScopeWorktree, Phase: 1},
	TypeGitRefsChanged:      {Type: TypeGitRefsChanged, Scope: ScopeRepo, Phase: 1},
	TypeGitWorktreesChanged: {Type: TypeGitWorktreesChanged, Scope: ScopeRepo, Phase: 1},
	TypeGitOperationChanged: {Type: TypeGitOperationChanged, Scope: ScopeWorktree, Phase: 1},
	TypeConfigLayerChanged:  {Type: TypeConfigLayerChanged, Scope: ScopeAny, Phase: 1, Global: GlobalAllowed},
	TypeStreamOverflow:      {Type: TypeStreamOverflow, Scope: ScopeAny, Transient: true, Phase: 1, Global: GlobalAllowed},
}

func init() {
	for _, info := range phase2Types {
		registry[info.Type] = info
	}
	for _, info := range phase3Types {
		registry[info.Type] = info
	}
	for _, info := range phase5Types {
		registry[info.Type] = info
	}
}

// Lookup returns the registration of t.
func Lookup(t Type) (TypeInfo, bool) {
	info, ok := registry[t]
	return info, ok
}

// Types lists every registered type, sorted by name.
func Types() []TypeInfo {
	out := make([]TypeInfo, 0, len(registry))
	for _, info := range registry {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Payloads of the phase 1 types. Producers marshal these into Event.Payload;
// consumers must ignore unknown fields.

// GitHeadChanged is the payload of git.head.changed.
type GitHeadChanged struct {
	// Ref is the symbolic ref HEAD points to ("refs/heads/main"), or empty
	// when detached.
	Ref string `json:"ref"`
	// OID is the commit HEAD resolves to, or empty on an unborn branch.
	OID         string `json:"oid"`
	PreviousRef string `json:"previous_ref"`
	PreviousOID string `json:"previous_oid"`
}

// GitIndexChanged is the payload of git.index.changed. It carries no
// counts: projections read the worktree status themselves.
type GitIndexChanged struct{}

// GitRefsChanged is the payload of git.refs.changed.
type GitRefsChanged struct {
	// Refs lists the full ref names that changed, when known. Empty means
	// "unknown, rescan" (for example after packed-refs was rewritten).
	Refs []string `json:"refs"`
}

// GitWorktreesChanged is the payload of git.worktrees.changed.
type GitWorktreesChanged struct{}

// Operation names an in-progress multi-step git operation.
type Operation string

// Operations reported by git.operation.changed. OperationNone means none.
const (
	OperationNone       Operation = ""
	OperationRebase     Operation = "rebase"
	OperationMerge      Operation = "merge"
	OperationCherryPick Operation = "cherry-pick"
	OperationRevert     Operation = "revert"
	OperationBisect     Operation = "bisect"
)

// GitOperationChanged is the payload of git.operation.changed.
type GitOperationChanged struct {
	Operation Operation `json:"operation"`
	Previous  Operation `json:"previous"`
}

// ConfigLayer names a configuration layer.
type ConfigLayer string

// Configuration layers. LayerRepo events belong to a repository and
// worktree; LayerPersonal events belong to GlobalRepo.
const (
	LayerRepo     ConfigLayer = "repo"     // <worktree>/.agentflow/*
	LayerPersonal ConfigLayer = "personal" // ~/.agentflow/config.yml and providers (repo "global")
	// LayerClient is reserved and unused: client settings files have their
	// own type, client.settings.changed (phase 2 amendment).
	LayerClient ConfigLayer = "client"
)

// ConfigLayerChanged is the payload of config.layer.changed.
type ConfigLayerChanged struct {
	Layer ConfigLayer `json:"layer"`
	// Paths are slash-separated and relative to the layer root, for
	// example ".agentflow/config.yml" for LayerRepo.
	Paths []string `json:"paths"`
}

// StreamOverflow is the payload of stream.overflow.
type StreamOverflow struct {
	// Subscription is the SubscribeOptions.Name of the subscriber.
	Subscription string `json:"subscription"`
	// Dropped counts the events dropped since the last overflow event the
	// subscriber received.
	Dropped uint64 `json:"dropped"`
}
