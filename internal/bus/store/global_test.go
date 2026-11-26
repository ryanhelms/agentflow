package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

func globalEvent(now time.Time) bus.Event {
	return bus.Event{Type: bus.TypeConfigLayerChanged, Source: "test", Repo: bus.GlobalRepo}.Normalize(now)
}

// globalDir returns a store directory inside a fresh private parent, as the
// daemon's registry is.
func globalDir(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "0123456789abcdef01234567.events")
}

func mustOpenGlobal(t *testing.T, dir string, role Role, opt options) *fileStore {
	t.Helper()
	s, err := openGlobal(context.Background(), dir, role, opt)
	if err != nil {
		t.Fatalf("open global %v: %v", role, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGlobalStoreAppendReplayResume(t *testing.T) {
	dir := globalDir(t)
	owner := mustOpenGlobal(t, dir, RoleOwner, testOptions())
	if owner.Repo() != bus.GlobalRepo {
		t.Fatalf("repo = %q", owner.Repo())
	}
	if data, err := os.ReadFile(filepath.Join(dir, RepoIDFile)); err != nil || string(data) != "global" {
		t.Fatalf("repo-id = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %v, %v", info.Mode(), err)
		}
	}
	for range 3 {
		if _, err := owner.Append(context.Background(), globalEvent(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	// A repository event, or one with a worktree, does not belong here.
	if _, err := owner.Append(context.Background(), event("0123456789abcdef", time.Now())); !errors.Is(err, ErrWrongRepo) {
		t.Fatalf("repo event err = %v", err)
	}
	wt := globalEvent(time.Now())
	wt.Worktree = "main"
	if _, err := owner.Append(context.Background(), wt); err == nil {
		t.Fatal("global event with a worktree accepted")
	}

	reader := mustOpenGlobal(t, dir, RoleReader, testOptions())
	wantRange(t, seqs(t, reader, bus.Cursor{}), 1, 3)
	got, err := Replay(context.Background(), reader, bus.Cursor{bus.GlobalRepo: 1})
	if err != nil || len(got) != 2 || got[0].Seq != 2 || got[0].Repo != bus.GlobalRepo {
		t.Fatalf("Replay = %+v, %v", got, err)
	}
	if head, _ := reader.Head(context.Background()); head.String() != "v1:global=3" {
		t.Fatalf("Head = %v", head)
	}
	if _, err := openGlobal(context.Background(), dir, RoleOwner, testOptions()); !errors.Is(err, ErrOwned) {
		t.Fatalf("second owner err = %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	again := mustOpenGlobal(t, dir, RoleOwner, testOptions())
	if _, err := again.Append(context.Background(), globalEvent(time.Now())); err != nil {
		t.Fatal(err)
	}
	wantRange(t, seqs(t, again, bus.Cursor{}), 1, 4)
}

func TestGlobalStoreRotatesAndRetains(t *testing.T) {
	c := newClock()
	opt := testOptions()
	opt.now = c.now
	opt.segmentBytes = 1 // one event per segment
	opt.retainBytes = 1 << 30
	opt.retainAge = 10 * time.Hour
	s := mustOpenGlobal(t, globalDir(t), RoleOwner, opt)
	for range 3 {
		if _, err := s.Append(context.Background(), globalEvent(c.now())); err != nil {
			t.Fatal(err)
		}
		c.advance(time.Hour)
	}
	if names := segmentNames(t, s); len(names) != 3 {
		t.Fatalf("segments = %v", names)
	}
	c.advance(12 * time.Hour)
	if _, err := s.Append(context.Background(), globalEvent(c.now())); err != nil {
		t.Fatal(err)
	}
	if names := segmentNames(t, s); len(names) != 1 || names[0] != "00000000000000000004.jsonl" {
		t.Fatalf("segments after retention = %v", names)
	}
}

func TestGlobalStoreInUnion(t *testing.T) {
	c := newClock()
	opt := testOptions()
	opt.now = c.now
	repo := mustOpen(t, t.TempDir(), RoleOwner, opt)
	global := mustOpenGlobal(t, globalDir(t), RoleOwner, opt)
	appendN(t, repo, 1)
	c.advance(time.Second)
	if _, err := global.Append(context.Background(), globalEvent(c.now())); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Second)
	appendN(t, repo, 1)

	u := Union(repo, global)
	var got []bus.RepoID
	for e, err := range u.ReadFrom(context.Background(), bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Repo)
	}
	if len(got) != 3 || got[0] != repo.Repo() || got[1] != bus.GlobalRepo || got[2] != repo.Repo() {
		t.Fatalf("union order = %v", got)
	}
	head, err := u.Head(context.Background())
	if err != nil || head[bus.GlobalRepo] != 1 || head[repo.Repo()] != 2 {
		t.Fatalf("Head = %v, %v", head, err)
	}
	// A cursor with a global entry resumes after it.
	cur, err := bus.ParseCursor("v1:global=1")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for e, err := range u.ReadFrom(context.Background(), cur) {
		if err != nil || e.Repo == bus.GlobalRepo {
			t.Fatalf("resumed read yielded %+v, %v", e, err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("resumed read yielded %d events", n)
	}
}

func TestGlobalStoreRefusesSharedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode bits; the daemon checks the registry DACL")
	}
	dir := globalDir(t)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { // umask may have cleared bits
		t.Fatal(err)
	}
	for _, role := range []Role{RoleOwner, RoleAppender, RoleReader} {
		if _, err := OpenGlobal(context.Background(), dir, role); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v on group-readable dir: err = %v", role, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, RepoIDFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repo-id written into a shared directory: %v", err)
	}
}

func TestGlobalStoreRefusesSymlink(t *testing.T) {
	parent := filepath.Dir(globalDir(t))
	real := filepath.Join(parent, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link.events")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := OpenGlobal(context.Background(), link, RoleOwner); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("symlinked dir err = %v", err)
	}
}

func TestGlobalAndRepoStoresDoNotMix(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenGlobal(ctx, "relative.events", RoleOwner); err == nil {
		t.Fatal("relative global dir accepted")
	}
	if _, err := OpenGlobal(ctx, globalDir(t), RoleReader); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader of missing global store err = %v", err)
	}
	// A repository store's directory is not a global store, and back.
	common := t.TempDir()
	repo := mustOpen(t, common, RoleOwner, testOptions())
	_ = repo.Close()
	if err := os.Chmod(Path(common), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGlobal(ctx, Path(common), RoleReader); err == nil {
		t.Fatal("repository store opened as global")
	}
	gdir := globalDir(t)
	g := mustOpenGlobal(t, gdir, RoleOwner, testOptions())
	_ = g.Close()
	if _, err := openDir(ctx, gdir, RoleReader, testOptions(), false); err == nil {
		t.Fatal("global store opened as a repository store")
	}
}
