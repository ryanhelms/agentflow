package webui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/notifysock"
)

func hookRan(repo bus.RepoID, worktree bus.WorktreeID) bus.Event {
	return bus.Event{Type: bus.TypeHookRan, Source: "hook:pre-bash", Repo: repo, Worktree: worktree,
		Payload: []byte(`{"hook":"pre-bash","client":"claude","outcome":"blocked","reason":"convention","duration_ms":4}`)}.Normalize(time.Now())
}

// The daemon advertises its notify socket in the registry file, accepts
// hook events for the worktrees it serves, appends them to the store it
// owns, and refuses anything else. After it stops there is no daemon.
func TestDaemonNotifySocket(t *testing.T) {
	ctx := t.Context()
	dir := eventRepo(t)
	registry := filepath.Join(t.TempDir(), "registry")
	_, stop := startTestDaemon(t, registry, registration(dir, "link", "overview"))
	defer func() {
		if stop != nil {
			stop()
		}
	}()

	ep, err := notifyEndpointAt(ctx, registry)
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := existingPrefix(registry)
	if path := notifysock.UnixPath(prefix); path != "" && ep.Network == "unix" {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("socket %v, %v", info, err)
		}
	}
	id, err := IdentifyWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(ctx, id.CommonDir, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	repo := reader.Repo()

	c, err := notifysock.Dial(ctx, func(context.Context) (notifysock.Endpoint, error) { return ep, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(ctx, hookRan(repo, id.Worktree)); err != nil {
		t.Fatal(err)
	}
	var found bool
	for e, err := range reader.ReadFrom(ctx, bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		found = found || (e.Type == bus.TypeHookRan && e.Source == "hook:pre-bash")
	}
	if !found {
		t.Fatal("hook.ran not stored by the daemon")
	}
	refusals := map[string]bus.Event{
		notifysock.ErrorUnknownRepo:     hookRan("fedcba9876543210", ""),
		notifysock.ErrorUnknownWorktree: hookRan(repo, "feature-0badc0de"),
	}
	forged := hookRan(repo, id.Worktree)
	forged.Type, forged.Payload = bus.TypeConfigLayerChanged, []byte(`{"layer":"repository"}`)
	refusals[notifysock.ErrorForbiddenType] = forged
	for code, e := range refusals {
		var r *notifysock.Rejection
		if err := c.Send(ctx, e); !errors.As(err, &r) || r.Code != code {
			t.Errorf("%s: %v", code, err)
		}
	}

	stop()
	stop = nil
	if _, err := notifyEndpointAt(ctx, registry); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal("registry still names a stopped daemon:", err)
	}
	if _, err := notifysock.Dial(ctx, func(context.Context) (notifysock.Endpoint, error) { return ep, nil }); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal("stopped daemon still answers:", err)
	}
}

func TestNotifyEndpointRefusesBadRegistry(t *testing.T) {
	ctx := context.Background()
	registry := filepath.Join(t.TempDir(), "registry")
	if _, err := notifyEndpointAt(ctx, registry); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal("missing registry:", err)
	}
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	key := randomKey()
	for name, notify := range map[string]string{
		"predates the socket": "",
		"remote host":         "tcp:10.0.0.1:4000",
		"host name":           "tcp:localhost:4000",
		"relative socket":     "unix:d.notify",
	} {
		if err := writeInstance(prefix, instance{Port: 1, Key: key, Notify: notify}); err != nil {
			t.Fatal(err)
		}
		if _, err := notifyEndpointAt(ctx, registry); !errors.Is(err, notifysock.ErrNoDaemon) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := writeInstance(prefix, instance{Port: 1, Key: key, Notify: "tcp:127.0.0.1:4000"}); err != nil {
		t.Fatal(err)
	}
	ep, err := notifyEndpointAt(ctx, registry)
	if err != nil || ep.Address != "127.0.0.1:4000" || len(ep.Key) != 32 {
		t.Fatal(ep, err)
	}
	// A registry file others can read is not trusted.
	if err := os.Chmod(prefix+".json", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := notifyEndpointAt(ctx, registry); !errors.Is(err, notifysock.ErrNoDaemon) && !isWindows() {
		t.Fatal("world-readable registry file trusted:", err)
	}
}

func isWindows() bool { return filepath.Separator == '\\' }
