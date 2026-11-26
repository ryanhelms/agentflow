//go:build darwin || linux

package managedgh

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provision"
	"github.com/ryanhelms/agentflow/internal/provision/local"
	"github.com/ryanhelms/agentflow/internal/provision/user"
)

func TestPinTrustAndIntegrity(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("AGENTFLOW_HOME", home)
	source := filepath.Join(t.TempDir(), "gh")
	const raw = "#!/bin/sh\nexit 0\n"
	put := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pin := fmt.Sprintf("gh:\n- platform: {os: %s, arch: %s}\n  version: 1.2.3\n  source: file://%s\n  sha256: %x\n", runtime.GOOS, runtime.GOARCH, source, sha256.Sum256([]byte(raw)))
	manifest := filepath.Join(home, manifestName)

	put(filepath.Join(repo, manifestName), pin)
	selection, err := Inspect(t.Context(), repo)
	if err != nil || selection.Install != nil || !strings.Contains(selection.Detail, "No installation artifact") {
		t.Fatalf("unexpected repository manifest handling: %+v, %v", selection, err)
	}
	if err := os.Symlink(filepath.Join(repo, manifestName), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(t.Context(), repo); err == nil {
		t.Fatal("accepted manifest symlink into checkout")
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	put(manifest, pin)
	for _, path := range []string{manifest, home} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, info.Mode().Perm()|0o020); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), repo); err == nil {
			t.Fatalf("accepted writable %s", path)
		}
		if err := os.Chmod(path, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("repository as home", func(t *testing.T) {
		t.Setenv("AGENTFLOW_HOME", repo)
		if _, err := Inspect(t.Context(), repo); err == nil {
			t.Fatal("accepted repository as trusted home")
		}
	})
	for _, invalid := range []string{
		strings.Replace(pin, "1.2.3", "latest", 1),
		strings.Replace(pin, "file://", "http://", 1),
		strings.Replace(pin, "sha256:", "args: []\n  sha256:", 1),
		strings.Replace(pin, "sha256:", "sha256: invalid #", 1),
		pin + "---\ncommand: other\n",
	} {
		put(manifest, invalid)
		if _, err := Inspect(t.Context(), repo); err == nil {
			t.Fatal("accepted invalid pin")
		}
	}
	put(manifest, strings.Replace(pin, "arch: "+runtime.GOARCH, "arch: other", 1))
	selection, err = Inspect(t.Context(), repo)
	if err != nil || selection.Install != nil || !strings.Contains(selection.Detail, runtime.GOARCH) {
		t.Fatalf("missing platform not diagnosed: %+v, %v", selection, err)
	}

	put(manifest, pin)
	selection, err = Inspect(t.Context(), repo)
	if err != nil || selection.Install == nil {
		t.Fatalf("missing install: %+v, %v", selection, err)
	}
	put(source, "different bytes")
	if err := selection.Install.Apply(t.Context()); !errors.Is(err, local.ErrDigestMismatch) {
		t.Fatalf("wrong source: %v", err)
	}
	selection, err = Inspect(t.Context(), repo)
	if err != nil || selection.Install == nil || selection.Command != "" {
		t.Fatalf("published corrupt source: %+v, %v", selection, err)
	}
	put(source, raw)
	if err := selection.Install.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	selection, err = Inspect(t.Context(), repo)
	if err != nil || selection.Command == "" {
		t.Fatalf("missing verified command: %+v, %v", selection, err)
	}
	if err := os.Chmod(selection.Command, 0o700); err != nil {
		t.Fatal(err)
	}
	put(selection.Command, "#!/bin/sh\n: > ran\n")
	if err := os.Chmod(selection.Command, 0o500); err != nil {
		t.Fatal(err)
	}
	selection, err = Inspect(t.Context(), repo)
	if err != nil || selection.Command != "" || selection.Install == nil {
		t.Fatalf("did not offer repair: %+v, %v", selection, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ran damaged executable")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHTTPSRawSource(t *testing.T) {
	const source = "https://tools.example.invalid/pinned-gh"
	const raw = "raw fixture bytes"
	status := http.StatusOK
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != source {
			t.Fatalf("unexpected artifact request: %s %s", req.Method, req.URL)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://other.example.invalid/gh"}}, Body: io.NopCloser(strings.NewReader(raw))}, nil
	})
	a := provision.Artifact{Source: source}
	body, err := user.OpenArtifact(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != raw {
		t.Fatalf("read source: %q, %v", data, err)
	}
	for _, status = range []int{http.StatusFound, http.StatusNotFound} {
		if _, err := user.OpenArtifact(t.Context(), a); err == nil {
			t.Fatalf("accepted HTTP %d", status)
		}
	}
}
