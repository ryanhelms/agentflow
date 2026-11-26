package nativemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider"
)

var (
	decimalID          = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
	confluenceSpaceKey = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9_:-]{0,127}$`)
)

// ConfluencePage maps a published page by numeric page ID and space ID or key.
func ConfluencePage(ref provider.Ref, siteOrigin string) (Read[provider.Document], error) {
	if err := ref.Validate(); err != nil {
		return Read[provider.Document]{}, err
	}
	if !confluenceSpaceKey.MatchString(ref.Scope) || !decimalID.MatchString(ref.ID) {
		return Read[provider.Document]{}, errors.New("confluence read requires a space id or key and numeric page id")
	}
	site, err := httpsOrigin(siteOrigin)
	if err != nil {
		return Read[provider.Document]{}, err
	}
	input := struct {
		CloudID         string `json:"cloudId"`
		ContentID       string `json:"content_id"`
		ContentFormat   string `json:"content_format"`
		Detail          string `json:"detail"`
		IncludeMetadata bool   `json:"include_metadata"`
	}{site, ref.ID, "markdown", "full", true}
	return newRead(Confluence, GetDocument, ref, input, func(raw json.RawMessage) (provider.Document, error) {
		return confluenceDocument(ref, site, raw)
	})
}

type confluencePage struct {
	ID              string   `json:"id"`
	ContentID       string   `json:"contentId"`
	SpaceID         string   `json:"spaceId"`
	SpaceKey        string   `json:"spaceKey"`
	Type            string   `json:"type"`
	ContentType     string   `json:"contentType"`
	Title           string   `json:"title"`
	Status          string   `json:"status"`
	SnapshotToken   string   `json:"snapshotToken"`
	LossyConversion bool     `json:"lossyConversion"`
	LostFeatures    []string `json:"lostFeatures"`
	Version         struct {
		Number int64 `json:"number"`
	} `json:"version"`
	Metadata struct {
		SpaceID  string `json:"spaceId"`
		SpaceKey string `json:"spaceKey"`
	} `json:"metadata"`
	Body struct {
		Format          string   `json:"format"`
		Value           *string  `json:"value"`
		LossyConversion bool     `json:"lossyConversion"`
		LostFeatures    []string `json:"lostFeatures"`
	} `json:"body"`
}

func confluenceDocument(ref provider.Ref, site string, raw json.RawMessage) (provider.Document, error) {
	var page confluencePage
	if err := decodeObject(raw, &page); err != nil {
		return provider.Document{}, err
	}
	space, metadataSpace := page.SpaceID, page.Metadata.SpaceID
	if !decimalID.MatchString(ref.Scope) {
		space, metadataSpace = page.SpaceKey, page.Metadata.SpaceKey
	}
	if changedIdentity(ref.ID, page.ID, page.ContentID) || changedIdentity(ref.Scope, space, metadataSpace) {
		return provider.Document{}, fmt.Errorf("confluence returned a different page or space: %w", provider.ErrConflict)
	}
	if (page.ID == "" && page.ContentID == "") || (space == "" && metadataSpace == "") ||
		(page.Type == "" && page.ContentType == "") || strings.TrimSpace(page.Title) == "" {
		return provider.Document{}, fmt.Errorf("%w: confluence page requires id, space, type and title", ErrInvalidResult)
	}
	if changedIdentity("page", page.Type, page.ContentType) || changedIdentity("current", page.Status) {
		return provider.Document{}, provider.Unsupported(ref.Instance, "read confluence content other than a published page")
	}
	if page.Body.Format != "markdown" || page.Body.Value == nil {
		return provider.Document{}, fmt.Errorf("%w: confluence response has no full markdown body", ErrInvalidResult)
	}
	if page.LossyConversion || page.Body.LossyConversion || len(page.LostFeatures) > 0 || len(page.Body.LostFeatures) > 0 {
		return provider.Document{}, provider.Unsupported(ref.Instance, "read confluence page with lossy markdown conversion")
	}
	revision := page.SnapshotToken
	if page.Version.Number > 0 {
		revision = strconv.FormatInt(page.Version.Number, 10)
	}
	return provider.Document{
		Ref: ref, Title: page.Title, Body: *page.Body.Value, Revision: revision,
		URL: site + "/wiki/pages/viewpage.action?pageId=" + ref.ID,
	}, nil
}

// changedIdentity allows omitted duplicate fields but requires all reported
// values to agree with the bound destination.
func changedIdentity(want string, reported ...string) bool {
	for _, value := range reported {
		if value != "" && value != want {
			return true
		}
	}
	return false
}
