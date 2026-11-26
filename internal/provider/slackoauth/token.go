package slackoauth

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/provider/nativeoauth"
)

const identityEndpoint = "https://slack.com/api/auth.test"

var (
	userID = regexp.MustCompile(`^[UW][A-Z0-9]{1,31}$`)
	teamID = regexp.MustCompile(`^T[A-Z0-9]{1,31}$`)
	appID  = regexp.MustCompile(`^A[A-Z0-9]{1,31}$`)
	bearer = regexp.MustCompile(`^[a-zA-Z0-9._~+/-]+=*$`)
)

type tokenResponse struct {
	OK         bool    `json:"ok"`
	Error      string  `json:"error"`
	Type       string  `json:"token_type"`
	Access     string  `json:"access_token"`
	Refresh    string  `json:"refresh_token"`
	ExpiresIn  *int64  `json:"expires_in"`
	Resource   *string `json:"resource"`
	Scope      *string `json:"scope"`
	AppID      string  `json:"app_id"`
	ID         string  `json:"id"`
	AuthedUser struct {
		ID    string  `json:"id"`
		Scope *string `json:"scope"`
	} `json:"authed_user"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
}

func exactScopes(s string) bool {
	// Slack documents CSV, and OAuth defines space-separated scope.
	values := strings.Split(strings.ReplaceAll(s, ",", " "), " ")
	return len(values) == len(strings.Split(scopes, ",")) && supportsScopes(values)
}

func (e *enrollment) exchange(ctx context.Context, g *grant, form url.Values) error {
	form.Set("client_id", g.App.ClientID)
	if !g.App.Public {
		form.Set("client_secret", g.App.Secret)
	}
	form.Set("resource", Resource)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return errToken
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return errToken
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || media != "application/json" {
		return errToken
	}
	const limit = 64 << 10
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	var t tokenResponse
	if err != nil || len(b) > limit || json.Unmarshal(b, &t) != nil || !t.OK || t.Error != "" ||
		!bearer.MatchString(t.Access) || (t.Resource != nil && *t.Resource != Resource) {
		return errToken
	}
	refreshing := form.Get("grant_type") == "refresh_token"
	if !refreshing && (t.Scope == nil && t.AuthedUser.Scope == nil || !teamID.MatchString(t.Team.ID)) {
		return errToken
	}
	for _, s := range []*string{t.Scope, t.AuthedUser.Scope} {
		if s != nil && !exactScopes(*s) {
			return errToken
		}
	}
	if g.Team != "" && t.Team.ID != "" && t.Team.ID != g.Team {
		if !refreshing {
			return errWrongTeam
		}
		return errToken
	}
	// Public desktop grants may use the generic OAuth bearer response. Verify
	// the user independently before accepting a grant without a typed user.
	if g.App.Public && strings.EqualFold(t.Type, "bearer") {
		identity, err := e.bearerIdentity(ctx, t.Access)
		if err != nil || (g.Team != "" && identity.Team != g.Team) || (t.Team.ID != "" && identity.Team != t.Team.ID) ||
			(t.AuthedUser.ID != "" && identity.User != t.AuthedUser.ID) {
			return errToken
		}
		t.AuthedUser.ID, t.Team.ID = identity.User, identity.Team
	} else if t.Type != "user" {
		return errToken
	}
	if !refreshing && !userID.MatchString(t.AuthedUser.ID) {
		return errToken
	}
	// A refresh can omit unchanged attributes. Any repeated identity or
	// scope must agree with the consent that supplied this refresh token.
	if t.ID != "" && ((!refreshing && t.ID != t.AuthedUser.ID) || (refreshing && t.ID != g.User)) {
		return errToken
	}
	if refreshing && ((t.AuthedUser.ID != "" && t.AuthedUser.ID != g.User) || (t.Team.ID != "" && t.Team.ID != g.Team) ||
		(t.AppID != "" && g.AppID != "" && t.AppID != g.AppID)) {
		return errToken
	}
	if !refreshing {
		g.User, g.Team = t.AuthedUser.ID, t.Team.ID
	}
	if t.AppID != "" {
		g.AppID = t.AppID
	}
	if refreshing || t.Refresh != "" || t.ExpiresIn != nil {
		if !nativeoauth.Opaque(t.Refresh) || t.Refresh == g.Refresh || t.ExpiresIn == nil || *t.ExpiresIn <= 0 || *t.ExpiresIn > 1<<31 {
			return errToken
		}
		expiry := start.Add(time.Duration(*t.ExpiresIn) * time.Second)
		if !time.Now().Before(expiry) {
			return errToken
		}
		g.Refresh, g.Expires = t.Refresh, expiry.Unix()
	}
	g.Access = t.Access
	return nil
}

type bearerIdentity struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	User  string `json:"user_id"`
	Team  string `json:"team_id"`
	Bot   string `json:"bot_id"`
}

func (e *enrollment) bearerIdentity(ctx context.Context, token string) (bearerIdentity, error) {
	var identity bearerIdentity
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, identityEndpoint, nil)
	if err != nil {
		return identity, errToken
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return identity, errToken
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || media != "application/json" {
		return identity, errToken
	}
	const limit = 64 << 10
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(b) > limit || json.Unmarshal(b, &identity) != nil || !identity.OK || identity.Error != "" ||
		identity.Bot != "" || !userID.MatchString(identity.User) || !teamID.MatchString(identity.Team) {
		return identity, errToken
	}
	return identity, nil
}
