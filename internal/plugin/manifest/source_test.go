package manifest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// install copies a plugin's files into home the way plugin add does, and
// returns its registry entry. It does not write the registry.
func install(t *testing.T, home, text string) manifest.Installed {
	t.Helper()
	m := mustLoad(t, text)
	files := []manifest.TreeFile{{Path: manifest.FileName, Data: []byte(text)}}
	hash := manifest.HashFiles(files)
	dir := manifest.InstallPath(home, m.ID, hash)
	must(t, os.MkdirAll(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(text), 0o400))
	return manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Hash: hash, Dir: dir, InstalledAt: time.Unix(1, 0).UTC(), Manifest: m}
}

func register(t *testing.T, home string, ps ...manifest.Installed) {
	t.Helper()
	must(t, manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
		r.Plugins = append(r.Plugins, ps...)
		return nil
	}))
}

func TestFileSourceListsRegistrySorted(t *testing.T) {
	home := t.TempDir()
	src := manifest.NewFileSource(home)
	if got, err := src.Installed(t.Context()); err != nil || len(got) != 0 {
		t.Fatalf("empty home: %v, %v", got, err)
	}
	register(t, home, install(t, home, timelineHints), install(t, home, ciNotify))
	got, err := src.Installed(t.Context())
	if err != nil || len(got) != 2 || got[0].ID != "ci-notify" || got[1].ID != "timeline-hints" {
		t.Fatalf("Installed = %+v, %v", got, err)
	}
	if got[0].Manifest.Settings[0].Key != "signal" {
		t.Errorf("the registry lost the manifest: %+v", got[0].Manifest)
	}
}

func TestFileSourceAddsBuiltins(t *testing.T) {
	home := t.TempDir()
	register(t, home, install(t, home, ciNotify))
	built := manifest.Installed{ID: "ci-notify", Tier: manifest.TierBuiltin, Version: "1.0.0"}
	other := manifest.Installed{ID: "aaa-builtin", Tier: manifest.TierBuiltin, Version: "1.0.0"}
	src := manifest.NewFileSource(home).WithBuiltins(func() []manifest.Installed { return []manifest.Installed{built, other} })
	got, err := src.Installed(t.Context())
	if err != nil || len(got) != 2 || got[0].ID != "aaa-builtin" || got[1].Tier != manifest.TierBuiltin {
		t.Fatalf("Installed = %+v, %v: a builtin shadows a registry entry of its ID", got, err)
	}
	if err := src.Verify(t.Context(), built); err != nil {
		t.Errorf("a builtin does not verify: %v", err)
	}
}

func TestFileSourceRevisionFollowsTheRegistry(t *testing.T) {
	home := t.TempDir()
	src := manifest.NewFileSource(home)
	if src.Revision() != "absent" {
		t.Errorf("Revision with no registry = %q", src.Revision())
	}
	register(t, home, install(t, home, ciNotify))
	first := src.Revision()
	if first == "absent" || first != src.Revision() {
		t.Fatalf("Revision = %q, then %q", first, src.Revision())
	}
	register(t, home, install(t, home, timelineHints))
	if src.Revision() == first {
		t.Error("Revision did not change after the registry did")
	}
}

func TestRegistryFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes differ on Windows")
	}
	home := t.TempDir()
	register(t, home, install(t, home, ciNotify))
	for path, want := range map[string]os.FileMode{
		manifest.PluginsPath(home):  0o700,
		manifest.RegistryPath(home): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", path, info.Mode().Perm(), err, want)
		}
	}
}

func TestUpdateRegistryKeepsEveryConcurrentEntry(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("plugin-%02d", i)
			err := manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
				r.Plugins = append(r.Plugins, manifest.Installed{ID: id, Version: "1.0.0", Tier: manifest.TierDeclarative})
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	reg, err := manifest.ReadRegistry(home)
	if err != nil || len(reg.Plugins) != 12 {
		t.Fatalf("registry holds %d plugins, %v", len(reg.Plugins), err)
	}
}

func TestUpdateRegistryWritesNothingOnError(t *testing.T) {
	home := t.TempDir()
	register(t, home, install(t, home, ciNotify))
	before, _ := os.ReadFile(manifest.RegistryPath(home))
	boom := errors.New("boom")
	err := manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
		r.Plugins = nil
		return boom
	})
	after, _ := os.ReadFile(manifest.RegistryPath(home))
	if !errors.Is(err, boom) || string(before) != string(after) {
		t.Errorf("err = %v; the registry changed", err)
	}
	entries, _ := os.ReadDir(manifest.PluginsPath(home))
	for _, e := range entries {
		if e.Name() != "installed.json" && e.Name() != "installed.json.lock" && e.Name() != "ci-notify" {
			t.Errorf("stray file %s", e.Name())
		}
	}
}

func TestReadRegistryRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":        "{",
		"wrong version":   `{"version": 2, "plugins": []}`,
		"missing version": `{"plugins": []}`,
		"duplicate":       `{"version": 1, "plugins": [{"id": "a"}, {"id": "a"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			must(t, os.MkdirAll(manifest.PluginsPath(home), 0o700))
			must(t, os.WriteFile(manifest.RegistryPath(home), []byte(content), 0o600))
			if _, err := manifest.ReadRegistry(home); err == nil {
				t.Error("ReadRegistry accepted it")
			}
			if _, err := manifest.NewFileSource(home).Installed(t.Context()); err == nil {
				t.Error("Installed accepted it")
			}
		})
	}
}

func TestInstallPaths(t *testing.T) {
	hash := manifest.HashFiles(nil)
	home := filepath.FromSlash("/h")
	if got, want := manifest.InstallPath(home, "ci-notify", hash), filepath.Join(home, "plugins", "ci-notify", hash[7:23]); got != want {
		t.Errorf("InstallPath = %q, want %q", got, want)
	}
	if manifest.InstallPath(home, "../x", hash) != "" || manifest.InstallPath(home, "ci-notify", "sha256:x") != "" {
		t.Error("InstallPath built a path from invalid input")
	}
	if got, want := manifest.DataPath(home, "ci-notify"), filepath.Join(home, "plugins", "ci-notify", "data"); got != want {
		t.Errorf("DataPath = %q, want %q", got, want)
	}
	if manifest.DataPath(home, "a/b") != "" {
		t.Error("DataPath built a path from an invalid ID")
	}
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	prepare := func(t *testing.T) (*manifest.FileSource, string, manifest.Installed) {
		t.Helper()
		home := t.TempDir()
		p := install(t, home, ciNotify)
		register(t, home, p)
		return manifest.NewFileSource(home), home, p
	}
	t.Run("intact", func(t *testing.T) {
		src, _, p := prepare(t)
		if err := src.Verify(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ := src.Installed(ctx)
		if err := src.Verify(ctx, got[0]); err != nil {
			t.Errorf("an entry read back from the registry does not verify: %v", err)
		}
	})
	t.Run("a file changed", func(t *testing.T) {
		src, _, p := prepare(t)
		path := filepath.Join(p.Dir, manifest.FileName)
		must(t, os.Chmod(path, 0o600))
		must(t, os.WriteFile(path, []byte(ciNotify+"# changed\n"), 0o600))
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch", err)
		}
	})
	t.Run("a file added", func(t *testing.T) {
		src, _, p := prepare(t)
		must(t, os.WriteFile(filepath.Join(p.Dir, "extra"), []byte("x"), 0o600))
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch", err)
		}
	})
	t.Run("a link added", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no symbolic links")
		}
		src, _, p := prepare(t)
		must(t, os.Symlink("/etc/passwd", filepath.Join(p.Dir, "link")))
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch", err)
		}
	})
	t.Run("copy removed", func(t *testing.T) {
		src, _, p := prepare(t)
		must(t, os.RemoveAll(p.Dir))
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch", err)
		}
	})
	t.Run("directory elsewhere", func(t *testing.T) {
		src, _, p := prepare(t)
		other := t.TempDir()
		must(t, os.WriteFile(filepath.Join(other, manifest.FileName), []byte(ciNotify), 0o600))
		p.Dir = other
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch: only agentflow's own layout is run", err)
		}
	})
	t.Run("hash of another plugin", func(t *testing.T) {
		src, _, p := prepare(t)
		p.Hash = manifest.HashFiles(nil)
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch", err)
		}
	})
	t.Run("registry grants more than the copy", func(t *testing.T) {
		src, _, p := prepare(t)
		p.Manifest.Permissions.Invoke = append(p.Manifest.Permissions.Invoke, "comment")
		if err := src.Verify(ctx, p); !errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want ErrHashMismatch", err)
		}
	})
	t.Run("manifest no longer valid", func(t *testing.T) {
		home := t.TempDir()
		bad := ciNotify + "extra: 1\n"
		files := []manifest.TreeFile{{Path: manifest.FileName, Data: []byte(bad)}}
		p := manifest.Installed{ID: "ci-notify", Tier: manifest.TierDeclarative, Hash: manifest.HashFiles(files)}
		p.Dir = manifest.InstallPath(home, p.ID, p.Hash)
		must(t, os.MkdirAll(p.Dir, 0o700))
		must(t, os.WriteFile(filepath.Join(p.Dir, manifest.FileName), []byte(bad), 0o400))
		err := manifest.NewFileSource(home).Verify(ctx, p)
		var ve *manifest.ValidationError
		if !errors.As(err, &ve) || errors.Is(err, manifest.ErrHashMismatch) {
			t.Errorf("Verify = %v, want a *ValidationError", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		src, _, p := prepare(t)
		c, cancel := context.WithCancel(ctx)
		cancel()
		if err := src.Verify(c, p); !errors.Is(err, context.Canceled) {
			t.Errorf("Verify = %v", err)
		}
	})
}
