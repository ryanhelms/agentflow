package watch

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// ConfigTranslator translates changes under <root>/.agentflow into
// config.layer.changed events for the repo layer. It remembers each
// configuration file's content hash and reports only files that were
// added, removed or changed.
type ConfigTranslator struct {
	mu    sync.Mutex
	files map[Key]map[string][sha256.Size]byte
}

var (
	_ Translator = (*ConfigTranslator)(nil)
	_ Tracker    = (*ConfigTranslator)(nil)
)

// NewConfigTranslator returns an empty ConfigTranslator.
func NewConfigTranslator() *ConfigTranslator {
	return &ConfigTranslator{files: map[Key]map[string][sha256.Size]byte{}}
}

// Track implements Tracker.
func (c *ConfigTranslator) Track(_ context.Context, t Target) error {
	if t.Worktree == "" {
		return nil
	}
	files := configFiles(t.Root)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[KeyOf(t)] = files
	return nil
}

// Untrack implements Tracker.
func (c *ConfigTranslator) Untrack(t Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.files, KeyOf(t))
}

// Translate implements Translator. Any configuration change in the Batch
// makes it compare every configuration file of the worktree, so a missed
// change is repaired too.
func (c *ConfigTranslator) Translate(_ context.Context, b Batch) ([]bus.Event, error) {
	if b.Target.Worktree == "" {
		return nil, nil
	}
	hit := false
	for _, ch := range b.Changes {
		if typ, _ := Classify(b.Target, ch.Path); typ == bus.TypeConfigLayerChanged {
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
	now := configFiles(b.Target.Root)
	var paths []string
	for p, sum := range now {
		if prev, ok := old[p]; !ok || prev != sum {
			paths = append(paths, p)
		}
	}
	for p := range old {
		if _, ok := now[p]; !ok {
			paths = append(paths, p)
		}
	}
	c.files[KeyOf(b.Target)] = now
	if len(paths) == 0 {
		return nil, nil
	}
	sort.Strings(paths)
	e, err := newEvent(bus.TypeConfigLayerChanged, SourceConfig, b.Target.Repo, b.Target.Worktree, batchTime(b),
		bus.ConfigLayerChanged{Layer: bus.LayerRepo, Paths: paths})
	if err != nil {
		return nil, err
	}
	return []bus.Event{e}, nil
}

// configFiles hashes every repo-layer configuration file of root, keyed by
// its slash-separated path relative to root (".agentflow/config.yml").
func configFiles(root string) map[string][sha256.Size]byte {
	out := map[string][sha256.Size]byte{}
	for _, dir := range []string{".agentflow", filepath.Join(".agentflow", "providers")} {
		list, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range list {
			ext := filepath.Ext(e.Name())
			if e.IsDir() || (ext != ".yml" && ext != ".yaml") {
				continue
			}
			if sum, ok := hashFile(filepath.Join(root, dir, e.Name())); ok {
				out[filepath.ToSlash(filepath.Join(dir, e.Name()))] = sum
			}
		}
	}
	return out
}

func hashFile(path string) ([sha256.Size]byte, bool) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, false
	}
	copy(sum[:], h.Sum(nil))
	return sum, true
}
