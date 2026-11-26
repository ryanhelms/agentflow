package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HashPrefixLen is how many hex characters of a hash name an installed copy's
// directory: <home>/plugins/<id>/<first 16 hex of the hash>.
const HashPrefixLen = 16

// maxTreeDepth bounds directory nesting in a plugin tree.
const maxTreeDepth = 16

var hashRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidHash reports whether h has the form "sha256:<64 hex>".
func ValidHash(h string) bool { return hashRe.MatchString(h) }

// HashPrefix is the first HashPrefixLen hex characters of h, or "" when h
// is not a valid hash.
func HashPrefix(h string) string {
	if !ValidHash(h) {
		return ""
	}
	return h[len("sha256:") : len("sha256:")+HashPrefixLen]
}

// TreeFile is one regular file of a plugin tree, read once: install hashes
// these bytes and writes the same bytes, so what a person confirms is what is
// installed.
type TreeFile struct {
	// Path is relative to the tree's root, with forward slashes.
	Path string
	// Exec is true when the file has an executable bit.
	Exec bool
	Data []byte
}

// TreeError says why a plugin tree is refused.
type TreeError struct {
	Path   string
	Reason string
}

func (e *TreeError) Error() string {
	if e.Path == "" || e.Path == "." {
		return "plugin files: " + e.Reason
	}
	return fmt.Sprintf("plugin files: %s: %s", e.Path, e.Reason)
}

// ReadTree reads every file below root. It accepts regular files and
// directories only: a symbolic link (the root included), a device, a socket
// or a pipe is refused, as are more than MaxFiles files, more than
// MaxTreeBytes bytes, a file name that is not plain text, and a .git
// directory. It never follows a link, so no file outside root is read. The
// files come back sorted by path.
func ReadTree(root string) ([]TreeFile, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("plugin files: %w", err)
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return nil, &TreeError{Reason: "the directory is a symbolic link"}
	case !info.IsDir():
		return nil, &TreeError{Reason: "not a directory"}
	}
	r := &treeReader{root: root}
	if err := r.dir("", 0); err != nil {
		return nil, err
	}
	slices.SortFunc(r.files, func(a, b TreeFile) int { return strings.Compare(a.Path, b.Path) })
	return r.files, nil
}

type treeReader struct {
	root  string
	files []TreeFile
	total int64
}

func (r *treeReader) dir(rel string, depth int) error {
	if depth > maxTreeDepth {
		return &TreeError{Path: rel, Reason: fmt.Sprintf("directories are nested more than %d levels", maxTreeDepth)}
	}
	entries, err := os.ReadDir(filepath.Join(r.root, filepath.FromSlash(rel)))
	if err != nil {
		return fmt.Errorf("plugin files: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		p := path.Join(rel, name)
		if !plainName(name) {
			return &TreeError{Path: strings.ToValidUTF8(p, "?"), Reason: "the file name is not plain text"}
		}
		switch t := e.Type(); {
		case t&os.ModeSymlink != 0:
			return &TreeError{Path: p, Reason: "is a symbolic link"}
		case t.IsDir():
			if name == ".git" {
				return &TreeError{Path: p, Reason: "is a Git directory: add a directory that holds only the plugin's files"}
			}
			if err := r.dir(p, depth+1); err != nil {
				return err
			}
		case t.IsRegular():
			if err := r.file(p); err != nil {
				return err
			}
		default:
			return &TreeError{Path: p, Reason: "is not a regular file"}
		}
	}
	return nil
}

func plainName(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, `\/`) {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

func (r *treeReader) file(rel string) error {
	if len(r.files) >= MaxFiles {
		return &TreeError{Path: rel, Reason: fmt.Sprintf("more than %d files", MaxFiles)}
	}
	full := filepath.Join(r.root, filepath.FromSlash(rel))
	before, err := os.Lstat(full)
	if err != nil {
		return fmt.Errorf("plugin files: %w", err)
	}
	f, err := os.Open(full)
	if err != nil {
		return fmt.Errorf("plugin files: %w", err)
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return fmt.Errorf("plugin files: %w", err)
	}
	// A path that became a link or another file between the listing and the
	// open is not the file that was listed.
	if !before.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return &TreeError{Path: rel, Reason: "changed while it was read"}
	}
	remaining := int64(MaxTreeBytes) - r.total
	data, err := io.ReadAll(io.LimitReader(f, remaining+1))
	if err != nil {
		return fmt.Errorf("plugin files: %w", err)
	}
	if int64(len(data)) > remaining {
		return &TreeError{Reason: fmt.Sprintf("more than %d MiB in all", MaxTreeBytes>>20)}
	}
	r.total += int64(len(data))
	r.files = append(r.files, TreeFile{Path: rel, Exec: before.Mode()&0o111 != 0, Data: data})
	return nil
}

// HashFiles is the content hash of a plugin tree (ADR, "Phase 5 install"):
// for each file sorted by its slash path, the line
// "<path>\x00<x or ->\x00<sha256 hex of the content>\n" ("x" for an
// executable bit), and "sha256:" plus the hex SHA-256 of all lines. The
// manifest is a file of the tree like any other. Directories and modes other
// than the executable bit are not part of it.
func HashFiles(files []TreeFile) string {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b TreeFile) int { return strings.Compare(a.Path, b.Path) })
	h := sha256.New()
	for _, f := range sorted {
		x := "-"
		if f.Exec {
			x = "x"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%x\n", f.Path, x, sha256.Sum256(f.Data))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// HashDir reads the tree at dir (ReadTree) and hashes it (HashFiles). It
// refuses what ReadTree refuses, so a hash always describes only regular
// files inside dir.
func HashDir(dir string) (string, error) {
	files, err := ReadTree(dir)
	if err != nil {
		return "", err
	}
	return HashFiles(files), nil
}
