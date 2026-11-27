package plugin

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The tier registry (Abstract Factory). Each tier's file registers a
// constructor from init; a Host builds one Factory per tier from them and
// picks a plugin's Factory by its tier here, and nowhere else.

// tierFactories are the registered Factory constructors, by tier.
var tierFactories = map[manifest.Tier]func(h *Host) Factory{}

// registerTier adds the Factory constructor of one tier. It panics on a
// duplicate, so a mistake fails every test.
func registerTier(tier manifest.Tier, newFactory func(h *Host) Factory) {
	if _, dup := tierFactories[tier]; dup {
		panic(fmt.Sprintf("plugin: tier %q registered twice", tier))
	}
	tierFactories[tier] = newFactory
}

// factories builds h's Factory per tier.
func factories(h *Host) map[manifest.Tier]Factory {
	out := make(map[manifest.Tier]Factory, len(tierFactories))
	for tier, newFactory := range tierFactories {
		out[tier] = newFactory(h)
	}
	return out
}

var builtins struct {
	mu   sync.RWMutex
	byID map[string]Builtin
}

// RegisterBuiltin adds a plugin compiled into agentflow (TierBuiltin). Call
// it from init. It panics on an invalid or duplicate ID. A builtin starts
// disabled like any plugin, and runs through Env with its manifest's
// permissions.
func RegisterBuiltin(b Builtin) {
	if b.New == nil || b.Manifest.ID == "" {
		panic("plugin: RegisterBuiltin needs a manifest ID and New")
	}
	b.Manifest.Tier = manifest.TierBuiltin
	builtins.mu.Lock()
	defer builtins.mu.Unlock()
	if builtins.byID == nil {
		builtins.byID = map[string]Builtin{}
	}
	if _, dup := builtins.byID[b.Manifest.ID]; dup {
		panic(fmt.Sprintf("plugin: builtin %q registered twice", b.Manifest.ID))
	}
	builtins.byID[b.Manifest.ID] = b
}

// Builtins returns the registered builtins, sorted by ID, for a
// manifest.Source to list.
func Builtins() []Builtin {
	builtins.mu.RLock()
	defer builtins.mu.RUnlock()
	out := make([]Builtin, 0, len(builtins.byID))
	for _, b := range builtins.byID {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

func init() { registerTier(manifest.TierBuiltin, func(*Host) Factory { return builtinFactory{} }) }

type builtinFactory struct{}

func (builtinFactory) Tier() manifest.Tier { return manifest.TierBuiltin }

func (builtinFactory) New(p manifest.Installed) (Instance, error) {
	builtins.mu.RLock()
	b, ok := builtins.byID[p.ID]
	builtins.mu.RUnlock()
	if !ok {
		return nil, coded(manifest.ErrNotFound, "manifest_invalid", true)
	}
	return b.New(), nil
}
