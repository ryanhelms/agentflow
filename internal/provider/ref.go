package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

// Ref identifies an object within a configured provider instance. Scope and ID
// belong to the adapter and may contain punctuation. Only a display key is
// expected to be human-friendly.
type Ref struct {
	Instance string `json:"instance"`
	Scope    string `json:"scope"`
	ID       string `json:"id"`
}

func (r Ref) Validate() error {
	for _, part := range []string{r.Instance, r.Scope, r.ID} {
		if strings.TrimSpace(part) == "" || strings.ContainsFunc(part, unicode.IsControl) {
			return errors.New("reference requires a nonempty instance, scope and id without control characters")
		}
	}
	return nil
}

// String returns the escaped form used in machine-readable trailers.
func (r Ref) String() string {
	return url.PathEscape(r.Instance) + "/" + url.PathEscape(r.Scope) + "/" + url.PathEscape(r.ID)
}

func ParseRef(value string) (Ref, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		return Ref{}, errors.New("reference must have instance, scope and id")
	}
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return Ref{}, fmt.Errorf("decode reference: %w", err)
		}
		parts[i] = decoded
	}
	ref := Ref{Instance: parts[0], Scope: parts[1], ID: parts[2]}
	return ref, ref.Validate()
}

// StorageKey derives a single safe filename component for new checklist and
// provider state. Legacy Jira checklist lookup retains its existing key.
func (r Ref) StorageKey() string {
	sum := sha256.Sum256([]byte(r.String()))
	return hex.EncodeToString(sum[:])
}

const TaskTrailer = "Task-Ref: "

// TaskRefs reads explicit task references from a commit or PR body. In commit
// messages, use the trailer after the subject. GitHub's squash suffix and
// ordinary #mentions carry no task intent.
func TaskRefs(body string) ([]Ref, error) {
	var refs []Ref
	for line := range strings.Lines(body) {
		value, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), TaskTrailer)
		if !ok {
			continue
		}
		ref, err := ParseRef(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("task reference %q: %w", value, err)
		}
		if !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}
