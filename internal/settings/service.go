package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Display paths of the two layer files. Personal YAML never leaves the disk:
// only this name and field values do.
const (
	RepoFile     = consolecfg.MainFile
	PersonalFile = "~/.agentflow/config.yml"
)

// Service checks, previews, saves and undoes ChangeSets for one worktree.
// Every method reads the layer files again: the files are the source of
// truth, never a client's copy.
type Service struct {
	// Root is the worktree root; empty outside a repository, where only the
	// personal layer can change.
	Root string
	Repo *gitx.Repo
	// Home is the agentflow home (config.Home), which holds the personal
	// layer, its lock and the undo journal.
	Home    string
	Catalog Catalog
	// Publisher publishes repository edits in draft and direct mode.
	Publisher Publisher
	// Client names the caller in the journal ("cli", "claude",
	// "repository-console").
	Client string
	Now    func() time.Time
	// PullState, when set, returns the known state ("open", "merged" or
	// "closed") of the pull request numbered n, without calling GitHub: the
	// console sets it from the GitHub poller's cache, so the projection's
	// undoable follows a merge. ok is false when the state is unknown.
	PullState func(n int) (state string, ok bool)
	// Audit writes the audit line of a save or undo, with the class and
	// action the save was actually gated under (policy.Gate.AuditOperation).
	// Nil audits nothing.
	Audit func(policy.OperationAudit)
}

// New returns the Service of a's worktree.
func New(a *app.App) *Service {
	s := &Service{Home: a.Home, Client: a.Client, Publisher: DefaultPublisher, Now: time.Now}
	if a.Gate != nil {
		s.Audit = a.Gate.AuditOperation
	}
	if a.Repo != nil {
		s.Root, s.Repo = a.Repo.Dir, a.Repo
	}
	if s.Home == "" {
		s.Home, _ = config.HomeDir()
	}
	s.Catalog = consolecfg.RepoCatalog(s.Root)
	return s
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// layer is one layer file as read now.
type layer struct {
	Layer    config.FieldLayer
	Path     string // display path
	abs      string
	Content  string
	Exists   bool
	Revision string
}

// files is both layer files.
type files struct {
	personal, repo layer
	// personalErr says why the personal layer cannot be used: its edits
	// are refused, and the repository layer still works.
	personalErr error
}

func (f files) of(l config.FieldLayer) *layer {
	if l == config.FieldPersonal {
		return &f.personal
	}
	return &f.repo
}

func (f files) revisions() Revisions {
	r := Revisions{Personal: f.personal.Revision}
	if f.repo.abs != "" {
		r.Repo = f.repo.Revision
	}
	return r
}

// Revision is the revision of a layer file's bytes.
func Revision(content string, exists bool) string {
	if !exists {
		return RevisionAbsent
	}
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Service) personalPath() string { return filepath.Join(s.Home, "config.yml") }

func (s *Service) read() (files, error) {
	var f files
	var err error
	f.personal, f.personalErr = readPersonal(s.personalPath())
	if s.Root != "" {
		f.repo, err = readLayer(config.FieldRepo, RepoFile, filepath.Join(s.Root, filepath.FromSlash(RepoFile)))
	} else {
		f.repo = layer{Layer: config.FieldRepo, Path: RepoFile, Revision: RevisionAbsent}
	}
	return f, err
}

// readPersonal reads the personal layer. Like config.LoadUser, it follows
// a symbolic link (a dotfiles checkout), but only to a regular file owned by
// the current user; saves then write through the link to that file. A file
// it cannot use reads as absent, with the reason as the error.
func readPersonal(path string) (layer, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return layer{Layer: config.FieldPersonal, Path: PersonalFile, abs: path, Revision: RevisionAbsent},
				fmt.Errorf("%s is a link to a file that does not exist", PersonalFile)
		}
		ti, err := os.Stat(target)
		if err != nil || !ti.Mode().IsRegular() || !ownedByCurrentUser(ti) {
			return layer{Layer: config.FieldPersonal, Path: PersonalFile, abs: path, Revision: RevisionAbsent},
				fmt.Errorf("%s links to a file that is not a regular file owned by you", PersonalFile)
		}
		path = target
	}
	l, err := readLayer(config.FieldPersonal, PersonalFile, path)
	if err != nil {
		l = layer{Layer: config.FieldPersonal, Path: PersonalFile, abs: path, Revision: RevisionAbsent}
	}
	return l, err
}

// readLayer reads a layer file: a regular file, not a link, at most
// MaxLayerBytes.
func readLayer(l config.FieldLayer, display, path string) (layer, error) {
	out := layer{Layer: l, Path: display, abs: path, Revision: RevisionAbsent}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("cannot read %s", display)
	}
	if !info.Mode().IsRegular() {
		return out, fmt.Errorf("%s must be a regular file", display)
	}
	file, err := os.Open(path)
	if err != nil {
		return out, fmt.Errorf("cannot read %s", display)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, consolecfg.MaxLayerBytes+1))
	if err != nil {
		return out, fmt.Errorf("cannot read %s", display)
	}
	if len(data) > consolecfg.MaxLayerBytes {
		return out, fmt.Errorf("%s exceeds the settings editor limit", display)
	}
	out.Content, out.Exists, out.Revision = string(data), true, Revision(string(data), true)
	return out, nil
}

// branch is the worktree's branch, "" when detached or outside a repository.
func (s *Service) branch(ctx context.Context) string {
	if s.Repo == nil {
		return ""
	}
	return s.Repo.Branch(ctx)
}

// fieldKeys splits an ID into its layer and keys. The ChangeSet syntax link
// has checked it.
func fieldKeys(id string) (config.FieldLayer, []string, error) {
	l, path, err := config.ParseFieldID(id)
	if err != nil {
		return "", nil, err
	}
	keys, err := config.ParseFieldPath(path)
	return l, keys, err
}

// isPrefix reports whether a is a prefix of b, or equal.
func isPrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
