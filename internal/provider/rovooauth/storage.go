package rovooauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
)

// All operations run under the per-user grant lock. Only account selects the
// active grant. The journal identifies up to two chunk sets for crash cleanup.
const (
	chunkSize      = 2048 // Leaves room for go-keyring's macOS base64 and command overhead.
	maxGrantSize   = 32 * chunkSize
	journalAccount = account + "/chunks/pending"
	manifestPrefix = "rovo-chunks-v1:"
)

type chunkManifest struct {
	nonce  string
	size   int
	digest string
}

func digest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (m chunkManifest) encode() string {
	if m.size == 0 {
		return ""
	}
	return manifestPrefix + m.nonce + ":" + strconv.Itoa(m.size) + ":" + m.digest
}

var manifestSyntax = regexp.MustCompile("^" + manifestPrefix + `([A-Z2-7]{26,64}):([1-9][0-9]{0,4}):([0-9a-f]{64})$`)

func parseManifest(raw string) (chunkManifest, bool) {
	if len(raw) > chunkSize {
		return chunkManifest{}, false
	}
	fields := manifestSyntax.FindStringSubmatch(raw)
	if fields == nil {
		return chunkManifest{}, false
	}
	size, _ := strconv.Atoi(fields[2]) // At most five decimal digits.
	if size <= chunkSize || size > maxGrantSize {
		return chunkManifest{}, false
	}
	return chunkManifest{nonce: fields[1], size: size, digest: fields[3]}, true
}

func (m chunkManifest) chunkAccount(offset int) string {
	return account + "/chunks/" + m.nonce + "/" + strconv.Itoa(offset/chunkSize)
}

func decodeGrant(raw string) (grant, error) {
	var g grant
	if json.Unmarshal([]byte(raw), &g) != nil || !opaque(g.Generation) || !opaque(g.ClientID) || !opaque(g.Refresh) {
		return grant{}, ErrLoginRequired
	}
	return g, nil
}

func validInline(head string) bool {
	if len(head) > maxRecord {
		return false
	}
	_, err := decodeGrant(head)
	return err == nil
}

func (e *enrollment) readGrant(head string) (grant, error) {
	m, ok := parseManifest(head)
	if !ok {
		if len(head) > maxRecord {
			return grant{}, ErrLoginRequired
		}
		return decodeGrant(head) // Legacy inline grant, including the spent {} marker.
	}
	var raw strings.Builder
	raw.Grow(m.size)
	for offset := 0; offset < m.size; offset += chunkSize {
		chunk, err := e.get(service, m.chunkAccount(offset))
		if errors.Is(err, keyring.ErrNotFound) {
			return grant{}, ErrLoginRequired
		}
		if err != nil {
			return grant{}, errStorage
		}
		if len(chunk) != min(chunkSize, m.size-offset) {
			return grant{}, ErrLoginRequired
		}
		raw.WriteString(chunk)
	}
	if digest(raw.String()) != m.digest {
		return grant{}, ErrLoginRequired
	}
	return decodeGrant(raw.String())
}

// An unsuccessful write or read-back may already have changed the OS store.
// Never expose backend errors, which may include native output.
func (e *enrollment) put(name, raw string) error {
	err := e.set(service, name, raw)
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return errGrantSize
	}
	if err != nil {
		return errStorage
	}
	saved, err := e.get(service, name)
	if err != nil || saved != raw {
		return errStorage
	}
	return nil
}

func (e *enrollment) discard(name string) error {
	if err := e.remove(service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errStorage
	}
	// A silent deletion failure must leave the retirement journal available
	// for the next lock holder.
	if _, err := e.get(service, name); !errors.Is(err, keyring.ErrNotFound) {
		return errStorage
	}
	return nil
}

type chunkChange struct {
	before, after chunkManifest
}

func (c chunkChange) encode() string {
	return c.before.encode() + "\n" + c.after.encode()
}

func parseChange(raw string) (chunkChange, bool) {
	if len(raw) > chunkSize {
		return chunkChange{}, false
	}
	old, next, ok := strings.Cut(raw, "\n")
	before, oldOK := parseManifest(old)
	after, nextOK := parseManifest(next)
	valid := ok && (oldOK || nextOK) && (oldOK || old == "") && (nextOK || next == "")
	return chunkChange{before, after}, valid
}

// Deletions are idempotent. Keep the journal until every retired chunk has
// been removed, and always protect the entire published nonce.
func (e *enrollment) finish(c chunkChange, active chunkManifest) error {
	for _, m := range []chunkManifest{c.before, c.after} {
		if m.nonce == active.nonce {
			continue
		}
		for offset := 0; offset < m.size; offset += chunkSize {
			if err := e.discard(m.chunkAccount(offset)); err != nil {
				return err
			}
		}
	}
	return e.discard(journalAccount)
}

func (e *enrollment) currentHead(reenroll bool) (string, error) {
	head, err := e.get(service, account)
	missing := errors.Is(err, keyring.ErrNotFound)
	if err != nil && !missing {
		return "", errStorage
	}
	pending, err := e.get(service, journalAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		if missing {
			return "", ErrNotEnrolled
		}
		return head, nil
	}
	if err != nil {
		return "", errStorage
	}
	change, valid := parseChange(pending)
	if !valid && !reenroll {
		return "", ErrLoginRequired
	}
	active, chunked := parseManifest(head)
	if !chunked && head != "{}" && (missing || !validInline(head)) {
		// An interrupted first enrollment or damaged head is never an
		// invitation to fall back. Only explicit login can retire it.
		if !reenroll {
			return "", ErrLoginRequired
		}
		if err := e.put(account, "{}"); err != nil {
			return "", err
		}
		head = "{}"
	}
	if !valid {
		// An explicit login can replace corrupt bookkeeping. Never derive
		// deletion targets from it.
		return head, e.discard(journalAccount)
	}
	return head, e.finish(change, active)
}

// The journal is verified before staging. Until the final single-account
// replacement the prior head remains authoritative, even after a crash.
// Leave the journal for currentHead on any ambiguous write or cleanup error.
func (e *enrollment) replace(head, raw string) error {
	if len(raw) > maxGrantSize {
		return errGrantSize
	}
	before, _ := parseManifest(head)
	change := chunkChange{before: before}
	replacement := raw
	if len(raw) > chunkSize {
		change.after = chunkManifest{nonce: rand.Text(), size: len(raw), digest: digest(raw)}
		replacement = change.after.encode()
	}
	if change == (chunkChange{}) {
		return e.put(account, replacement)
	}
	if err := e.put(journalAccount, change.encode()); err != nil {
		return err
	}
	for offset := 0; offset < change.after.size; offset += chunkSize {
		if err := e.put(change.after.chunkAccount(offset), raw[offset:min(offset+chunkSize, len(raw))]); err != nil {
			return err
		}
	}
	if err := e.put(account, replacement); err != nil {
		return err
	}
	return e.finish(change, change.after)
}
