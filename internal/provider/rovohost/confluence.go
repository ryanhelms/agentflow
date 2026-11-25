package rovohost

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
)

func (s *session) confluenceCall(call native.Call) (native.Call, error) {
	args := call.Arguments
	if s.site == "" || !atlassian.ValidConfluenceID(s.pageID) || args["cloudId"] != s.site {
		return native.Call{}, native.ErrDenied
	}
	switch call.Tool {
	case pageTool:
		if len(args) != 5 || args["content_id"] != s.pageID || args["include_metadata"] != true ||
			args["detail"] != "full" || args["content_format"] != "html" {
			return native.Call{}, native.ErrDenied
		}
		call.Arguments = maps.Clone(args)
		delete(call.Arguments, "include_metadata") // The flat tool always returns metadata.
	case updateTool:
		body, _ := args["body"].(map[string]any)
		value, bodyOK := body["value"].(string)
		title, _ := args["title"].(string)
		token, _ := args["snapshotToken"].(string)
		message, _ := args["versionMessage"].(string)
		if len(args) != 7 || args["contentId"] != s.pageID || args["contentType"] != "page" ||
			len(body) != 2 || body["format"] != "html" || !bodyOK || value == "" ||
			strings.TrimSpace(title) == "" || strings.TrimSpace(token) == "" || strings.TrimSpace(message) == "" {
			return native.Call{}, native.ErrDenied
		}
		// Later edits can require history to reconcile the approved successor.
		if err := s.checkConfluenceSchema(versionTool, map[string]any{"cloudId": s.site, "contentId": s.pageID, "versionNumber": 1}); err != nil {
			return native.Call{}, err
		}
		call.Arguments = maps.Clone(args)
		delete(call.Arguments, "contentType") // The authenticated page read proves this locally.
	case "executeRead":
		inputs, _ := args["inputs"].(map[string]any)
		if len(args) != 3 || args["name"] != versionTool || len(inputs) != 2 || inputs["contentId"] != s.pageID {
			return native.Call{}, native.ErrDenied
		}
		call.Tool = versionTool
		call.Arguments = map[string]any{"cloudId": s.site, "contentId": s.pageID, "versionNumber": inputs["versionNumber"]}
	default:
		return native.Call{}, native.ErrDenied
	}
	return call, s.checkConfluenceSchema(call.Tool, call.Arguments)
}

func (s *session) checkConfluenceSchema(name string, args map[string]any) error {
	schema := s.tools[name]
	for field := range args {
		// An open schema can accept and silently ignore unknown safety fields.
		if schema == nil || schema.Properties[field] == nil {
			return fmt.Errorf("%w: Rovo did not advertise %s.%s", native.ErrDenied, name, field)
		}
	}
	if err := native.ValidateInputSchema(schema, args); err != nil {
		return fmt.Errorf("%w: incompatible Confluence schema for %s", native.ErrDenied, name)
	}
	return nil
}

// The flat page read omits the space key. Join by the provider's numeric ID,
// on this connection, before passing the page to the canonical scope checks.
func (s *session) pageWithSpace(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var page struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		Status        string `json:"status"`
		SpaceID       string `json:"spaceId"`
		SnapshotToken string `json:"snapshotToken"`
		Metadata      struct {
			Version *struct {
				Number  int64   `json:"number"`
				Message *string `json:"message"`
			} `json:"version"`
		} `json:"metadata"`
	}
	if len(raw) > nativemap.MaxResultBytes || json.Unmarshal(raw, &page) != nil || page.ID != s.pageID ||
		page.Type != "page" || page.Status != "current" || !atlassian.ValidConfluenceID(page.SpaceID) ||
		strings.TrimSpace(page.SnapshotToken) == "" || page.Metadata.Version == nil ||
		page.Metadata.Version.Number < 1 || page.Metadata.Version.Message == nil {
		return nil, fmt.Errorf("%w: incomplete published Rovo page or revision", nativemap.ErrInvalidResult)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields["version"] != nil || fields["spaceKey"] != nil {
		return nil, fmt.Errorf("%w: ambiguous flat Confluence page metadata", nativemap.ErrInvalidResult)
	}

	args := map[string]any{"cloudId": s.site, "spaceIdOrKey": page.SpaceID}
	if err := s.checkConfluenceSchema(spaceTool, args); err != nil {
		return nil, err
	}
	result, err := s.invoke(ctx, spaceTool, args)
	if err != nil {
		return nil, err
	}
	var space struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		BaseURL string `json:"baseUrl"`
		WebURL  string `json:"webUrl"`
	}
	if json.Unmarshal(result, &space) != nil || space.ID != page.SpaceID || strings.TrimSpace(space.Key) == "" ||
		!sameSiteURL(s.site, space.BaseURL) || !sameSiteURL(s.site, space.WebURL) {
		return nil, fmt.Errorf("%w: Rovo space lookup did not verify the page's space and site", nativemap.ErrInvalidResult)
	}

	// Preserve the other reported identifiers and URLs for the canonical
	// decoder, which rejects any that disagree with the requested target.
	fields["spaceKey"], err = json.Marshal(space.Key)
	if err != nil {
		return nil, err
	}
	fields["version"], err = json.Marshal(page.Metadata.Version)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(fields)
	if err == nil {
		s.verifiedSnapshot = page.SnapshotToken
	}
	return canonical, err
}

func sameSiteURL(site, raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && strings.EqualFold(site, "https://"+u.Host)
}

func (s *session) historyResult(raw json.RawMessage) (json.RawMessage, error) {
	var result struct {
		OK          bool   `json:"ok"`
		ContentType string `json:"contentType"`
		ID          string `json:"id"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.OK || result.ContentType != "page" || result.ID != "" && result.ID != s.pageID {
		return nil, fmt.Errorf("%w: Rovo history did not identify a successful page revision", nativemap.ErrInvalidResult)
	}
	// The flat result omits contentId. The exact page and version were
	// supplied to the named history tool on the bound session. DecodeVersion
	// checks any reported contentId, version number and version message.
	return raw, nil
}
