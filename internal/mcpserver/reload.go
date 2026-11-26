package mcpserver

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// Live configuration reload. The contract is the ADR, section "Phase 2, MCP
// configuration reload". Two triggers rebuild the App, and both end in
// reloader.refresh:
//
//   - Events: a daemon that watches the files records config.layer.changed,
//     and app.FollowConfigEvents turns it into one call after a 250 ms
//     debounce. The server only reads those stores and never starts the daemon.
//   - Before each tool call: the fingerprint of the configuration files
//     (app.ConfigFingerprint). No event arrives while no daemon runs, so
//     this keeps the server correct on its own.
//
// A call that started before a reload finishes on the App it started with.
// What does not reload: the instructions sent at initialize, the registered
// tool list, and recovery mode. A change of providers adds a notice to
// restart the session.

// reloader owns the swap of the server's App.
type reloader struct {
	s *Server

	mu           sync.Mutex // one reload at a time, and guards the fields below
	fingerprint  string     // of the configuration files the current App was built from
	instructions string     // what this session was told at start
	warned       string     // the changed instructions the last notice was about
	failed       string     // the last reload failure reported, so it is reported once
}

// newReloader returns the reloader of a server that started on a.
func newReloader(s *Server, a *app.App, instructions string) *reloader {
	r := &reloader{s: s, instructions: instructions}
	// An unreadable home leaves the fingerprint empty, which differs from any
	// later one and so forces a reload that reports the problem.
	r.fingerprint, _ = app.ConfigFingerprint(a.Repo.Dir)
	return r
}

// refresh rebuilds the App when the configuration files changed since the
// last build, or always with force (an event said they changed). A failure
// keeps the current App and adds a notice. It does not retry until a file
// changes again, so a broken file is reported once, not on every call.
func (r *reloader) refresh(ctx context.Context, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.s.app.Load()
	fingerprint, err := app.ConfigFingerprint(current.Repo.Dir)
	if err != nil {
		return
	}
	changed := fingerprint != r.fingerprint
	if !changed && !force {
		return
	}
	r.fingerprint = fingerprint
	next, err := current.Reload(ctx)
	if err != nil {
		if msg := err.Error(); msg != r.failed {
			r.failed = msg
			r.s.notices.add(fmt.Sprintf("agentflow could not reload its configuration after a file changed on disk: %s. It keeps the settings from before; fix the file and the next tool call tries again.", msg))
		}
		return
	}
	r.failed = ""
	r.s.app.Store(next)
	r.s.applyApprovalWindow(next)
	if changed {
		r.s.notices.add("agentflow reloaded its configuration after a file changed on disk. This call and later ones use the new settings.")
	}
	if now := instructionsFor(next); now != r.instructions && now != r.warned {
		r.warned = now
		r.s.notices.add("The configuration change selects different providers. This session keeps the instructions and tool list it started with; ask the user to restart this AI session to refresh them.")
	}
}

// current returns the App for one tool call, after a reload if the
// configuration files changed. The caller uses it for the whole call.
func (s *Server) current(ctx context.Context) *app.App {
	if s.reload != nil {
		s.reload.refresh(ctx, false)
	}
	return s.app.Load()
}

// startReload follows the configuration events until ctx ends. It returns
// the function that waits for the follower to stop after ctx ends. The server
// still reloads before each call without it, for example when the worktree
// cannot be identified.
func (s *Server) startReload(ctx context.Context) (wait func()) {
	if s.reload == nil {
		return func() {}
	}
	a := s.app.Load()
	id, err := webui.IdentifyWorktree(ctx, a.Repo.Dir)
	if err != nil {
		return func() {}
	}
	var following sync.WaitGroup
	following.Go(func() {
		a.FollowConfigEvents(ctx, id.Worktree, func() { s.reload.refresh(ctx, true) })
	})
	return following.Wait
}

// notices are messages for the agent that go out with the next tool result,
// like the update notice.
type notices struct {
	mu   sync.Mutex
	list []string
}

func (n *notices) add(msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.list = append(n.list, msg)
}

func (n *notices) drain() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.list
	n.list = nil
	return out
}

// withNotices appends the pending notices to a tool result.
func (s *Server) withNotices(res *mcp.CallToolResult) *mcp.CallToolResult {
	for _, msg := range s.notices.drain() {
		res.Content = append(res.Content, &mcp.TextContent{Text: msg})
	}
	return res
}
