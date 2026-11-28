package ai

import (
	"fmt"
	"slices"
	"sync"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/config"
)

// The feature registry (Registry with per-file registration): each file in
// internal/ai/features registers its feature in init.

var (
	regMu    sync.RWMutex
	registry = map[FeatureID]Feature{}
)

// order is every feature ID, in the fixed order Features and the console
// list them.
var order = []FeatureID{FeatureCommit, FeaturePR, FeatureTicket, FeatureAC, FeatureReply}

// labels are agentflow's words for each feature.
var labels = map[FeatureID]string{
	FeatureCommit: "Commit message",
	FeaturePR:     "PR title and body",
	FeatureTicket: "Ticket",
	FeatureAC:     "Acceptance-criteria check",
	FeatureReply:  "Review replies",
}

// IDs lists every feature ID in the fixed order, registered or not.
func IDs() []FeatureID { return slices.Clone(order) }

// Label is agentflow's name for a feature.
func Label(id FeatureID) string {
	if l, ok := labels[id]; ok {
		return l
	}
	return string(id)
}

// Known reports whether id is one of the FeatureID constants.
func Known(id FeatureID) bool { return slices.Contains(order, id) }

// Register adds f. It is called from init; a second feature with the same
// ID, or an ID that is not a FeatureID constant, is a programming error.
func Register(f Feature) {
	regMu.Lock()
	defer regMu.Unlock()
	id := f.ID()
	if !Known(id) {
		panic(fmt.Sprintf("ai: register: %q is not a feature ID", id))
	}
	if _, dup := registry[id]; dup {
		panic(fmt.Sprintf("ai: register: feature %q registered twice", id))
	}
	registry[id] = f
}

// Replace sets the feature for f.ID() and returns the function that puts
// the previous one back. Only tests use it (aitest.Use).
func Replace(f Feature) (restore func()) {
	regMu.Lock()
	defer regMu.Unlock()
	id := f.ID()
	old, had := registry[id]
	registry[id] = f
	return func() {
		regMu.Lock()
		defer regMu.Unlock()
		if had {
			registry[id] = old
		} else {
			delete(registry, id)
		}
	}
}

// Lookup finds a registered feature.
func Lookup(id FeatureID) (Feature, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	f, ok := registry[id]
	return f, ok
}

// Features lists the registered features in the fixed order.
func Features() []Feature {
	regMu.RLock()
	defer regMu.RUnlock()
	var out []Feature
	for _, id := range order {
		if f, ok := registry[id]; ok {
			out = append(out, f)
		}
	}
	return out
}

// Route is the Content-Based Router from a feature to its harness: the
// person's ai.features.<id>.harness. A feature with none is off and sends
// nothing (feature_off). The repository layer has no such key, so only the
// person's own layer can turn a feature on or redirect it.
func Route(user *config.User, id FeatureID) (string, error) {
	h := ""
	if user != nil {
		h = user.AI.Features.Harness(string(id))
	}
	if h == "" {
		return "", &aisession.Refusal{Code: CodeFeatureOff,
			Message: Label(id) + " drafts are off: no harness is set for them.",
			Fix:     "Choose a harness for this feature in Settings (ai.features." + string(id) + ".harness), or carry on without AI."}
	}
	return h, nil
}

// Refusal codes of ai.draft that internal/ai decides itself ("Phase 8
// commands"); the others are aisession's and the features'.
const (
	CodeUnknownFeature  = "unknown_feature"
	CodeUsage           = "usage"
	CodeFeatureOff      = "feature_off"
	CodeNeedsPerson     = "needs_person"
	CodeSecretInContext = "secret_in_context"
	CodeTooLarge        = aisession.RefuseTooLarge
)

func refuse(code, msg, fix string) error {
	return &aisession.Refusal{Code: code, Message: msg, Fix: fix}
}
