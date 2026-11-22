package rovooauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func largeLogin(t *testing.T, f *fixture) chunkManifest {
	t.Helper()
	f.token["refresh_token"] = "fixture-refresh-" + strings.Repeat("abc", 2000)
	if err := f.e.login(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, ok := parseManifest(f.records[account])
	if !ok || m.size < 2*chunkSize {
		t.Fatal("expected multi-chunk grant")
	}
	return m
}

func TestChunkedSpendAndRotation(t *testing.T) {
	f := newFixture(t)
	before := largeLogin(t, f)
	generation := f.e.cached.Load().generation
	f.e.cached.Store(nil)
	base := f.e.client.Transport
	f.e.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == tokenEndpoint {
			if len(f.records) != 1 || f.records[account] != "{}" {
				t.Fatal("refresh dispatched before spending and retiring old chunks")
			}
			if err := r.ParseForm(); err != nil || r.PostForm.Get("refresh_token") != f.token["refresh_token"] {
				t.Fatal("reassembled refresh credential differs from the enrolled credential")
			}
		}
		return base.RoundTrip(r)
	})
	tok, gen, err := f.e.boundToken(t.Context())
	if err != nil || tok != "fixture-access" || gen != generation {
		t.Fatal("chunked refresh failed", err)
	}
	after, ok := parseManifest(f.records[account])
	if !ok || after.nonce == before.nonce || len(f.records) != 1+(after.size+chunkSize-1)/chunkSize {
		t.Fatal("refresh did not publish exactly one fresh chunk set")
	}
	f.e.client.Transport = base
	f.token["refresh_token"] = "fixture-refresh-short"
	if err := f.e.login(t.Context()); err != nil || len(f.records) != 1 || !validInline(f.records[account]) {
		t.Fatal("replacement with inline grant did not retire chunks", err)
	}
}

func TestLegacyInlineGrant(t *testing.T) {
	f := newFixture(t)
	g := grant{Generation: "original", ClientID: "devflow-fixture-client", Refresh: "fixture-refresh-"}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	g.Refresh += strings.Repeat("a", maxRecord-len(raw))
	raw, err = json.Marshal(g)
	if err != nil || len(raw) != maxRecord {
		t.Fatal("invalid legacy fixture")
	}
	f.records[account] = string(raw)
	got, gen, err := f.e.boundToken(t.Context())
	if got != "fixture-access" || gen != g.Generation || err != nil || !validInline(f.records[account]) {
		t.Fatal("legacy inline record could not be refreshed", err)
	}
}

func TestUnpublishedChunks(t *testing.T) {
	for _, mode := range []string{"set failure", "truncated write", "missing readback"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			active := largeLogin(t, f)
			old := f.records[account]
			set, get := f.e.set, f.e.get
			var failedKey string
			f.e.set = func(s, a, raw string) error {
				if strings.HasSuffix(a, "/1") {
					failedKey = a
					switch mode {
					case "set failure":
						return errors.New("synthetic private output")
					case "truncated write":
						raw = raw[:len(raw)-1]
					}
				}
				return set(s, a, raw)
			}
			f.e.get = func(s, a string) (string, error) {
				if mode == "missing readback" && a == failedKey {
					return "", keyring.ErrNotFound
				}
				return get(s, a)
			}
			if err := f.e.login(t.Context()); !errors.Is(err, errStorage) || f.records[account] != old || f.records[journalAccount] == "" {
				t.Fatal("unverified stage disturbed the published grant")
			}
			f.e.set, f.e.get = set, get
			// Simulate the next lock holder, with no transaction state in memory.
			head, err := f.e.currentHead(false)
			if err != nil || head != old || len(f.records) != 1+(active.size+chunkSize-1)/chunkSize {
				t.Fatal("orphan recovery failed to preserve the active set", err)
			}
			if _, err := f.e.readGrant(head); err != nil {
				t.Fatal("prior chunks were damaged", err)
			}
		})
	}
}

func TestAmbiguousManifestPublication(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "retained old manifest"
		if published {
			name = "wrote new manifest"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			old := largeLogin(t, f)
			set := f.e.set
			f.e.set = func(s, a, raw string) error {
				if a == account {
					if published {
						if err := set(s, a, raw); err != nil {
							return err
						}
					}
					return errors.New("uncertain native write")
				}
				return set(s, a, raw)
			}
			f.e.cached.Store(nil)
			if err := f.e.login(t.Context()); !errors.Is(err, errStorage) || f.e.cached.Load() != nil {
				t.Fatal("ambiguous publication exposed bearer")
			}
			f.e.set = set
			head, err := f.e.currentHead(false)
			m, ok := parseManifest(head)
			if err != nil || !ok || (m.nonce != old.nonce) != published || len(f.records) != 1+(m.size+chunkSize-1)/chunkSize {
				t.Fatal("recovery retired the wrong set", err)
			}
			if _, err := f.e.readGrant(head); err != nil {
				t.Fatal("recovered chunks unreadable", err)
			}
		})
	}
}

func TestInterruptedFirstEnrollment(t *testing.T) {
	f := newFixture(t)
	set := f.e.set
	f.e.set = func(s, a, raw string) error {
		if a == account {
			return errors.New("interrupted")
		}
		return set(s, a, raw)
	}
	f.token["refresh_token"] = strings.Repeat("x", 5000)
	if err := f.e.login(t.Context()); !errors.Is(err, errStorage) || f.records[account] != "" {
		t.Fatal("expected unpublished enrollment")
	}
	if tok, gen, err := f.e.boundToken(t.Context()); tok != "" || gen != "" || !errors.Is(err, ErrLoginRequired) || f.posts.Load() != 1 {
		t.Fatal("partial enrollment accepted or treated as absent")
	}
	f.e.set = set
	f.token["refresh_token"] = "fixture-refresh-recovered"
	if err := f.e.login(t.Context()); err != nil || len(f.records) != 1 || !validInline(f.records[account]) {
		t.Fatal("explicit login failed to clear first enrollment debris", err)
	}
}

func TestMalformedChunkedGrants(t *testing.T) {
	for _, mode := range []string{"missing", "short", "long", "same size corruption", "swapped", "wrong digest", "oversize manifest", "bad nonce", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := largeLogin(t, f)
			first := m.chunkAccount(0)
			want := ErrLoginRequired
			switch mode {
			case "missing":
				delete(f.records, first)
			case "short":
				f.records[first] = f.records[first][1:]
			case "long":
				f.records[first] += "a"
			case "same size corruption":
				f.records[first] = strings.Repeat("x", chunkSize)
			case "swapped":
				second := m.chunkAccount(chunkSize)
				f.records[first], f.records[second] = f.records[second], f.records[first]
			case "wrong digest":
				m.digest = digest("another record")
				f.records[account] = m.encode()
			case "oversize manifest":
				m.size = maxGrantSize + 1
				f.records[account] = m.encode()
			case "bad nonce":
				m.nonce = "../pending"
				f.records[account] = m.encode()
			case "unreadable":
				want = errStorage
				get := f.e.get
				f.e.get = func(s, a string) (string, error) {
					if a == first {
						return "", errors.New("synthetic private keyring output")
					}
					return get(s, a)
				}
			}
			head := f.records[account]
			if tok, gen, err := f.e.boundToken(t.Context()); tok != "" || gen != "" || !errors.Is(err, want) ||
				f.posts.Load() != 1 || f.records[account] != head {
				t.Fatal("damaged grant permitted cache use, fallback, or refresh")
			}
		})
	}
}

func TestCleanupRetry(t *testing.T) {
	f := newFixture(t)
	old := largeLogin(t, f)
	remove := f.e.remove
	f.e.remove = func(s, a string) error {
		if a == old.chunkAccount(chunkSize) {
			return errors.New("synthetic private delete failure")
		}
		return remove(s, a)
	}
	f.e.cached.Store(nil)
	if tok, _, err := f.e.boundToken(t.Context()); tok != "" || !errors.Is(err, errStorage) || f.posts.Load() != 1 ||
		f.records[account] != "{}" || f.records[journalAccount] == "" {
		t.Fatal("cleanup failure allowed refresh or lost the retirement journal")
	}
	f.e.remove = remove
	if _, _, err := f.e.boundToken(t.Context()); !errors.Is(err, ErrLoginRequired) || len(f.records) != 1 || f.posts.Load() != 1 {
		t.Fatal("retry did not finish cleanup and preserve spend")
	}
}

func TestStorageErrorCategories(t *testing.T) {
	f := newFixture(t)
	g := grant{Generation: "original", ClientID: "fixture-client", Refresh: strings.Repeat("<", maxGrantSize/6)}
	if err := f.e.save("", g, &tokenResponse{}); !errors.Is(err, errGrantSize) || len(f.records) != 0 || f.e.cached.Load() != nil {
		t.Fatal("oversize serialized grant was staged or exposed")
	}
	f.e.set = func(string, string, string) error { return keyring.ErrSetDataTooBig }
	if err := f.e.login(t.Context()); !errors.Is(err, errGrantSize) {
		t.Fatal("backend size rejection incorrectly classified")
	}
	f.e.lockDir = func() (string, error) { return "", errors.New("private lock path") }
	if _, _, err := f.e.boundToken(t.Context()); !errors.Is(err, errLock) {
		t.Fatal("lock failure incorrectly classified")
	}
}

func TestFailedRotationStaysSpent(t *testing.T) {
	f := newFixture(t)
	largeLogin(t, f)
	f.e.cached.Store(nil)
	set := f.e.set
	f.e.set = func(s, a, raw string) error {
		if strings.HasSuffix(a, "/1") {
			return errors.New("synthetic write failure")
		}
		return set(s, a, raw)
	}
	tok, gen, err := f.e.boundToken(t.Context())
	if tok != "" || gen != "" || !errors.Is(err, errStorage) || f.posts.Load() != 2 || f.records[account] != "{}" {
		t.Fatal("unsaved rotation exposed a bearer or restored the spent grant")
	}
	f.e.set = set
	tok, gen, err = f.e.boundToken(t.Context())
	if tok != "" || gen != "" || !errors.Is(err, ErrLoginRequired) || f.posts.Load() != 2 || len(f.records) != 1 {
		t.Fatal("failed rotation was replayed or staged chunks survived recovery")
	}
}

func TestMalformedCleanupJournal(t *testing.T) {
	f := newFixture(t)
	m := largeLogin(t, f)
	f.records[journalAccount] = m.encode() + "\n" + "invalid"
	head := f.records[account]
	if tok, gen, err := f.e.boundToken(t.Context()); tok != "" || gen != "" || !errors.Is(err, ErrLoginRequired) {
		t.Fatal("malformed cleanup journal allowed access")
	}
	if f.records[account] != head || f.records[m.chunkAccount(0)] == "" || f.posts.Load() != 1 {
		t.Fatal("corrupt bookkeeping changed the active grant")
	}
	// Corrupt metadata cannot identify orphan chunks. A fresh enrollment
	// may retire only the independently identified, formerly active set.
	unidentified := account + "/chunks/unidentified/0"
	f.records[unidentified] = "synthetic orphan"
	f.token["refresh_token"] = "fixture-refresh-new"
	if err := f.e.login(t.Context()); err != nil || len(f.records) != 2 || !validInline(f.records[account]) || f.records[unidentified] == "" {
		t.Fatal("explicit repair deleted an unidentified item or left known debris", err)
	}
}
