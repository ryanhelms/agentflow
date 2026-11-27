package jsonrpc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDirAllow(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir()) // Launch passes the resolved root
	data := t.TempDir()
	work := t.TempDir()
	sub := filepath.Join(work, "src")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	link := filepath.Join(work, "out")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }

	// Without AllowDir the old rule refuses a directory outside root and data.
	if _, err := resolveDirAllow(work, root, nil, data, ""); err == nil {
		t.Fatal("no AllowDir: a directory outside root and data was accepted")
	}
	for _, c := range []struct {
		name, dir, allow string
		want             string
	}{
		{"the allowed directory", work, work, real(work)},
		{"below it", sub, work, real(sub)},
		{"inside root still", root, work, real(root)},
		{"inside data still", data, work, real(data)},
	} {
		got, err := resolveDirAllow(c.dir, root, nil, data, c.allow)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct{ name, dir, allow string }{
		{"outside every directory", other, work},
		{"a link that leaves it", link, work},
		{"a relative allowed directory", work, "rel"},
		{"a missing allowed directory", work, filepath.Join(work, "missing")},
		{"a relative working directory", "src", work},
	} {
		if got, err := resolveDirAllow(c.dir, root, nil, data, c.allow); err == nil {
			t.Errorf("%s: accepted as %q", c.name, got)
		}
	}
}
