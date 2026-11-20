// Package policy decides whether an action may run (autonomy levels), issues
// single-use confirmation tokens, keeps the audit log, and enforces the
// "never" list that applies at every autonomy level.
package policy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nscaledev/devflow/internal/config"
)

// Class is the kind of side effect an action has.
type Class string

const (
	Read   Class = "read"
	Local  Class = "local"
	Remote Class = "remote"
)

// Outcome of a gate check.
type Outcome string

const (
	Proceed           Outcome = "proceed"
	SuggestOnly       Outcome = "suggest"
	NeedsConfirmation Outcome = "needs_confirmation"
)

// TokenTTL is how long a confirmation token stays valid.
const TokenTTL = 5 * time.Minute

// Gate applies autonomy levels and confirmation tokens.
type Gate struct {
	Autonomy config.Autonomy
	Client   string
	secret   []byte
	mu       sync.Mutex
	used     map[string]bool
	auditDir string
}

// NewGate loads (or creates) the per-user token secret under dir.
func NewGate(dir string, a config.Autonomy, client string) (*Gate, error) {
	keyPath := filepath.Join(dir, "confirm.key")
	secret, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) || len(secret) < 32 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, secret, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return &Gate{Autonomy: a, Client: client, secret: secret, used: map[string]bool{}, auditDir: dir}, nil
}

// Decision is the result of Check.
type Decision struct {
	Outcome Outcome `json:"status"`
	Token   string  `json:"confirm_token,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

// Check decides whether action with payload may run. forceConfirm raises
// "auto" to "confirm" (used when the payload came from ticket or page text).
// A valid token for the same action and payload lets it proceed once.
func (g *Gate) Check(class Class, action string, payload any, token string, forceConfirm bool) (Decision, error) {
	if class == Read {
		return Decision{Outcome: Proceed}, nil
	}
	level := g.Autonomy.Local
	if class == Remote {
		level = g.Autonomy.Remote
	}
	if forceConfirm && level == config.Auto {
		level = config.Confirm
	}
	switch level {
	case config.Suggest:
		return Decision{Outcome: SuggestOnly, Reason: fmt.Sprintf("%s actions are set to suggest; run it yourself", class)}, nil
	case config.Auto:
		return Decision{Outcome: Proceed}, nil
	}
	digest, err := g.digest(action, payload)
	if err != nil {
		return Decision{}, err
	}
	if token != "" {
		if err := g.redeem(token, digest); err != nil {
			return Decision{}, err
		}
		return Decision{Outcome: Proceed}, nil
	}
	return Decision{Outcome: NeedsConfirmation, Token: g.issue(digest),
		Reason: "show this summary to the user and call again with confirm_token after they agree"}, nil
}

func (g *Gate) digest(action string, payload any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(append([]byte(action+"\x00"), b...))
	return h[:], nil
}

func (g *Gate) mac(digest []byte, exp uint64) []byte {
	m := hmac.New(sha256.New, g.secret)
	var e [8]byte
	binary.BigEndian.PutUint64(e[:], exp)
	m.Write(e[:])
	m.Write(digest)
	return m.Sum(nil)[:18]
}

func (g *Gate) issue(digest []byte) string {
	exp := unixNow() + uint64(TokenTTL/time.Second)
	var e [8]byte
	binary.BigEndian.PutUint64(e[:], exp)
	return "cf_" + base64.RawURLEncoding.EncodeToString(append(e[:], g.mac(digest, exp)...))
}

func unixNow() uint64 {
	n := time.Now().Unix()
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// ErrToken is returned for expired, reused or mismatched tokens.
var ErrToken = errors.New("confirmation token is invalid, expired, already used, or the request changed; ask the user again")

func (g *Gate) redeem(token string, digest []byte) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "cf_"))
	if err != nil || len(raw) != 8+18 {
		return ErrToken
	}
	exp := binary.BigEndian.Uint64(raw[:8])
	if unixNow() > exp || !hmac.Equal(raw[8:], g.mac(digest, exp)) {
		return ErrToken
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used[token] {
		return ErrToken
	}
	g.used[token] = true
	return nil
}

// Audit appends one JSON line to ~/.devflow/audit.log.
func (g *Gate) Audit(class Class, action string, payload any, result string) {
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	entry := map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339), "client": g.Client, "class": class,
		"action": action, "payload_sha256": fmt.Sprintf("%x", sum[:8]), "result": result,
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(filepath.Join(g.auditDir, "audit.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ---- never list ----

var (
	forcePushRe  = regexp.MustCompile(`\bgit\s+push\b[^|;&]*(\s--force\b|\s-f\b|\s--force-with-lease\b|\s\+\S+)`)
	deleteRefRe  = regexp.MustCompile(`\bgit\s+push\b[^|;&]*(\s--delete\b|\s-d\b|\s:\S+)`)
	ghMergeRe    = regexp.MustCompile(`\bgh\s+pr\s+merge\b`)
	pushTargetRe = regexp.MustCompile(`\bgit\s+push\s+(?:-\S+\s+)*\S+\s+(?:\S+:)?(\S+)`)
)

// Forbidden returns a reason if a shell command is on the never list.
func Forbidden(cmd string, protected []string, currentBranch string) string {
	switch {
	case forcePushRe.MatchString(cmd):
		return "force-push is never allowed through devflow-managed agents"
	case deleteRefRe.MatchString(cmd):
		return "deleting remote branches is never allowed through devflow-managed agents"
	case ghMergeRe.MatchString(cmd):
		return "merging PRs is a human decision; ask the user to merge"
	}
	target := ""
	if m := pushTargetRe.FindStringSubmatch(cmd); m != nil {
		target = m[1]
	} else if regexp.MustCompile(`\bgit\s+push\b`).MatchString(cmd) {
		target = currentBranch
	}
	if target != "" && IsProtected(target, protected) {
		return fmt.Sprintf("pushing to protected branch %q is never allowed; push a feature branch and open a PR", target)
	}
	return ""
}

// IsProtected reports whether branch matches any protected pattern.
func IsProtected(branch string, patterns []string) bool {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	for _, p := range patterns {
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	return false
}
