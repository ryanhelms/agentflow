// Package aisession runs ACP sessions: one prompt turn of an installed
// harness in a task worktree, hosted by the process that starts it or a
// detached `agentflow ai host` (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 7").
//
// types.go is the contract. Changing an exported shape there needs an ADR
// amendment.
//
// Patterns: the Manager and the Host are a Process Manager (EIP); the
// session lifecycle is a State table (Transitions); every agent request
// goes through a Chain of Responsibility of PermissionHandlers (never-list,
// repository boundary, policy rule, policy.Gate, default deny); the harness
// is a Proxy behind acp.ClientHandler; a held request is a Claim Check (the
// ask file) with a correlation ID (its fingerprint); agent text passes a
// Content Filter (sanitizing, best-effort redaction) before the store;
// ai.session.output is an Aggregator (coalesced); unanswered asks expire
// (Message Expiration).
//
// The package may import only internal/acp, internal/jsonrpc,
// internal/harness, internal/policy, internal/config, internal/boundary,
// internal/bus, internal/notifysock, internal/filelock, internal/gitx and
// the standard library (TestAISessionImports). What it needs from the app,
// command, webui and plugin layers comes in through Options.
//
// Lane 7.2 adds, beside types.go:
//
//	func NewManager(o Options) Manager
//	func OpenStore(home string) (Store, error)
//	func DefaultChain(c ChainConfig) []PermissionHandler
//	func WithRequester(ctx context.Context, r Requester) context.Context
//	func RequesterFrom(ctx context.Context) (Requester, bool)
//	func NewID() ID
//	func ValidID(s string) bool
//
// Package aisessiontest holds the test helpers (Home, InProcessDetach).
package aisession
