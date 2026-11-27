package aisession

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// files does every file operation of a session through one os.Root opened
// at the worktree root's resolved path ("Phase 7 file access", step 6): a
// link that leaves the root at the moment of use is refused, so a link
// swapped in after the checks still cannot escape.
type files struct {
	root *os.Root
}

func openFiles(w worktree) (*files, error) {
	r, err := os.OpenRoot(w.real)
	if err != nil {
		return nil, err
	}
	return &files{root: r}, nil
}

func (f *files) Close() error { return f.root.Close() }

// fsError is a refused or failed file operation, with its verdict code.
type fsError struct{ code string }

func (e *fsError) Error() string { return "aisession: file access: " + e.code }

func fsCode(err error) string {
	var fe *fsError
	if errors.As(err, &fe) {
		return fe.code
	}
	return VerdictIO
}

func rootErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return &fsError{VerdictNotFound}
	case strings.Contains(err.Error(), "path escapes from parent"):
		return &fsError{VerdictOutsideWorktree}
	}
	return &fsError{VerdictIO}
}

// readChecked returns the content of rel: a regular file of at most
// MaxFileBytes of valid UTF-8, optionally from line (1-based) for limit
// lines. checked is the file the chain classified, as resolve found it (nil
// when it did not exist): the file opened must be that same file
// (os.SameFile), so a file or link swapped in at any time after the check
// is refused (changed).
func (f *files) readChecked(rel string, checked fs.FileInfo, line, limit *uint32) (string, int64, error) {
	name := path.Clean(rel)
	file, err := f.root.Open(name)
	if err != nil {
		return "", 0, rootErr(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, rootErr(err)
	}
	if checked == nil || !os.SameFile(info, checked) {
		return "", 0, &fsError{VerdictChanged}
	}
	if !info.Mode().IsRegular() {
		return "", 0, &fsError{VerdictNotRegular}
	}
	if info.Size() > MaxFileBytes {
		return "", 0, &fsError{VerdictTooLarge}
	}
	b, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return "", 0, rootErr(err)
	}
	if len(b) > MaxFileBytes {
		return "", 0, &fsError{VerdictTooLarge}
	}
	if !utf8.Valid(b) {
		return "", 0, &fsError{VerdictNotText}
	}
	n := int64(len(b))
	s := string(b)
	if line != nil || limit != nil {
		s = lines(s, line, limit)
	}
	return s, n, nil
}

func lines(s string, line, limit *uint32) string {
	all := strings.SplitAfter(s, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = int(*line) - 1
	}
	if start >= len(all) {
		return ""
	}
	end := len(all)
	if limit != nil && start+int(*limit) < end {
		end = start + int(*limit)
	}
	return strings.Join(all[start:end], "")
}

// write replaces rel with content: at most MaxFileBytes; the final
// component is not a link; an existing file has one link only (Unix);
// parents are created inside the root (0755); the file keeps its mode or
// gets 0644; the content goes to a temporary file in the same directory,
// then is renamed through the root. It returns the content's SHA-256.
func (f *files) write(rel, content string) (string, error) {
	return f.writeChecked(rel, content, nil, nil)
}

// writeChecked is write with two checks. The folder rel is in is opened
// once (an os.Root of its own) and the temporary file and the rename go
// through that open folder, so swapping a parent for a link afterwards
// changes nothing. dir is the folder the chain classified, as resolve
// found it: the opened folder must be that same folder (nil: it did not
// exist then, so the folder made now is used). recheck runs right before
// the rename: a non-nil error from it (the path's classification changed
// since the chain ran) refuses the write.
func (f *files) writeChecked(rel, content string, dir fs.FileInfo, recheck func() error) (string, error) {
	if len(content) > MaxFileBytes {
		return "", &fsError{VerdictTooLarge}
	}
	name := path.Clean(rel)
	parent, base := path.Dir(name), path.Base(name)
	// Classified first through the root, before anything is created: a
	// path through a link that leaves the root is outside_worktree on every
	// system and Go release (MkdirAll's own error for it differs).
	if err := classifyTarget(f.root, name); err != nil {
		return "", err
	}
	if parent != "." {
		if err := f.root.MkdirAll(parent, 0o755); err != nil {
			return "", rootErr(err)
		}
	}
	d := f.root
	if parent != "." {
		sub, err := f.root.OpenRoot(parent)
		if err != nil {
			return "", rootErr(err)
		}
		defer sub.Close()
		d = sub
	}
	if dir != nil {
		if info, err := d.Stat("."); err != nil || !os.SameFile(info, dir) {
			return "", &fsError{VerdictChanged}
		}
	}
	// Again through the pinned folder: this is the file the rename replaces.
	if err := classifyTarget(d, base); err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if info, err := d.Lstat(base); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := ".agentflow-write-" + randomHex(8)
	file, err := d.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", rootErr(err)
	}
	_, werr := io.Copy(file, bytes.NewReader([]byte(content)))
	cerr := file.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = d.Chmod(tmp, mode)
	}
	if werr == nil {
		// Checked again right before the rename: a link or hard link made
		// meanwhile is refused.
		if info, err := d.Lstat(base); err == nil && (info.Mode()&os.ModeSymlink != 0 || links(info) > 1) {
			werr = &fsError{VerdictLink}
		}
	}
	if werr == nil && recheck != nil {
		werr = recheck()
	}
	if werr == nil {
		werr = d.Rename(tmp, base)
	}
	if werr != nil {
		_ = d.Remove(tmp)
		var fe *fsError
		if errors.As(werr, &fe) {
			return "", werr
		}
		return "", rootErr(werr)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:]), nil
}

// classifyTarget refuses a write target name in r that is a link, not a
// regular file or a file with more than one link; a missing one is fine.
func classifyTarget(r *os.Root, name string) error {
	info, err := r.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return rootErr(err)
	case info.Mode()&os.ModeSymlink != 0:
		return &fsError{VerdictLink}
	case !info.Mode().IsRegular():
		return &fsError{VerdictNotRegular}
	case links(info) > 1:
		return &fsError{VerdictHardlink}
	}
	return nil
}

// state is what rel holds now: whether it exists, the SHA-256 of its
// content and the content (at most MaxFileBytes + 1 bytes read). A file
// that cannot be read as a regular file has ok false.
func (f *files) state(rel string) (exists bool, sum string, content []byte, ok bool) {
	name := path.Clean(rel)
	file, err := f.root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil, true
	}
	if err != nil {
		return false, "", nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return true, "", nil, false
	}
	b, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return true, "", nil, false
	}
	s := sha256.Sum256(b)
	return true, hex.EncodeToString(s[:]), b, true
}
