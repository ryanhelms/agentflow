package webui

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/config"
)

// The AI projection's daemon side (lane 7.2; ADR "Phase 7 AI projection").
// It starts from this runtimeExtension (stream_runtime.go is not edited)
// and gives the ai projection the session store of the daemon's agentflow
// home. Sessions are hosted by `ai run` and `ai host` processes, never by
// the daemon: their events reach the bus through the notify socket, and
// the projection reads the store on each one.

func init() {
	runtimeExtensions = append(runtimeExtensions, func() runtimeExtension { return &aiExtension{} })
}

// AIOptions are the AI projection's dependencies.
type AIOptions struct {
	// Store is the session store; nil means the projection shows no
	// sessions.
	Store aisession.Store
	// MaxRunning reads personal:ai.sessions.max_running; nil means the
	// default.
	MaxRunning func() int
}

// newAIOptions gives the production dependencies: the store of the
// daemon's agentflow home. In a test binary it gives no store unless a
// test replaces it, so no daemon test reads a real store by accident.
var newAIOptions = func() AIOptions {
	if testing.Testing() {
		return AIOptions{}
	}
	return productionAIOptions()
}

func productionAIOptions() AIOptions {
	home, err := config.HomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentflow: the AI panel is off:", err)
		return AIOptions{}
	}
	st, err := aisession.OpenStore(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentflow: the AI panel is off:", err)
		return AIOptions{}
	}
	return AIOptions{Store: st, MaxRunning: func() int {
		u, err := config.LoadUserAt(home)
		if err != nil {
			return aisession.DefaultMaxRunning
		}
		return u.AI.Sessions.EffectiveMaxRunning()
	}}
}

var aiRT struct {
	mu sync.Mutex
	o  AIOptions
}

func currentAI() AIOptions {
	aiRT.mu.Lock()
	defer aiRT.mu.Unlock()
	return aiRT.o
}

func setAI(o AIOptions) {
	aiRT.mu.Lock()
	aiRT.o = o
	aiRT.mu.Unlock()
}

type aiExtension struct{}

func (aiExtension) start(*eventRuntime) error {
	setAI(newAIOptions())
	return nil
}

func (aiExtension) attached(*repoEvents, registeredWorktree) {}

func (aiExtension) stop() { setAI(AIOptions{}) }
