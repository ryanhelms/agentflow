package nativemap

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

var (
	slackActorID    = regexp.MustCompile(`^[UW][A-Z0-9]{8,31}$`)
	slackActorLine  = regexp.MustCompile("(?m)^(?:- )?(?:\\*\\*)?User ID:(?:\\*\\*)? +`?([UW][A-Z0-9]{8,31})`? *$")
	slackChannelRow = regexp.MustCompile("(?m)^### [^\r\n]+\r?\n- \\*\\*ID:\\*\\* +`?([CDG][A-Z0-9]{8,31})`? *\r?$")
	slackCursorLine = regexp.MustCompile("(?m)^Pagination:[^\r\n]*`([^`\r\n]+)`[^\r\n]*$")
)

type SlackIdentity struct {
	ActorID     string
	WorkspaceID string
}

type slackProfileIDs struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
}

// DecodeSlackIdentity accepts the hosted MCP self-profile response. Its caller
// must omit user_id so that the result describes the authenticated user.
func DecodeSlackIdentity(raw json.RawMessage) (SlackIdentity, error) {
	var result struct {
		slackProfileIDs
		OK      *bool           `json:"ok"`
		User    slackProfileIDs `json:"user"`
		Profile slackProfileIDs `json:"profile"`
		Result  string          `json:"result"`
	}
	var identity SlackIdentity
	if err := decodeObject(raw, &result); err != nil {
		return identity, err
	}
	if result.OK != nil && !*result.OK {
		return identity, fmt.Errorf("%w: slack self profile was rejected", ErrInvalidResult)
	}
	actors := slackActorLine.FindAllStringSubmatch(result.Result, 2)
	if len(actors) > 1 {
		return identity, fmt.Errorf("%w: slack self profile has ambiguous actor identity", ErrInvalidResult)
	}
	if len(actors) == 1 {
		identity.ActorID = actors[0][1]
	}
	for _, ids := range []slackProfileIDs{result.slackProfileIDs, result.User, result.Profile} {
		if !mergeSlackID(&identity.ActorID, slackActorID, ids.ID, ids.UserID) ||
			!mergeSlackID(&identity.WorkspaceID, slackWorkspace, ids.Team, ids.TeamID) {
			return SlackIdentity{}, fmt.Errorf("%w: invalid or conflicting slack self profile identity", ErrInvalidResult)
		}
	}
	return identity, nil
}

func mergeSlackID(dst *string, pattern *regexp.Regexp, values ...string) bool {
	for _, value := range values {
		if value == "" {
			continue
		}
		if !pattern.MatchString(value) || (*dst != "" && *dst != value) {
			return false
		}
		*dst = value
	}
	return true
}

// FindSlackWorkspaceChannel examines a page of the hosted MCP channel list.
// The caller must supply team_id and exclude_archived on the bound connection.
func FindSlackWorkspaceChannel(raw json.RawMessage, workspace, channel string) (bool, string, error) {
	var result struct {
		OK       *bool  `json:"ok"`
		TeamID   string `json:"team_id"`
		Result   string `json:"result"`
		Channels []struct {
			ID            string `json:"id"`
			ContextTeamID string `json:"context_team_id"`
			Archived      bool   `json:"is_archived"`
		} `json:"channels"`
		Pagination struct {
			NextCursor string `json:"next_cursor"`
		} `json:"pagination_info"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return false, "", err
	}
	if result.OK != nil && !*result.OK {
		return false, "", fmt.Errorf("%w: slack channel listing was rejected", ErrInvalidResult)
	}
	if result.TeamID != "" && result.TeamID != workspace {
		return false, "", fmt.Errorf("slack channel list returned a different workspace: %w", provider.ErrConflict)
	}
	for _, item := range result.Channels {
		if item.ID == channel {
			if item.Archived || (item.ContextTeamID != "" && item.ContextTeamID != workspace) {
				return false, "", fmt.Errorf("slack conversation is archived or belongs to a different workspace: %w", provider.ErrConflict)
			}
			return true, "", nil
		}
	}
	for _, row := range slackChannelRow.FindAllStringSubmatch(result.Result, -1) {
		if row[1] == channel {
			return true, "", nil
		}
	}
	cursor := result.Pagination.NextCursor
	if cursor == "" {
		cursor = result.Metadata.NextCursor
	}
	if cursor == "" {
		if match := slackCursorLine.FindStringSubmatch(result.Result); match != nil {
			cursor = match[1]
		}
	}
	if cursor != "" && !validName(cursor) {
		return false, "", fmt.Errorf("%w: invalid slack channel list cursor", ErrInvalidResult)
	}
	return false, cursor, nil
}

// SlackChannelTypes selects Slack's workspace membership filter for a conversation.
func SlackChannelTypes(channel string) string {
	switch {
	case strings.HasPrefix(channel, "D"):
		return "im"
	case strings.HasPrefix(channel, "G"):
		return "private_channel,mpim"
	default:
		return "public_channel,private_channel"
	}
}
