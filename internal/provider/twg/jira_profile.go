package twg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type jiraProfile struct {
	ID, Name, Environment, AuthMethod, UserID string
	Site, Domain                              string
	Revision                                  int
	Sites                                     []struct{ Site string }
}

func (p jiraProfile) coversSite(origin string) bool {
	host := strings.TrimPrefix(origin, "https://")
	slug, cloud := strings.CutSuffix(host, ".atlassian.net")
	matches := func(site string) bool {
		site = strings.ToLower(strings.TrimSuffix(site, "/"))
		return site == origin || site == host || cloud && site == slug
	}
	if matches(p.Site) || matches(p.Domain) {
		return true
	}
	for _, site := range p.Sites {
		if matches(site.Site) {
			return true
		}
	}
	return false
}

func (j *Jira) jiraProfile(ctx context.Context) (jiraProfile, string, error) {
	// The vendor command only returns profile identities, never their secrets.
	var catalog struct {
		Storage, ActiveEnvironment string
		Defaults                   map[string]string
		Profiles                   []jiraProfile
	}
	if err := j.read(ctx, "select jira OAuth profile", []string{"auth", "profile", "list"}, &catalog); err != nil {
		return jiraProfile{}, "", err
	}
	if catalog.Profiles == nil || catalog.ActiveEnvironment == "" {
		return jiraProfile{}, "", fmt.Errorf("twg omitted its profile catalog: %w", ErrFailure)
	}
	defaultID := catalog.Defaults[catalog.ActiveEnvironment]
	var candidates []jiraProfile
	for _, profile := range catalog.Profiles {
		if profile.Environment != catalog.ActiveEnvironment || profile.AuthMethod != "oauth" || !profile.coversSite(j.site) {
			continue
		}
		selector := profile.Name
		if catalog.Storage == "legacy" {
			// Before profile migration the CLI only accepts the default alias.
			if profile.ID != defaultID {
				continue
			}
			selector = "default"
		}
		if profile.ID == "" || selector == "" || profile.UserID == "" {
			return jiraProfile{}, "", fmt.Errorf("twg OAuth profile omitted its login identity: %w", ErrFailure)
		}
		if profile.ID == defaultID {
			return profile, selector, nil
		}
		candidates = append(candidates, profile)
	}
	switch len(candidates) {
	case 0:
		return jiraProfile{}, "", ErrAuthRequired
	case 1:
		return candidates[0], candidates[0].Name, nil
	default:
		return jiraProfile{}, "", fmt.Errorf("multiple twg OAuth profiles cover the configured Jira site: %w", ErrAccessDenied)
	}
}

func (p jiraProfile) intent(selector string) map[string]string {
	return map[string]string{"profile": p.Name, "profile_id": p.ID, "account_id": p.UserID,
		"profile_selector": selector, "profile_revision": strconv.Itoa(p.Revision)}
}
