package slackoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
)

const (
	service = "devflow.slackoauth.v1"
	account = Resource
	// One atomic OS credential replacement. Fits Windows Credential Manager
	// and go-keyring's encoded interactive command on macOS.
	maxRecord = 2560
)

type grant struct {
	App        credentials `json:"app"`
	Generation string      `json:"generation"`
	User       string      `json:"user"`
	Team       string      `json:"team"`
	AppID      string      `json:"app_id,omitempty"`
	Scope      string      `json:"scope"`
	Access     string      `json:"access"`
	Refresh    string      `json:"refresh,omitempty"`
	Expires    int64       `json:"expires,omitempty"`
}

func (g grant) valid() bool {
	return g.App.valid() && nativeoauth.Opaque(g.Generation) && userID.MatchString(g.User) && teamID.MatchString(g.Team) &&
		(g.AppID == "" || appID.MatchString(g.AppID)) && g.Scope == scopes && bearer.MatchString(g.Access) &&
		((g.Refresh == "" && g.Expires == 0) || (nativeoauth.Opaque(g.Refresh) && g.Expires > 0))
}

func (e *enrollment) put(raw string) error {
	if len(raw) > maxRecord {
		return errors.New("the Slack enrollment exceeds the OS credential record size limit")
	}
	if e.set(service, account, raw) != nil {
		return errStorage
	}
	verify, err := e.get(service, account)
	if err != nil || verify != raw {
		return errStorage
	}
	return nil
}

func (e *enrollment) save(g grant) error {
	if !g.valid() {
		return errToken
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return errStorage
	}
	return e.put(string(raw))
}

// BoundToken is an internal snapshot for discovery and preparation. Final
// dispatch must use WithBoundToken so logout or reconsent cannot race it.
func BoundToken(ctx context.Context) (accessToken, generation string, err error) {
	return local.token(ctx, "", nil)
}

// WithBoundToken runs one dispatch on the expected enrollment while holding the
// same lock used to rotate, replace or retire it. The callback must not reenter.
func WithBoundToken(ctx context.Context, expectedGeneration string, dispatch func(string) error) error {
	if expectedGeneration == "" || dispatch == nil {
		return ErrGrantChanged
	}
	_, _, err := local.token(ctx, expectedGeneration, dispatch)
	return err
}

func (e *enrollment) token(ctx context.Context, expected string, dispatch func(string) error) (string, string, error) {
	lock, err := nativeoauth.Lock(ctx, e.lockDir)
	if err != nil {
		return "", "", err
	}
	defer lock.Close()
	raw, err := e.get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		if expected != "" {
			return "", "", ErrGrantChanged
		}
		return "", "", ErrNotEnrolled
	}
	if err != nil {
		return "", "", errStorage
	}
	var g grant
	if len(raw) > maxRecord || json.Unmarshal([]byte(raw), &g) != nil || !g.valid() {
		return "", "", ErrLoginRequired
	}
	if expected != "" && expected != g.Generation {
		return "", "", ErrGrantChanged
	}
	if g.Expires != 0 && time.Until(time.Unix(g.Expires, 0)) <= 30*time.Second {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		// A lost response or process crash may have consumed the refresh
		// token remotely. Publish a spent marker before its single exchange.
		if err := e.put("{}"); err != nil {
			return "", "", err
		}
		if err := e.exchange(ctx, &g, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}}); err != nil {
			return "", "", errors.Join(ErrLoginRequired, err)
		}
		if err := e.save(g); err != nil {
			return "", "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if dispatch != nil {
		return "", "", dispatch(g.Access)
	}
	return g.Access, g.Generation, nil
}

// Disconnect removes the local client credentials and the user grant together.
func Disconnect(ctx context.Context) error { return local.disconnect(ctx) }

func (e *enrollment) disconnect(ctx context.Context) error {
	lock, err := nativeoauth.Lock(ctx, e.lockDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := e.remove(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errStorage
	}
	if _, err := e.get(service, account); !errors.Is(err, keyring.ErrNotFound) {
		return errStorage
	}
	return nil
}
