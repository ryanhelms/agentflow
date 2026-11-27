package webui

import (
	"crypto/hmac"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A binding travels inside the authenticated, encrypted local control request.
// Keep it out of the registry, logs, MCP responses, and browser storage.
type commanderBinding struct {
	Socket string `json:"socket,omitempty"`
	Token  string `json:"token,omitempty"`
	Worker string `json:"worker,omitempty"`
}

func launchCommanderBinding() commanderBinding {
	b := commanderBinding{Socket: commanderSocket(), Token: os.Getenv("AGENT_COMMANDER_TOKEN"), Worker: os.Getenv("AGENT_COMMANDER_WORKER_ID")}
	if !filepath.IsAbs(b.Socket) || (b.Token == "" && (b.Worker != "" || os.Getenv("AGENT_COMMANDER_RUN_ID") != "" || os.Getenv("AGENT_COMMANDER_ROLE") != "")) {
		return commanderBinding{}
	}
	return b
}

func clearCommanderEnv() {
	for _, key := range []string{"AGENT_COMMANDER_TOKEN", "AGENT_COMMANDER_SOCKET", "AGENT_COMMANDER_HOME", "AGENT_COMMANDER_WORKER_ID", "AGENT_COMMANDER_RUN_ID", "AGENT_COMMANDER_ROLE"} {
		_ = os.Unsetenv(key)
	}
}

// A foreground identity change revokes old Commander grants across the clone.
// Configuration sessions and remembered browser grants stay independent.
type commanderScope struct {
	mu     sync.Mutex
	actor  commanderBinding
	epoch  string
	expiry *time.Timer
}

func (c *commanderScope) bind(b commanderBinding) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !filepath.IsAbs(b.Socket) || (b.Worker != "" && b.Token == "") {
		b = commanderBinding{}
	}
	if c.actor != b || c.epoch == "" {
		c.actor = b
		c.epoch = ""
		if b.Socket != "" {
			c.epoch = randomKey()
		}
	}
	if c.expiry != nil {
		c.expiry.Stop()
	}
	if c.epoch == "" {
		return ""
	}
	// Discard the credential even if the browser and service are left idle.
	var timer *time.Timer
	epoch := c.epoch
	timer = time.AfterFunc(tabSessionFor+launchTTL, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.epoch == epoch && c.expiry == timer {
			c.epoch = ""
			c.actor = commanderBinding{}
			c.expiry = nil
		}
	})
	c.expiry = timer
	return signature(c.epoch, "commander/tab-scope", "")
}

func (s *server) scopedLaunch(now time.Time, scope string) string {
	ticket := mintLaunch(s.instanceKey, now)
	c := s.commander
	if c == nil || scope == "" {
		return ticket
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == "" || signature(c.epoch, "commander/tab-scope", "") != scope {
		return ticket
	}
	key := signature(c.epoch, "commander/worktree", s.instanceKey)
	// The configuration ticket remains usable if a different actor opens the
	// clone before exchange. A separate proof authorizes the optional Handoff.
	return ticket + "." + signature(key, "commander/launch", ticket)
}

func (s *server) exchangeCommander(launch string, now time.Time) string {
	c := s.commander
	if c == nil {
		return ""
	}
	ticket, proof, ok := strings.Cut(launch, ".")
	if !ok || len(proof) != 43 {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == "" {
		return ""
	}
	key := signature(c.epoch, "commander/worktree", s.instanceKey)
	if !hmac.Equal([]byte(proof), []byte(signature(key, "commander/launch", ticket))) {
		return ""
	}
	return grantToken(key, "commander/browser", now.Add(tabSessionFor))
}

func (s *server) commanderFor(r *http.Request) (commanderBinding, string) {
	c := s.commander
	if c == nil {
		return commanderBinding{}, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == "" {
		return commanderBinding{}, ""
	}
	key := signature(c.epoch, "commander/worktree", s.instanceKey)
	_, valid := grantDeadline(r.Header.Get("X-Agentflow-Commander"), key, "commander/browser", time.Now(), tabSessionFor)
	if !valid {
		return commanderBinding{}, ""
	}
	return c.actor, signature(c.epoch, "commander/tab-scope", "")
}

const commanderReconnect = "Reconnect Agent Commander: in your own terminal, run agentflow ui --screen handoff --no-open from this worktree and open its one-time link."
