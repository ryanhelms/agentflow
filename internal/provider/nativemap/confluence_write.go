package nativemap

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/confluence"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

// ConfluencePublication maps a replacement on the configured Rovo v2 site.
// Snapshot tokens must come from an authenticated read on the prepared route.
type ConfluencePublication struct {
	site string
	confluence.Replacement
}

func PrepareConfluencePublication(site string, ref provider.Ref, draft provider.DocumentDraft, options provider.WriteOptions) (ConfluencePublication, error) {
	origin, err := httpsOrigin(site)
	if err != nil {
		return ConfluencePublication{}, err
	}
	replacement, err := confluence.NewReplacement(confluence.PageURL(site, ref.ID), ref, draft, options)
	return ConfluencePublication{site: origin, Replacement: replacement}, err
}

func (p ConfluencePublication) call(tool string, args map[string]any) native.Call {
	args["cloudId"] = p.site
	return native.Call{Provider: "confluence", Server: "atlassian", Tool: tool, Arguments: args}
}

func (p ConfluencePublication) PageCall() native.Call {
	return p.call("getConfluenceContent", map[string]any{
		"content_id": p.Ref.ID, "content_format": "html", "detail": "full", "include_metadata": true,
	})
}

func (p ConfluencePublication) UpdateCall(snapshot string) native.Call {
	return p.call("updateConfluenceContent", map[string]any{
		"contentId": p.Ref.ID, "contentType": "page", "title": p.Title, "snapshotToken": snapshot,
		"body": map[string]any{"format": "html", "value": p.HTML}, "versionMessage": p.Successor.Message,
	})
}

// VersionCall uses the read-only execution path for a deferred Rovo tool.
func (p ConfluencePublication) VersionCall(number int64) native.Call {
	return p.call("executeRead", map[string]any{
		"name":   "getConfluenceContentVersion",
		"inputs": map[string]any{"contentId": p.Ref.ID, "versionNumber": number},
	})
}

type ConfluenceSnapshot struct {
	Token      string
	SpaceID    string
	Title      string
	Version    atlassian.PageVersion
	HasMessage bool
}

type confluenceWritePage struct {
	ID            json.Number     `json:"id"`
	ContentID     json.Number     `json:"contentId"`
	Title         string          `json:"title"`
	Type          string          `json:"type"`
	ContentType   string          `json:"contentType"`
	Status        string          `json:"status"`
	Success       *bool           `json:"success"`
	Operation     string          `json:"operationPerformed"`
	Error         json.RawMessage `json:"error"`
	SnapshotToken string          `json:"snapshotToken"`
	SpaceID       json.Number     `json:"spaceId"`
	SpaceKey      string          `json:"spaceKey"`
	URL           string          `json:"url"`
	WebURL        string          `json:"webUrl"`
	Version       struct {
		Number  int64   `json:"number"`
		Message *string `json:"message"`
	} `json:"version"`
	Metadata struct {
		SpaceID  json.Number `json:"spaceId"`
		SpaceKey string      `json:"spaceKey"`
	} `json:"metadata"`
}

func (p ConfluencePublication) checkPageIdentity(page confluenceWritePage) error {
	if (page.ID == "" && page.ContentID == "") || changedIdentity(p.Ref.ID, page.ID.String(), page.ContentID.String()) {
		return fmt.Errorf("confluence response did not identify the requested page: %w", ErrInvalidResult)
	}
	if changedIdentity(p.Ref.Scope, page.SpaceKey, page.Metadata.SpaceKey) {
		return fmt.Errorf("confluence response is outside the requested space: %w", provider.ErrNotFound)
	}
	if changedIdentity("page", page.Type, page.ContentType) {
		return fmt.Errorf("confluence response does not describe a page: %w", ErrInvalidResult)
	}
	site, _ := url.Parse(p.site)
	for _, raw := range []string{page.URL, page.WebURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.Opaque != "" ||
			(u.IsAbs() && (u.Scheme != "https" || !strings.EqualFold(u.Host, site.Host))) ||
			(!u.IsAbs() && (u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//"))) {
			return fmt.Errorf("confluence response url is outside the configured site: %w", ErrInvalidResult)
		}
	}
	return nil
}

func (p ConfluencePublication) DecodeSnapshot(raw json.RawMessage) (ConfluenceSnapshot, error) {
	var page confluenceWritePage
	if err := decodeObject(raw, &page); err != nil {
		return ConfluenceSnapshot{}, err
	}
	if err := p.checkPageIdentity(page); err != nil {
		return ConfluenceSnapshot{}, err
	}
	spaceID := page.SpaceID.String()
	if spaceID == "" {
		spaceID = page.Metadata.SpaceID.String()
	}
	if (page.SpaceKey == "" && page.Metadata.SpaceKey == "") || !atlassian.ValidConfluenceID(spaceID) ||
		changedIdentity(spaceID, page.Metadata.SpaceID.String()) || (page.Type == "" && page.ContentType == "") ||
		page.Status != "current" || page.Version.Number < 1 || strings.TrimSpace(page.Title) == "" {
		return ConfluenceSnapshot{}, fmt.Errorf("confluence response lacks published page, space or version metadata: %w", ErrInvalidResult)
	}
	snapshot := ConfluenceSnapshot{Token: page.SnapshotToken, SpaceID: spaceID, Title: page.Title,
		Version: atlassian.PageVersion{Number: page.Version.Number}, HasMessage: page.Version.Message != nil}
	if snapshot.HasMessage {
		snapshot.Version.Message = *page.Version.Message
	}
	return snapshot, nil
}

// CheckAcknowledgement verifies the write response or requires an authenticated
// read-back when the provider returns only the content ID and snapshot.
func (p ConfluencePublication) CheckAcknowledgement(raw json.RawMessage, spaceID string) (bool, error) {
	var page confluenceWritePage
	if err := decodeObject(raw, &page); err != nil {
		return false, err
	}
	if strings.EqualFold(page.Status, "snapshot_stale") {
		return false, fmt.Errorf("confluence snapshot is stale: %w", provider.ErrConflict)
	}
	switch strings.ToLower(page.Status) {
	case "", "current", "success", "updated":
	default:
		return false, fmt.Errorf("confluence update reported an unconfirmed status: %w", ErrInvalidResult)
	}
	if err := p.checkPageIdentity(page); err != nil {
		return false, err
	}
	if (page.Success != nil && !*page.Success) || (len(page.Error) != 0 && string(page.Error) != "null") ||
		(page.Operation != "" && !strings.EqualFold(page.Operation, "updated")) ||
		changedIdentity(spaceID, page.SpaceID.String(), page.Metadata.SpaceID.String()) ||
		changedIdentity(p.Title, page.Title) || (page.Version.Number != 0 && page.Version.Number != p.Successor.Number) ||
		(page.Version.Message != nil && *page.Version.Message != p.Successor.Message) {
		return false, fmt.Errorf("confluence update acknowledgement conflicts with the requested successor: %w", ErrInvalidResult)
	}
	hasSpaceID := page.SpaceID != "" || page.Metadata.SpaceID != ""
	return hasSpaceID && page.Status == "current" && page.Title == p.Title &&
		page.Version.Number == p.Successor.Number && page.Version.Message != nil, nil
}

func (p ConfluencePublication) DecodeVersion(raw json.RawMessage, number int64) (atlassian.PageVersion, error) {
	type version struct {
		Number  int64   `json:"number"`
		Message *string `json:"message"`
	}
	var result struct {
		version
		Version   *version        `json:"version"`
		ContentID json.Number     `json:"contentId"`
		Data      json.RawMessage `json:"data"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return atlassian.PageVersion{}, err
	}
	if len(result.Data) != 0 {
		if result.Number != 0 || result.Message != nil || result.Version != nil || result.ContentID != "" {
			return atlassian.PageVersion{}, fmt.Errorf("ambiguous confluence version response: %w", ErrInvalidResult)
		}
		payload := result.Data
		result.Data = nil
		if err := decodeObject(payload, &result); err != nil {
			return atlassian.PageVersion{}, err
		}
	}
	if result.Version != nil && result.Number == 0 && result.Message == nil {
		result.version = *result.Version
	} else if result.Version != nil {
		return atlassian.PageVersion{}, fmt.Errorf("ambiguous confluence version response: %w", ErrInvalidResult)
	}
	if len(result.Data) != 0 || changedIdentity(p.Ref.ID, result.ContentID.String()) || result.Number != number || result.Message == nil {
		return atlassian.PageVersion{}, fmt.Errorf("confluence response lacks the exact historical version: %w", ErrInvalidResult)
	}
	return atlassian.PageVersion{Number: number, Message: *result.Message}, nil
}
