package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/notifysock"
)

// The daemon's notify socket (ADR, "Phase 2, Local socket"): CLI, MCP and
// hook processes hand it hook.ran and commit.created events for stores the
// daemon owns. serveRegistered puts a notifySetup in the context it starts
// the event runtime with; notifyExtension listens while serveRegistered
// holds the instance lock, and serveRegistered then advertises the
// endpoint in the registry file's "notify" field.

func init() {
	runtimeExtensions = append(runtimeExtensions, func() runtimeExtension { return &notifyExtension{} })
}

type notifySetupKey struct{}

// notifySetup carries the registry prefix and instance key to the
// extension, and the endpoint back to serveRegistered.
type notifySetup struct {
	prefix string
	key    []byte

	mu       sync.Mutex
	endpoint string
}

func withNotifySetup(ctx context.Context, s *notifySetup) context.Context {
	return context.WithValue(ctx, notifySetupKey{}, s)
}

func (s *notifySetup) setEndpoint(e string) {
	s.mu.Lock()
	s.endpoint = e
	s.mu.Unlock()
}

// Endpoint is the registry form of the listening endpoint, or "".
func (s *notifySetup) Endpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
}

// newNotifySetup decodes the instance key for the socket handshake.
func newNotifySetup(prefix string, i instance) (*notifySetup, error) {
	key, err := base64.RawURLEncoding.DecodeString(i.Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid instance key")
	}
	return &notifySetup{prefix: prefix, key: key}, nil
}

type notifyExtension struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (x *notifyExtension) start(rt *eventRuntime) error {
	setup, _ := rt.ctx.Value(notifySetupKey{}).(*notifySetup)
	if setup == nil {
		return nil // an event runtime without a registry entry, as in tests
	}
	l, ep, err := notifysock.Listen(setup.prefix)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(rt.ctx)
	x.cancel, x.done = cancel, make(chan struct{})
	setup.setEndpoint(ep.String())
	go func() {
		defer close(x.done)
		if err := notifysock.Serve(ctx, l, setup.key, daemonAcceptor{rt}); err != nil {
			fmt.Fprintln(os.Stderr, "agentflow: notify socket stopped:", err)
		}
	}()
	return nil
}

func (x *notifyExtension) attached(*repoEvents, registeredWorktree) {}

func (x *notifyExtension) stop() {
	if x.cancel != nil {
		x.cancel()
		<-x.done
	}
}

// daemonAcceptor publishes a socket event when the daemon owns its
// repository's store and registered its worktree. notifysock.Serve has
// already applied notifysock.Check.
type daemonAcceptor struct{ rt *eventRuntime }

func (a daemonAcceptor) Accept(ctx context.Context, e bus.Event) (bus.Event, error) {
	if code := notifysock.Check(e); code != "" {
		return bus.Event{}, notifysock.Reject(code, "")
	}
	a.rt.mu.Lock()
	r := a.rt.byID[e.Repo]
	a.rt.mu.Unlock()
	if r == nil {
		return bus.Event{}, notifysock.Reject(notifysock.ErrorUnknownRepo, "")
	}
	if e.Worktree != "" {
		if _, ok := r.worktree(e.Worktree); !ok {
			return bus.Event{}, notifysock.Reject(notifysock.ErrorUnknownWorktree, "")
		}
	}
	out, err := a.rt.bus.Publish(ctx, e)
	if err != nil {
		return bus.Event{}, notifysock.Reject(notifysock.ErrorInternal, "")
	}
	return out, nil
}

// NotifyEndpoint is the notifysock.Locator of this OS user's daemon on this
// host. It reads the private registry file the daemon wrote, found through
// os/user rather than $HOME, and returns an error wrapping
// notifysock.ErrNoDaemon when no daemon is registered or the registered
// one predates the socket. It does not check that the daemon is alive: the
// socket handshake does, and fails against a stale listener.
func NotifyEndpoint(ctx context.Context) (notifysock.Endpoint, error) {
	registry, err := registryDir()
	if err != nil {
		return notifysock.Endpoint{}, fmt.Errorf("%w: %w", notifysock.ErrNoDaemon, err)
	}
	return notifyEndpointAt(ctx, registry)
}

func notifyEndpointAt(ctx context.Context, registry string) (notifysock.Endpoint, error) {
	none := func(why string) (notifysock.Endpoint, error) {
		return notifysock.Endpoint{}, fmt.Errorf("%w: %s", notifysock.ErrNoDaemon, why)
	}
	if err := ctx.Err(); err != nil {
		return notifysock.Endpoint{}, err
	}
	prefix, err := existingPrefix(registry)
	if err != nil {
		return none("no console registry")
	}
	path := prefix + ".json"
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !privateMode(path, info) || info.Size() > 1024 {
		return none("no registered daemon")
	}
	raw, err := os.ReadFile(path)
	var i instance
	if err != nil || json.Unmarshal(raw, &i) != nil {
		return none("unreadable registry file")
	}
	if i.Notify == "" {
		return none("the running daemon has no notify socket")
	}
	ep, err := notifysock.ParseEndpoint(i.Notify)
	if err != nil {
		return none(err.Error())
	}
	key, err := base64.RawURLEncoding.DecodeString(i.Key)
	if err != nil || len(key) != 32 {
		return none("invalid instance key")
	}
	ep.Key = key
	return ep, nil
}
