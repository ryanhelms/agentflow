package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// Version is the "jsonrpc" member of every message.
const Version = "2.0"

// Framing and connection limits. A Conn uses Limits; zero fields take these.
const (
	// DefaultMaxFrame is the largest frame, newline excluded. A larger one
	// is a protocol error and closes the connection.
	DefaultMaxFrame = 1 << 20
	// DefaultMaxInFlight is how many requests the peer may have waiting
	// for an answer from this side. One more gets CodeBusy.
	DefaultMaxInFlight = 4
	// DefaultCallTimeout bounds a Call whose context has no deadline.
	DefaultCallTimeout = 10 * time.Second
	// DefaultWriteTimeout bounds writing one frame.
	DefaultWriteTimeout = 2 * time.Second
	// DefaultShutdownGrace is how long Process.Stop waits after closing
	// stdin before it kills the process (group).
	DefaultShutdownGrace = 2 * time.Second
	// DefaultStderrLimit is how many bytes of the child's stderr the
	// launcher keeps (the newest ones).
	DefaultStderrLimit = 64 << 10
)

// Limits configure a Conn. Zero values take the defaults above.
type Limits struct {
	MaxFrame     int
	MaxInFlight  int
	CallTimeout  time.Duration
	WriteTimeout time.Duration
}

// ID is a request ID: a JSON number or string, kept as its raw JSON so it
// is echoed exactly. A notification has no ID.
type ID = json.RawMessage

// Message is one frame. Exactly one shape is valid:
//
//   - request: ID, Method, optional Params;
//   - notification: Method, optional Params, no ID;
//   - response: ID and exactly one of Result or Error.
//
// The reader rejects anything else with CodeInvalidRequest, and a frame
// that is not JSON with CodeParseError. Unknown members are ignored.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      ID              `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object. Data, when present, is
// {"code": "<fixed code>"}: a string code that callers can switch on
// (ErrorData). Message is text for a person and never carries a secret, a
// path or a peer's own text.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// ErrorData is the shape of Error.Data.
type ErrorData struct {
	Code string `json:"code"`
}

// Standard JSON-RPC 2.0 codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// agentflow server error codes (the -32000 to -32099 range).
const (
	// CodeForbidden: the method or its arguments are outside what this
	// peer may do (a plugin asking for an event type or command it was not
	// granted). Data.code says which rule.
	CodeForbidden = -32001
	// CodeRateLimited: too many calls of this method in the window.
	CodeRateLimited = -32002
	// CodeBusy: too many requests in flight.
	CodeBusy = -32003
	// CodeTimeout: the work behind the request timed out.
	CodeTimeout = -32004
	// CodeShuttingDown: the connection is closing.
	CodeShuttingDown = -32005
)

// Errors returned by Conn.
var (
	// ErrClosed: the connection closed (EOF, Close, a protocol error, or
	// the process exited) before the answer arrived.
	ErrClosed = errors.New("jsonrpc: connection closed")
	// ErrFrameTooLarge: a frame exceeded Limits.MaxFrame. It closes the
	// connection.
	ErrFrameTooLarge = errors.New("jsonrpc: frame too large")
	// ErrProtocol: the peer sent a frame that is not valid JSON-RPC 2.0,
	// or a response to an ID this side never sent. It closes the
	// connection.
	ErrProtocol = errors.New("jsonrpc: protocol error")
)

// Request is an incoming request or notification, given to a Handler.
type Request struct {
	// ID is nil for a notification.
	ID     ID
	Method string
	Params json.RawMessage
}

// Notification reports whether r expects no answer.
func (r *Request) Notification() bool { return len(r.ID) == 0 }

// Handler answers the peer's requests and notifications. A request runs on
// its own goroutine, at most Limits.MaxInFlight at once. Notifications are
// not counted in MaxInFlight: they run in arrival order on one goroutine,
// and when enough wait the Conn stops reading until the Handler catches up,
// so a notification Handler must not Call on the same Conn. ctx ends when
// the connection closes. For a notification the result is ignored. An error
// of type *Error is sent as is; any other error becomes CodeInternalError
// with the message "internal error" (its text is never sent).
type Handler interface {
	Handle(ctx context.Context, r *Request) (result any, err error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, r *Request) (any, error)

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, r *Request) (any, error) { return f(ctx, r) }

// Conn is one bidirectional JSON-RPC connection over a byte stream: either
// side may send requests and notifications. Frames are one JSON object per
// line ("\n"-terminated, no embedded newlines), written atomically.
type Conn interface {
	// Call sends a request and decodes the result into result (which may
	// be nil). A peer error is returned as *Error. The context, or
	// Limits.CallTimeout when it has no deadline, bounds the wait; on
	// timeout the late answer is discarded.
	Call(ctx context.Context, method string, params, result any) error
	// Notify sends a notification.
	Notify(ctx context.Context, method string, params any) error
	// Close stops reading, fails waiting Calls with ErrClosed and closes
	// the writer. It is safe to call more than once.
	Close() error
	// Done is closed when the connection ends for any reason; Err then
	// says why (nil after Close, io.EOF, ErrFrameTooLarge, ErrProtocol).
	Done() <-chan struct{}
	Err() error
}

// Lane 5.1 adds the constructor
//
//	func NewConn(r io.Reader, w io.WriteCloser, h Handler, l Limits) Conn
//
// It starts reading at once. h may be nil: every request then gets
// CodeMethodNotFound. jsonrpctest.Pipe gives two connected Conns for tests.

// LaunchSpec describes a child process to start (Launch, lane 5.1).
type LaunchSpec struct {
	// Path is the absolute path of the executable. Launch never searches
	// PATH and refuses a relative path, a path that does not resolve
	// (symbolic links included) to a regular file inside Root, or a file
	// that is not executable.
	Path string
	// Root is the directory Path must stay inside: the plugin's installed
	// copy. It is also the working directory unless Dir is set.
	Root string
	// Dir is the working directory, inside Root or the data directory.
	Dir  string
	Args []string
	// Env is the complete environment of the child, normally from
	// ScrubbedEnv. Launch refuses a name that is not allowed by
	// EnvAllowed.
	Env []string
	// Stderr receives the child's stderr line by line as it arrives, each
	// line cut at 4 KiB, at most StderrLimit bytes in all; then one
	// "[stderr truncated]" line and nothing more. The newest StderrLimit
	// bytes stay available through StderrTail. Nil discards it.
	Stderr io.Writer
	// StderrLimit, Limits and ShutdownGrace take the defaults when zero.
	StderrLimit   int
	Limits        Limits
	ShutdownGrace time.Duration
	// Handler answers the child's requests.
	Handler Handler
}

// Process is a started child with its connection (Launch, lane 5.1):
//
//	func Launch(ctx context.Context, s LaunchSpec) (Process, error)
//
// On Unix the child runs in its own process group, and Stop and Kill signal
// the whole group. On Windows only the child itself is killed: job
// objects are left out of phase 5 (ADR, "Phase 5 decisions", 9).
type Process interface {
	Conn() Conn
	// PID is the child's process ID, for logs only.
	PID() int
	// Stop closes stdin, waits ShutdownGrace for the child to exit, then
	// kills it. It returns the exit error, if any.
	Stop() error
	// Kill kills the child (group) at once.
	Kill() error
	// Exited is closed when the child has exited and its pipes are
	// drained; ExitCode is then valid (-1 when killed by a signal).
	Exited() <-chan struct{}
	ExitCode() int
}

// EnvAllowlist lists the variable names a child may inherit from the
// parent's environment. Nothing else passes: no token, key, proxy
// credential, client session ID or AGENTFLOW_* variable of the parent.
// ScrubbedEnv (lane 5.1) builds the child environment from it.
var EnvAllowlist = []string{
	// Unix
	"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TZ",
	// Windows: needed to start any process
	"SystemRoot", "SystemDrive", "COMSPEC", "PATHEXT", "WINDIR",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// Variables ScrubbedEnv sets itself rather than inherits.
const (
	// EnvPath: a fixed system PATH ("/usr/bin:/bin:/usr/sbin:/sbin" on
	// Unix; "%SystemRoot%\System32;%SystemRoot%" on Windows), never the
	// parent's.
	EnvPath = "PATH"
	// EnvHome, EnvTmp: under the child's data directory, so a library that
	// reads ~/.netrc, ~/.config/gh or a cloud CLI's credentials finds
	// nothing. The same directory is USERPROFILE, APPDATA and LOCALAPPDATA
	// on Windows, and XDG_CONFIG_HOME, XDG_CACHE_HOME, XDG_DATA_HOME
	// (subdirectories) on Unix.
	EnvHome = "HOME"
	EnvTmp  = "TMPDIR"
)

// Plugin variables the plugin host adds (EnvAllowed accepts them).
const (
	EnvPluginID       = "AGENTFLOW_PLUGIN_ID"
	EnvPluginDir      = "AGENTFLOW_PLUGIN_DIR"  // the installed copy, read-only
	EnvPluginData     = "AGENTFLOW_PLUGIN_DATA" // the plugin's own writable directory
	EnvPluginProtocol = "AGENTFLOW_PLUGIN_PROTOCOL"
)

// Lane 5.1 adds
//
//	func ScrubbedEnv(parent []string, data string, extra map[string]string) ([]string, error)
//	func EnvAllowed(name string) bool
//
// parent is os.Environ(); data is the child's data directory (absolute,
// owned by this user, mode 0700); extra are the AGENTFLOW_PLUGIN_*
// variables. The result is sorted. It fails for an extra name EnvAllowed
// refuses. EnvAllowed accepts EnvAllowlist, the variables ScrubbedEnv sets
// itself and the AGENTFLOW_PLUGIN_* names above, on Windows ignoring case.
