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

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/okf"
)

type store struct {
	root         *os.Root
	instance     string
	kind         string
	okfKnowledge bool
	format       string
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
	seen := make(map[provider.Ref]string)
	var records []record
	err := s.walk(ctx, func(name string) error {
		r, err := s.read(name)
		if errors.Is(err, okf.ErrNotConcept) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %q: %w", name, err)
		}
		inScope := scope == "" || r.Ref.Scope == scope
		if r.Kind != s.kind || r.Ref.Instance != s.instance || (!s.okfKnowledge && !inScope) {
			return nil
		}
		if other, ok := seen[r.Ref]; ok {
			return fmt.Errorf("duplicate %s in %q and %q: %w", r.Ref, other, r.name, provider.ErrConflict)
		}
		seen[r.Ref] = r.name
		if inScope {
			records = append(records, r)
		}
		return nil
	})
	if err != nil {
		if s.okfKnowledge {
			return nil, fmt.Errorf("%w: %w", ErrKnowledgeScan, err)
		}
		return nil, err
	}
	return records, nil
}

func (s *store) walk(ctx context.Context, visit func(string) error) error {
	if s.okfKnowledge {
		return fs.WalkDir(s.root.FS(), ".", func(name string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if e.IsDir() && name != "." && strings.HasPrefix(e.Name(), ".") {
				return fs.SkipDir
			}
			if e.Type().IsRegular() && IsKnowledgeCandidate(name) {
				return visit(name)
			}
			return nil
		})
	}
	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return fmt.Errorf("list provider directory: %w", err)
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		if !e.Type().IsRegular() {
			return fmt.Errorf("%q must be a regular Markdown file", e.Name())
		}
		if err := visit(e.Name()); err != nil {
			return err
		}
	}
	return nil
}

var errRecordReplaced = errors.New("record replaced while opening")

func (s *store) read(name string) (record, error) {
	// An atomic publish may replace the file between Lstat and OpenFile.
	// Retry from the checked path rather than accepting a different inode.
	r, err := s.readFrom(s.root, name, name)
	for attempt := 0; attempt < 3 && errors.Is(err, errRecordReplaced); attempt++ {
		r, err = s.readFrom(s.root, name, name)
	}
	return r, err
}

func (s *store) readFrom(dir *os.Root, file, name string) (record, error) {
	info, err := dir.Lstat(file)
	if err != nil {
		return record{}, err
	}
	if !info.Mode().IsRegular() {
		return record{}, fmt.Errorf("%q must be a regular file", name)
	}
	// A concurrent replacement could turn the name into a pipe between Lstat
	// and OpenFile. Open without blocking and check the actual descriptor.
	f, err := dir.OpenFile(file, readFlags, 0)
	if err != nil {
		return record{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return record{}, err
	}
	if !opened.Mode().IsRegular() {
		return record{}, fmt.Errorf("%q must be a regular file: %w", name, provider.ErrConflict)
	}
	if !os.SameFile(info, opened) {
		return record{}, fmt.Errorf("file %q: %w: %w", name, errRecordReplaced, provider.ErrConflict)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return record{}, err
	}
	var r record
	if s.okfKnowledge {
		r, err = parseKnowledge(s.instance, name, data)
	} else {
		r, err = parse(data)
	}
	r.name, r.mode, r.info = name, opened.Mode().Perm(), opened
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

func openParent(root *os.Root, parent string) (*os.Root, error) {
	dir := root
	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		next, err := openChild(dir, part)
		if dir != root {
			_ = dir.Close()
		}
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}

func openChild(dir *os.Root, part string) (*os.Root, error) {
	info, err := dir.Lstat(part)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("expected directory at %q: %w", part, provider.ErrConflict)
	}
	child, err := dir.OpenRoot(part)
	if err != nil {
		return nil, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, fmt.Errorf("directory %q changed: %w", part, errors.Join(provider.ErrConflict, err))
	}
	return child, nil
}

// install publishes a complete file. Link is exclusive even if an unrelated file
// appeared after scanning. Rename replaces only a previously identified record.
func (s *store) install(ctx context.Context, r record, data []byte, dirHandle *os.File) error {
	dir := s.root
	file := filepath.Base(r.name)
	if parent := filepath.Dir(r.name); parent != "." {
		// Pin each real directory. An imported path must never redirect a
		// replacement through a symlink, even one pointing inside the bundle.
		nested, err := openParent(s.root, parent)
		if err != nil {
			return err
		}
		defer nested.Close()
		dir = nested
		parentHandle, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer parentHandle.Close()
		dirHandle = parentHandle // fsync the directory containing the rename
	}
	tmp := ".agentflow-stage-" + rand.Text()
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Remove(tmp) }() // A crash may also leave an ignored stage.
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
		current, err := s.readFrom(dir, file, r.name)
		if err != nil {
			return fmt.Errorf("recheck original %q: %w", r.name, err)
		}
		if current.revision != r.revision || current.mode != r.mode || !os.SameFile(current.info, r.info) {
			return fmt.Errorf("reload %q before writing: %w", r.name, provider.ErrConflict)
		}
		if err := dir.Rename(tmp, file); err != nil {
			return err
		}
	} else if err := dir.Link(tmp, file); err != nil {
		if _, statErr := dir.Lstat(file); statErr == nil || errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("destination %q is occupied: %w", r.name, provider.ErrConflict)
		}
		return fmt.Errorf("create %q by hard link: %w", r.name, err)
	}
	if err := syncDirectory(dirHandle); err != nil {
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
	if opts.OperationID != "" && strings.TrimSpace(opts.OperationID) == "" {
		return zero, errors.New("operation ID cannot be blank")
	}
	request, err := r.markdown()
	if err != nil {
		return zero, err
	}
	fingerprint := digest(append([]byte(string(op)+"\n"), request...))
	okfHash := ""
	if op == publish {
		okfHash, err = r.publishFingerprint(fingerprint)
		if err != nil {
			return zero, err
		}
	}
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
	var receiptErr error
	replayed := false
	for i := range records {
		existing := &records[i]
		if existing.Ref == r.Ref {
			old = existing
		}
		if hash, ok := existing.Operations[opts.OperationID]; ok {
			legacyMatch := hash == fingerprint && !r.hasConceptMetadata()
			if (legacyMatch || (okfHash != "" && hash == okfHash)) && existing.Ref == r.Ref {
				replayed = true
			} else {
				receiptErr = fmt.Errorf("operation ID %q already used for a different request: %w", opts.OperationID, provider.ErrConflict)
			}
		}
	}
	if s.format == "" && old != nil && old.concept.Frontmatter != nil && (!replayed || receiptErr != nil) {
		return zero, fmt.Errorf("OKF document %s is read-only: %w", old.Ref, errors.Join(provider.ErrUnsupported, receiptErr))
	}
	if receiptErr != nil {
		return zero, receiptErr
	}
	if op == transition && old != nil {
		if opts.CheckTask != nil {
			if err := opts.CheckTask(task(*old)); err != nil {
				return zero, err
			}
		} else if old.Template != nil {
			return zero, errors.New("task requires validation against its stored template")
		}
	}
	if replayed {
		return provider.WriteResult{Ref: r.Ref, Revision: old.revision, Status: provider.AlreadyApplied}, nil
	}
	if err := r.prepare(op, old, opts.ExpectedRevision, s.format); err != nil {
		return zero, err
	}
	if err := r.validate(); err != nil {
		return zero, err
	}
	if opts.OperationID != "" {
		if r.Operations == nil {
			r.Operations = make(map[string]string)
		}
		if r.concept.Frontmatter != nil {
			r.Operations[opts.OperationID] = okfHash
			if err := okf.SetField(r.concept.Frontmatter, "x-agentflow-operations", r.Operations); err != nil {
				return zero, err
			}
		} else {
			r.Operations[opts.OperationID] = fingerprint
		}
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
func (r *record) prepare(op verb, old *record, expected, format string) error {
	if expected != "" && (old == nil || expected != old.revision) {
		return fmt.Errorf("expected revision for %s is stale, reload before writing: %w", r.Ref, provider.ErrConflict)
	}
	if old == nil {
		if op == transition {
			return fmt.Errorf("%s: %w", r.Ref, provider.ErrNotFound)
		}
		r.name = r.Kind + "-" + r.Ref.StorageKey() + ".md"
		if op == publish {
			return r.prepareDocument(nil, format)
		}
		return nil
	}
	switch op {
	case create:
		return fmt.Errorf("task %s already exists: %w", r.Ref, provider.ErrConflict)
	case publish:
		if expected == "" {
			return fmt.Errorf("publish to existing %s requires ExpectedRevision from Get: %w", r.Ref, provider.ErrConflict)
		}
		r.name, r.revision, r.mode, r.info, r.Operations = old.name, old.revision, old.mode, old.info, old.Operations
		return r.prepareDocument(old, format)
	case transition:
		state := r.State
		*r = *old
		r.State = state
	}
	return nil
}
