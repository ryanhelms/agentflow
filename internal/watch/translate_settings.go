package watch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// maxSettingsRead bounds how much of one client settings file is hashed.
// The files are small JSON or TOML documents; the size is hashed too, so a
// change past the bound is still seen.
const maxSettingsRead = 1 << 20

// ClientSettingsTranslator translates changes to the client settings files
// of a worktree (RepoClientSettings) into client.settings.changed events
// with scope repo. Like ConfigTranslator it remembers a content hash per
// file and reports only files that were added, removed or changed, one
// event per client. The event names files; it never carries their content.
type ClientSettingsTranslator struct {
	mu    sync.Mutex
	files map[Key]map[string][sha256.Size]byte
}

var (
	_ Translator = (*ClientSettingsTranslator)(nil)
	_ Tracker    = (*ClientSettingsTranslator)(nil)
)

// NewClientSettingsTranslator returns an empty ClientSettingsTranslator.
func NewClientSettingsTranslator() *ClientSettingsTranslator {
	return &ClientSettingsTranslator{files: map[Key]map[string][sha256.Size]byte{}}
}

// Track implements Tracker.
func (c *ClientSettingsTranslator) Track(_ context.Context, t Target) error {
	if t.Worktree == "" {
		return nil
	}
	files := settingsHashes(t.Root, RepoClientSettings)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[KeyOf(t)] = files
	return nil
}

// Untrack implements Tracker.
func (c *ClientSettingsTranslator) Untrack(t Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.files, KeyOf(t))
}

// Translate implements Translator. Any settings change in the Batch makes
// it compare every settings file of the worktree, so a missed change is
// repaired too.
func (c *ClientSettingsTranslator) Translate(_ context.Context, b Batch) ([]bus.Event, error) {
	if b.Target.Worktree == "" {
		return nil, nil
	}
	hit := false
	for _, ch := range b.Changes {
		if typ, _ := Classify(b.Target, ch.Path); typ == bus.TypeClientSettingsChanged {
			hit = true
			break
		}
	}
	if !hit {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.files[KeyOf(b.Target)]
	if !ok {
		return nil, nil
	}
	now := settingsHashes(b.Target.Root, RepoClientSettings)
	c.files[KeyOf(b.Target)] = now
	return settingsEvents(old, now, RepoClientSettings, bus.SettingsRepo, b.Target.Repo, b.Target.Worktree, batchTime(b))
}

// settingsHashes hashes every file of files that exists below root, keyed
// by its slash-separated path.
func settingsHashes(root string, files []ClientSettingsFile) map[string][sha256.Size]byte {
	out := map[string][sha256.Size]byte{}
	for _, f := range files {
		if sum, ok := hashSettingsFile(filepath.Join(root, filepath.FromSlash(f.Path))); ok {
			out[f.Path] = sum
		}
	}
	return out
}

// hashSettingsFile hashes a regular file, following a symbolic link (dotfile
// managers link these files). Only the hash leaves this function.
func hashSettingsFile(path string) ([sha256.Size]byte, bool) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return sum, false
	}
	h := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(max(fi.Size(), 0)))
	_, _ = h.Write(size[:])
	if _, err := io.Copy(h, io.LimitReader(f, maxSettingsRead)); err != nil {
		return sum, false
	}
	copy(sum[:], h.Sum(nil))
	return sum, true
}

// settingsEvents compares two hash sets and returns one client.settings.changed
// event per client with a file that was added, removed or changed. The
// events are in client-name order, their paths sorted and capped.
func settingsEvents(old, now map[string][sha256.Size]byte, files []ClientSettingsFile, scope bus.SettingsScope,
	repo bus.RepoID, wt bus.WorktreeID, at time.Time) ([]bus.Event, error) {
	byClient := map[string][]string{}
	for _, f := range files {
		prev, had := old[f.Path]
		cur, has := now[f.Path]
		if had != has || prev != cur {
			byClient[f.Client] = append(byClient[f.Client], f.Path)
		}
	}
	clients := make([]string, 0, len(byClient))
	for c := range byClient {
		clients = append(clients, c)
	}
	sort.Strings(clients)
	var out []bus.Event
	for _, c := range clients {
		paths := byClient[c]
		sort.Strings(paths)
		e, err := newEvent(bus.TypeClientSettingsChanged, SourceConfig, repo, wt, at,
			bus.ClientSettingsChanged{Client: c, Scope: scope, Paths: capNames(paths)})
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

// capNames keeps at most bus.MaxPayloadNames entries, each at most
// bus.MaxPayloadText bytes.
func capNames(names []string) []string {
	if len(names) > bus.MaxPayloadNames {
		names = names[:bus.MaxPayloadNames]
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = clipText(n, bus.MaxPayloadText)
	}
	return out
}
