package filesystem

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nscaledev/devflow/internal/provider"
)

type store struct {
	root     *os.Root
	instance string
	kind     string
}

func openStore(instance, directory, kind string) (*store, error) {
	if err := (provider.Ref{Instance: instance, Scope: "_", ID: "_"}).Validate(); err != nil {
		return nil, fmt.Errorf("invalid provider instance: %w", err)
	}
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("provider directory is required")
	}
	directory = filepath.Clean(directory)
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect provider directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("provider root %q must be a directory (symlinks are unsupported)", directory)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open provider directory %q: %w", directory, err)
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect opened provider directory %q: %w", directory, err)
	}
	if !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("provider directory %q changed while opening: %w", directory, provider.ErrConflict)
	}
	return &store{root: root, instance: instance, kind: kind}, nil
}

// Close releases the directory handle.
func (s *store) Close() error { return s.root.Close() }

func (s *store) checkRef(ref provider.Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Instance != s.instance {
		return fmt.Errorf("reference belongs to instance %q, expected %q", ref.Instance, s.instance)
	}
	return nil
}

// scan also checks identities before allowing writes, even if the calling query has a limit.
func (s *store) scan(ctx context.Context, scope string) ([]record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list provider directory: %w", err)
	}
	seen := make(map[provider.Ref]string)
	var records []record
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("%q must be a regular Markdown file", e.Name())
		}
		r, err := s.read(e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", e.Name(), err)
		}
		if r.Kind != s.kind || r.Ref.Instance != s.instance || (scope != "" && scope != r.Ref.Scope) {
			continue
		}
		if other, ok := seen[r.Ref]; ok {
			return nil, fmt.Errorf("duplicate %s in %q and %q: %w", r.Ref, other, r.name, provider.ErrConflict)
		}
		seen[r.Ref] = r.name
		records = append(records, r)
	}
	return records, nil
}

func (s *store) read(name string) (record, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return record{}, err
	}
	if !info.Mode().IsRegular() {
		return record{}, fmt.Errorf("%q must be a regular file", name)
	}
	// A concurrent replacement could turn the name into a pipe between Lstat
	// and OpenFile. Open without blocking and check the actual descriptor.
	f, err := s.root.OpenFile(name, readFlags, 0)
	if err != nil {
		return record{}, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return record{}, err
	}
	if !info.Mode().IsRegular() {
		return record{}, fmt.Errorf("%q must be a regular file", name)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return record{}, err
	}
	r, err := parse(data)
	r.name, r.mode = name, info.Mode().Perm()
	return r, err
}

func (s *store) get(ctx context.Context, ref provider.Ref) (record, error) {
	if err := s.checkRef(ref); err != nil {
		return record{}, err
	}
	rs, err := s.scan(ctx, ref.Scope)
	if err != nil {
		return record{}, err
	}
	for _, r := range rs {
		if r.Ref == ref {
			return r, nil
		}
	}
	return record{}, fmt.Errorf("%s: %w", ref, provider.ErrNotFound)
}

func (s *store) lock(ctx context.Context) (*os.File, error) {
	f, err := lockHandle(s.root)
	if err != nil {
		return nil, fmt.Errorf("open provider directory lock: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		locked, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock provider directory: %w", err)
		}
		if locked {
			return f, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// install publishes a complete file. Link is exclusive even if an unrelated file
// appeared after scanning. Rename replaces only a previously identified record.
func (s *store) install(ctx context.Context, r record, data []byte, lock *os.File) error {
	tmp := ".devflow-stage-" + rand.Text()
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = s.root.Remove(tmp) }() // A crash may also leave an ignored stage.
	if _, err = f.Write(data); err == nil && r.revision != "" {
		err = f.Chmod(r.mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.revision != "" {
		current, err := s.read(r.name)
		if err != nil {
			return fmt.Errorf("recheck original %q: %w", r.name, err)
		}
		if current.revision != r.revision {
			return fmt.Errorf("reload %q before writing: %w", r.name, provider.ErrConflict)
		}
		if err := s.root.Rename(tmp, r.name); err != nil {
			return err
		}
	} else if err := s.root.Link(tmp, r.name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("destination %q is occupied: %w", r.name, provider.ErrConflict)
		}
		return fmt.Errorf("create %q by hard link: %w", r.name, err)
	}
	if err := syncDirectory(lock); err != nil {
		return fmt.Errorf("record published but directory sync failed, reload %q: %w", r.name, err)
	}
	return nil
}

type verb string

const (
	create     verb = "create"
	transition verb = "transition"
	publish    verb = "publish"
)

func (s *store) write(ctx context.Context, op verb, r record, opts provider.WriteOptions) (provider.WriteResult, error) {
	var zero provider.WriteResult
	if err := s.checkRef(r.Ref); err != nil {
		return zero, err
	}
	if !utf8.ValidString(r.body) {
		return zero, errors.New("markdown body must be UTF-8")
	}
	if op != transition {
		if err := r.validate(); err != nil {
			return zero, err
		}
	}
	if opts.OperationID != "" && strings.TrimSpace(opts.OperationID) == "" {
		return zero, errors.New("operation ID cannot be blank")
	}
	request, err := r.markdown()
	if err != nil {
		return zero, err
	}
	fingerprint := digest(append([]byte(string(op)+"\n"), request...))
	lock, err := s.lock(ctx)
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	records, err := s.scan(ctx, r.Ref.Scope)
	if err != nil {
		return zero, err
	}
	var old *record
	replayed := false
	for i := range records {
		existing := &records[i]
		if existing.Ref == r.Ref {
			old = existing
		}
		if hash, ok := existing.Operations[opts.OperationID]; ok {
			if hash != fingerprint || existing.Ref != r.Ref {
				return zero, fmt.Errorf("operation ID %q already used for a different request: %w", opts.OperationID, provider.ErrConflict)
			}
			replayed = true
		}
	}
	if replayed {
		return provider.WriteResult{Ref: r.Ref, Revision: old.revision, Status: provider.AlreadyApplied}, nil
	}
	if err := r.prepare(op, old, opts.ExpectedRevision); err != nil {
		return zero, err
	}
	if opts.OperationID != "" {
		if r.Operations == nil {
			r.Operations = make(map[string]string)
		}
		r.Operations[opts.OperationID] = fingerprint
	}
	data, err := r.markdown()
	if err != nil {
		return zero, err
	}
	if err := s.install(ctx, r, data, lock); err != nil {
		return zero, fmt.Errorf("write %s to %q: %w", r.Ref, r.name, err)
	}
	return provider.WriteResult{Ref: r.Ref, Revision: digest(data), Status: provider.Applied}, nil
}

// prepare resolves the destination and carries forward any fields outside the mutation.
func (r *record) prepare(op verb, old *record, expected string) error {
	if expected != "" && (old == nil || expected != old.revision) {
		return fmt.Errorf("expected revision for %s is stale, reload before writing: %w", r.Ref, provider.ErrConflict)
	}
	if old == nil {
		if op == transition {
			return fmt.Errorf("%s: %w", r.Ref, provider.ErrNotFound)
		}
		r.name = r.Kind + "-" + r.Ref.StorageKey() + ".md"
		return nil
	}
	switch op {
	case create:
		return fmt.Errorf("task %s already exists: %w", r.Ref, provider.ErrConflict)
	case publish:
		if expected == "" {
			return fmt.Errorf("publish to existing %s requires ExpectedRevision from Get: %w", r.Ref, provider.ErrConflict)
		}
		r.name, r.revision, r.mode, r.Operations = old.name, old.revision, old.mode, old.Operations
	case transition:
		state := r.State
		*r = *old
		r.State = state
	}
	return nil
}
