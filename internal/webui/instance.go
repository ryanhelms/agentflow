package webui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

type instance struct {
	Port int    `json:"port"`
	Key  string `json:"key"`
	// Notify is the notify socket's endpoint in registry form
	// (notifysock.Endpoint.String), empty when the socket is not running.
	Notify string `json:"notify,omitempty"`
}

func (i instance) origin() string {
	return "http://127.0.0.1:" + strconv.Itoa(i.Port)
}

func registryDir() (string, error) {
	// A repository or agent can supply its own AGENTFLOW_HOME or XDG environment.
	// Locate these credentials through the OS user's private profile instead.
	account, err := user.Current()
	if err != nil || !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("could not locate the console user's profile")
	}
	return filepath.Join(account.HomeDir, ".agentflow-ui"), nil
}

// The same user profile can be mounted on multiple machines.
func daemonPrefix(registry string) (string, error) {
	if err := makePrivateDir(registry); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create console registry: %w", err)
	}
	return existingPrefix(registry)
}

// existingPrefix is daemonPrefix for a registry that must already exist.
func existingPrefix(registry string) (string, error) {
	info, err := os.Lstat(registry)
	if err != nil || !info.IsDir() || !privateMode(registry, info) {
		return "", errors.New("console registry requires a private user-owned directory")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(hostname))
	return filepath.Join(registry, fmt.Sprintf("%x", hash[:12])), nil
}

func findConsole(ctx context.Context, dir string, dest Destination, mode string, port int) (string, bool, error) {
	registry, err := registryDir()
	if err != nil {
		return "", false, err
	}
	op, err := openRequest(ctx, registry, dir, dest, mode)
	if err != nil {
		return "", false, err
	}
	return findRegistered(ctx, registry, op, port)
}

func tabMode(automatic bool) string {
	if automatic {
		return "auto"
	}
	return "open"
}

func findRegistered(ctx context.Context, registry string, op daemonRequest, port int) (string, bool, error) {
	prefix, err := daemonPrefix(registry)
	if err != nil {
		return "", false, err
	}
	existing, lock, err := discover(ctx, prefix)
	if lock != nil {
		_ = lock.Close()
	}
	if err != nil || existing == nil {
		return "", false, err
	}
	if port != 0 && existing.Port != port {
		return "", true, fmt.Errorf("console daemon is already running on port %d", existing.Port)
	}
	reply, err := existing.control(ctx, op)
	return reply.URL, true, err
}

func daemonCommand(ctx context.Context, action string) (bool, int, error) {
	if action != "status" && action != "stop" {
		return false, 0, errors.New("unknown daemon command")
	}
	registry, err := registryDir()
	if err != nil {
		return false, 0, err
	}
	prefix, err := daemonPrefix(registry)
	if err != nil {
		return false, 0, err
	}
	current, lock, err := discover(ctx, prefix)
	if lock != nil {
		_ = lock.Close()
	}
	if current == nil || err != nil {
		return false, 0, err
	}
	_, err = current.control(ctx, daemonRequest{Action: action})
	return true, current.Port, err
}

// ServeAt keeps the first daemon in the foreground and always provides a new ticket.
func ServeAt(ctx context.Context, dir string, port int, dest Destination, ready func(string, bool)) error {
	return serveAt(ctx, dir, port, dest, ready, "link")
}

// ServeForeground navigates an existing browser tab when one is available.
func ServeForeground(ctx context.Context, dir string, port int, dest Destination, ready func(string, bool)) error {
	return serveAt(ctx, dir, port, dest, ready, "open")
}

func serveAt(ctx context.Context, dir string, port int, dest Destination, ready func(string, bool), mode string) error {
	registry, err := registryDir()
	if err != nil {
		return err
	}
	op, err := openRequest(ctx, registry, dir, dest, mode)
	if err != nil {
		return err
	}
	clearCommanderEnv()
	return serveRegistered(ctx, registry, port, op, ready)
}

func serveRegistered(ctx context.Context, registry string, port int, op daemonRequest, ready func(string, bool)) error {
	prefix, err := daemonPrefix(registry)
	if err != nil {
		return err
	}
	existing, lock, err := discover(ctx, prefix)
	if err != nil {
		return err
	}
	if existing != nil {
		if port > 0 && existing.Port != port {
			return fmt.Errorf("console daemon is already running on port %d", existing.Port)
		}
		reply, err := existing.control(ctx, op)
		if err == nil {
			ready(reply.URL, true)
		}
		return err
	}
	defer lock.Close()
	prefs, err := daemonIdentity(prefix)
	if err != nil {
		return err
	}
	l, l6, err := listenDaemon(port, prefs.Port)
	if err != nil {
		return err
	}
	defer l.Close()
	if l6 != nil {
		defer l6.Close()
	}
	i := instance{Port: l.Addr().(*net.TCPAddr).Port, Key: randomKey()}
	if port == 0 && prefs.Port != i.Port {
		prefs.Port = i.Port
		if err := writePrivateJSON(prefix+".identity.json", prefs); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d, err := loadDaemon(prefix, i, prefs.Key, cancel)
	if err != nil {
		return err
	}
	notify, err := newNotifySetup(prefix, i)
	if err != nil {
		return err
	}
	defer d.startEvents(withNotifySetup(ctx, notify))()
	launch, err := d.register(ctx, op)
	if err != nil {
		return err
	}
	i.Notify = notify.Endpoint()
	if err := writeInstance(prefix, i); err != nil {
		return err
	}
	defer os.Remove(prefix + ".json")
	srv := &http.Server{Handler: d, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	if l6 != nil {
		go func() { _ = srv.Serve(l6) }()
	}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	ready(launch, false)
	err = <-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func writeInstance(prefix string, i instance) error { return writePrivateJSON(prefix+".json", i) }

func writePrivateJSON(path string, value any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".starting-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(value); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// The OS releases the lock on process exit. Never unlink its inode: a waiting
// process must contend on the same file even across a console restart.
func discover(ctx context.Context, prefix string) (*instance, *os.File, error) {
	f, err := os.OpenFile(prefix+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		locked, err := tryInstanceLock(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if locked {
			return nil, f, nil
		}
		if current, ok := liveInstance(ctx, prefix+".json"); ok {
			_ = f.Close()
			return &current, nil, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(45 * time.Millisecond):
		}
	}
	_ = f.Close()
	return nil, nil, fmt.Errorf("could not verify the running worktree console: %w", ctx.Err())
}

var instanceHTTP = &http.Client{
	Transport:     &http.Transport{DisableKeepAlives: true}, // connect directly, including with HTTP_PROXY set
	Timeout:       500 * time.Millisecond,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func liveInstance(ctx context.Context, path string) (instance, bool) {
	var i instance
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !privateMode(path, info) || info.Size() > 1024 {
		return i, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &i) != nil || i.Port < 1 || i.Port > 65535 {
		return i, false
	}
	key, err := base64.RawURLEncoding.DecodeString(i.Key)
	if err != nil || len(key) != 32 {
		return i, false
	}
	nonce := randomKey()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.origin()+"/_agentflow/instance", nil)
	if err != nil {
		return i, false
	}
	req.Header.Set("X-Agentflow-Challenge", nonce)
	req.Header.Set("X-Agentflow-Proof", signature(i.Key, "client/v1", nonce))
	resp, err := instanceHTTP.Do(req)
	if err != nil {
		return i, false
	}
	defer resp.Body.Close()
	var proof string
	return i, resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 128)).Decode(&proof) == nil &&
		hmac.Equal([]byte(proof), []byte(signature(i.Key, "instance/v1", nonce)))
}
