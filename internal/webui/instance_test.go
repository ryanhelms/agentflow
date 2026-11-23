package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func registration(root, mode, screen string) daemonRequest {
	return daemonRequest{Action: "register", Dir: root, Mode: mode, Destination: Destination{Screen: screen}}
}

func startTestDaemon(t *testing.T, registry string, op daemonRequest) (*url.URL, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- serveRegistered(ctx, registry, -1, op, func(raw string, _ bool) { ready <- raw }) }()
	var raw string
	select {
	case raw = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("start daemon: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("timed out starting daemon")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		cancel()
		t.Fatal("invalid launch URL")
	}
	return u, func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestDaemonRegistryAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("DEVFLOW_TEST_REGISTRY_WORKTREE"); dir != "" {
		raw, found, err := findRegistered(t.Context(), os.Getenv("DEVFLOW_TEST_REGISTRY_ROOT"), registration(dir, "link", "providers"), 0)
		if err != nil || !found {
			t.Fatalf("child process failed to find daemon: %v", err)
		}
		if err := os.WriteFile(os.Getenv("DEVFLOW_TEST_REGISTRY_RESULT"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := testServer(t).repo.Dir
	registry := filepath.Join(t.TempDir(), "registry")
	initial, stop := startTestDaemon(t, registry, registration(root, "link", "overview"))
	defer stop()
	childPath := filepath.Join(t.TempDir(), "private-result")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDaemonRegistryAcrossProcesses$")
	cmd.Env = append(os.Environ(), "DEVFLOW_TEST_REGISTRY_WORKTREE="+filepath.Join(root, ".devflow"), "DEVFLOW_TEST_REGISTRY_ROOT="+registry, "DEVFLOW_TEST_REGISTRY_RESULT="+childPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second process: %v %s", err, out)
	}
	raw, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := url.Parse(string(raw))
	if next.Host != initial.Host || next.Path != initial.Path || launchToken(t, next) == launchToken(t, initial) || !strings.Contains(next.Fragment, "screen=providers") {
		t.Fatal("second process did not return fresh routed launch on same origin")
	}
	code, session := daemonBrowser(t, next, "POST", "api/session", launchToken(t, next), "")
	if code != 200 || session == "" {
		t.Fatalf("exchange: %d", code)
	}
	if code, _ = daemonBrowser(t, next, "POST", "api/session", launchToken(t, next), ""); code != 401 {
		t.Fatal("replayed launch")
	}
	if code, _ = daemonBrowser(t, initial, "POST", "api/session", launchToken(t, initial), ""); code != 200 {
		t.Fatal("second launch invalidated first one")
	}
}

func launchToken(t *testing.T, u *url.URL) string {
	t.Helper()
	q, err := url.ParseQuery(u.Fragment)
	if err != nil || q.Get("launch") == "" {
		t.Fatal("missing launch ticket")
	}
	return q.Get("launch")
}

func daemonBrowser(t *testing.T, u *url.URL, method, path, ticket, session string) (int, string) {
	t.Helper()
	origin := "http://" + u.Host
	data, _ := json.Marshal(map[string]string{"launch": ticket})
	req, err := http.NewRequestWithContext(t.Context(), method, origin+u.Path+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Devflow-Request", "console")
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Authorization", "Bearer "+session)
	}
	client := http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", u.Port()))
	}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 && path == "api/session" {
		var a struct{ Session string }
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, a.Session
	}
	return resp.StatusCode, resp.Header.Get("Location")
}

func TestOneDaemonMultipleClonesAndWorktrees(t *testing.T) {
	root := testServer(t).repo.Dir
	other := testServer(t).repo.Dir // same origin, independent local clone
	linked := filepath.Join(t.TempDir(), "feature")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
		t.Fatalf("git worktree: %v %s", err, out)
	}
	registry := filepath.Join(t.TempDir(), "registry")
	main, stop := startTestDaemon(t, registry, registration(root, "link", "providers"))
	register := func(dir string) *url.URL {
		raw, ok, err := findRegistered(t.Context(), registry, registration(dir, "link", "overview"), 0)
		if err != nil || !ok {
			t.Fatalf("register: %v", err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	worktree := register(linked)
	clone := register(other)
	if main.Port() != worktree.Port() || main.Port() != clone.Port() || main.Hostname() != worktree.Hostname() || main.Path == worktree.Path || main.Hostname() == clone.Hostname() || !strings.HasSuffix(main.Hostname(), ".localhost") {
		t.Fatal("invalid clone/worktree routes")
	}
	code, session := daemonBrowser(t, main, "POST", "api/session", launchToken(t, main), "")
	if code != 200 {
		t.Fatal(code)
	}
	for _, dest := range []*url.URL{worktree, clone} {
		if code, _ := daemonBrowser(t, dest, "POST", "api/session", launchToken(t, main), ""); code != 401 {
			t.Fatal("cross-worktree ticket accepted")
		}
		if code, _ := daemonBrowser(t, dest, "GET", "api/state", "", session); code != 401 {
			t.Fatal("cross-worktree session accepted")
		}
	}
	bad := *main
	bad.Host = "unknown.localhost:" + main.Port()
	if code, _ := daemonBrowser(t, &bad, "GET", "", "", ""); code != 404 {
		t.Fatal("unknown host accepted")
	}
	canonical := *worktree
	canonical.Path = "/"
	if code, loc := daemonBrowser(t, &canonical, "GET", "", "", ""); code != 307 || loc != main.Path {
		t.Fatalf("canonical route %d %s", code, loc)
	}
	stop()
	restarted, stop := startTestDaemon(t, registry, registration(other, "link", "overview"))
	defer stop()
	if restarted.Hostname() != clone.Hostname() || restarted.Path != clone.Path {
		t.Fatal("persistent alias changed across restart")
	}
	refreshed := register(linked)
	if refreshed.Hostname() != worktree.Hostname() || refreshed.Path != worktree.Path {
		t.Fatal("worktree route changed after restart")
	}
}

func TestConsoleDiscoveryAuthenticatesPort(t *testing.T) {
	dir := t.TempDir()
	key := randomKey()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, values := range r.Header {
			for _, value := range values {
				if strings.Contains(value, key) {
					t.Error("daemon key exposed")
				}
			}
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("private-checkout")) {
			t.Error("plaintext checkout disclosure")
		}
		reply(w, 200, r.Header.Get("X-Devflow-Proof"))
	}))
	defer fake.Close()
	u, _ := url.Parse(fake.URL)
	port, _ := strconv.Atoi(u.Port())
	i := instance{Port: port, Key: key}
	path := filepath.Join(dir, "record")
	if err := writeInstance(path, i); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveInstance(t.Context(), path+".json"); ok {
		t.Fatal("accepted stale port")
	}
	if _, err := i.control(t.Context(), registration("/private-checkout", "link", "providers")); err == nil {
		t.Fatal("accepted forged daemon reply")
	}
	s := testServer(t)
	if s.consumeLaunch(mintLaunch(key, time.Now())) || s.consumeLaunch(mintLaunch(s.instanceKey, time.Now().Add(-2*launchTTL))) {
		t.Fatal("accepted invalid ticket")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", fake.URL, nil)
	r.Header.Set("X-Devflow-Challenge", randomKey())
	i.proveInstance(w, r)
	if w.Code == 200 {
		t.Fatal("accepted unsigned probe")
	}
}

func TestRegistryOutsideWorktree(t *testing.T) {
	root := testServer(t).repo.Dir
	dir := filepath.Join(root, ".devflow-ui")
	if _, err := openRequest(t.Context(), dir, root, Destination{}, "open"); err == nil {
		t.Fatal("accepted registry inside checkout")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("created workspace registry")
	}
	if runtime.GOOS == "windows" {
		return
	}
	dir = t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonPrefix(dir); err == nil {
		t.Fatal("accepted public registry")
	}
}

func TestSimultaneousDaemonStartup(t *testing.T) {
	root := testServer(t).repo.Dir
	registry := filepath.Join(t.TempDir(), "registry")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const callers = 8
	type result struct {
		raw    string
		reused bool
	}
	output := make(chan result, callers)
	done := make(chan error, callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			<-start
			done <- serveRegistered(ctx, registry, -1, registration(root, "auto", "overview"), func(raw string, reused bool) { output <- result{raw, reused} })
		}()
	}
	close(start)
	var owners, launches int
	for range callers {
		select {
		case res := <-output:
			if !res.reused {
				owners++
			}
			if res.raw != "" {
				launches++
			}
		case <-ctx.Done():
			t.Fatal("concurrent callers did not all finish")
		}
	}
	cancel()
	for range callers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if owners != 1 || launches != 1 {
		t.Fatalf("got %d daemons and %d browser launches", owners, launches)
	}
}

func TestControlStopAndStaleRecovery(t *testing.T) {
	dir := testServer(t).repo.Dir
	registry := filepath.Join(t.TempDir(), "registry")
	_, wait := startTestDaemon(t, registry, registration(dir, "link", "overview"))
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	current, lock, err := discover(t.Context(), prefix)
	if err != nil || lock != nil || current == nil {
		t.Fatalf("discovery: %v", err)
	}
	if _, err := current.control(t.Context(), daemonRequest{Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	wait()
	if err := writeInstance(prefix, *current); err != nil {
		t.Fatal(err)
	}
	if _, found, err := findRegistered(t.Context(), registry, registration(dir, "open", "overview"), 0); err != nil || found {
		t.Fatal("stale data treated as a running daemon")
	}
	_, stop := startTestDaemon(t, registry, registration(dir, "link", "overview"))
	stop()
}

func TestFallbackPortSelection(t *testing.T) {
	preferred, pref6, err := listenPair(0)
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	defer preferred.Close()
	if pref6 != nil {
		defer pref6.Close()
	}
	p := preferred.Addr().(*net.TCPAddr).Port
	first, first6, err := listenDefault(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	fallback := first.Addr().(*net.TCPAddr).Port
	if fallback == p || fallback == 0 {
		t.Fatal("expected an available high port")
	}
	_ = first.Close()
	if first6 != nil {
		_ = first6.Close()
	}
	again, again6, err := listenDefault(p, fallback)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again6 != nil {
		defer again6.Close()
	}
	if again.Addr().(*net.TCPAddr).Port != fallback {
		t.Fatal("forgot the previous working fallback port")
	}
	_ = again.Close()
	if again6 != nil {
		_ = again6.Close()
	}
	_ = preferred.Close()
	if pref6 != nil {
		_ = pref6.Close()
	}
	sticky, sticky6, err := listenDefault(p, fallback)
	if err != nil {
		t.Fatal(err)
	}
	defer sticky.Close()
	if sticky6 != nil {
		defer sticky6.Close()
	}
	if sticky.Addr().(*net.TCPAddr).Port != fallback {
		t.Fatal("changed browser origin when the preferred port became free")
	}
}

func TestFirstAutoOpenCoversLinkedWorktrees(t *testing.T) {
	root := testServer(t).repo.Dir
	linked := filepath.Join(t.TempDir(), "branch")
	if b, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
		t.Fatalf("linked worktree: %v %s", err, b)
	}
	registry := filepath.Join(t.TempDir(), "registry")
	_, stop := startTestDaemon(t, registry, registration(root, "auto", "overview"))
	defer stop()
	// The first browser is still starting and has not registered a tab yet.
	raw, found, err := findRegistered(t.Context(), registry, registration(linked, "auto", "overview"), 0)
	if !found || err != nil || raw != "" {
		t.Fatal("opened a duplicate browser tab during clone-wide startup")
	}
	// An explicit recovery request can always obtain a ticket for its own worktree.
	raw, _, err = findRegistered(t.Context(), registry, registration(linked, "link", "providers"), 0)
	if raw == "" || err != nil {
		t.Fatal("unable to issue recovery link for linked worktree")
	}
}

func TestBareCloneWorktreePickerRequiresBrowserGrant(t *testing.T) {
	ctx := t.Context()
	repo := testServer(t).repo.Dir
	registry := filepath.Join(t.TempDir(), "registry")
	first := filepath.Join(t.TempDir(), "one")
	second := filepath.Join(t.TempDir(), "two")
	for _, path := range []string{first, second} {
		if b, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", path).CombinedOutput(); err != nil {
			t.Fatalf("worktree: %v %s", err, b)
		}
	}
	firstURL, stop := startTestDaemon(t, registry, registration(first, "link", "overview"))
	defer stop()
	if _, _, err := findRegistered(ctx, registry, registration(second, "link", "overview"), 0); err != nil {
		t.Fatal(err)
	}
	cloneURL := *firstURL
	cloneURL.Path = "/"
	if code, _ := daemonBrowser(t, &cloneURL, "GET", "", "", ""); code != 200 {
		t.Fatal("missing clone picker")
	}
	if code, _ := daemonBrowser(t, &cloneURL, "GET", "_devflow/worktrees", "", ""); code != 401 {
		t.Fatal("disclosed picker list without browser grant")
	}
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := daemonIdentity(prefix)
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := inspectWorktree(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	key := signature(identity.Key, "remember/v1", info.CloneID+"\x00http://"+firstURL.Host)
	grant := grantToken(key, "browser/v1", time.Now().Add(rememberBrowserFor))
	if code, _ := daemonBrowser(t, &cloneURL, "GET", "_devflow/worktrees", "", grant); code != 200 {
		t.Fatal("remembered browser could not choose its worktree")
	}
}

func TestReplacedCheckoutCannotInheritBrowserSession(t *testing.T) {
	root := testServer(t).repo.Dir
	other := testServer(t).repo.Dir
	registry := filepath.Join(t.TempDir(), "registry")
	initial, stop := startTestDaemon(t, registry, registration(root, "link", "overview"))
	defer stop()
	code, session := daemonBrowser(t, initial, "POST", "api/session", launchToken(t, initial), "")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	// Keep the original clone alive under a different name while another
	// independent clone takes its path.
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, root); err != nil {
		t.Fatal(err)
	}
	if code, _ := daemonBrowser(t, initial, "GET", "api/state", "", session); code != http.StatusGone {
		t.Fatal("accepted session against a replacement checkout")
	}
	for _, tc := range []struct {
		path string
		same bool
	}{{root, false}, {moved, true}} {
		raw, found, err := findRegistered(t.Context(), registry, registration(tc.path, "link", "overview"), 0)
		if !found || err != nil {
			t.Fatal("could not register moved or replaced checkout")
		}
		u, _ := url.Parse(raw)
		if (u.Host == initial.Host) != tc.same {
			t.Fatal("did not preserve alias with its original clone")
		}
	}
}

func TestManualOpenDuringBrowserStartupReusesFirstTab(t *testing.T) {
	ctx := t.Context()
	root := testServer(t).repo.Dir
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
		t.Fatalf("linked worktree: %v %s", err, out)
	}
	for _, firstMode := range []string{"auto", "open"} {
		t.Run(firstMode, func(t *testing.T) {
			d, err := loadDaemon(filepath.Join(t.TempDir(), "daemon"), instance{Port: 80, Key: randomKey()}, randomKey(), func() {})
			if err != nil {
				t.Fatal(err)
			}
			first, err := d.register(ctx, registration(root, firstMode, "overview"))
			if err != nil || first == "" {
				t.Fatal("missing first browser launch")
			}
			origin, _ := url.Parse(first)
			hub := &d.clones[origin.Hostname()].tabs
			ws := d.live[origin.Hostname()+"/main"].server
			type result struct {
				url string
				err error
			}
			done := make(chan result, 1)
			go func() { u, err := d.register(ctx, registration(linked, "open", "providers")); done <- result{u, err} }()
			// Wait until the manual request has observed the first launch and is
			// waiting for its stream. This keeps the assertion independent of Git
			// and browser startup timing.
			deadline := time.Now().Add(3 * time.Second)
			for {
				hub.mu.Lock()
				waiting := hub.attached != nil
				hub.mu.Unlock()
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("manual request did not wait for pending browser launch")
				}
				time.Sleep(time.Millisecond)
			}
			// A printed recovery URL remains available during a pending open.
			recovery := make(chan result, 1)
			go func() {
				u, err := d.register(ctx, registration(linked, "link", "providers"))
				recovery <- result{u, err}
			}()
			select {
			case got := <-recovery:
				if got.url == "" || got.err != nil {
					t.Fatal("recovery request lost its ticket")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("recovery request blocked on a pending browser launch")
			}
			id := strings.Repeat("a", 32)
			tab, stream := hub.attach(id, ws.origin, ws.worktreePath)
			if tab == nil {
				t.Fatal("attach failed")
			}
			defer hub.detach(tab, stream, false)
			select {
			case event := <-tab.events:
				target, err := url.Parse(event.Target)
				if err != nil || event.Screen != "providers" || target.Host != origin.Host || target.Path == origin.Path {
					t.Fatal("wrong cross-worktree navigation")
				}
				w := request(t, ws, "POST", "/api/tabs/ack", ws.origin, "session", map[string]any{"id": event.ID, "tab": id, "handled": true})
				if w.Code != 200 {
					t.Fatalf("ack: %d", w.Code)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("late tab did not receive navigation")
			}
			if got := <-done; got.err != nil || got.url != "" {
				t.Fatal("manual request returned a duplicate launch after acknowledgement")
			}
		})
	}
}

func TestManualOpenAfterUnclaimedLaunch(t *testing.T) {
	d, err := loadDaemon(filepath.Join(t.TempDir(), "daemon"), instance{Port: 80, Key: randomKey()}, randomKey(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	root := testServer(t).repo.Dir
	first, err := d.register(t.Context(), registration(root, "auto", "overview"))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(first)
	ws := d.clones[u.Hostname()]
	ws.awaitingTab = time.Now().Add(90 * time.Millisecond)
	second, err := d.register(t.Context(), registration(root, "open", "providers"))
	if err != nil || second == "" || second == first {
		t.Fatal("missing replacement when no browser connected")
	}
}
