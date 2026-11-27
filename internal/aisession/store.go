package aisession

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/filelock"
)

// Store file names ("Phase 7 session store and retention").
const (
	storeDir       = "ai"
	sessionsDir    = "sessions"
	lockFile       = "sessions.lock"
	sessionFile    = "session.json"
	transcriptFile = "transcript.jsonl"
	asksDir        = "asks"
	answerKeyFile  = "answer.key"
	hostLogFile    = "host.log"
	heartbeatFile  = "host.heartbeat"
	cancelFile     = "cancel.json"
	harnessPIDFile = "harness.pid"
	terminalsFile  = "terminals.json"
	claimFile      = "host.claim"
	claimTokenFile = "host.token" // SHA-256 of the claim token, hex
)

// ErrNoSession: no session has this ID.
var ErrNoSession = errors.New("aisession: no such session")

// ErrBadTransition: the state change is not in Transitions.
var ErrBadTransition = errors.New("aisession: state change not allowed")

// fileStore is the Store under <home>/ai.
type fileStore struct {
	dir string // <home>/ai
	now func() time.Time

	mu   sync.Mutex // in-process, around the file lock
	tmu  sync.Mutex // transcript appends
	seqs map[ID]int64
	full map[ID]bool
}

// OpenStore opens (and creates, 0700) the session store of an agentflow
// home. It needs no Options: the AI projection uses it.
func OpenStore(home string) (Store, error) { return openStore(home, time.Now) }

func openStore(home string, now func() time.Time) (*fileStore, error) {
	if home == "" || !filepath.IsAbs(home) {
		return nil, errors.New("aisession: the agentflow home must be an absolute path")
	}
	s := &fileStore{dir: filepath.Join(home, storeDir), now: now, seqs: map[ID]int64{}, full: map[ID]bool{}}
	if err := mkdirPrivate(filepath.Join(s.dir, sessionsDir)); err != nil {
		return nil, err
	}
	return s, nil
}

func mkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700) //nolint:gosec // G302: a directory needs its search bit; 0700 is owner-only
}

func (s *fileStore) Dir(id ID) string { return filepath.Join(s.dir, sessionsDir, string(id)) }

func (s *fileStore) path(id ID, name ...string) string {
	return filepath.Join(append([]string{s.Dir(id)}, name...)...)
}

// locked runs fn under the store lock.
func (s *fileStore) locked(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := filelock.Exclusive(f); err != nil {
		return err
	}
	return fn()
}

// writePrivate writes a 0600 file atomically.
func writePrivate(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *fileStore) read(id ID) (Session, error) {
	if !ValidID(string(id)) {
		return Session{}, ErrNoSession
	}
	b, err := os.ReadFile(s.path(id, sessionFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	var out Session
	if err := json.Unmarshal(b, &out); err != nil || out.ID != id {
		return Session{}, fmt.Errorf("aisession: %s: session.json does not load", id)
	}
	return out, nil
}

func (s *fileStore) write(sess Session) error {
	b, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(s.path(sess.ID, sessionFile), append(b, '\n'))
}

// heartbeat is the newest sign of life: the host's heartbeat file, the
// host's start or the record's creation.
func (s *fileStore) heartbeat(sess Session) time.Time {
	t := sess.CreatedAt
	if sess.Host.StartedAt.After(t) {
		t = sess.Host.StartedAt
	}
	if sess.Host.Heartbeat.After(t) {
		t = sess.Host.Heartbeat
	}
	if info, err := os.Stat(s.path(sess.ID, heartbeatFile)); err == nil && info.ModTime().After(t) {
		t = info.ModTime()
	}
	return t
}

func (s *fileStore) stale(sess Session, now time.Time) bool {
	return sess.State != StateEnded && now.Sub(s.heartbeat(sess)) > HeartbeatLost
}

// endLost ends a session whose host stopped beating, under the lock.
func (s *fileStore) endLost(sess *Session, now time.Time) {
	sess.State, sess.Code = StateEnded, CodeHostLost
	t := now
	sess.EndedAt = &t
	_ = s.write(*sess)
	killLostHarness(s.path(sess.ID, harnessPIDFile))
	killLostTerminals(s.path(sess.ID, terminalsFile))
	s.removeEnded(sess.ID)
}

// removeEnded removes what no ended session keeps: its asks (write
// previews and env values), its TMPDIR and its answer key.
func (s *fileStore) removeEnded(id ID) {
	_ = os.RemoveAll(s.path(id, asksDir))
	_ = os.RemoveAll(s.path(id, "tmp"))
	_ = os.Remove(s.path(id, answerKeyFile))
}

func (s *fileStore) listLocked() ([]Session, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, sessionsDir))
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []Session
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		sess, err := s.read(ID(e.Name()))
		if err != nil {
			continue
		}
		if s.stale(sess, now) {
			s.endLost(&sess, now)
		}
		out = append(out, sess)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *fileStore) List() ([]Session, error) {
	var out []Session
	err := s.locked(func() error {
		var err error
		out, err = s.listLocked()
		return err
	})
	return out, err
}

func (s *fileStore) Get(id ID) (Session, error) {
	var out Session
	err := s.locked(func() error {
		sess, err := s.read(id)
		if err != nil {
			return err
		}
		if now := s.now(); s.stale(sess, now) {
			s.endLost(&sess, now)
		}
		out = sess
		return nil
	})
	return out, err
}

func (s *fileStore) Create(sess Session, prompt string, caps Caps) (Session, error) {
	if !ValidID(string(sess.ID)) {
		return Session{}, errors.New("aisession: invalid session ID")
	}
	err := s.locked(func() error {
		all, err := s.listLocked()
		if err != nil {
			return err
		}
		now := s.now()
		running, here, recent := caps.Running, caps.Here, caps.Recent
		for _, o := range all {
			if o.ID == sess.ID {
				return errors.New("aisession: the session exists")
			}
			if now.Sub(o.CreatedAt) < time.Hour {
				recent++
			}
			if o.State == StateEnded {
				continue
			}
			running++
			if sameDir(o.Worktree, sess.Worktree) {
				here++
			}
		}
		switch {
		case caps.PerWorktree > 0 && here >= caps.PerWorktree:
			return &Refusal{Code: RefuseLimitWorktree, Message: "A session is already running in this worktree.", Fix: "Wait for it to end, or cancel it with agentflow ai cancel."}
		case caps.MaxRunning > 0 && running >= caps.MaxRunning:
			return &Refusal{Code: RefuseLimitRunning, Message: fmt.Sprintf("%d sessions are already running, the most ai.sessions.max_running allows.", running), Fix: "Wait for one to end, or cancel one with agentflow ai cancel."}
		case caps.StartsPerHour > 0 && recent >= caps.StartsPerHour:
			return &Refusal{Code: RefuseLimitRate, Message: fmt.Sprintf("%d sessions were started in the last hour, the most agentflow allows.", recent), Fix: "Try again later."}
		}
		dir := s.Dir(sess.ID)
		for _, d := range []string{dir, filepath.Join(dir, asksDir), filepath.Join(dir, "tmp")} {
			if err := mkdirPrivate(d); err != nil {
				return err
			}
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		if err := writePrivate(filepath.Join(dir, answerKeyFile), key); err != nil {
			return err
		}
		sess.State = StateStarting
		if sess.CreatedAt.IsZero() {
			sess.CreatedAt = now
		}
		if sess.EnvNames == nil {
			sess.EnvNames = []string{}
		}
		if err := s.write(sess); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, transcriptFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	})
	if err != nil {
		return Session{}, err
	}
	if _, err := s.Append(sess.ID, Item{Kind: ItemPrompt, Text: Sanitize(prompt, MaxPrompt)}); err != nil {
		return Session{}, err
	}
	return s.Get(sess.ID)
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return resolvedPath(a) == resolvedPath(b) || filepath.Clean(a) == filepath.Clean(b)
}

func (s *fileStore) Update(id ID, fn func(*Session) error) (Session, error) {
	var out Session
	err := s.locked(func() error {
		sess, err := s.read(id)
		if err != nil {
			return err
		}
		before := sess
		if err := fn(&sess); err != nil {
			return err
		}
		if sess.ID != before.ID {
			return errors.New("aisession: a session ID never changes")
		}
		if sess.State != before.State && !slices.Contains(Transitions[before.State], sess.State) {
			return fmt.Errorf("%w: %s to %s", ErrBadTransition, before.State, sess.State)
		}
		if sess.State == StateEnded && sess.EndedAt == nil {
			t := s.now()
			sess.EndedAt = &t
		}
		out = sess
		return s.write(sess)
	})
	return out, err
}

// ---- transcript ----

func (s *fileStore) lastSeq(id ID) int64 {
	if seq, ok := s.seqs[id]; ok {
		return seq
	}
	var last int64
	_ = scanItems(s.path(id, transcriptFile), func(it Item) bool {
		last = it.Seq
		return true
	})
	s.seqs[id] = last
	return last
}

func (s *fileStore) Append(id ID, items ...Item) ([]Item, error) {
	if !ValidID(string(id)) {
		return nil, ErrNoSession
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	path := s.path(id, transcriptFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoSession
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	seq := s.lastSeq(id)
	var buf bytes.Buffer
	out := make([]Item, 0, len(items))
	for _, it := range items {
		seq++
		it.Seq = seq
		if it.Time.IsZero() {
			it.Time = s.now().UTC()
		}
		it.Text = Clamp(it.Text, MaxItemText)
		line, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		if size+int64(buf.Len()+len(line)+1) > MaxTranscript {
			// Past the limit text is dropped and counting goes on.
			if !s.full[id] {
				s.full[id] = true
				note, _ := json.Marshal(Item{Seq: seq, Time: it.Time, Kind: ItemInfo, Text: "transcript_truncated"})
				buf.Write(note)
				buf.WriteByte('\n')
				seq++
				it.Seq = seq
			}
			it.Text, it.Data = "", nil
			line, _ = json.Marshal(it)
			if size+int64(buf.Len()+len(line)+1) > MaxTranscript+(1<<20) {
				out = append(out, it)
				continue
			}
		}
		buf.Write(line)
		buf.WriteByte('\n')
		out = append(out, it)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	s.seqs[id] = seq
	return out, nil
}

// scanItems calls fn for each transcript item until it returns false.
func scanItems(path string, fn func(Item) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var it Item
		if json.Unmarshal(sc.Bytes(), &it) != nil {
			continue
		}
		if !fn(it) {
			return nil
		}
	}
	return sc.Err()
}

func (s *fileStore) Items(id ID, since int64, max int) ([]Item, error) {
	if !ValidID(string(id)) {
		return nil, ErrNoSession
	}
	if max <= 0 {
		max = 1000
	}
	out := []Item{}
	err := scanItems(s.path(id, transcriptFile), func(it Item) bool {
		if it.Seq > since {
			out = append(out, it)
		}
		return len(out) < max
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	return out, err
}

func (s *fileStore) Tail(id ID, max, maxBytes int) ([]Item, bool, error) {
	if !ValidID(string(id)) {
		return nil, false, ErrNoSession
	}
	var all []Item
	err := scanItems(s.path(id, transcriptFile), func(it Item) bool {
		all = append(all, it)
		if len(all) > 4*max+64 {
			all = all[len(all)-max-1:]
		}
		return true
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	first := len(all)
	size := 0
	for first > 0 && len(all)-first < max {
		n := len(all[first-1].Text) + len(all[first-1].Data) + 64
		if size+n > maxBytes && first < len(all) {
			break
		}
		size += n
		first--
	}
	truncated := first > 0 || (len(all) > 0 && all[0].Seq > 1)
	return append([]Item{}, all[first:]...), truncated, nil
}

// ---- asks and answers ----

func (s *fileStore) askPath(id ID, ask, suffix string) (string, error) {
	if !ValidID(string(id)) || !validAskID(ask) {
		return "", ErrNoSession
	}
	return s.path(id, asksDir, ask+suffix), nil
}

func (s *fileStore) PutAsk(a AskRecord) error {
	p, err := s.askPath(a.Session, a.ID, ".json")
	if err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return writePrivate(p, b)
}

func (s *fileStore) GetAsk(id ID, ask string) (AskRecord, error) {
	p, err := s.askPath(id, ask, ".json")
	if err != nil {
		return AskRecord{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return AskRecord{}, err
	}
	var a AskRecord
	if err := json.Unmarshal(b, &a); err != nil || a.ID != ask || a.Session != id {
		return AskRecord{}, errors.New("aisession: the ask does not load")
	}
	return a, nil
}

func (s *fileStore) Asks(id ID) ([]AskRecord, error) {
	if !ValidID(string(id)) {
		return nil, ErrNoSession
	}
	entries, err := os.ReadDir(s.path(id, asksDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AskRecord
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".answer.json") {
			continue
		}
		ask := strings.TrimSuffix(name, ".json")
		if !validAskID(ask) {
			continue
		}
		if _, ok, _ := s.ReadAnswer(id, ask); ok {
			continue
		}
		if a, err := s.GetAsk(id, ask); err == nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *fileStore) RemoveAsk(id ID, ask string) error {
	for _, suffix := range []string{".json", ".answer.json"} {
		p, err := s.askPath(id, ask, suffix)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *fileStore) WriteAnswer(id ID, a AnswerRecord) error {
	p, err := s.askPath(id, a.Ask, ".answer.json")
	if err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return &Refusal{Code: AnswerAnswered, Message: "This request was already answered."}
		}
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func (s *fileStore) ReadAnswer(id ID, ask string) (AnswerRecord, bool, error) {
	p, err := s.askPath(id, ask, ".answer.json")
	if err != nil {
		return AnswerRecord{}, false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return AnswerRecord{}, false, nil
	}
	if err != nil {
		return AnswerRecord{}, false, err
	}
	var a AnswerRecord
	if err := json.Unmarshal(b, &a); err != nil || a.Ask != ask {
		// A file that does not load is an answer that rejects.
		return AnswerRecord{Ask: ask, Decision: Deny}, true, nil
	}
	return a, true, nil
}

func (s *fileStore) AnswerKey(id ID) ([]byte, error) {
	if !ValidID(string(id)) {
		return nil, ErrNoSession
	}
	b, err := os.ReadFile(s.path(id, answerKeyFile))
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("aisession: the answer key is not valid")
	}
	return b, nil
}

func (s *fileStore) RequestCancel(id ID, by Requester) error {
	if _, err := s.read(id); err != nil {
		return err
	}
	b, _ := json.Marshal(struct {
		By Requester `json:"by"`
		At time.Time `json:"at"`
	}{by, s.now().UTC()})
	f, err := os.OpenFile(s.path(id, cancelFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func (s *fileStore) CancelRequested(id ID) bool {
	if !ValidID(string(id)) {
		return false
	}
	_, err := os.Stat(s.path(id, cancelFile))
	return err == nil
}

// beat writes the heartbeat file.
func (s *fileStore) beat(id ID) error {
	return writePrivate(s.path(id, heartbeatFile), []byte(s.now().UTC().Format(time.RFC3339Nano)+"\n"))
}

// dropTranscript deletes what keep_transcripts off does not keep: the
// transcript, the asks, host.log and answer.key.
func (s *fileStore) dropTranscript(id ID) {
	for _, n := range []string{transcriptFile, hostLogFile, hostLogFile + ".1", answerKeyFile} {
		_ = os.Remove(s.path(id, n))
	}
	_ = os.RemoveAll(s.path(id, asksDir))
}

// ---- retention ----

func (s *fileStore) Prune(keepDays int, keepTranscripts bool) error {
	if keepDays <= 0 {
		keepDays = DefaultKeepDays
	}
	return s.locked(func() error {
		all, err := s.listLocked()
		if err != nil {
			return err
		}
		now := s.now()
		var ended []Session
		for _, sess := range all {
			if sess.State == StateEnded {
				ended = append(ended, sess)
			}
		}
		// ended is newest first.
		var total int64
		kept := 0
		for _, sess := range ended {
			end := sess.CreatedAt
			if sess.EndedAt != nil {
				end = *sess.EndedAt
			}
			size := dirSize(s.Dir(sess.ID))
			old := now.Sub(end) > time.Duration(keepDays)*24*time.Hour
			if old || kept >= MaxEndedKept || total+size > MaxStoreBytes {
				_ = os.RemoveAll(s.Dir(sess.ID))
				delete(s.seqs, sess.ID)
				continue
			}
			if !keepTranscripts {
				s.dropTranscript(sess.ID)
				size = dirSize(s.Dir(sess.ID))
			}
			kept++
			total += size
		}
		return nil
	})
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
