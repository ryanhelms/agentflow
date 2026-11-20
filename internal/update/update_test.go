package update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/gh"
)

func TestVersions(t *testing.T) {
	a, _ := ParseVersion("v0.4.2")
	b, _ := ParseVersion("0.5.0-beta.1")
	c, _ := ParseVersion("0.5.0")
	if !a.Less(b) || !b.Less(c) || c.Less(a) {
		t.Error("ordering wrong")
	}
	p := Policy{MinVersion: "0.5.1", Blocked: []string{"0.6.0"}}
	if !p.Requires("0.5.0") || p.Requires("0.5.1") || !p.Requires("v0.6.0") || p.Requires("dev") {
		t.Error("policy wrong")
	}
}

func makeArchive(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho new\n")
	_ = tw.WriteHeader(&tar.Header{Name: "devflow", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	f.Close()
	return path
}

func TestLatestAndInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz path only")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DEVFLOW_INSECURE_SKIP_ATTESTATION", "1")
	src := t.TempDir()
	name := AssetName("0.5.0")
	arch := makeArchive(t, src, name)
	data, _ := os.ReadFile(arch)
	sum := sha256.Sum256(data)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			rels := []map[string]any{
				{"tag_name": "v0.6.0-beta.1", "prerelease": true, "assets": []any{}},
				{"tag_name": "v1.0.0", "assets": []any{}},
				{"tag_name": "v0.5.0", "assets": []map[string]string{
					{"name": name, "url": srv.URL + "/a"}, {"name": "checksums.txt", "url": srv.URL + "/s"}}},
				{"tag_name": "v0.4.0", "assets": []any{}},
			}
			_ = json.NewEncoder(w).Encode(rels)
		case r.URL.Path == "/a":
			_, _ = w.Write(data)
		case r.URL.Path == "/s":
			_, _ = w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := gh.New("")
	client.API = srv.URL
	u, err := New("0.4.0", Options{Channel: "stable"}, client)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := u.Latest(t.Context())
	if err != nil || rel == nil || rel.Tag != "v0.5.0" {
		t.Fatalf("latest = %+v, %v (track should exclude v1, stable should exclude beta)", rel, err)
	}
	if !u.NewerThanCurrent(rel) {
		t.Fatal("expected newer")
	}
	if err := u.Install(t.Context(), rel); err != nil {
		t.Fatal(err)
	}
	if !u.Installed("0.5.0") {
		t.Fatal("not activated")
	}
	if _, err := os.Stat(filepath.Join(u.Dir, "current", "devflow")); err != nil {
		t.Fatal(err)
	}

	u.Opts.Channel = "beta"
	rel, _ = u.Latest(t.Context())
	if rel.Tag != "v0.6.0-beta.1" {
		t.Errorf("beta channel latest = %s", rel.Tag)
	}
}

func TestChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.tar.gz")
	_ = os.WriteFile(f, []byte("data"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("deadbeef  x.tar.gz\n"), 0o644)
	if err := VerifyChecksum(f, filepath.Join(dir, "checksums.txt")); err == nil {
		t.Fatal("mismatch accepted")
	}
}
