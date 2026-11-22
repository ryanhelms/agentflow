package native

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/nscaledev/devflow/internal/policy"
)

const (
	maxWriteLeases   = 8
	maxWriteCallSize = 8 << 20
	writeCallTimeout = 60 * time.Second
	writeReadbackTTL = 30 * time.Second
)

// WriteSession is an authenticated connection to one official provider.
// Implementations must never retry a mutation or change its authenticated account.
type WriteSession interface {
	Origin() string
	Check(Call) error
	Call(context.Context, Call) (json.RawMessage, error)
	Close()
}

// SessionWriter retains the same provider session through approval, dispatch
// and receipt verification for each exact native write.
type SessionWriter struct {
	owner    string
	open     func(context.Context, Call) (WriteSession, error)
	mu       sync.Mutex
	leases   map[[sha256.Size]byte]*writeLease
	readback map[string]*writeLease
}

type writeLease struct {
	binding WriteBinding
	session WriteSession
	ctx     context.Context
	cancel  context.CancelFunc
	timer   *time.Timer
	payload [sha256.Size]byte
}

func NewSessionWriter(owner string, open func(context.Context, Call) (WriteSession, error)) *SessionWriter {
	return &SessionWriter{owner: owner, open: open, leases: make(map[[sha256.Size]byte]*writeLease), readback: make(map[string]*writeLease)}
}

var _ WriteHost = (*SessionWriter)(nil)

// Close ends any outstanding previews. Already consumed bindings are never
// recreated by this method.
func (w *SessionWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, lease := range w.leases {
		delete(w.leases, key)
		lease.close()
	}
	for connection, lease := range w.readback {
		delete(w.readback, connection)
		lease.close()
	}
}

func writeKey(call Call) ([sha256.Size]byte, error) {
	raw, err := json.Marshal(call)
	if err != nil || len(raw) >= maxWriteCallSize || call.Provider == "" || call.Server == "" || call.Tool == "" {
		return [sha256.Size]byte{}, errors.New("invalid native provider write")
	}
	return sha256.Sum256(raw), nil
}

var providerHosts = map[string]string{
	"jira": "mcp.atlassian.com", "confluence": "mcp.atlassian.com",
	"asana": "mcp.asana.com", "github": "api.githubcopilot.com",
	"github-issues": "api.githubcopilot.com", "slack": "mcp.slack.com",
}

func trustedWriteOrigin(provider, origin string) bool {
	u, err := url.Parse(origin)
	want := providerHosts[provider]
	return err == nil && want != "" && u.Scheme == "https" && strings.EqualFold(u.Host, want) &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" &&
		(u.Path == "" || u.Path == "/")
}

func (w *SessionWriter) Prepare(ctx context.Context, call Call) (WriteBinding, error) {
	var zero WriteBinding
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if strings.EqualFold(call.Server, "devflow") ||
		strings.HasPrefix(strings.ToLower(call.Tool), "devflow_") {
		return zero, ErrDenied
	}
	if providerHosts[call.Provider] == "" {
		return zero, ErrUnavailable
	}
	key, err := writeKey(call)
	if err != nil {
		return zero, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if existing := w.leases[key]; existing != nil {
		if existing.ctx.Err() != nil {
			w.drop(key, existing)
			return zero, errors.New("native provider connection expired; request a fresh preview")
		}
		return existing.binding, nil
	}
	if len(w.leases)+len(w.readback) >= maxWriteLeases {
		return zero, errors.New("too many outstanding native write previews")
	}

	hold, cancel := context.WithTimeout(context.WithoutCancel(ctx), policy.TokenTTL+writeCallTimeout)
	ready, stop := context.WithTimeout(ctx, writeCallTimeout)
	defer stop()
	stopCancel := context.AfterFunc(ready, cancel)
	defer stopCancel()
	session, err := w.open(hold, call)
	if err != nil {
		cancel()
		return zero, err
	}
	lease := &writeLease{session: session, ctx: hold, cancel: cancel}
	keep := false
	defer func() {
		if !keep {
			lease.close()
		}
	}()
	if err := checkWriteSession(session, call); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return zero, err
	}
	lease.binding = WriteBinding{
		Provider: call.Provider, Owner: w.owner, Server: call.Server, Tool: call.Tool,
		Origin: session.Origin(), Connection: hex.EncodeToString(nonce[:]),
		Payload: hex.EncodeToString(key[:]),
	}
	lease.payload = key
	lease.timer = time.AfterFunc(policy.TokenTTL, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.drop(key, lease)
	})
	w.leases[key] = lease
	keep = true
	return lease.binding, nil
}

func (w *SessionWriter) Write(ctx context.Context, binding WriteBinding, call Call) (json.RawMessage, error) {
	key, err := writeKey(call)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var lease *writeLease
	for lookup, candidate := range w.leases {
		if candidate.binding == binding && candidate.payload == key {
			lease = candidate
			delete(w.leases, lookup)
			break
		}
	}
	if lease == nil {
		return nil, errors.Join(ErrWriteNotDispatched, fmt.Errorf("%w: write binding expired, changed, or was consumed", ErrDenied))
	}
	if lease.timer != nil {
		lease.timer.Stop()
	}
	retain := false
	defer func() {
		if !retain {
			lease.close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrWriteNotDispatched, err)
	}
	if err := lease.ctx.Err(); err != nil {
		return nil, errors.Join(ErrWriteNotDispatched, fmt.Errorf("%w: prepared provider connection ended", ErrDenied))
	}
	limit, cancel := context.WithTimeout(ctx, writeCallTimeout)
	defer cancel()
	raw, err := lease.session.Call(limit, call)
	if err != nil {
		if errors.Is(err, ErrWriteNotDispatched) {
			return nil, err
		}
		if errors.Is(err, ErrUnavailable) {
			return nil, fmt.Errorf("%w: provider capability became unavailable after dispatch", ErrWriteUncertain)
		}
		return nil, errors.Join(ErrWriteUncertain, err)
	}
	// Receipt verification may need one or more bounded reads on this same
	// connection. It remains consumed for all writes.
	w.readback[binding.Connection] = lease
	lease.timer = time.AfterFunc(writeReadbackTTL, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.dropReadback(binding.Connection, lease)
	})
	retain = true
	return raw, nil
}

func (w *SessionWriter) Rebind(ctx context.Context, binding WriteBinding, call Call) (WriteBinding, error) {
	var zero WriteBinding
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if binding.Provider != call.Provider || binding.Server != call.Server || binding.Tool != call.Tool {
		return zero, ErrDenied
	}
	key, err := writeKey(call)
	if err != nil {
		return zero, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, lease := range w.leases {
		if lease.binding == binding && lease.ctx.Err() == nil {
			if err := lease.session.Check(call); err != nil {
				return zero, err
			}
			lease.payload = key
			lease.binding.Payload = hex.EncodeToString(key[:])
			return lease.binding, nil
		}
	}
	return zero, fmt.Errorf("%w: no live provider write binding", ErrDenied)
}

func (w *SessionWriter) ReadForWrite(ctx context.Context, call Call) (json.RawMessage, error) {
	if !allowedBoundRead(call) {
		return nil, ErrDenied
	}
	// The catalogue resolves the server with the client's own OAuth login.
	// Unlike a read continuation, no input results are accepted here.
	ctx, cancel := context.WithTimeout(ctx, writeCallTimeout)
	defer cancel()
	session, err := w.open(ctx, call)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	if err := checkWriteSession(session, call); err != nil {
		return nil, err
	}
	return session.Call(ctx, call)
}

func checkWriteSession(session WriteSession, call Call) error {
	if session.Origin() == "" {
		return fmt.Errorf("%w: the MCP host did not attest the provider origin", ErrUnavailable)
	}
	if !trustedWriteOrigin(call.Provider, session.Origin()) {
		return fmt.Errorf("%w: MCP server does not match the official provider origin", ErrDenied)
	}
	return session.Check(call)
}

var boundReads = map[string][]string{
	"jira":          {"atlassianUserInfo", "getAccessibleAtlassianResources", "getJiraIssue", "listJiraIssueTransitions", "listJiraIssueRemoteIssueLinks", "getJiraCurrentUser", "listJiraIssueComments", "listJiraProjectIssueTypesMetadata"},
	"confluence":    {"atlassianUserInfo", "getAccessibleAtlassianResources", "getConfluenceContent", "getConfluenceContentVersion"},
	"asana":         {"get_task", "get_user", "get_project"},
	"github":        {"get_me", "issue_read", "search_repositories"},
	"github-issues": {"get_me", "issue_read", "search_repositories"},
	"slack":         {"slack_read_user_profile", "slack_list_user_channels", "slack_read_channel", "slack_read_thread"},
}

func allowedBoundRead(call Call) bool {
	if slices.Contains(boundReads[call.Provider], call.Tool) {
		return true
	}
	if call.Tool != "executeRead" {
		return false
	}
	name, _ := call.Arguments["name"].(string)
	switch call.Provider {
	case "jira":
		return name == "listJiraIssueRemoteIssueLinks" || name == "listJiraIssueTransitions" || name == "getJiraCurrentUser"
	case "confluence":
		return name == "getConfluenceContentVersion"
	default:
		return false
	}
}

// ValidateInputSchema verifies exact arguments locally. Resolution never fetches
// references from a network URL.
func ValidateInputSchema(schema *jsonschema.Schema, args map[string]any) error {
	if schema == nil {
		return fmt.Errorf("%w: the provider tool did not supply an input schema", ErrDenied)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("%w: the provider tool supplied an invalid input schema", ErrDenied)
	}
	if args == nil {
		args = map[string]any{}
	}
	if resolved.Validate(args) != nil {
		return fmt.Errorf("%w: provider schema rejected the prepared arguments", ErrUnavailable)
	}
	return nil
}

func (w *SessionWriter) ReadBound(ctx context.Context, binding WriteBinding, call Call) (json.RawMessage, error) {
	if binding.Provider != call.Provider || binding.Server != call.Server || !allowedBoundRead(call) {
		return nil, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if lease := w.readback[binding.Connection]; lease != nil && lease.binding == binding {
		raw, err := lease.read(ctx, call)
		if err != nil && !errors.Is(err, ErrUnavailable) {
			w.dropReadback(binding.Connection, lease)
		}
		return raw, err
	}
	for key, lease := range w.leases {
		if lease.binding != binding {
			continue
		}
		raw, err := lease.read(ctx, call)
		if err != nil && !errors.Is(err, ErrUnavailable) {
			w.drop(key, lease)
		}
		return raw, err
	}
	return nil, fmt.Errorf("%w: no live provider write binding", ErrDenied)
}

func (l *writeLease) read(ctx context.Context, call Call) (json.RawMessage, error) {
	if err := l.session.Check(call); err != nil {
		return nil, err
	}
	limit, cancel := context.WithTimeout(ctx, writeCallTimeout)
	defer cancel()
	return l.session.Call(limit, call)
}

func (w *SessionWriter) dropReadback(connection string, lease *writeLease) {
	if w.readback[connection] != lease {
		return
	}
	delete(w.readback, connection)
	lease.close()
}

func (w *SessionWriter) drop(key [sha256.Size]byte, lease *writeLease) {
	if w.leases[key] != lease {
		return
	}
	delete(w.leases, key)
	lease.close()
}

func (l *writeLease) close() {
	if l.timer != nil {
		l.timer.Stop()
	}
	l.session.Close()
	l.cancel()
}
