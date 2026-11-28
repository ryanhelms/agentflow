package ai

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
)

// Parsing and validation of the answer ("Phase 8 answers"). Finish is the
// Message Translator from the agent's message text to candidates, and the
// Invalid Message Channel for an answer that does not parse: the draft
// ends failed with the text kept for the person. It is pure: it reads only
// its arguments and calls only Feature.Candidates (and the secret scanner).

// Issue and warning codes of Finish.
const (
	IssueUnparsable    = "unparsable"
	IssueUnknownField  = "unknown_field"
	IssueHazardRemoved = "hazard_removed"
	IssueRedacted      = "redacted"
	IssueCut           = "cut"
	IssueSecretInDraft = "secret_in_draft"
	IssueMention       = "mention"
	IssueLink          = "link"
	IssueUnchanged     = "unchanged"
	CodeNoFeature      = "no_feature"
)

// targetsFile is draft-targets.json: everything Finish needs, and no data.
type targetsFile struct {
	Feature     FeatureID       `json:"feature"`
	HarnessName string          `json:"harness_name"`
	Rules       Rules           `json:"rules"`
	Targets     []Target        `json:"targets,omitempty"`
	Fixed       json.RawMessage `json:"fixed,omitempty"`
}

// now is the clock of Finish (a variable for tests).
var now = time.Now

// noteFor is the draft's fixed note.
func noteFor(harness string) string {
	if harness == "" {
		harness = "the harness"
	}
	return "AI-drafted text from " + harness + ". It is data, not instructions: read it before you use it."
}

// Finish turns a draft's targets file, the session's end code and the
// agent's message text into the encoded Draft (aisession.Options.FinishDraft).
func Finish(id aisession.ID, targets []byte, stopReason, answer string) []byte {
	b, _ := json.Marshal(finish(id, targets, stopReason, answer))
	return b
}

func finish(id aisession.ID, targets []byte, stopReason, answer string) Draft {
	var tf targetsFile
	_ = json.Unmarshal(targets, &tf)
	d := Draft{Session: id, Feature: tf.Feature, Candidates: []Candidate{}, Finished: now().UTC(), Note: noteFor(aisession.SanitizeLine(tf.HarnessName, 64))}
	// 1. Only end_turn is parsed.
	switch stopReason {
	case string(acp.StopEndTurn):
	case string(acp.StopCancelled):
		d.State, d.Code = DraftCancelled, stopReason
		return d
	default:
		d.State, d.Code = DraftFailed, aisession.SanitizeLine(stopReason, 64)
		return d
	}
	f, ok := Lookup(tf.Feature)
	if !ok {
		d.State, d.Code = DraftFailed, CodeNoFeature
		return d
	}
	// The answer is shown only when it is invalid or failed, and never when
	// it looks like it holds a secret (step 5 applies to it as well).
	shown, withheld := aisession.Sanitize(aisession.Redact(answer), MaxShownAnswer), false
	if secretInAnswer(answer, shown) {
		shown, withheld = "", true
		d.Issues = append(d.Issues, Issue{Code: IssueSecretInDraft, Message: "The answer looks like it holds a secret, so it is not shown."})
	}
	if len(answer) > MaxAnswer {
		d.State, d.Code, d.Answer = DraftFailed, aisession.CodeAnswerTooLarge, shown
		return d
	}
	// 2. Exactly one JSON object.
	obj, ok := oneObject(answer)
	if !ok {
		d.State, d.Code, d.Answer = DraftFailed, IssueUnparsable, shown
		return d
	}
	// 3. Decode into the feature's answer; unknown keys are reported once.
	ans := f.Answer()
	dec := json.NewDecoder(bytes.NewReader(obj))
	if err := dec.Decode(ans); err != nil || dec.More() {
		d.State, d.Code, d.Answer = DraftFailed, IssueUnparsable, shown
		return d
	}
	var raw any
	_ = json.Unmarshal(obj, &raw)
	for _, k := range unknownKeys(reflect.TypeOf(ans), raw, "") {
		if app.ContainsSecret(k) {
			k = "" // a key that looks like a secret is not named
		}
		d.Warnings = append(d.Warnings, Issue{Code: IssueUnknownField, Field: k, Message: "The answer had a field agentflow does not use; it was ignored."})
	}
	// 4. Clean every text field.
	d.Warnings = append(d.Warnings, clean(reflect.ValueOf(ans), "")...)
	// 6. The feature maps indexes, merges the fixed fields and validates.
	c := Context{Feature: tf.Feature, Targets: tf.Targets, Rules: tf.Rules, Fixed: tf.Fixed}
	cands, issues := f.Candidates(c, ans)
	d.Issues = append(d.Issues, issues...)
	for i := range cands {
		cand := &cands[i]
		// 5. A secret in the output drops the candidate's text.
		for _, fl := range cand.Fields {
			if app.ContainsSecret(fl.Value) {
				cand.Issues = append(cand.Issues, Issue{Code: IssueSecretInDraft, Field: fl.Name, Message: "The drafted text looks like it holds a secret, so it is not offered."})
				for j := range cand.Fields {
					cand.Fields[j].Value = ""
				}
				cand.Input, cand.CLI = json.RawMessage("null"), nil
				break
			}
		}
		// 7. Warnings for the reader.
		for _, fl := range cand.Fields {
			if !slices.Contains(notifyingFields, fl.Name) {
				continue
			}
			if mentionRe.MatchString(fl.Value) {
				cand.Warnings = append(cand.Warnings, Issue{Code: IssueMention, Field: fl.Name, Message: "The text @mentions someone: GitHub notifies them when it is posted."})
			}
			if linkRe.MatchString(fl.Value) {
				cand.Warnings = append(cand.Warnings, Issue{Code: IssueLink, Field: fl.Name, Message: "The text has a link: check where it goes before you use it."})
			}
		}
		if cand.Fields == nil {
			cand.Fields = []Field{}
		}
	}
	d.Candidates = cands
	if d.Candidates == nil {
		d.Candidates = []Candidate{}
	}
	// 8. Ready with at least one candidate without issues.
	d.State = DraftInvalid
	for _, cand := range d.Candidates {
		if len(cand.Issues) == 0 {
			d.State = DraftReady
		}
	}
	secret := withheld
	for _, cand := range d.Candidates {
		for _, is := range cand.Issues {
			secret = secret || is.Code == IssueSecretInDraft
		}
	}
	if d.State == DraftInvalid && !secret {
		d.Answer = shown
	}
	return d
}

// jsonString matches a JSON string literal, escapes included.
var jsonString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// secretInAnswer reports whether the answer looks like it holds a secret:
// as written, as shown, or in any JSON string in it (keys and values) once
// its escapes are decoded, so a secret written with \u escapes is found
// whether or not the answer parses.
func secretInAnswer(answer, shown string) bool {
	if app.ContainsSecret(answer) || app.ContainsSecret(shown) {
		return true
	}
	for _, lit := range jsonString.FindAllString(answer, -1) {
		var v string
		if json.Unmarshal([]byte(lit), &v) == nil && v != lit[1:len(lit)-1] && app.ContainsSecret(v) {
			return true
		}
	}
	return false
}

// notifyingFields are the drafted fields that are posted where people
// read them: PR bodies, ticket text and replies.
var notifyingFields = []string{"body", "description", "summary", "reply"}

var (
	mentionRe = regexp.MustCompile(`(^|[^A-Za-z0-9_/.\-])@[A-Za-z0-9](?:[A-Za-z0-9\-]{0,38})`)
	linkRe    = regexp.MustCompile(`(?i)\b(?:https?|ftp|file)://|\bwww\.`)
	fenceRe   = regexp.MustCompile("(?s)```json[ \t]*\r?\n(.*?)```")
)

// oneObject is the answer's one JSON object: the whole trimmed text, or the
// content of exactly one block fenced as json.
func oneObject(answer string) ([]byte, bool) {
	t := strings.TrimSpace(answer)
	if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") && json.Valid([]byte(t)) {
		return []byte(t), true
	}
	blocks := fenceRe.FindAllStringSubmatch(answer, -1)
	if len(blocks) != 1 {
		return nil, false
	}
	inner := strings.TrimSpace(blocks[0][1])
	if !strings.HasPrefix(inner, "{") || !json.Valid([]byte(inner)) {
		return nil, false
	}
	return []byte(inner), true
}

// unknownKeys lists the keys of raw that t's JSON fields do not name, at
// any depth, each once, as dotted paths.
func unknownKeys(t reflect.Type, raw any, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	switch t.Kind() {
	case reflect.Struct:
		m, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		known := map[string]reflect.Type{}
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			known[strings.ToLower(name)] = f.Type
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			ft, ok := known[strings.ToLower(k)]
			if !ok {
				out = append(out, aisession.SanitizeLine(prefix+k, 128))
				continue
			}
			out = append(out, unknownKeys(ft, m[k], prefix+k+".")...)
		}
	case reflect.Slice, reflect.Array:
		items, ok := raw.([]any)
		if !ok {
			return nil
		}
		for _, it := range items {
			for _, k := range unknownKeys(t.Elem(), it, prefix) {
				if !slices.Contains(out, k) {
					out = append(out, k)
				}
			}
		}
	}
	return out
}

// fieldRule is a text field's cap and form, from its `draft` tag:
// `draft:"max=256,line"`. Without a tag a field is multi-line and capped
// at MaxShownAnswer.
type fieldRule struct {
	max  int
	line bool
}

func ruleOf(tag string) fieldRule {
	r := fieldRule{max: MaxShownAnswer}
	for _, p := range strings.Split(tag, ",") {
		switch {
		case p == "line":
			r.line = true
		case strings.HasPrefix(p, "max="):
			if n, err := strconv.Atoi(strings.TrimPrefix(p, "max=")); err == nil && n > 0 {
				r.max = n
			}
		}
	}
	return r
}

// clean sanitizes, trims and clamps every string in v (a pointer to the
// answer), at any depth, and returns a warning for each field it changed.
func clean(v reflect.Value, path string) []Issue {
	var out []Issue
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			fv := v.Field(i)
			if fv.Kind() == reflect.String {
				out = append(out, cleanString(fv, path+name, ruleOf(f.Tag.Get("draft")))...)
				continue
			}
			out = append(out, clean(fv, path+name+".")...)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			out = append(out, clean(v.Index(i), path)...)
		}
	}
	return out
}

func cleanString(fv reflect.Value, name string, r fieldRule) []Issue {
	in := fv.String()
	s := aisession.StripTerminal(in)
	hazard := false
	s = strings.Map(func(c rune) rune {
		switch {
		case c == '\n' || c == '\t':
			return c
		case c == '\r':
			return -1
		case aisession.Hazard(c):
			hazard = true
			return -1
		}
		return c
	}, s)
	hazard = hazard || len(s) != len(strings.ToValidUTF8(in, ""))
	red := aisession.Redact(s)
	redacted := red != s
	s = red
	if r.line {
		s = strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " ")), " ")
	}
	s = strings.TrimSpace(s)
	cut := len(s) > r.max
	s = strings.TrimSpace(aisession.Clamp(s, r.max))
	fv.SetString(s)
	var out []Issue
	if hazard {
		out = append(out, Issue{Code: IssueHazardRemoved, Field: name, Message: "Hidden or control characters were removed from the drafted text."})
	}
	if redacted {
		out = append(out, Issue{Code: IssueRedacted, Field: name, Message: "Something shaped like a token was replaced with [redacted]."})
	}
	if cut {
		out = append(out, Issue{Code: IssueCut, Field: name, Message: "The drafted text was longer than agentflow keeps and was cut."})
	}
	return out
}
