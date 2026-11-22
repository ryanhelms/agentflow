package codexhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider/native"
)

const (
	maxWriteLeases   = 8
	writeReadbackTTL = 30 * time.Second
)

// Writer pins one live app-server session during each Devflow confirmation.
type Writer struct {
	dir      string
	mu       sync.Mutex
	leases   map[[sha256.Size]byte]*writeLease
	readback map[string]*writeLease
}

type writeLease struct {
	binding native.WriteBinding
	host    *host
	thread  string
	target  *serverStatus
	cancel  context.CancelFunc
	timer   *time.Timer
	payload [sha256.Size]byte
}

func NewWriter(dir string) *Writer {
	return &Writer{dir: dir, leases: make(map[[sha256.Size]byte]*writeLease), readback: make(map[string]*writeLease)}
}

var _ native.WriteHost = (*Writer)(nil)

// Close ends any outstanding previews. Already consumed bindings are never
// recreated by this method.
func (w *Writer) Close() {
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

func writeKey(call native.Call) ([sha256.Size]byte, error) {
	raw, err := json.Marshal(call)
	if err != nil || len(raw) >= maxFrameBytes || call.Provider == "" || call.Server == "" || call.Tool == "" {
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

func (w *Writer) Prepare(ctx context.Context, call native.Call) (native.WriteBinding, error) {
	var zero native.WriteBinding
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if os.Getenv(activeEnvKey) != "" || strings.EqualFold(call.Server, "devflow") ||
		strings.HasPrefix(strings.ToLower(call.Tool), "devflow_") {
		return zero, native.ErrDenied
	}
	if providerHosts[call.Provider] == "" {
		return zero, native.ErrUnavailable
	}
	key, err := writeKey(call)
	if err != nil {
		return zero, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if existing := w.leases[key]; existing != nil {
		if existing.host.ctx.Err() != nil {
			w.drop(key, existing)
			return zero, errors.New("native provider connection expired; request a fresh preview")
		}
		return existing.binding, nil
	}
	if len(w.leases)+len(w.readback) >= maxWriteLeases {
		return zero, errors.New("too many outstanding native write previews")
	}

	hold, cancel := context.WithTimeout(context.WithoutCancel(ctx), policy.TokenTTL+callTimeout)
	ready, stop := context.WithTimeout(ctx, callTimeout)
	defer stop()
	stopCancel := context.AfterFunc(ready, cancel)
	defer stopCancel()
	h, err := startHost(hold, w.dir)
	if err != nil {
		cancel()
		return zero, writeTransportError(call.Provider, err, false)
	}
	lease := &writeLease{host: h, cancel: cancel}
	keep := false
	defer func() {
		if !keep {
			lease.close()
		}
	}()
	lease.thread, err = h.startThread(w.dir)
	if err != nil {
		return zero, writeTransportError(call.Provider, err, false)
	}
	lease.target, err = h.findTool(lease.thread, call.Server, call.Tool)
	if err != nil {
		return zero, writeTransportError(call.Provider, err, false)
	}
	if err := verifyWriteHost(call.Provider, lease.target); err != nil {
		return zero, err
	}
	if err := lease.target.validateArguments(call); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return zero, err
	}
	lease.binding = native.WriteBinding{
		Provider: call.Provider, Server: call.Server, Tool: call.Tool,
		Origin: lease.target.HTTPOrigin, Connection: hex.EncodeToString(nonce[:]),
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

func (w *Writer) Write(ctx context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
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
		return nil, fmt.Errorf("%w: write binding expired, changed, or was consumed", native.ErrDenied)
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
		return nil, err
	}
	if err := lease.host.ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: prepared provider connection ended", native.ErrDenied)
	}
	limit, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	stop := context.AfterFunc(limit, lease.cancel)
	defer stop()
	raw, err := lease.host.callTool(lease.thread, call.Server, call.Tool, call.Arguments, lease.target)
	if err != nil {
		return nil, writeTransportError(call.Provider, err, true)
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

func (w *Writer) Rebind(ctx context.Context, binding native.WriteBinding, call native.Call) (native.WriteBinding, error) {
	var zero native.WriteBinding
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if binding.Provider != call.Provider || binding.Server != call.Server || binding.Tool != call.Tool {
		return zero, native.ErrDenied
	}
	key, err := writeKey(call)
	if err != nil {
		return zero, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, lease := range w.leases {
		if lease.binding == binding && lease.host.ctx.Err() == nil {
			if err := lease.target.validateArguments(call); err != nil {
				return zero, err
			}
			lease.payload = key
			lease.binding.Payload = hex.EncodeToString(key[:])
			return lease.binding, nil
		}
	}
	return zero, fmt.Errorf("%w: no live provider write binding", native.ErrDenied)
}

func (w *Writer) ReadForWrite(ctx context.Context, call native.Call) (json.RawMessage, error) {
	if !allowedBoundRead(call) {
		return nil, native.ErrDenied
	}
	// The catalogue resolves the server with the client's own OAuth login.
	// Unlike a read continuation, no input results are accepted here.
	return verifiedRead(ctx, w.dir, call)
}

func verifiedRead(ctx context.Context, dir string, call native.Call) (json.RawMessage, error) {
	if os.Getenv(activeEnvKey) != "" || strings.EqualFold(call.Server, "devflow") {
		return nil, native.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	h, err := startHost(ctx, dir)
	if err != nil {
		return nil, writeTransportError(call.Provider, err, false)
	}
	defer h.close()
	thread, err := h.startThread(dir)
	if err != nil {
		return nil, writeTransportError(call.Provider, err, false)
	}
	target, err := h.findTool(thread, call.Server, call.Tool)
	if err != nil {
		return nil, writeTransportError(call.Provider, err, false)
	}
	if err := verifyWriteHost(call.Provider, target); err != nil {
		return nil, err
	}
	if err := target.validateArguments(call); err != nil {
		return nil, err
	}
	raw, err := h.callTool(thread, call.Server, call.Tool, call.Arguments, target)
	return raw, writeTransportError(call.Provider, err, false)
}

func verifyWriteHost(provider string, target *serverStatus) error {
	if target.HTTPOrigin == "" {
		return fmt.Errorf("%w: the MCP host did not attest the provider origin", native.ErrUnavailable)
	}
	if !trustedWriteOrigin(provider, target.HTTPOrigin) {
		return fmt.Errorf("%w: MCP server does not match the official provider origin", native.ErrDenied)
	}
	switch target.AuthStatus {
	case "oAuth":
		return nil
	case "notLoggedIn":
		return fmt.Errorf("%s: %w; start the MCP client's OAuth flow", provider, native.ErrSignIn)
	default:
		return fmt.Errorf("%w: the host did not verify an OAuth session", native.ErrUnavailable)
	}
}

var boundReads = map[string][]string{
	"jira":          {"atlassianUserInfo", "getAccessibleAtlassianResources", "getJiraIssue", "listJiraIssueTransitions", "listJiraIssueRemoteIssueLinks", "getJiraCurrentUser", "listJiraIssueComments"},
	"confluence":    {"atlassianUserInfo", "getAccessibleAtlassianResources", "getConfluenceContent", "getConfluenceContentVersion"},
	"asana":         {"get_task", "get_user", "get_project"},
	"github":        {"get_me", "issue_read", "search_repositories"},
	"github-issues": {"get_me", "issue_read", "search_repositories"},
	"slack":         {"slack_read_user_profile", "slack_list_user_channels", "slack_read_channel", "slack_read_thread"},
}

func allowedBoundRead(call native.Call) bool {
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

func (s *serverStatus) validateArguments(call native.Call) error {
	var spec struct {
		InputSchema *jsonschema.Schema `json:"inputSchema"`
	}
	if err := json.Unmarshal(s.Tools[call.Tool], &spec); err != nil || spec.InputSchema == nil {
		return fmt.Errorf("%w: the provider tool did not supply an input schema", native.ErrUnavailable)
	}
	resolved, err := spec.InputSchema.Resolve(nil)
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	if err != nil || resolved.Validate(args) != nil {
		return fmt.Errorf("%w: provider schema rejected the prepared arguments", native.ErrUnavailable)
	}
	return nil
}

func (w *Writer) ReadBound(ctx context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	if binding.Provider != call.Provider || binding.Server != call.Server || !allowedBoundRead(call) {
		return nil, native.ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if lease := w.readback[binding.Connection]; lease != nil && lease.binding == binding {
		raw, err := lease.read(ctx, call)
		if err != nil && !errors.Is(err, native.ErrUnavailable) {
			w.dropReadback(binding.Connection, lease)
		}
		return raw, err
	}
	for key, lease := range w.leases {
		if lease.binding != binding {
			continue
		}
		raw, err := lease.read(ctx, call)
		if err != nil && !errors.Is(err, native.ErrUnavailable) {
			w.drop(key, lease)
		}
		return raw, err
	}
	return nil, fmt.Errorf("%w: no live provider write binding", native.ErrDenied)
}

func (l *writeLease) read(ctx context.Context, call native.Call) (json.RawMessage, error) {
	if err := l.target.checkTool(call.Tool); err != nil {
		return nil, writeTransportError(call.Provider, err, false)
	}
	if err := l.target.validateArguments(call); err != nil {
		return nil, err
	}
	limit, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	stop := context.AfterFunc(limit, l.cancel)
	defer stop()
	raw, err := l.host.callTool(l.thread, call.Server, call.Tool, call.Arguments, l.target)
	return raw, writeTransportError(call.Provider, err, false)
}

func (w *Writer) dropReadback(connection string, lease *writeLease) {
	if w.readback[connection] != lease {
		return
	}
	delete(w.readback, connection)
	lease.close()
}

func (w *Writer) drop(key [sha256.Size]byte, lease *writeLease) {
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
	l.host.close()
	l.cancel()
}

func writeTransportError(provider string, err error, dispatched bool) error {
	if err == nil {
		return nil
	}
	var hostErr *Error
	if errors.As(err, &hostErr) {
		switch hostErr.Kind {
		case Authentication:
			err = fmt.Errorf("%s: %w; start the MCP client's OAuth flow", provider, native.ErrSignIn)
		case Permission:
			err = fmt.Errorf("%s: %w", provider, native.ErrDenied)
		case Unavailable:
			if !dispatched {
				return native.ErrUnavailable
			}
		}
	}
	if dispatched {
		return errors.Join(native.ErrWriteUncertain, err)
	}
	return err
}
