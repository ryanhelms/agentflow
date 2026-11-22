package local

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/provision"
)

const (
	selectionName  = "current"
	executableMode = 0o500
)

func openCache(path string) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect cache directory: %w", err)
	}
	if err := checkDirectory(info); err != nil {
		return nil, err
	}
	return openCheckedDirectory(os.OpenRoot, path, info)
}

func (c *Cache) targetDir(target provision.Target, create bool) (*os.Root, error) {
	dir, err := openCache(c.path)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{c.env.ID, c.env.Platform.OS + "-" + c.env.Platform.Arch, target.ID} {
		child, err := openChild(dir, name, create)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		if child == nil {
			return nil, nil
		}
		dir = child
	}
	return dir, nil
}

func openChild(parent *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create cache directory: %w", err)
		}
	}
	info, err := parent.Lstat(name)
	if !create && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect cache directory: %w", err)
	}
	if err := checkDirectory(info); err != nil {
		return nil, err
	}
	return openCheckedDirectory(parent.OpenRoot, name, info)
}

func checkDirectory(info fs.FileInfo) error {
	if !info.IsDir() || !ownedByEffectiveUser(info) || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("unsafe cache directory %q", info.Name())
	}
	return nil
}

func openCheckedDirectory(open func(string) (*os.Root, error), name string, before fs.FileInfo) (*os.Root, error) {
	dir, err := open(name)
	if err != nil {
		return nil, fmt.Errorf("open cache directory: %w", err)
	}
	info, err := dir.Stat(".")
	if err != nil || !os.SameFile(before, info) {
		_ = dir.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect opened cache directory: %w", err)
		}
		return nil, fmt.Errorf("cache directory %q changed while opening", name)
	}
	return dir, nil
}

type artifactRef struct {
	version provision.Version
	sha256  string
}

func (r artifactRef) name() string {
	return fmt.Sprintf("%d.%d.%d-%s", r.version.Major, r.version.Minor, r.version.Patch, r.sha256)
}

type selection struct {
	ref  artifactRef
	file objectState
}

func (s selection) installation() provision.Installation {
	if !s.file.verified(s.ref) {
		return provision.Installation{}
	}
	return provision.Installation{Present: true, VersionKnown: true, Version: s.ref.version, SHA256: s.file.sha256}
}

func readSelection(ctx context.Context, dir *os.Root) (selection, error) {
	name, err := dir.Readlink(selectionName)
	if errors.Is(err, fs.ErrNotExist) {
		return selection{}, nil
	}
	if err != nil {
		return selection{}, fmt.Errorf("read cache selection: %w", err)
	}
	version, sha, ok := strings.Cut(name, "-")
	ref := artifactRef{sha256: sha}
	if !ok || !digest.MatchString(sha) {
		return selection{}, errors.New("invalid cache selection")
	}
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &ref.version.Major, &ref.version.Minor, &ref.version.Patch); err != nil || ref.name() != name {
		return selection{}, errors.New("invalid cache selection")
	}
	file, err := inspectObject(ctx, dir, name)
	return selection{ref: ref, file: file}, err
}

type objectState struct {
	mode    fs.FileMode
	present bool
	sha256  string
}

func (s objectState) verified(ref artifactRef) bool {
	return s.present && s.mode == executableMode && s.sha256 == ref.sha256
}

func inspectObject(ctx context.Context, dir *os.Root, name string) (objectState, error) {
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return objectState{}, nil
	}
	if err != nil {
		return objectState{}, fmt.Errorf("inspect cached executable: %w", err)
	}
	if !info.Mode().IsRegular() || !ownedByEffectiveUser(info) {
		return objectState{}, fmt.Errorf("unsafe cached executable %q", name)
	}
	state := objectState{present: true, mode: info.Mode()}
	if state.mode != executableMode {
		return state, nil
	}
	file, err := openNonblocking(dir, name)
	if err != nil {
		return objectState{}, fmt.Errorf("open cached executable: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return objectState{}, fmt.Errorf("inspect opened executable: %w", err)
	}
	if !os.SameFile(info, opened) || opened.Mode() != state.mode {
		return objectState{}, provision.ErrStalePlan
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx, file}); err != nil {
		return objectState{}, fmt.Errorf("hash cached executable: %w", err)
	}
	state.sha256 = hex.EncodeToString(hash.Sum(nil))
	return state, ctx.Err()
}

func (c *Cache) install(ctx context.Context, dir *os.Root, artifact provision.Artifact, expected provision.Installation) error {
	before, err := readSelection(ctx, dir)
	if err != nil {
		return err
	}
	if before.installation() != expected {
		return provision.ErrStalePlan
	}
	ref := artifactRef{version: artifact.Version, sha256: artifact.SHA256}
	if before.ref == ref && before.file.verified(ref) {
		return ctx.Err()
	}
	name := ref.name()
	candidate, err := inspectObject(ctx, dir, name)
	if err != nil {
		return err
	}

	var staged string
	if !candidate.verified(ref) {
		staged, err = c.stageArtifact(ctx, dir, artifact)
		if err != nil {
			return err
		}
		defer func() { _ = dir.Remove(staged) }()
	}
	var link string
	if before.ref != ref {
		link = ".selection-" + rand.Text()
		if err := dir.Symlink(name, link); err != nil {
			return fmt.Errorf("stage cache selection: %w", err)
		}
		defer func() { _ = dir.Remove(link) }()
	}

	// The lock serializes cooperating writers. Recheck the paths after invoking
	// the opener and compare the selection immediately before publishing.
	checked, err := inspectObject(ctx, dir, name)
	if err != nil {
		return err
	}
	if checked != candidate {
		return provision.ErrStalePlan
	}
	current, err := readSelection(ctx, dir)
	if err != nil {
		return err
	}
	if current != before {
		return provision.ErrStalePlan
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if staged != "" {
		if err := dir.Rename(staged, name); err != nil {
			return fmt.Errorf("publish verified executable: %w", err)
		}
	}
	if link != "" {
		if err := dir.Rename(link, selectionName); err != nil {
			return fmt.Errorf("publish cache selection: %w", err)
		}
	}
	return nil
}

func (c *Cache) stageArtifact(ctx context.Context, dir *os.Root, artifact provision.Artifact) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name := ".executable-" + rand.Text()
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("stage executable: %w", err)
	}
	verified := c.writeArtifact(ctx, file, artifact)
	if err := errors.Join(verified, file.Close()); err != nil {
		_ = dir.Remove(name)
		return "", err
	}
	return name, nil
}

func (c *Cache) writeArtifact(ctx context.Context, file *os.File, artifact provision.Artifact) error {
	source, err := c.open(ctx, artifact)
	if err != nil {
		return fmt.Errorf("open trusted artifact: %w", err)
	}
	if source == nil {
		return errors.New("trusted artifact opener returned no stream")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), contextReader{ctx, source})
	if err := errors.Join(copyErr, source.Close()); err != nil {
		return fmt.Errorf("read trusted artifact: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return ErrDigestMismatch
	}
	if err := file.Chmod(executableMode); err != nil {
		return fmt.Errorf("set executable mode: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync verified executable: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}
