// Package notifysock is the local socket that CLI, MCP and hook processes
// use to hand events to the running webui daemon, which owns every event
// store it serves (the single-writer rule of internal/bus/store).
//
// The contract is fixed by the ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 2, Local socket". This file holds the wire types, limits and the
// pure parts of the handshake; lane 2.2 adds the client (Dial, Client.Send)
// and the daemon side (Serve). Deliver is the fallback chain every producer
// uses. Package notifysocktest holds fakes.
//
// Transport:
//
//   - Unix: a socket at "<registry prefix>.notify" in the private console
//     registry (~/.agentflow-ui, 0700, owner-checked), created with mode
//     0600. The daemon also checks the peer's user ID where the platform
//     reports it (SO_PEERCRED, LOCAL_PEERCRED).
//   - Windows, or a Unix path longer than MaxUnixPath: TCP on 127.0.0.1 at
//     a port the daemon picks.
//
// Both transports use the same mutual challenge-response handshake keyed by
// the daemon instance's registry key, so a stale listener at a recycled
// port or path learns nothing and a process that cannot read the private
// registry cannot publish.
//
// Framing: newline-delimited JSON, one frame per line, at most MaxFrame
// bytes. After the handshake the client sends Request frames and the
// daemon answers each, in order, with one Response.
package notifysock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Version is the protocol version in Hello and Challenge.
const Version = 1

// Limits and timeouts.
const (
	// MaxFrame caps one line, newline included. An event payload is at
	// most bus.MaxPayloadBytes, so one frame always fits.
	MaxFrame = 1 << 20
	// MaxUnixPath is the longest socket path the daemon uses; beyond it the
	// daemon listens on loopback TCP instead (sun_path is 104 bytes on
	// macOS and BSD, 108 on Linux).
	MaxUnixPath = 100
	// DialTimeout bounds connecting. A hook must never wait on a daemon
	// that is not there.
	DialTimeout = 200 * time.Millisecond
	// HandshakeTimeout bounds the whole handshake on both sides.
	HandshakeTimeout = time.Second
	// SendTimeout bounds one Send when ctx has no earlier deadline.
	SendTimeout = 2 * time.Second
	// IdleTimeout closes a connection with no request for this long.
	IdleTimeout = 30 * time.Second
	// WriteTimeout bounds writing one frame.
	WriteTimeout = 2 * time.Second
	// MaxConnections caps concurrent connections. Beyond it the daemon
	// accepts, answers the Hello with ErrorBusy and closes.
	MaxConnections = 32
	// MaxInFlight caps requests a client sends before reading their
	// responses. The daemon reads the next request only after it answered
	// the previous one, so a fast producer waits (back-pressure) instead
	// of losing events.
	MaxInFlight = 16
	// NonceBytes is the length of each handshake nonce before encoding.
	NonceBytes = 32
)

// Errors.
var (
	// ErrNoDaemon: no daemon is reachable. There is no registry entry or
	// endpoint, nothing listens, or the listener failed the handshake
	// (a stale or foreign listener). The caller drops the event with a
	// warning; see Deliver.
	ErrNoDaemon = errors.New("no agentflow daemon is listening for events")
	// ErrRejected: the daemon answered with an error Response. The error
	// wraps it with the Response's code and message. Never retried.
	ErrRejected = errors.New("agentflow daemon rejected the event")
	// ErrBadEndpoint: an endpoint string is malformed or not local.
	ErrBadEndpoint = errors.New("invalid notify socket endpoint")
)

// Endpoint is where a daemon listens and the key that authenticates it.
// The daemon advertises Network and Address in its registry file
// ("<prefix>.json", field "notify", see String); Key is the instance key
// from the same file, never sent on the wire.
type Endpoint struct {
	Network string // "unix" or "tcp"
	Address string // absolute socket path, or "127.0.0.1:<port>"
	Key     []byte // the instance key (instance.Key, decoded)
}

// String is the registry form: "unix:<path>" or "tcp:127.0.0.1:<port>".
func (e Endpoint) String() string { return e.Network + ":" + e.Address }

// ParseEndpoint parses the registry form. TCP endpoints must name the
// literal loopback address 127.0.0.1, never a host name, so a tampered
// registry cannot point a producer at another machine.
func ParseEndpoint(s string) (Endpoint, error) {
	network, address, ok := strings.Cut(s, ":")
	if !ok || address == "" {
		return Endpoint{}, fmt.Errorf("%w: %q", ErrBadEndpoint, s)
	}
	switch network {
	case "unix":
		if !filepath.IsAbs(address) || len(address) > MaxUnixPath || filepath.Clean(address) != address {
			return Endpoint{}, fmt.Errorf("%w: socket path %q", ErrBadEndpoint, address)
		}
	case "tcp":
		host, port, err := net.SplitHostPort(address)
		n, perr := strconv.Atoi(port)
		if err != nil || host != "127.0.0.1" || perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return Endpoint{}, fmt.Errorf("%w: %q is not 127.0.0.1:<port>", ErrBadEndpoint, address)
		}
	default:
		return Endpoint{}, fmt.Errorf("%w: network %q", ErrBadEndpoint, network)
	}
	return Endpoint{Network: network, Address: address}, nil
}

// UnixPath is the socket path for the daemon whose registry prefix is
// prefix, or "" when it would exceed MaxUnixPath (use TCP).
func UnixPath(prefix string) string {
	p := prefix + ".notify"
	if len(p) > MaxUnixPath {
		return ""
	}
	return p
}

// Locator finds the running daemon's endpoint. internal/webui provides it
// (NotifyEndpoint, lane 2.2), reading the private registry; it returns an
// error wrapping ErrNoDaemon when no daemon is registered.
type Locator func(ctx context.Context) (Endpoint, error)

// Sender hands one event to the daemon. Lane 2.2's Client implements it.
// e must be normalized and valid; the daemon assigns Seq. Send returns nil
// once the daemon stored and published e, or an error wrapping ErrNoDaemon
// or ErrRejected, or a context error. A Client keeps one connection and is
// safe for concurrent use.
type Sender interface {
	Send(ctx context.Context, e bus.Event) error
}

// Acceptor is the daemon side behind the socket. Lane 2.2's server calls
// it for each publish Request after the handshake. The daemon's Acceptor
// checks Check, then that e.Repo is a repository it owns and e.Worktree
// one of its registered worktrees, then publishes through its bus, whose
// store interceptor appends as store.RoleOwner.
type Acceptor interface {
	Accept(ctx context.Context, e bus.Event) (bus.Event, error)
}

// Handshake frames, in order: Hello (client), Challenge (daemon), Proof
// (client), Ready (daemon).
type (
	// Hello opens a connection.
	Hello struct {
		V     int    `json:"v"`
		Nonce string `json:"nonce"` // client nonce, base64url, NonceBytes
	}
	// Challenge proves the daemon knows the key and sends its nonce.
	Challenge struct {
		V     int    `json:"v"`
		Nonce string `json:"nonce"` // daemon nonce
		Proof string `json:"proof"` // ServerProof(key, client nonce, daemon nonce)
		// Error is set instead of Nonce and Proof when the daemon refuses
		// the connection (ErrorBusy, ErrorVersion).
		Error *FrameError `json:"error,omitempty"`
	}
	// Proof proves the client knows the key.
	Proof struct {
		Proof string `json:"proof"` // ClientProof(key, daemon nonce, client nonce)
	}
	// Ready ends the handshake.
	Ready struct {
		OK    bool        `json:"ok"`
		Error *FrameError `json:"error,omitempty"`
	}
)

// Ops of a Request.
const (
	OpPublish = "publish" // Event is required
	OpPing    = "ping"    // no Event; checks the connection
)

// Request is one client frame after the handshake.
type Request struct {
	// ID is chosen by the client, increasing per connection.
	ID    uint64     `json:"id"`
	Op    string     `json:"op"`
	Event *bus.Event `json:"event,omitempty"`
}

// Response answers the Request with the same ID.
type Response struct {
	ID uint64 `json:"id"`
	OK bool   `json:"ok"`
	// Seq is the stored event's Seq (0 for a ping).
	Seq   uint64      `json:"seq,omitempty"`
	Error *FrameError `json:"error,omitempty"`
}

// FrameError is a refusal. Message is for logs; Code is for code.
type FrameError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Error codes.
const (
	ErrorBusy            = "busy"             // MaxConnections reached
	ErrorVersion         = "version"          // unsupported Hello.V
	ErrorAuth            = "auth"             // the client's proof is wrong
	ErrorFrame           = "frame"            // malformed or oversized frame; the daemon closes
	ErrorInvalidEvent    = "invalid_event"    // fails bus.Event.Validate, or carries Seq
	ErrorForbiddenType   = "forbidden_type"   // not a socket type (Allowed)
	ErrorForbiddenSource = "forbidden_source" // Source fails ValidSource
	ErrorUnknownRepo     = "unknown_repo"     // the daemon does not own that repository's store
	ErrorUnknownWorktree = "unknown_worktree" // not a worktree the daemon registered
	ErrorInternal        = "internal"         // publish failed
)

// socketTypes are the types a producer may send. Observations of git,
// configuration, approvals and GitHub come only from the daemon's own
// sources, so no process can forge them. Phase 3 adds "op.**", which the
// command Invoker of CLI and MCP processes publishes.
var socketTypes = []bus.Pattern{"hook.ran", "commit.created", "op.**"}

// Allowed reports whether t may be sent over the socket.
func Allowed(t bus.Type) bool {
	for _, p := range socketTypes {
		if p.Match(t) {
			return true
		}
	}
	return false
}

var sourceRe = regexp.MustCompile(`^(cli|mcp:[a-z0-9][a-z0-9-]{0,31}|hook:[a-z0-9][a-z0-9-]{0,31})$`)

// ValidSource reports whether source is one a socket producer may claim:
// "cli", "mcp:<client>" or "hook:<name>".
func ValidSource(source string) bool { return sourceRe.MatchString(source) }

// Check applies the rules the daemon enforces before Accept publishes e:
// a socket type, a producer source, no Seq, and a valid envelope. It
// returns the FrameError code, or "" when e passes.
func Check(e bus.Event) string {
	switch {
	case !Allowed(e.Type):
		return ErrorForbiddenType
	case !ValidSource(e.Source):
		return ErrorForbiddenSource
	case e.Seq != 0 || e.Validate() != nil:
		return ErrorInvalidEvent
	}
	return ""
}

// DeriveKey derives the socket key from the instance key, so the control
// channel's AES key is never used directly as a MAC key.
func DeriveKey(instanceKey []byte) []byte {
	m := hmac.New(sha256.New, instanceKey)
	m.Write([]byte("agentflow-notify/v1"))
	return m.Sum(nil)
}

func proof(key []byte, role, first, second string) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%s\x00%s\x00%s", role, first, second)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// ServerProof is the daemon's Challenge.Proof for the two nonces.
func ServerProof(key []byte, clientNonce, serverNonce string) string {
	return proof(key, "server", clientNonce, serverNonce)
}

// ClientProof is the client's Proof.Proof for the two nonces. The role and
// nonce order differ from ServerProof, so neither side can replay the
// other's proof.
func ClientProof(key []byte, serverNonce, clientNonce string) string {
	return proof(key, "client", serverNonce, clientNonce)
}

// VerifyProof compares proofs in constant time.
func VerifyProof(want, got string) bool { return hmac.Equal([]byte(want), []byte(got)) }
