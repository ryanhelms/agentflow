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

	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider/native"
)

const maxWriteLeases = 8

// Writer pins one live app-server session during each Devflow confirmation.
type Writer struct {
	dir    string
	mu     sync.Mutex
	leases map[[sha256.Size]byte]*writeLease
}

type writeLease struct {
	binding native.WriteBinding
	host    *host
	thread  string
	target  *serverStatus
	cancel  context.CancelFunc
	timer   *time.Timer
}

func NewWriter(dir string) *Writer {
	return &Writer{dir: dir, leases: make(map[[sha256.Size]byte]*writeLease)}
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
	if len(w.leases) >= maxWriteLeases {
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
	if lease.target.HTTPOrigin == "" {
		return zero, fmt.Errorf("%w: the MCP host did not attest the provider origin", native.ErrUnavailable)
	}
	if !trustedWriteOrigin(call.Provider, lease.target.HTTPOrigin) {
		return zero, fmt.Errorf("%w: MCP server does not match the official provider origin", native.ErrDenied)
	}
	switch lease.target.AuthStatus {
	case "oAuth":
	case "notLoggedIn":
		return zero, fmt.Errorf("%s: %w; start the MCP client's OAuth flow", call.Provider, native.ErrSignIn)
	default:
		return zero, fmt.Errorf("%w: the host did not verify an OAuth session", native.ErrUnavailable)
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
	}
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
	lease := w.leases[key]
	if lease == nil || lease.binding != binding {
		w.mu.Unlock()
		return nil, fmt.Errorf("%w: write binding expired, changed, or was consumed", native.ErrDenied)
	}
	delete(w.leases, key)
	w.mu.Unlock()
	defer lease.close()
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
	return raw, writeTransportError(call.Provider, err, true)
}

var boundReads = map[string][]string{
	"jira":          {"atlassianUserInfo", "getAccessibleAtlassianResources", "getJiraIssue", "getTransitionsForJiraIssue", "listJiraIssueComments"},
	"confluence":    {"atlassianUserInfo", "getAccessibleAtlassianResources", "getConfluenceContent"},
	"asana":         {"get_task", "get_user", "get_project"},
	"github":        {"get_me", "issue_read"},
	"github-issues": {"get_me", "issue_read"},
	"slack":         {"slack_read_user_profile", "slack_list_user_channels", "slack_read_channel", "slack_read_thread"},
}

func (w *Writer) ReadBound(ctx context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	if binding.Provider != call.Provider || binding.Server != call.Server || !slices.Contains(boundReads[call.Provider], call.Tool) {
		return nil, native.ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, lease := range w.leases {
		if lease.binding != binding {
			continue
		}
		if err := lease.target.checkTool(call.Tool); err != nil {
			return nil, writeTransportError(call.Provider, err, false)
		}
		limit, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		stop := context.AfterFunc(limit, lease.cancel)
		defer stop()
		raw, err := lease.host.callTool(lease.thread, call.Server, call.Tool, call.Arguments, lease.target)
		if err != nil {
			w.drop(key, lease)
		}
		return raw, writeTransportError(call.Provider, err, false)
	}
	return nil, fmt.Errorf("%w: no live provider write binding", native.ErrDenied)
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
			return fmt.Errorf("%s: %w; start the MCP client's OAuth flow", provider, native.ErrSignIn)
		case Permission:
			return fmt.Errorf("%s: %w", provider, native.ErrDenied)
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
