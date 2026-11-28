package aisession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

// The draft profile (ADR "Phase 8 draft sessions"). A draft is a phase 7
// session that may do nothing but answer: every file, terminal and
// permission request except think is denied before the chain, no ask is
// ever held, any tool call the harness reports other than think ends it,
// and the harness starts in an empty folder inside the session directory.
// The host collects the agent's message text and, at the end, writes what
// Options.FinishDraft makes of it to draft.json.

// Draft store files.
const (
	draftPromptFile  = "draft-prompt.txt"
	draftTargetsFile = "draft-targets.json"
	draftFile        = "draft.json"
	draftWorkDir     = "work"
)

// HandlerDraft is the verdict handler of the draft link.
const HandlerDraft = "draft"

// RefuseTooLarge: the draft prompt is past MaxDraftPrompt.
const RefuseTooLarge = "too_large"

// IsDraft reports whether s is a draft session.
func (s Session) IsDraft() bool { return s.Kind == KindDraft }

// draftLink is the first link of a draft's chain: it denies every
// fs/read_text_file, fs/write_text_file, terminal/* and every permission
// request whose tool kind is not think. A think request goes on through
// the chain.
type draftLink struct{}

func (draftLink) Decide(ctx context.Context, r *Request, next Next) Verdict {
	if r.Kind == KindPermission && r.ToolKind == acp.ToolThink {
		return next(ctx, r)
	}
	return deny(HandlerDraft, CodeDraftProfile)
}

// draftVerdict is the draft profile's last word on a verdict: an Ask from
// any link becomes a deny, so no ask is ever held for a draft.
func draftVerdict(v Verdict) Verdict {
	if v.Decision == Ask {
		return deny(HandlerDraft, CodeDraftProfile)
	}
	return v
}

// draftDuration is a draft's time limit: the request's, else the
// person's ai.drafts.max_duration, clamped to the draft range.
func draftDuration(user *config.User, asked time.Duration) time.Duration {
	d := user.AI.Drafts.EffectiveMaxDuration()
	if asked > 0 && asked < d {
		d = asked
	}
	return max(config.MinAIDraftDuration, min(d, config.MaxAIDraftDuration))
}

// draftLimits are a draft's limits: the draft time, and the idle limit at
// the lower of the session's and the draft time.
func draftLimits(user *config.User, asked time.Duration) Limits {
	s := user.AI.Sessions
	d := draftDuration(user, asked)
	return Limits{MaxRunning: s.EffectiveMaxRunning(), MaxDuration: d, IdleTimeout: min(s.EffectiveIdleTimeout(), d), AskTimeout: s.EffectiveAskTimeout()}
}

// writeDraftFiles writes a starting draft's prompt, targets and empty
// working folder.
func (s *fileStore) writeDraftFiles(id ID, d *DraftStart) error {
	if err := writePrivate(s.path(id, draftPromptFile), []byte(d.Prompt)); err != nil {
		return err
	}
	if err := writePrivate(s.path(id, draftTargetsFile), d.Targets); err != nil {
		return err
	}
	return mkdirPrivate(s.path(id, draftWorkDir))
}

// removeDraftTurn removes what lives only while a draft runs: the prompt
// and the working folder.
func (s *fileStore) removeDraftTurn(id ID) {
	_ = os.Remove(s.path(id, draftPromptFile))
	_ = os.RemoveAll(s.path(id, draftWorkDir))
}

// readDraft reads draft.json, or nil when there is none yet.
func (s *fileStore) readDraft(id ID) ([]byte, error) {
	if !ValidID(string(id)) {
		return nil, ErrNoSession
	}
	b, err := os.ReadFile(s.path(id, draftFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func (m *manager) Draft(ctx context.Context, id ID) (Session, []byte, error) {
	st, err := m.store()
	if err != nil {
		return Session{}, nil, err
	}
	s, err := m.scoped(ctx, st, id)
	if err != nil {
		return Session{}, nil, err
	}
	if !s.IsDraft() {
		return Session{}, nil, ErrNoSession
	}
	b, err := st.readDraft(id)
	return s, b, err
}

// ---- the host side ----

// draftAnswer collects a draft's agent message text in memory, up to
// MaxDraftAnswer; past it the draft is cancelled answer_too_large.
func (h *host) draftAnswer(ctx context.Context, text string) {
	h.mu.Lock()
	if h.answer.Len()+len(text) > MaxDraftAnswer {
		h.mu.Unlock()
		go h.cancel(context.WithoutCancel(ctx), CodeAnswerTooLarge)
		return
	}
	h.answer.WriteString(text)
	h.mu.Unlock()
}

// draftTool ends a draft on any tool call whose kind is not think,
// whatever its status: the harness's own read and search tools are how
// injected text would pull a file into a reply.
func (h *host) draftTool(ctx context.Context, ts *toolState) {
	h.mu.Lock()
	kind, seen := ts.Kind, ts.Unasked
	if kind != acp.ToolThink && !seen {
		ts.Unasked = true
		h.sess.Counters.Unasked++
	}
	h.mu.Unlock()
	if kind == acp.ToolThink || seen {
		return
	}
	h.record(ctx, Item{Kind: ItemUnasked, ToolCall: ts.ID, Data: dataOf(map[string]any{"kind": kind, "draft": true})})
	go h.cancel(context.WithoutCancel(ctx), CodeDraftTool)
}

// draftIssue is ai.Issue as the host writes it into draft.json (the host
// does not import internal/ai).
type draftIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// draftCounts reads the counts ai.draft.finished carries.
type draftCounts struct {
	Feature    string            `json:"feature"`
	State      string            `json:"state"`
	Code       string            `json:"code"`
	Candidates []json.RawMessage `json:"candidates"`
	Issues     []json.RawMessage `json:"issues"`
	Warnings   []json.RawMessage `json:"warnings"`
}

// finishDraft runs Options.FinishDraft with the targets file, the end code
// and the collected answer, adds the worktree_changed warning, and writes
// draft.json and ai.draft.finished. It runs before the session ends.
func (h *host) finishDraft(ctx context.Context, code string, before, after map[string]string) {
	targets, _ := os.ReadFile(h.st.path(h.id, draftTargetsFile))
	h.mu.Lock()
	answer := h.answer.String()
	h.mu.Unlock()
	var out []byte
	if f := h.m.o.FinishDraft; f != nil {
		out = f(h.id, targets, code, answer)
	}
	var doc map[string]json.RawMessage
	if len(out) == 0 || json.Unmarshal(out, &doc) != nil || doc == nil {
		// No finish step, or one that gave no object: the draft failed.
		doc = map[string]json.RawMessage{
			"session": dataOf(h.id), "feature": dataOf(h.sess.Feature), "state": dataOf("failed"),
			"code": dataOf(code), "candidates": json.RawMessage("[]"), "finished": dataOf(h.now().UTC()),
		}
	}
	if changed := changedSince(before, after); len(changed) > 0 {
		var warnings []draftIssue
		_ = json.Unmarshal(doc["warnings"], &warnings)
		warnings = append(warnings, draftIssue{Code: "worktree_changed",
			Message: "The worktree changed while the draft ran: " + strings.Join(changed, ", ")})
		doc["warnings"] = dataOf(warnings)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	if err := writePrivate(h.st.path(h.id, draftFile), append(b, '\n')); err != nil {
		return
	}
	var c draftCounts
	_ = json.Unmarshal(b, &c)
	h.ev.send(ctx, bus.TypeAIDraftFinished, bus.AIDraftFinished{
		Session: string(h.id), Feature: SanitizeLine(c.Feature, 32), State: SanitizeLine(c.State, 16), Code: SanitizeLine(c.Code, 64),
		Candidates: len(c.Candidates), Issues: len(c.Issues), Warnings: len(c.Warnings),
	})
}

// worktreeState is the worktree's changed paths (git status, untracked
// files included), each with its status and the file's size and time: a
// draft records it at its start and at its end, and a difference is the
// warning worktree_changed.
func worktreeState(ctx context.Context, root string) map[string]string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out, err := (&gitx.Repo{Dir: root}).Git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil
	}
	state := map[string]string{}
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts) && len(state) < MaxChangedFiles; i++ {
		p := parts[i]
		if len(p) < 4 {
			continue
		}
		status, name := p[:2], p[3:]
		if status[0] == 'R' || status[0] == 'C' {
			i++
		}
		mark := status
		if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); err == nil {
			mark += fmt.Sprintf(" %d %d", info.Size(), info.ModTime().UnixNano())
		}
		state[SanitizeLine(name, 512)] = mark
	}
	return state
}

// changedSince lists the paths whose state differs between two
// worktreeState snapshots, sorted, at most 20 named.
func changedSince(before, after map[string]string) []string {
	var out []string
	for p, v := range after {
		if before[p] != v {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	if len(out) > 20 {
		out = append(out[:20], fmt.Sprintf("and %d more", len(out)-20))
	}
	return out
}

// ReadDraft reads a draft session's draft.json from st, or nil before it
// is finished (the AI projection's reader).
func ReadDraft(st Store, id ID) ([]byte, error) {
	if st == nil || !ValidID(string(id)) {
		return nil, ErrNoSession
	}
	b, err := os.ReadFile(filepath.Join(st.Dir(id), draftFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}
