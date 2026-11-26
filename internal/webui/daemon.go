package webui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

type registeredWorktree struct {
	CloneID string `json:"clone_id"`
	AdminID string `json:"admin_id"`
	RootID  string `json:"root_id"`
	Host    string `json:"host"`
	ID      string `json:"id"`
	Root    string `json:"root"`
	Common  string `json:"common"`
	GitDir  string `json:"git_dir"`
}

type workspace struct {
	server  *server
	handler http.Handler
	clone   *cloneConsole
}

type cloneConsole struct {
	commander   commanderScope
	tabs        tabHub
	streams     streamLimit // event streams share the clone's origin
	mu          sync.Mutex
	awaitingTab time.Time
}

type daemon struct {
	instance
	identity    string
	clones      map[string]*cloneConsole
	index       string
	cancel      context.CancelFunc
	mu          sync.Mutex
	entries     []registeredWorktree
	live        map[string]*workspace
	controlMu   sync.Mutex
	usedControl map[string]int64
	events      *eventRuntime // nil until startEvents
}

// startEvents starts the daemon's bus, watcher and per-repository stores.
// The daemon is the only process that opens a store as store.RoleOwner.
func (d *daemon) startEvents(ctx context.Context) func() {
	rt, stop := startEventRuntime(ctx)
	d.mu.Lock()
	d.events = rt
	d.mu.Unlock()
	return stop
}

var routeLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

var invalidLabel = regexp.MustCompile(`[^a-z0-9]+`)

func dnsLabel(s string) string {
	s = strings.Trim(invalidLabel.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 50 {
		s = strings.TrimRight(s[:50], "-")
	}
	if s == "" {
		return "repo"
	}
	return s
}

func openRequest(ctx context.Context, registry, dir string, dest Destination, mode string) (daemonRequest, error) {
	dest, err := dest.Normalize("overview")
	if err != nil {
		return daemonRequest{}, err
	}
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return daemonRequest{}, err
	}
	root, err := filepath.EvalSymlinks(repo.Dir)
	if err != nil {
		return daemonRequest{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(registry))
	if err != nil {
		return daemonRequest{}, err
	}
	rel, err := filepath.Rel(root, filepath.Join(parent, filepath.Base(registry)))
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return daemonRequest{}, errors.New("console registry must be outside the worktree")
	}
	return daemonRequest{Action: "register", Dir: root, Destination: dest, Mode: mode, Commander: launchCommanderBinding()}, nil
}

func inspectWorktree(ctx context.Context, dir string) (registeredWorktree, string, error) {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return registeredWorktree{}, "", err
	}
	root, err := filepath.EvalSymlinks(repo.Dir)
	if err != nil {
		return registeredWorktree{}, "", err
	}
	paths, err := repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir", "--absolute-git-dir")
	if err != nil {
		return registeredWorktree{}, "", err
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(paths, "\n"), "\r"), "\n")
	if len(parts) != 2 {
		return registeredWorktree{}, "", errors.New("unable to identify git worktree")
	}
	common, err := filepath.EvalSymlinks(strings.TrimSpace(parts[0]))
	if err != nil {
		return registeredWorktree{}, "", err
	}
	gitDir, err := filepath.EvalSymlinks(strings.TrimSpace(parts[1]))
	if err != nil {
		return registeredWorktree{}, "", err
	}
	base := filepath.Base(strings.TrimSuffix(common, ".git"))
	if filepath.Base(common) == ".git" {
		base = filepath.Base(filepath.Dir(common))
	}
	if raw, err := repo.Git(ctx, "remote", "get-url", "origin"); err == nil {
		if remote, err := gitx.ParseRemoteURL(strings.TrimSpace(raw)); err == nil {
			base = remote.Name
		}
	}
	cloneID, err := directoryID(common)
	if err != nil {
		return registeredWorktree{}, "", err
	}
	rootID, err := directoryID(root)
	if err != nil {
		return registeredWorktree{}, "", err
	}
	adminID, err := directoryID(gitDir)
	if err != nil {
		return registeredWorktree{}, "", err
	}
	id := worktreeID(common, gitDir, adminID)
	return registeredWorktree{ID: string(id), Root: root, Common: common, GitDir: gitDir, CloneID: cloneID, AdminID: adminID, RootID: rootID}, dnsLabel(base), nil
}

// worktreeID is the console's ID of a worktree and its bus.WorktreeID:
// "main" for the primary checkout, otherwise "<dns-label>-<8 hex>" from the
// name and file identity of its administrative git dir. It never contains a
// path, so it is safe in URLs, topics and events.
func worktreeID(common, gitDir, adminID string) bus.WorktreeID {
	if gitDir == common {
		return "main"
	}
	hash := sha256.Sum256([]byte(adminID))
	return bus.WorktreeID(dnsLabel(filepath.Base(gitDir)) + fmt.Sprintf("-%x", hash[:4]))
}

// WorktreeIdentity is where a worktree's events belong.
type WorktreeIdentity struct {
	Worktree  bus.WorktreeID
	Root      string
	GitDir    string
	CommonDir string
}

// IdentifyWorktree computes the worktree ID of the checkout containing dir
// without a running daemon, as the console and the event stream name it.
func IdentifyWorktree(ctx context.Context, dir string) (WorktreeIdentity, error) {
	r, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		return WorktreeIdentity{}, err
	}
	return WorktreeIdentity{Worktree: bus.WorktreeID(r.ID), Root: r.Root, GitDir: r.GitDir, CommonDir: r.Common}, nil
}

func loadDaemon(prefix string, i instance, identity string, cancel context.CancelFunc) (*daemon, error) {
	d := &daemon{instance: i, identity: identity, clones: map[string]*cloneConsole{}, index: prefix + ".repos.json", cancel: cancel, live: map[string]*workspace{}, usedControl: map[string]int64{}}
	info, err := os.Lstat(d.index)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !privateMode(d.index, info) {
		return nil, errors.New("invalid private worktree registry")
	}
	raw, err := os.ReadFile(d.index)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &d.entries); err != nil {
		return nil, fmt.Errorf("read local console registry: %w", err)
	}
	for _, r := range d.entries {
		if !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.Common) || !filepath.IsAbs(r.GitDir) || !strings.HasSuffix(r.Host, ".localhost") ||
			!routeLabel.MatchString(r.ID) || !routeLabel.MatchString(strings.TrimSuffix(r.Host, ".localhost")) {
			return nil, errors.New("invalid worktree in local console registry")
		}
	}
	return d, nil
}

func (d *daemon) publicHost(host string) string {
	if d.Port == 80 {
		return host
	}
	return host + ":" + strconv.Itoa(d.Port)
}

func (d *daemon) record(r registeredWorktree, base string) (registeredWorktree, error) {
	var old *registeredWorktree
	used := map[string]bool{}
	for idx := range d.entries {
		other := &d.entries[idx]
		if other.CloneID == r.CloneID {
			r.Host = other.Host
			if other.AdminID == r.AdminID {
				r.ID = other.ID
				if other.RootID == r.RootID && other.Root == r.Root && other.Common == r.Common && other.GitDir == r.GitDir {
					return *other, nil
				}
				old = other
			}
		}
		used[other.Host] = true
	}
	if r.Host == "" {
		r.Host = base + ".localhost"
		for n := 1; used[r.Host]; n++ {
			hash := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", r.CloneID, n)))
			r.Host = fmt.Sprintf("%s-%x.localhost", base, hash[:4])
		}
	}
	entries := append([]registeredWorktree(nil), d.entries...)
	if old == nil {
		entries = append(entries, r)
	} else {
		for i := range entries {
			if entries[i].CloneID == r.CloneID && entries[i].AdminID == r.AdminID {
				entries[i] = r
				break
			}
		}
	}
	if err := writePrivateJSON(d.index, entries); err != nil {
		return registeredWorktree{}, err
	}
	d.entries = entries
	if old != nil {
		delete(d.live, r.Host+"/"+r.ID)
	}
	return r, nil
}

func (d *daemon) activate(r registeredWorktree) *workspace {
	key := r.Host + "/" + r.ID
	if active := d.live[key]; active != nil {
		return active
	}
	clone := d.clones[r.Host]
	if clone == nil {
		clone = &cloneConsole{}
		d.clones[r.Host] = clone
	}
	origin := "http://" + d.publicHost(r.Host)
	s := &server{commander: &clone.commander, repo: &gitx.Repo{Dir: r.Root}, origin: origin, instanceKey: signature(d.Key, "workspace/v1", key),
		rememberKey: signature(d.identity, "remember/v1", r.CloneID+"\x00"+origin), session: randomKey(), tabs: &clone.tabs, worktreePath: "/w/" + r.ID + "/",
		streams: &clone.streams}
	if d.events != nil {
		// Without a store the console still works; only the stream is off.
		if events, err := d.events.attach(r); err == nil {
			s.events = events
		} else {
			fmt.Fprintf(os.Stderr, "agentflow: live events unavailable for %s: %v\n", r.ID, err)
		}
	}
	ws := &workspace{server: s, clone: clone, handler: http.StripPrefix("/w/"+r.ID, s.handler())}
	d.live[key] = ws
	return ws
}

func (d *daemon) register(ctx context.Context, op daemonRequest) (string, error) {
	if op.Mode != "auto" && op.Mode != "link" && op.Mode != "open" {
		return "", errors.New("unknown console launch mode")
	}
	dest, err := op.Destination.Normalize("overview")
	if err != nil {
		return "", err
	}
	r, base, err := inspectWorktree(ctx, op.Dir)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	r, err = d.record(r, base)
	if err != nil {
		d.mu.Unlock()
		return "", err
	}
	ws := d.activate(r)
	d.mu.Unlock()
	link := func(ticket string) string {
		return ws.server.origin + ws.server.worktreePath + "#launch=" + ticket + "&" + dest.Fragment()
	}
	if op.Mode == "link" {
		scope := ws.clone.commander.bind(op.Commander)
		return link(ws.server.scopedLaunch(time.Now(), scope)), nil
	}
	ws.clone.mu.Lock()
	defer ws.clone.mu.Unlock()
	auto := op.Mode == "auto"
	if time.Now().Before(ws.clone.awaitingTab) {
		if auto {
			return "", nil
		}
		ws.clone.tabs.waitForAttachment(ctx, ws.clone.awaitingTab)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	scope := ""
	if !auto {
		scope = ws.clone.commander.bind(op.Commander)
	}
	plain := link(mintLaunch(ws.server.instanceKey, time.Now()))
	scoped := ""
	if scope != "" {
		scoped = link(ws.server.scopedLaunch(time.Now(), scope))
	}
	if outcome := ws.clone.tabs.navigateScoped(ctx, dest, auto, plain, scope, scoped); outcome != tabMissing {
		ws.clone.awaitingTab = time.Time{}
		return "", outcome.err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ws.clone.awaitingTab = time.Now().Add(5 * time.Second)
	if auto {
		scope = ws.clone.commander.bind(op.Commander)
	}
	// An unacknowledged browser may already have received the routed ticket.
	// Give the direct caller a separate one.
	return link(ws.server.scopedLaunch(time.Now(), scope)), nil
}

func (d *daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host == "127.0.0.1:"+strconv.Itoa(d.Port) || (d.Port == 80 && r.Host == "127.0.0.1") {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/_agentflow/instance":
			d.proveInstance(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/_agentflow/control":
			d.handleControl(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	d.mu.Lock()
	var selected *registeredWorktree
	var first *registeredWorktree
	var main *registeredWorktree
	var firstLive *registeredWorktree
	count := 0
	for idx := range d.entries {
		e := &d.entries[idx]
		if d.publicHost(e.Host) != r.Host {
			continue
		}
		if first == nil {
			first = e
		}
		if r.URL.Path == "/" && e.available() {
			count++
			if firstLive == nil {
				firstLive = e
			}
			if e.GitDir == e.Common {
				main = e
			}
		}
		if len(parts) >= 2 && parts[0] == "w" && parts[1] == e.ID {
			selected = e
			break
		}
	}
	if selected == nil {
		if first != nil && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/_agentflow/") {
			d.pickerResource(w, r, *first)
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		if first != nil && r.Method == http.MethodGet && r.URL.Path == "/" {
			if main == nil && count == 1 {
				main = firstLive
			}
			if main != nil {
				redirectWorktree(w, main.ID)
				return
			}
			pickerAsset(w, "workspace-picker.html", "text/html; charset=utf-8")
			return
		}
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 {
		d.mu.Unlock()
		redirectWorktree(w, selected.ID)
		return
	}
	key := selected.Host + "/" + selected.ID
	ws := d.live[key]
	if ws == nil {
		// On restart, a bookmark may point to a checkout that was removed or replaced.
		probe, _, err := inspectWorktree(r.Context(), selected.Root)
		if err != nil || probe.CloneID != selected.CloneID || probe.AdminID != selected.AdminID || probe.RootID != selected.RootID || probe.Root != selected.Root {
			d.mu.Unlock()
			http.Error(w, "This checkout moved or was removed. Open its current worktree from Agent Commander.", http.StatusGone)
			return
		}
		ws = d.activate(*selected)
	}
	entry := *selected
	d.mu.Unlock()
	if strings.HasPrefix(parts[2], "api/") {
		// A worktree can be moved/deleted and a different clone installed at
		// the same path while an authenticated tab is still open.
		if !entry.available() {
			http.Error(w, "This worktree changed. Open its current checkout from Agent Commander.", http.StatusGone)
			return
		}
	}
	ws.handler.ServeHTTP(w, r)
}

func redirectWorktree(w http.ResponseWriter, id string) {
	w.Header().Set("Location", "/w/"+url.PathEscape(id)+"/")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func (r registeredWorktree) available() bool {
	for _, expected := range []struct{ dir, id string }{{r.Common, r.CloneID}, {r.Root, r.RootID}} {
		id, err := directoryID(expected.dir)
		if err != nil || id != expected.id {
			return false
		}
	}
	// A linked checkout has a gitfile. Validate its current target rather than
	// assuming the old administrative directory still belongs to this root.
	ref := filepath.Join(r.Root, ".git")
	info, err := os.Stat(ref)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() || info.Size() > 8192 {
			return false
		}
		f, err := os.Open(ref)
		if err != nil {
			return false
		}
		b, err := io.ReadAll(io.LimitReader(f, 8193))
		_ = f.Close()
		if err != nil || len(b) > 8192 {
			return false
		}
		path, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return false
		}
		ref = path
		if !filepath.IsAbs(ref) {
			ref = filepath.Join(r.Root, ref)
		}
	}
	id, err := directoryID(ref)
	return err == nil && id == r.AdminID
}
