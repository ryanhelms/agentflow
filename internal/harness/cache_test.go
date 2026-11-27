package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	s := NewFileStore(t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	checked := now.Add(-time.Minute)
	r := ProbeRecord{ID: "gemini", Identity: identity("a"), Version: "0.71.1", Status: ProbeOK, Code: CodeOK, ProbedAt: now, DurationMS: 812,
		ACP:    &ACPInfo{ProtocolVersion: 1, AgentName: "gemini-cli", LoadSession: true},
		SignIn: []SignIn{{Method: "oauth", Type: "agent", Name: "Log in"}},
		Auth:   &SignInStatus{Status: SignedIn, Code: CodeOK, CheckedAt: &checked}}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("gemini", identity("a"))
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Errorf("round trip\n got %s\nwant %s", b, a)
	}
	if _, ok, _ := s.Get("gemini", identity("b")); ok {
		t.Error("a record applied to another identity")
	}
	if _, ok, _ := s.Get("goose", identity("a")); ok {
		t.Error("a record applied to another id")
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{s.Dir: 0o700, filepath.Join(s.Dir, CacheFile): 0o600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != want {
				t.Errorf("%s: %v %v", path, info.Mode(), err)
			}
		}
	}
	if err := s.Check(); err != nil {
		t.Errorf("check: %v", err)
	}
}

func TestCacheTTL(t *testing.T) {
	now := time.Now().UTC()
	s := NewFileStore(t.TempDir())
	s.Now = func() time.Time { return now }
	old := now.Add(-25 * time.Hour)
	if err := s.Put(ProbeRecord{ID: "old", Identity: identity("o"), Status: ProbeOK, Code: CodeOK, ProbedAt: now.Add(-23 * time.Hour), Auth: &SignInStatus{Status: SignedIn, Code: CodeOK, CheckedAt: &old}}); err != nil {
		t.Fatal(err)
	}
	r, ok, _ := s.Get("old", identity("o"))
	if !ok || r.Auth != nil {
		t.Errorf("an expired verdict was kept: %+v %v", r, ok)
	}
	now = now.Add(2 * time.Hour)
	if _, ok, _ := s.Get("old", identity("o")); ok {
		t.Error("a record older than 24 hours applied")
	}
	if err := s.Put(ProbeRecord{ID: "new", Identity: identity("n"), Status: ProbeOK, Code: CodeOK, ProbedAt: now}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, CacheFile))
	if strings.Contains(string(b), `"old"`) {
		t.Error("an expired record was not dropped")
	}
}

func TestCacheCaps(t *testing.T) {
	s := NewFileStore(t.TempDir())
	s.MaxRecords = 5
	base := time.Now().UTC()
	for i := 0; i < 8; i++ {
		if err := s.Put(ProbeRecord{ID: fmt.Sprintf("h%d", i), Identity: identity(fmt.Sprint(i)), Status: ProbeOK, Code: CodeOK, ProbedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		_, ok, _ := s.Get(fmt.Sprintf("h%d", i), identity(fmt.Sprint(i)))
		if ok != (i >= 3) {
			t.Errorf("h%d kept = %v", i, ok)
		}
	}

	s = NewFileStore(t.TempDir())
	for i := 0; i < 150; i++ {
		long := strings.Repeat("d", MaxDescriptionLen)
		r := ProbeRecord{ID: fmt.Sprintf("h%d", i), Identity: identity(fmt.Sprint(i)), Status: ProbeOK, Code: CodeOK, ProbedAt: base.Add(time.Duration(i) * time.Millisecond)}
		for j := 0; j < MaxSignInMethods; j++ {
			r.SignIn = append(r.SignIn, SignIn{Method: "m", Type: "agent", Name: "n", Description: long})
		}
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := os.Stat(filepath.Join(s.Dir, CacheFile))
	if info.Size() > DefaultMaxCacheLen {
		t.Errorf("cache is %d bytes", info.Size())
	}
	if _, ok, _ := s.Get("h149", identity("149")); !ok {
		t.Error("the newest record was dropped")
	}
	if _, ok, _ := s.Get("h0", identity("0")); ok {
		t.Error("the oldest record was kept")
	}
}

func TestCacheCleansRecords(t *testing.T) {
	s := NewFileStore(t.TempDir())
	if err := s.Put(ProbeRecord{ID: "Bad ID", Identity: identity("x")}); err == nil {
		t.Error("invalid id stored")
	}
	if err := s.Put(ProbeRecord{ID: "ok", Identity: "/usr/bin/gemini"}); err == nil {
		t.Error("a path stored as identity")
	}
	r := ProbeRecord{ID: "ok", Identity: identity("x"), Status: ProbeOK, Code: "Code with /path", Version: "1.2.3; rm -rf /", ProbedAt: time.Now(),
		ACP: &ACPInfo{AgentName: "evil\nsecond\u202e"},
		SignIn: []SignIn{{Method: "a", Type: "terminal", Name: "x", Command: "agent $(curl evil)"},
			{Method: "b", Type: "weird", Name: "y"}}}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get("ok", identity("x"))
	if got.Code != "" || got.Version != "" || got.ACP.AgentName != "evil" {
		t.Errorf("not cleaned: %+v %+v", got, got.ACP)
	}
	if len(got.SignIn) != 1 || got.SignIn[0].Command != "" || !got.SignIn[0].Unsafe {
		t.Errorf("sign in: %+v", got.SignIn)
	}
}

func TestCacheCorruptIsRebuilt(t *testing.T) {
	s := NewFileStore(t.TempDir())
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, CacheFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("x", identity("x")); err == nil {
		t.Error("a corrupt cache read without error")
	}
	if err := s.Check(); err == nil {
		t.Error("check passed a corrupt cache")
	}
	if err := s.Put(ProbeRecord{ID: "x", Identity: identity("x"), Status: ProbeOK, Code: CodeOK, ProbedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get("x", identity("x")); !ok || err != nil {
		t.Errorf("not rebuilt: %v %v", ok, err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(filepath.Join(s.Dir, CacheFile), 0o644)
		if err := s.Check(); !errors.Is(err, ErrCacheUnsafe) {
			t.Errorf("check of a 0644 cache: %v", err)
		}
	}
}

func TestCacheMarkAuthRequiredAndPublished(t *testing.T) {
	s := NewFileStore(t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.Put(ProbeRecord{ID: "claude-acp", Identity: identity("c"), Status: ProbeOK, Code: CodeOK, ProbedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAuthRequired("claude-acp", identity("c"), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.Get("claude-acp", identity("c"))
	if r.AuthRequiredAt == nil || !r.AuthRequiredAt.Equal(now.Add(time.Second)) || r.Status != ProbeOK {
		t.Errorf("record %+v", r)
	}
	if err := s.MarkAuthRequired("goose", identity("g"), now); err != nil {
		t.Fatal(err)
	}
	if r, ok, _ := s.Get("goose", identity("g")); !ok || r.AuthRequiredAt == nil || r.Status != ProbeNone {
		t.Errorf("new record %+v %v", r, ok)
	}
	pub := map[string]PublishedState{"gemini": {State: StateNative, Version: "0.71.1"}, "Bad": {State: StateNative}}
	if err := s.SetPublished(pub); err != nil {
		t.Fatal(err)
	}
	got, err := s.Published()
	if err != nil || len(got) != 1 || got["gemini"].Version != "0.71.1" {
		t.Errorf("published %+v %v", got, err)
	}
	// Records survive a published update and the other way round.
	if _, ok, _ := s.Get("claude-acp", identity("c")); !ok {
		t.Error("SetPublished lost a record")
	}
}

func TestCacheConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			s := NewFileStore(dir) // separate stores, as the CLI and the daemon
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("w%d-%d", w, i)
				if err := s.Put(ProbeRecord{ID: id, Identity: identity(id), Status: ProbeOK, Code: CodeOK, ProbedAt: time.Now()}); err != nil {
					t.Error(err)
				}
			}
		}(w)
	}
	wg.Wait()
	s := NewFileStore(dir)
	for w := 0; w < 4; w++ {
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("w%d-%d", w, i)
			if _, ok, _ := s.Get(id, identity(id)); !ok {
				t.Errorf("%s lost", id)
			}
		}
	}
}
