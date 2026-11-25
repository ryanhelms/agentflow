package twg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

// AddRemoteLink creates or updates a Jira web link by its caller-supplied stable
// global ID. A successful result acknowledges the command. The caller verifies
// the resulting link with a separate read.
func (j *Jira) AddRemoteLink(ctx context.Context, ref provider.Ref, linkURL, title, globalID string) error {
	ref, err := j.issueRef(ref)
	if err != nil {
		return err
	}

	// TWG trims these strings before writing. Reject values that would change
	// in transit, including a byte order mark trimmed by JavaScript.
	for _, value := range []string{linkURL, title, globalID} {
		if value == "" || !utf8.ValidString(value) || strings.TrimFunc(value, func(r rune) bool {
			return unicode.IsSpace(r) || r == '\uFEFF'
		}) != value {
			return errors.New("jira web link url, title and global id must be nonempty exact text without surrounding whitespace")
		}
	}

	const name = "add jira web link"
	args := []string{"jira", "workitem", "link", "weblink", "--id", ref.ID, "--url", linkURL, "--title", title, "--global-id", globalID}
	var ack struct {
		Success bool
		Issue   struct{ IDOrKey string }
		Weblink struct{ URL, Title, GlobalID string }
	}
	started, err := j.run(ctx, name, args, &ack)
	if !started {
		return err
	}
	if err == nil && (!ack.Success || ack.Issue.IDOrKey != ref.ID || ack.Weblink.URL != linkURL ||
		ack.Weblink.Title != title || ack.Weblink.GlobalID != globalID) {
		err = fmt.Errorf("%s: incomplete or mismatched acknowledgement: %w", name, ErrFailure)
	}
	if err != nil {
		return errors.Join(native.ErrWriteUncertain, err)
	}
	return nil
}
