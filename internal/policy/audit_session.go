package policy

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxAuditSubject bounds SessionAudit.Subject.
const MaxAuditSubject = 256

// SessionAudit is the audit line of one resolved ACP session request (ADR,
// "Phase 7 permission chain", amended). Subject is the worktree-relative
// path or the program name only: never file contents, other arguments,
// environment values or answer keys.
type SessionAudit struct {
	Class                                                                 Class
	Action, Session, Kind, ToolKind, Subject, Decision, Handler, Code, By string
}

// AuditSession appends one line for a to audit.log: time, client, class,
// action, session, kind, tool_kind, subject, decision, handler, code and
// by, leaving out empty fields. Like Audit, it is best effort.
func (g *Gate) AuditSession(a SessionAudit) {
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	entry := map[string]any{"time": now().UTC().Format(time.RFC3339), "client": g.Client}
	if a.Class != "" {
		entry["class"] = a.Class
	}
	for _, f := range []struct{ k, v string }{
		{"action", a.Action}, {"session", a.Session}, {"kind", a.Kind}, {"tool_kind", a.ToolKind},
		{"subject", auditSubject(a.Subject)}, {"decision", a.Decision}, {"handler", a.Handler},
		{"code", a.Code}, {"by", a.By},
	} {
		if v := auditField(f.v); v != "" {
			entry[f.k] = v
		}
	}
	g.appendAudit(entry)
}

// auditField removes control and bidirectional characters.
func auditField(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || bidi(r) {
			return -1
		}
		return r
	}, s)
}

// auditSubject cleans s and clamps it to MaxAuditSubject bytes at a
// character boundary.
func auditSubject(s string) string {
	s = auditField(s)
	if len(s) <= MaxAuditSubject {
		return s
	}
	cut := MaxAuditSubject
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
