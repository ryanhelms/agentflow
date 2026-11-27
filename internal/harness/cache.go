package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/ryanhelms/agentflow/internal/filelock"
)

// The probe cache (lane 6.2): <home>/harnesses/cache.json, an Idempotent
// Receiver keyed by binary identity ("Phase 6 Harness type", the cache
// paragraph). The file is mode 0600 in a 0700 directory and is replaced
// atomically under cache.json.lock, read-modify-write per entry, so the CLI
// and the daemon can both write it. It holds no path, no stderr, no command
// output and no agent text beyond the sanitized ACPInfo and SignIn.

// Cache file names under <home>/harnesses.
const (
	CacheFile     = "cache.json"
	cacheLockFile = "cache.json.lock"
	cacheFormat   = 1
)

// ErrCacheUnsafe is returned by FileStore.Check when cache.json or its
// directory can be read or written by others.
var ErrCacheUnsafe = errors.New("harness: the probe cache can be read or written by other users")

// cacheIdentityRule is the shape of an identity: "sha256:" and 64 hex.
var (
	cacheIdentityRule = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	cacheIDRule       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// FileStore is the Store over cache.json.
type FileStore struct {
	// Dir is <home>/harnesses.
	Dir string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// MaxRecords and MaxBytes cap the file; zero means DefaultMaxRecords
	// and DefaultMaxCacheLen.
	MaxRecords int
	MaxBytes   int
	// TTL is how long a record (and a sign-in verdict) applies; zero means
	// DefaultRecordTTL.
	TTL time.Duration
}

// NewFileStore returns the store of the agentflow home home.
func NewFileStore(home string) *FileStore {
	return &FileStore{Dir: filepath.Join(home, "harnesses")}
}

var _ Store = (*FileStore)(nil)

// cacheFile is the JSON shape of cache.json.
type cacheFile struct {
	Format    int                       `json:"format"`
	Records   []ProbeRecord             `json:"records"`
	Published map[string]PublishedState `json:"published,omitempty"`
}

func (s *FileStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *FileStore) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultRecordTTL
}

func (s *FileStore) path() string { return filepath.Join(s.Dir, CacheFile) }

// Get returns the record for id and identity, if one applies: same
// identity, probed within the TTL. A sign-in verdict older than the TTL is
// left out of the record.
func (s *FileStore) Get(id, identity string) (ProbeRecord, bool, error) {
	f, err := s.read()
	if err != nil {
		return ProbeRecord{}, false, err
	}
	now := s.now()
	for _, r := range f.Records {
		if r.ID != id || r.Identity != identity {
			continue
		}
		if !s.fresh(r.ProbedAt, now) {
			return ProbeRecord{}, false, nil
		}
		if r.Auth != nil && (r.Auth.CheckedAt == nil || !s.fresh(*r.Auth.CheckedAt, now)) {
			r.Auth = nil
		}
		return r, true, nil
	}
	return ProbeRecord{}, false, nil
}

func (s *FileStore) fresh(at, now time.Time) bool {
	return !at.IsZero() && now.Sub(at) < s.ttl() && !at.After(now.Add(time.Minute))
}

// Put stores r, replacing the record of the same ID and identity. The
// record is cleaned first (cacheClean): no field can carry a path, output
// or unsanitized text.
func (s *FileStore) Put(r ProbeRecord) error {
	r, err := cacheClean(r)
	if err != nil {
		return err
	}
	return s.update(func(f *cacheFile) {
		out := f.Records[:0]
		for _, old := range f.Records {
			if old.ID != r.ID || old.Identity != r.Identity {
				out = append(out, old)
			}
		}
		f.Records = append(out, r)
	})
}

// MarkAuthRequired records that an ACP session for id and identity was
// refused with auth_required at at (phase 7). Without a record for the
// identity it adds one with probe status none, so the state still shows it.
func (s *FileStore) MarkAuthRequired(id, identity string, at time.Time) error {
	if !cacheIDRule.MatchString(id) || !cacheIdentityRule.MatchString(identity) {
		return errors.New("harness: cache: invalid id or identity")
	}
	at = at.UTC()
	return s.update(func(f *cacheFile) {
		for i := range f.Records {
			if f.Records[i].ID == id && f.Records[i].Identity == identity {
				f.Records[i].AuthRequiredAt = &at
				return
			}
		}
		f.Records = append(f.Records, ProbeRecord{ID: id, Identity: identity, Status: ProbeNone, Code: CodeNotProbed, ProbedAt: at, AuthRequiredAt: &at})
	})
}

// Published returns the last snapshot the daemon published.
func (s *FileStore) Published() (map[string]PublishedState, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := map[string]PublishedState{}
	for id, p := range f.Published {
		out[id] = p
	}
	return out, nil
}

// SetPublished replaces the published snapshot.
func (s *FileStore) SetPublished(m map[string]PublishedState) error {
	clean := map[string]PublishedState{}
	for id, p := range m {
		if !cacheIDRule.MatchString(id) {
			continue
		}
		clean[id] = PublishedState{State: p.State, Code: cacheCode(p.Code), Version: cacheVersion(p.Version)}
	}
	return s.update(func(f *cacheFile) { f.Published = clean })
}

// ModTime is cache.json's modification time, zero when it does not exist.
// The daemon compares it between passes to notice a CLI probe.
func (s *FileStore) ModTime() time.Time {
	info, err := os.Stat(s.path())
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// Check reports whether cache.json can be read and has safe modes, for
// ai doctor. A missing cache is fine.
func (s *FileStore) Check() error {
	if info, err := os.Stat(s.Dir); err == nil && info.Mode().Perm()&0o077 != 0 && !windowsOS() {
		return ErrCacheUnsafe
	}
	info, err := os.Stat(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 && !windowsOS() {
		return ErrCacheUnsafe
	}
	_, err = s.read()
	return err
}

// read loads cache.json. A missing file is empty; a file that does not
// decode is an error.
func (s *FileStore) read() (cacheFile, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return cacheFile{Format: cacheFormat}, nil
	}
	if err != nil {
		return cacheFile{}, fmt.Errorf("harness: read probe cache: %w", err)
	}
	var f cacheFile
	if err := json.Unmarshal(b, &f); err != nil || f.Format != cacheFormat {
		return cacheFile{}, errors.New("harness: the probe cache cannot be read; it is rebuilt on the next probe")
	}
	return f, nil
}

// update runs fn on the current file under the lock and writes the result
// atomically. A file that cannot be decoded is replaced (rebuilt).
func (s *FileStore) update(fn func(*cacheFile)) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("harness: probe cache directory: %w", err)
	}
	if !windowsOS() {
		_ = os.Chmod(s.Dir, 0o700)
	}
	lock, err := os.OpenFile(filepath.Join(s.Dir, cacheLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("harness: probe cache lock: %w", err)
	}
	defer lock.Close()
	if err := filelock.Exclusive(lock); err != nil {
		return fmt.Errorf("harness: probe cache lock: %w", err)
	}
	f, err := s.read()
	if err != nil {
		f = cacheFile{Format: cacheFormat}
	}
	fn(&f)
	f.Format = cacheFormat
	b, err := s.encode(&f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".cache-*")
	if err != nil {
		return fmt.Errorf("harness: write probe cache: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil && !windowsOS() {
		_ = tmp.Close()
		return fmt.Errorf("harness: write probe cache: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("harness: write probe cache: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("harness: write probe cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("harness: write probe cache: %w", err)
	}
	if err := os.Rename(name, s.path()); err != nil {
		return fmt.Errorf("harness: write probe cache: %w", err)
	}
	return nil
}

// encode drops expired records, then the oldest ones, until the file fits
// MaxRecords and MaxBytes.
func (s *FileStore) encode(f *cacheFile) ([]byte, error) {
	maxRecords, maxBytes := s.MaxRecords, s.MaxBytes
	if maxRecords <= 0 {
		maxRecords = DefaultMaxRecords
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxCacheLen
	}
	now := s.now()
	kept := f.Records[:0]
	for _, r := range f.Records {
		if s.fresh(r.ProbedAt, now) {
			kept = append(kept, r)
		}
	}
	f.Records = kept
	// Newest first, so the oldest are cut.
	sort.SliceStable(f.Records, func(i, j int) bool { return f.Records[i].ProbedAt.After(f.Records[j].ProbedAt) })
	if len(f.Records) > maxRecords {
		f.Records = f.Records[:maxRecords]
	}
	if f.Records == nil {
		f.Records = []ProbeRecord{}
	}
	for {
		b, err := json.Marshal(f)
		if err != nil {
			return nil, fmt.Errorf("harness: encode probe cache: %w", err)
		}
		if len(b) <= maxBytes || len(f.Records) == 0 {
			return b, nil
		}
		f.Records = f.Records[:len(f.Records)-1]
	}
}

// cacheClean makes r safe to store: a valid ID and identity, agentflow's
// own codes, a clamped version, and the agent text sanitized again.
func cacheClean(r ProbeRecord) (ProbeRecord, error) {
	if !cacheIDRule.MatchString(r.ID) {
		return ProbeRecord{}, errors.New("harness: cache: invalid harness id")
	}
	if !cacheIdentityRule.MatchString(r.Identity) {
		return ProbeRecord{}, errors.New("harness: cache: invalid identity")
	}
	if r.BaseIdentity != "" && !cacheIdentityRule.MatchString(r.BaseIdentity) {
		r.BaseIdentity = ""
	}
	r.Version = cacheVersion(r.Version)
	r.VersionCode = cacheCode(r.VersionCode)
	r.Status = cacheCode(r.Status)
	r.Code = cacheCode(r.Code)
	r.ProbedAt = r.ProbedAt.UTC()
	if r.DurationMS < 0 {
		r.DurationMS = 0
	}
	if r.ACP != nil {
		a := *r.ACP
		a.AgentName = acpText(a.AgentName, MaxNameLen)
		a.AgentTitle = acpText(a.AgentTitle, MaxNameLen)
		a.AgentVersion = acpText(a.AgentVersion, MaxNameLen)
		r.ACP = &a
	}
	if len(r.SignIn) > MaxSignInMethods {
		r.SignIn = r.SignIn[:MaxSignInMethods]
	}
	signIn := make([]SignIn, 0, len(r.SignIn))
	for _, m := range r.SignIn {
		m.Method = acpText(m.Method, MaxIDLen)
		m.Name = acpText(m.Name, MaxNameLen)
		m.Description = acpText(m.Description, MaxDescriptionLen)
		if m.Type != signInAgent && m.Type != signInTerminal {
			continue
		}
		if m.Command != "" && !signInCommandSafe(m.Command) {
			m.Command, m.Unsafe = "", true
		}
		signIn = append(signIn, m)
	}
	if len(signIn) == 0 {
		signIn = nil
	}
	r.SignIn = signIn
	if r.Auth != nil {
		a := SignInStatus{Status: cacheCode(r.Auth.Status), Code: cacheCode(r.Auth.Code)}
		if r.Auth.CheckedAt != nil {
			t := r.Auth.CheckedAt.UTC()
			a.CheckedAt = &t
		}
		r.Auth = &a
	}
	if r.AuthRequiredAt != nil {
		t := r.AuthRequiredAt.UTC()
		r.AuthRequiredAt = &t
	}
	return r, nil
}

var (
	cacheCodeRule    = regexp.MustCompile(`^[a-z_]{0,32}$`)
	cacheVersionRule = regexp.MustCompile(`^[0-9A-Za-z.+-]{0,64}$`)
)

// cacheCode keeps an agentflow code (lowercase letters and _), else "".
func cacheCode(s string) string {
	if cacheCodeRule.MatchString(s) {
		return s
	}
	return ""
}

// cacheVersion keeps a version made of version characters, else "".
func cacheVersion(s string) string {
	if cacheVersionRule.MatchString(s) {
		return s
	}
	return ""
}
