package aisession

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NewID returns a fresh session ID.
func NewID() ID { return ID(IDPrefix + randomHex(16)) }

// ValidID reports whether s is a session ID: "ais_" and 32 lowercase hex.
func ValidID(s string) bool { return idRe.MatchString(s) }

var (
	idRe    = regexp.MustCompile(`^ais_[0-9a-f]{32}$`)
	askIDRe = regexp.MustCompile(`^ask_[0-9a-f]{16}$`)
)

func newAskID() string { return "ask_" + randomHex(8) }

func validAskID(s string) bool { return askIDRe.MatchString(s) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

// ---- the Content Filter: every agent string passes it before the store ----

// Sanitize makes agent text safe to store and show: invalid UTF-8 dropped,
// terminal sequences removed, control characters other than newline and
// tab removed, bidirectional controls removed, known token shapes
// redacted, and the result clamped to max bytes at a character boundary.
func Sanitize(s string, max int) string {
	s = StripTerminal(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return -1
		case unicode.IsControl(r), bidiControl(r):
			return -1
		}
		return r
	}, s)
	s = Redact(s)
	return Clamp(s, max)
}

// SanitizeLine is Sanitize for one-line text (titles, names, IDs): newlines
// and tabs become spaces.
func SanitizeLine(s string, max int) string {
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(s, max))
	return strings.TrimSpace(s)
}

// Clamp cuts s to at most max bytes at a character boundary. max <= 0
// keeps s.
func Clamp(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// ClampStart keeps the last max bytes of s, cut at a character boundary
// (terminal output is truncated from the start).
func ClampStart(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func bidiControl(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}

// terminal sequences: CSI, OSC (to BEL or ST), DCS/SOS/PM/APC strings,
// two-byte escapes, and the 8-bit C1 forms.
var terminalRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[PX^_][^\x1b]*(?:\x1b\\)?|\x1b[@-Z\\-_]|\x1b.?|\x{9b}[0-?]*[ -/]*[@-~]|\x{9d}[^\x07\x{9c}]*[\x07\x{9c}]?`)

// StripTerminal removes terminal control sequences from s and drops
// invalid UTF-8.
func StripTerminal(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return terminalRe.ReplaceAllString(s, "")
}

// redaction: a help, not a guarantee.
var redactRe = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-ant-[A-Za-z0-9_\-]{10,}|sk-[A-Za-z0-9_\-]{16,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9\-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)

// Redacted replaces a token match.
const Redacted = "[redacted]"

// Redact replaces known token shapes with [redacted].
func Redact(s string) string { return redactRe.ReplaceAllString(s, Redacted) }
