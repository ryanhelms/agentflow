// Package policy's local approval journal is shared by CLI and MCP processes.
// A kernel lock protects the scope index and all state transitions. Each
// durable record contains only a fingerprint and metadata, never the bearer
// token, command, message, paths, or provider payload.
package policy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/filelock"
)

var (
	ErrToken       = errors.New("confirmation token is invalid")
	ErrExpired     = fmt.Errorf("%w: approval expired. Request a fresh preview", ErrToken)
	ErrChanged     = fmt.Errorf("%w: request changed or policy tightened. Request a fresh preview", ErrToken)
	ErrConsumed    = fmt.Errorf("%w: approval already used. Check the operation result before retrying", ErrToken)
	ErrCancelled   = fmt.Errorf("%w: approval cancelled", ErrToken)
	ErrNotApproved = fmt.Errorf("%w: the prompt owner has not approved this operation", ErrToken)
)

// ErrorCode identifies a confirmation failure without parsing its explanation.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrExpired):
		return "approval_expired"
	case errors.Is(err, ErrChanged):
		return "operation_changed"
	case errors.Is(err, ErrConsumed):
		return "approval_consumed"
	case errors.Is(err, ErrCancelled):
		return "approval_cancelled"
	case errors.Is(err, ErrNotApproved):
		return "approval_pending"
	case errors.Is(err, ErrToken):
		return "invalid_token"
	default:
		return ""
	}
}

// Approval is the public, bearer-free view of an operation. An operation ID
// can be used to resume only after the bearer has explicitly granted approval.
type Approval struct {
	// PreviouslyIssued is reported on a repeated lookup of a pending prompt.
	// It records issuance, not whether the user saw or answered the prompt.
	PreviouslyIssued bool      `json:"previously_issued,omitempty"`
	OperationID      string    `json:"operation_id"`
	State            string    `json:"state"`
	PromptOwner      string    `json:"prompt_owner,omitempty"`
	Action           string    `json:"action"`
	Class            Class     `json:"class"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type approvalRecord struct {
	Approval
	InteractivePrompt bool      `json:"interactive_prompt,omitempty"`
	ProposalKey       string    `json:"proposal_key,omitempty"`
	ProposalNonce     string    `json:"proposal_nonce,omitempty"`
	IssuedAt          time.Time `json:"issued_at"`
	Fingerprint       string    `json:"fingerprint"`
	Related           []string  `json:"related,omitempty"`
	Forced            bool      `json:"forced,omitempty"`
	Source            string    `json:"source"`
	// RequestedBy is the client whose call created the record. A plugin
	// client (PluginClientPrefix) may redeem only records it created.
	RequestedBy string `json:"requested_by,omitempty"`
}

// PluginClientPrefix starts the client name of a plugin's calls.
const PluginClientPrefix = "plugin:"

// ErrNotRequester: a plugin tried to redeem an approval another client
// requested.
var ErrNotRequester = fmt.Errorf("%w: this approval was requested by another client", ErrToken)

const (
	pending   = "pending"
	prepared  = "prepared"
	approved  = "approved"
	consumed  = "consumed"
	expired   = "expired"
	cancelled = "cancelled"
)

// NewGate opens the same per-user secret across restarts and concurrent clients.
func NewGate(dir string, a config.Autonomy, client string) (*Gate, error) {
	g := &Gate{Autonomy: a, initial: a, Client: client, auditDir: dir, now: time.Now}
	key := filepath.Join(dir, "confirm.key")
	secret, err := os.ReadFile(key)
	if errors.Is(err, os.ErrNotExist) {
		// Serialize first-use key creation. Existing installations can initialize
		// read-only tools without opening any writable lock or ledger.
		err = g.locked(func() error {
			secret, err = os.ReadFile(key)
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			secret = make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			return atomicFile(key, secret)
		})
	}
	if err != nil {
		return nil, err
	}
	if len(secret) < 32 {
		return nil, errors.New("invalid approval key")
	}
	g.secret = secret
	codex, claude := os.Getenv("CODEX_THREAD_ID"), os.Getenv("CLAUDE_CODE_SESSION_ID")
	session := codex
	if (strings.Contains(strings.ToLower(client), "claude") || client == "cli" && os.Getenv("CLAUDECODE") != "") && claude != "" {
		session = claude
	}
	if session == "" {
		session = claude
	}
	if session == "" {
		session = rand.Text()
	}
	signer := hmac.New(sha256.New, g.secret)
	fmt.Fprintf(signer, "%s\x00%s", client, session)
	g.owner = fmt.Sprintf("%s/%x", client, signer.Sum(nil)[:8])
	return g, nil
}

func (g *Gate) directory() string { return ApprovalsDir(g.auditDir) }

func (g *Gate) locked(fn func() error) error {
	dir := g.directory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("open approval directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "ledger.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open approval lock: %w", err)
	}
	defer f.Close()
	if err := filelock.Exclusive(f); err != nil {
		return fmt.Errorf("lock approval journal: %w", err)
	}
	return fn()
}

// Sync the complete file before replacing its old value while holding the lock.
// A process crash therefore cannot leave a truncated record for another agent.
func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".approval-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceLedgerFile(name, path)
}

func (g *Gate) fingerprint(class Class, action string, related []string, payload any) (string, error) {
	effects := slices.Clone(related)
	slices.Sort(effects)
	effects = slices.Compact(effects)
	if len(effects) == 0 {
		effects = nil
	}
	obj := struct {
		Repository string
		Class      Class
		Action     string
		Related    []string
		Payload    any
	}{g.scope, class, action, effects, payload}
	mac := hmac.New(sha256.New, g.secret)
	if err := json.NewEncoder(mac).Encode(obj); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func parseID(id string) ([]byte, error) {
	if len(id) != 35 || !strings.HasPrefix(id, "op_") {
		return nil, ErrToken
	}
	b, err := hex.DecodeString(id[3:])
	if err != nil || hex.EncodeToString(b) != id[3:] {
		return nil, ErrToken
	}
	return b, nil
}

// IDs and credentials are unrelated capabilities. Only the original owner is
// given the bearer that can authorize a pending operation.
func (g *Gate) token(id string) string {
	b, _ := parseID(id)
	mac := hmac.New(sha256.New, g.secret)
	fmt.Fprintf(mac, "approval-v2\x00%s\x00%s", g.scope, id)
	return "cf_" + base64.RawURLEncoding.EncodeToString(append(b, mac.Sum(nil)...))
}

func (g *Gate) credential(s string) (string, bool, error) {
	if strings.HasPrefix(s, "op_") {
		_, err := parseID(s)
		return s, false, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "cf_"))
	if err != nil || len(raw) != 16+sha256.Size {
		return "", false, ErrToken
	}
	id := "op_" + hex.EncodeToString(raw[:16])
	if !hmac.Equal([]byte(g.token(id)), []byte(s)) {
		return "", false, ErrToken
	}
	return id, true, nil
}

// OperationIDOf returns the operation ID a confirm token or operation ID
// names, without checking the bearer's MAC. It is a correlation hint for
// events and the timeline only: it grants nothing, and the gate still
// checks the token when the operation runs.
func OperationIDOf(token string) (string, bool) {
	if strings.HasPrefix(token, "op_") {
		if _, err := parseID(token); err != nil {
			return "", false
		}
		return token, true
	}
	if !strings.HasPrefix(token, "cf_") {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[3:])
	if err != nil || len(raw) != 16+sha256.Size {
		return "", false
	}
	return "op_" + hex.EncodeToString(raw[:16]), true
}

// ScopeKey is the file-name prefix of the approval records of the
// repository whose common git dir is repository, after Configure resolved
// its symbolic links: 32 lowercase hex characters. Records are named
// "<ScopeKey>-<operation ID>.json" in the approvals directory. The event
// watcher uses it to route a record to its repository without reading
// paths from the record.
func ScopeKey(repository string) string {
	scope := sha256.Sum256([]byte(repository))
	return fmt.Sprintf("%x", scope[:16])
}

// ApprovalsDir is the approvals directory of the journal rooted at dir
// (NewGate's dir, normally config.Home()).
func ApprovalsDir(dir string) string { return filepath.Join(dir, "approvals") }

func (g *Gate) recordPath(id string) string {
	return filepath.Join(g.directory(), ScopeKey(g.scope)+"-"+id+".json")
}

func (g *Gate) load(id string) (approvalRecord, error) {
	var r approvalRecord
	if _, err := parseID(id); err != nil {
		return r, err
	}
	b, err := os.ReadFile(g.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return r, ErrToken
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("read approval record: %w", err)
	}
	if r.OperationID != id || r.Fingerprint == "" {
		return r, errors.New("invalid approval record")
	}
	return r, nil
}

func (g *Gate) persist(r *approvalRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicFile(g.recordPath(r.OperationID), b)
}

func (g *Gate) logEvent(r *approvalRecord, event string) {
	entry := struct {
		At     time.Time `json:"time"`
		ID     string    `json:"operation_id"`
		Client string    `json:"client"`
		Action string    `json:"action"`
		Class  Class     `json:"class"`
		Source string    `json:"source"`
		Event  string    `json:"event"`
	}{g.now().UTC(), r.OperationID, g.Client, r.Action, r.Class, r.Source, event}
	f, err := os.OpenFile(filepath.Join(g.directory(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(entry)
}

func (g *Gate) change(r *approvalRecord, state string) error {
	r.State = state
	if err := g.persist(r); err != nil {
		return err
	}
	g.logEvent(r, state)
	return nil
}

// Shortening the window takes effect for existing approvals. A later increase
// never extends one that was already issued.
func (g *Gate) refresh(r *approvalRecord) error {
	if r.State != pending && r.State != approved && r.State != prepared {
		return nil
	}
	if r.InteractivePrompt && r.State == pending {
		abandoned, err := g.abandonedPrompt(r.OperationID)
		if err != nil {
			return err
		}
		if abandoned {
			return g.change(r, cancelled)
		}
	}
	rule := g.Explain(r.Class, r.Action, r.Related, r.Forced)
	limit := r.IssuedAt.Add(rule.Window)
	shorten := limit.Before(r.ExpiresAt)
	if shorten {
		r.ExpiresAt = limit
	}
	if !g.now().Before(r.ExpiresAt) {
		return g.change(r, expired)
	}
	if shorten {
		return g.persist(r)
	}
	return nil
}

func (g *Gate) prepare(d *Decision, class Class, action string, related []string, digest string, force bool, deadline time.Time) error {
	index := filepath.Join(g.directory(), "request-"+digest)
	ref, err := os.ReadFile(index)
	if err == nil {
		r, err := g.load(string(ref))
		if err != nil {
			return err
		}
		if err = g.refresh(&r); err != nil {
			return err
		}
		if !deadline.IsZero() && deadline.Before(r.ExpiresAt) && (r.State == prepared || r.State == pending || r.State == approved) {
			r.ExpiresAt = deadline.UTC()
			if err = g.persist(&r); err != nil {
				return err
			}
		}
		// Retire the old prompt when a newly enabled publication policy
		// entitles this exact snapshot to a prepared token.
		if (r.State == pending && d.Policy.Source == config.PRPublicationPolicySource && d.Policy.Level == config.Auto) ||
			(r.State == prepared && d.Policy.Level != config.Auto) {
			if err = g.change(&r, cancelled); err != nil {
				return err
			}
		}
		switch r.State {
		case prepared, pending, approved:
			g.describe(d, &r, true)
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var uid [16]byte
	if _, err := rand.Read(uid[:]); err != nil {
		return err
	}
	now := g.now().UTC()
	r := approvalRecord{Approval: Approval{OperationID: "op_" + hex.EncodeToString(uid[:]), Class: class, Action: action, ExpiresAt: now.Add(d.Policy.Window)},
		IssuedAt: now, Fingerprint: digest, Related: related, Forced: force, Source: d.Policy.Source, RequestedBy: g.Client}
	if !deadline.IsZero() && deadline.Before(r.ExpiresAt) {
		r.ExpiresAt = deadline.UTC()
	}
	if d.Policy.Level == config.Auto {
		r.State = prepared
	} else {
		r.State = pending
		r.PromptOwner = g.owner
	}
	if err := g.persist(&r); err != nil {
		return err
	}
	if err := atomicFile(index, []byte(r.OperationID)); err != nil {
		return err
	}
	g.logEvent(&r, r.State)
	g.describe(d, &r, false)
	return nil
}

func (g *Gate) describe(d *Decision, r *approvalRecord, existing bool) {
	view := r.Approval
	view.PreviouslyIssued = existing && r.State == pending
	d.Approval = &view
	reason := fmt.Sprintf("%s sets %s (%s) to %s", d.Policy.Source, d.Policy.Action, d.Policy.Class, d.Policy.Level)
	if d.Policy.ForceConfirm {
		reason += ". This operation requires confirmation"
	}
	expiry := r.ExpiresAt.UTC().Format(time.RFC3339)
	switch {
	case r.State == prepared:
		d.Outcome, d.Token = Prepared, g.token(r.OperationID)
		d.Reason = fmt.Sprintf("%s. Apply this exact snapshot before %s", reason, expiry)
	case r.State == approved:
		d.Outcome = AwaitingApproval
		d.Reason = fmt.Sprintf("approved. Resume the original operation with its operation ID before %s", expiry)
	case r.PromptOwner == g.owner:
		d.Outcome, d.Token = NeedsConfirmation, g.token(r.OperationID)
		d.Reason = fmt.Sprintf("%s. %s owns this prompt until %s. Show the summary and wait for approval before applying it", reason, r.PromptOwner, expiry)
		if existing {
			d.Reason = fmt.Sprintf("%s. %s already owns this preview until %s. Reuse the existing prompt if it was shown", reason, r.PromptOwner, expiry)
		}
	default:
		d.Outcome = AwaitingApproval
		d.Reason = fmt.Sprintf("%s. %s owns the prompt until %s. Check status and resume after approval", reason, r.PromptOwner, expiry)
	}

}

func stateError(state string) error {
	switch state {
	case cancelled:
		return ErrCancelled
	case expired:
		return ErrExpired
	case consumed:
		return ErrConsumed
	}
	return nil
}

func (g *Gate) consume(d *Decision, credential, digest string) error {
	id, bearer, err := g.credential(credential)
	if err != nil {
		return err
	}
	r, err := g.load(id)
	if err != nil {
		return err
	}
	if err = g.refresh(&r); err != nil {
		return err
	}
	d.Approval = &r.Approval
	if err = stateError(r.State); err != nil {
		return err
	}
	if strings.HasPrefix(g.Client, PluginClientPrefix) && r.RequestedBy != g.Client {
		return ErrNotRequester
	}
	if r.Fingerprint != digest || r.State == prepared && d.Policy.Level != config.Auto {
		g.logEvent(&r, "operation_changed")
		return ErrChanged
	}
	if !bearer && r.State != approved {
		return ErrNotApproved
	}
	if r.State != pending && r.State != approved && r.State != prepared {
		return ErrToken
	}
	return g.change(&r, consumed)
}

func (g *Gate) Status(id string) (Approval, error) {
	var r approvalRecord
	err := g.locked(func() error {
		var err error
		if r, err = g.load(id); err != nil {
			return err
		}
		return g.refresh(&r)
	})
	return r.Approval, err
}

// Approve hands off execution. The ID alone can never authorize a pending operation.
func (g *Gate) Approve(id, token string) (Approval, error) {
	return g.updatePending(id, token, approved)
}

// Cancel releases a prompt after its owner declines or stops waiting.
func (g *Gate) Cancel(id, token string) error {
	_, err := g.updatePending(id, token, cancelled)
	return err
}

func (g *Gate) updatePending(id, token, state string) (Approval, error) {
	var r approvalRecord
	err := g.locked(func() error {
		signed, bearer, err := g.credential(token)
		if err != nil || !bearer || id != signed {
			return ErrToken
		}
		if r, err = g.load(id); err != nil {
			return err
		}
		if err = g.refresh(&r); err != nil {
			return err
		}
		if err = stateError(r.State); err != nil {
			return err
		}
		if r.State == approved && state == approved {
			return nil
		}
		if r.State != pending {
			return ErrToken
		}
		if rule := g.Explain(r.Class, r.Action, r.Related, r.Forced); state == approved && rule.Level == config.Suggest {
			return ErrChanged
		}
		return g.change(&r, state)
	})
	return r.Approval, err
}
