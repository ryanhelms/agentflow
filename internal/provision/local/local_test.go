//go:build darwin || linux

package local

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/ryanhelms/agentflow/internal/provision"
)

var (
	env = provision.Environment{ID: "test", Platform: provision.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}}
	cli = provision.Target{ID: "companion", Kind: provision.CLI, Command: "companion", Health: provision.HealthProbe{Name: "local"}}
)

func script(value string) string { return "#!/bin/sh\nprintf '" + value + "\\n'\n" }

func fixture(t *testing.T, sourceDir, value string, version uint) provision.Artifact {
	t.Helper()
	name := filepath.Join(sourceDir, value)
	data := script(value)
	if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return provision.Artifact{
		Platform: env.Platform, Version: provision.Version{Major: version},
		Source: (&url.URL{Scheme: "file", Path: name}).String(), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(data))),
	}
}

func openFixture(_ context.Context, artifact provision.Artifact) (io.ReadCloser, error) {
	u, err := url.Parse(artifact.Source)
	if err != nil {
		return nil, err
	}
	return os.Open(u.Path)
}

func newCache(t *testing.T, dir string, open OpenArtifact) *Cache {
	t.Helper()
	cache, err := New(dir, env, open)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func planFor(t *testing.T, cache *Cache, artifact provision.Artifact) provision.Plan {
	t.Helper()
	manifest := provision.Manifest{ProviderID: "provider", Companions: []provision.Companion{{
		Target: cli, PinArtifact: true, Artifacts: []provision.Artifact{artifact},
	}}}
	plan, err := provision.BuildPlan(t.Context(), env, []provision.Manifest{manifest}, cache)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func apply(t *testing.T, cache *Cache, plan provision.Plan) Receipt {
	t.Helper()
	if err := provision.Apply(t.Context(), env, plan, cache, cache); err != nil {
		t.Fatal(err)
	}
	receipt, err := cache.Resolve(t.Context(), env, cli)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func expectedFor(receipt Receipt) provision.Installation {
	return provision.Installation{Present: true, VersionKnown: true, Version: receipt.Version, SHA256: receipt.SHA256}
}

func TestApplyResolvesAndReusesPinnedExecutable(t *testing.T) {
	root, source, path := t.TempDir(), t.TempDir(), t.TempDir()
	one, two := fixture(t, source, "one", 1), fixture(t, source, "two", 1)
	unmanaged := filepath.Join(path, cli.Command)
	if err := os.WriteFile(unmanaged, []byte(script("unmanaged")), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	var opens int
	cache := newCache(t, root, func(ctx context.Context, artifact provision.Artifact) (io.ReadCloser, error) {
		opens++
		return openFixture(ctx, artifact)
	})
	plan := planFor(t, cache, one)
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 || plan.Steps()[0].Action != provision.Install {
		t.Fatalf("planning touched the cache or did not plan an install: %v, %v", entries, err)
	}
	first := apply(t, cache, plan)
	if first.Version != one.Version || first.SHA256 != one.SHA256 || !filepath.IsAbs(first.Path) {
		t.Fatalf("receipt = %+v", first)
	}
	if info, err := os.Lstat(first.Path); err != nil || info.Mode() != 0o500 {
		t.Fatalf("published executable info = %v, %v", info, err)
	}
	if output, err := exec.CommandContext(t.Context(), first.Path).Output(); err != nil || string(output) != "one\n" {
		t.Fatalf("launch using receipt: %q, %v", output, err)
	}
	verified := planFor(t, cache, one)
	if verified.Steps()[0].Action != provision.Verify || apply(t, cache, verified) != first || opens != 1 {
		t.Fatalf("installed artifact was not reused, opens = %d", opens)
	}
	second := apply(t, cache, planFor(t, cache, two))
	if second.Path == first.Path || second.Version != two.Version || second.SHA256 != two.SHA256 {
		t.Fatalf("second receipt = %+v", second)
	}
	if data, err := os.ReadFile(first.Path); err != nil || string(data) != script("one") {
		t.Fatalf("selecting a new digest changed the prior executable: %q, %v", data, err)
	}
	// A new cache handle and a failed source still allow selecting verified local bytes.
	reopened := newCache(t, root, func(context.Context, provision.Artifact) (io.ReadCloser, error) {
		return nil, errors.New("source is unavailable")
	})
	if got := apply(t, reopened, planFor(t, reopened, one)); got != first || opens != 2 {
		t.Fatalf("reselected receipt = %+v, opens = %d", got, opens)
	}
	if data, err := os.ReadFile(unmanaged); err != nil || string(data) != script("unmanaged") {
		t.Fatalf("unmanaged executable changed: %q, %v", data, err)
	}
}

func TestReconcilesDamagedAndMissingExecutables(t *testing.T) {
	cache := newCache(t, t.TempDir(), openFixture)
	one := fixture(t, t.TempDir(), "one", 1)
	first := apply(t, cache, planFor(t, cache, one))
	for name, damage := range map[string]func(string) error{
		"bytes": func(path string) error {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(script("tampered")), 0o600); err != nil {
				return err
			}
			return os.Chmod(path, 0o500)
		},
		"mode":    func(path string) error { return os.Chmod(path, 0o600) },
		"missing": os.Remove,
	} {
		t.Run(name, func(t *testing.T) {
			if err := damage(first.Path); err != nil {
				t.Fatal(err)
			}
			if receipt, err := cache.Resolve(t.Context(), env, cli); !errors.Is(err, fs.ErrNotExist) || receipt.Path != "" {
				t.Fatalf("damaged executable resolved: %+v, %v", receipt, err)
			}
			plan := planFor(t, cache, one)
			if plan.Steps()[0].Action != provision.Install || apply(t, cache, plan) != first {
				t.Fatal("damaged executable did not reconcile to its pinned identity")
			}
		})
	}
}

type closeFailure struct {
	io.Reader
	err error
}

func (r closeFailure) Close() error { return r.err }

func TestFailedInstallsKeepSelectionAndCanRetry(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	one, two := fixture(t, source, "one", 1), fixture(t, source, "two", 2)
	failure := errors.New("fixture failure")
	for _, tc := range []struct {
		name string
		open OpenArtifact
		want error
	}{
		{"open", func(context.Context, provision.Artifact) (io.ReadCloser, error) { return nil, failure }, failure},
		{"read", func(context.Context, provision.Artifact) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(failure))), nil
		}, failure},
		{"close", func(context.Context, provision.Artifact) (io.ReadCloser, error) {
			return closeFailure{strings.NewReader(script("two")), failure}, nil
		}, failure},
		{"digest", func(context.Context, provision.Artifact) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(script("wrong"))), nil
		}, ErrDigestMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp(root, "attempt-")
			if err != nil {
				t.Fatal(err)
			}
			cache := newCache(t, dir, openFixture)
			first := apply(t, cache, planFor(t, cache, one))
			failing := newCache(t, dir, tc.open)
			if err := failing.Install(t.Context(), env, cli, two, expectedFor(first)); !errors.Is(err, tc.want) {
				t.Fatalf("failed install = %v, want %v", err, tc.want)
			}
			if receipt, err := cache.Resolve(t.Context(), env, cli); err != nil || receipt != first {
				t.Fatalf("selection after failure = %+v, %v", receipt, err)
			}
			if entries, err := os.ReadDir(filepath.Dir(first.Path)); err != nil || len(entries) != 2 {
				t.Fatalf("failed install left a cache entry: %v, %v", entries, err)
			}
			if receipt := apply(t, cache, planFor(t, cache, two)); receipt.Version != two.Version {
				t.Fatalf("retry receipt = %+v", receipt)
			}
			if err := cache.Install(t.Context(), env, cli, one, expectedFor(first)); !errors.Is(err, provision.ErrStalePlan) {
				t.Fatalf("stale install = %v", err)
			}
		})
	}
}

func TestInstallRechecksAtPublication(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	one, two := fixture(t, source, "one", 1), fixture(t, source, "two", 2)
	cache := newCache(t, root, openFixture)
	first := apply(t, cache, planFor(t, cache, one))
	selectionPath := filepath.Join(filepath.Dir(first.Path), selectionName)
	changing := newCache(t, root, func(ctx context.Context, artifact provision.Artifact) (io.ReadCloser, error) {
		if err := os.Remove(selectionPath); err != nil {
			return nil, err
		}
		return openFixture(ctx, artifact)
	})
	if err := changing.Install(t.Context(), env, cli, two, expectedFor(first)); !errors.Is(err, provision.ErrStalePlan) {
		t.Fatalf("install after selection changed = %v", err)
	}
	if entries, err := os.ReadDir(filepath.Dir(first.Path)); err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(first.Path) {
		t.Fatalf("stale install published cache entries: %v, %v", entries, err)
	}
	if got := apply(t, cache, planFor(t, cache, one)); got != first {
		t.Fatalf("selection recovery = %+v", got)
	}
}

func TestRejectsPathsOutsideManagedLayout(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	one := fixture(t, outside, "one", 1)
	opener := func(context.Context, provision.Artifact) (io.ReadCloser, error) {
		t.Fatal("unsafe input reached the artifact opener")
		return nil, nil
	}
	cache := newCache(t, root, opener)
	escaping := cli
	escaping.ID = "../companion"
	if err := cache.Install(t.Context(), env, escaping, one, provision.Installation{}); err == nil {
		t.Fatal("accepted escaping target id")
	}
	invalid := one
	invalid.Source = "https://user:secret@example.com/companion"
	if err := cache.Install(t.Context(), env, cli, invalid, provision.Installation{}); err == nil {
		t.Fatal("accepted source with credentials")
	}
	if err := os.Symlink(outside, filepath.Join(root, env.ID)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Install(t.Context(), env, cli, one, provision.Installation{}); err == nil {
		t.Fatal("followed a symlink in the cache layout")
	}
	cache = newCache(t, t.TempDir(), opener)
	dir, err := cache.targetDir(cli, true)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := dir.Symlink(one.Source, selectionName); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(t.Context(), env, cli); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid selection was not rejected: %v", err)
	}
	if err := dir.Remove(selectionName); err != nil {
		t.Fatal(err)
	}
	if err := dir.Symlink(filepath.Join(outside, "one"), (artifactRef{one.Version, one.SHA256}).name()); err != nil {
		t.Fatal(err)
	}
	if err := cache.Install(t.Context(), env, cli, one, provision.Installation{}); err == nil {
		t.Fatal("accepted a symlink in the executable slot")
	}
}

func TestCacheLockAcrossProcesses(t *testing.T) {
	const childCache = "AGENTFLOW_TEST_LOCKED_COMPANION_CACHE"
	if root := os.Getenv(childCache); root != "" {
		cache := newCache(t, root, func(context.Context, provision.Artifact) (io.ReadCloser, error) {
			t.Fatal("a second process reached the opener with the lock held")
			return nil, nil
		})
		artifact := fixture(t, t.TempDir(), "child", 1)
		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
		defer cancel()
		if err := cache.Install(ctx, env, cli, artifact, provision.Installation{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("install while parent holds lock = %v", err)
		}
		return
	}
	root := t.TempDir()
	cache := newCache(t, root, openFixture)
	dir, err := cache.targetDir(cli, true)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	lock, err := lockDirectory(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCacheLockAcrossProcesses$")
	cmd.Env = append(os.Environ(), childCache+"="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child lock check: %v\n%s", err, output)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	one := fixture(t, t.TempDir(), "one", 1)
	apply(t, cache, planFor(t, cache, one))
}
