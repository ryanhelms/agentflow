package nativemap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nscaledev/agentflow/internal/provider"
)

var (
	slackChannel = regexp.MustCompile(`^[CDG][A-Z0-9]{8,31}$`)
	slackTS      = regexp.MustCompile(`^[0-9]{1,16}\.[0-9]{6}$`)
)

const slackThreadLimit = 20

type SlackMessage struct {
	Timestamp string `json:"ts"`
	ThreadID  string `json:"thread_ts"`
	User      string `json:"user"`
	Text      string `json:"text"`
}

type SlackThreadPage struct {
	Ref      provider.Ref
	Messages []SlackMessage
	More     bool
	// Presentation holds a successful prose-only tool response. It is
	// suitable for display, with no machine-verifiable message or cursor.
	Presentation string
}

// SlackThread requests at most 20 messages from one conversation and parent
// timestamp. The caller selects the workspace session for ref.Instance.
func SlackThread(ref provider.Ref) (Read[SlackThreadPage], error) {
	if err := ref.Validate(); err != nil {
		return Read[SlackThreadPage]{}, err
	}
	if !slackChannel.MatchString(ref.Scope) || !slackTS.MatchString(ref.ID) {
		return Read[SlackThreadPage]{}, errors.New("slack read requires a conversation id and parent message timestamp")
	}
	input := struct {
		ChannelID      string `json:"channel_id"`
		MessageTS      string `json:"message_ts"`
		Limit          int    `json:"limit"`
		ResponseFormat string `json:"response_format"`
	}{ref.Scope, ref.ID, slackThreadLimit, "detailed"}
	return newRead(Slack, ReadSlackThread, ref, input, func(raw json.RawMessage) (SlackThreadPage, error) {
		return slackThreadPage(ref, raw)
	})
}

func slackThreadPage(ref provider.Ref, raw json.RawMessage) (SlackThreadPage, error) {
	if len(raw) > MaxResultBytes {
		return SlackThreadPage{}, fmt.Errorf("%w: slack result exceeds byte limit", ErrInvalidResult)
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte{'"'}) {
		var presentation string
		if json.Unmarshal(raw, &presentation) != nil || strings.TrimSpace(presentation) == "" {
			return SlackThreadPage{}, fmt.Errorf("%w: expected nonempty slack thread text", ErrInvalidResult)
		}
		return SlackThreadPage{Ref: ref, Presentation: presentation}, nil
	}
	var page struct {
		OK         *bool          `json:"ok"`
		ChannelID  string         `json:"channel_id"`
		Messages   []SlackMessage `json:"messages"`
		HasMore    bool           `json:"has_more"`
		NextCursor string         `json:"next_cursor"`
		Metadata   struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := decodeObject(raw, &page); err != nil {
		return SlackThreadPage{}, err
	}
	if page.OK != nil && !*page.OK || len(page.Messages) == 0 || len(page.Messages) > slackThreadLimit {
		return SlackThreadPage{}, fmt.Errorf("%w: expected a bounded slack thread page", ErrInvalidResult)
	}
	if changedIdentity(ref.Scope, page.ChannelID) || page.Messages[0].Timestamp != ref.ID {
		return SlackThreadPage{}, fmt.Errorf("slack returned a different conversation or thread: %w", provider.ErrConflict)
	}
	if changedIdentity(ref.ID, page.Messages[0].ThreadID) {
		return SlackThreadPage{}, fmt.Errorf("slack result begins with a reply to a different thread: %w", provider.ErrConflict)
	}
	seen := map[string]bool{ref.ID: true}
	for _, message := range page.Messages[1:] {
		if !slackTS.MatchString(message.Timestamp) || message.ThreadID != ref.ID || seen[message.Timestamp] {
			return SlackThreadPage{}, fmt.Errorf("%w: invalid slack reply identity", ErrInvalidResult)
		}
		seen[message.Timestamp] = true
	}
	more := page.HasMore || page.NextCursor != "" || page.Metadata.NextCursor != ""
	return SlackThreadPage{Ref: ref, Messages: page.Messages, More: more}, nil
}
