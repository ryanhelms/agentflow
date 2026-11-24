package asanaoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
)

const (
	service        = "agentflow.asanaoauth.v2.1"
	account        = issuer + " " + Resource
	maxRecord      = 2560 // A single atomic replacement fits Windows Credential Manager and macOS security -i.
	refreshPending = "refresh result unknown"
)

type grant struct {
	Binding
	Registration registration `json:"registration"`
	Resource     string       `json:"resource"`
	Refresh      string       `json:"refresh_token"`
}

// A caller pins the nonce together with its immutable identities. Rotating a
// refresh token leaves this value stable. Replacing any identity invalidates it.
func (g grant) binding() Binding {
	material := strings.Join([]string{issuer, g.Resource, g.Registration.ID, g.Generation, g.User, g.Workspace}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return Binding{Generation: base64.RawURLEncoding.EncodeToString(digest[:]), User: g.User, Workspace: g.Workspace}
}

type accessGrant struct {
	binding Binding
	token   string
	expiry  time.Time
}

func (e *enrollment) put(raw string) error {
	if len(raw) > maxRecord || e.set(service, account, raw) != nil {
		return errStorage
	}
	stored, err := e.get(service, account)
	if err != nil || stored != raw {
		return errStorage
	}
	return nil
}

func (e *enrollment) load(expected string) (grant, error) {
	raw, err := e.get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		if expected != "" {
			return grant{}, ErrGrantChanged
		}
		return grant{}, ErrNotEnrolled
	}
	if err != nil {
		return grant{}, errStorage
	}
	switch raw {
	case refreshPending:
		return grant{}, errors.Join(ErrTransport, errors.New("refresh outcome unknown, explicit Asana login required"))
	case "denied":
		return grant{}, ErrDenied
	case "changed":
		return grant{}, ErrGrantChanged
	case "expired":
		return grant{}, ErrLoginRequired
	}
	var g grant
	if len(raw) > maxRecord || json.Unmarshal([]byte(raw), &g) != nil || g.Resource != Resource ||
		!gid.MatchString(g.Registration.ID) || !nativeoauth.Opaque(g.Registration.Secret) || !nativeoauth.Opaque(g.Refresh) ||
		!gid.MatchString(g.User) || !gid.MatchString(g.Workspace) || !nativeoauth.Opaque(g.Generation) {
		return grant{}, ErrGrantChanged
	}
	if expected != "" && expected != g.binding().Generation {
		return grant{}, ErrGrantChanged
	}
	return g, nil
}

func BoundToken(ctx context.Context) (Binding, error) { return local.bound(ctx, "", nil) }

func WithBoundToken(ctx context.Context, generation string, dispatch func(accessToken string) error) error {
	if generation == "" || dispatch == nil {
		return ErrGrantChanged
	}
	_, err := local.bound(ctx, generation, dispatch)
	return err
}

func (e *enrollment) bound(ctx context.Context, expected string, dispatch func(string) error) (Binding, error) {
	lock, err := nativeoauth.Lock(ctx, e.lockDir)
	if err != nil {
		return Binding{}, err
	}
	defer lock.Close()
	g, err := e.load(expected)
	if err != nil {
		return Binding{}, err
	}
	a := e.cached.Load()
	if a == nil || a.binding != g.binding() || time.Until(a.expiry) <= 30*time.Second {
		if err := ctx.Err(); err != nil {
			return Binding{}, err
		}
		// Persist the uncertain outcome before sending. A process can die after
		// Asana rotates the refresh token and before we receive the replacement.
		e.cached.Store(nil)
		if err := e.put(refreshPending); err != nil {
			return Binding{}, err
		}
		if err := e.exchange(ctx, &g, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}}); err != nil {
			var marker string
			switch {
			case errors.Is(err, ErrDenied):
				marker = "denied"
			case errors.Is(err, ErrGrantChanged):
				marker = "changed"
			case errors.Is(err, ErrLoginRequired):
				marker = "expired"
			}
			if marker != "" {
				if storeErr := e.put(marker); storeErr != nil {
					return Binding{}, errors.Join(err, storeErr)
				}
			}
			return Binding{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	if dispatch != nil {
		return Binding{}, dispatch(e.cached.Load().token)
	}
	return g.binding(), nil
}

// Disconnect retires the local registration and grant under the dispatch lease.
func Disconnect(ctx context.Context) error { return local.disconnect(ctx) }

func (e *enrollment) disconnect(ctx context.Context) error {
	lock, err := nativeoauth.Lock(ctx, e.lockDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	e.cached.Store(nil)
	if err := e.remove(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errStorage
	}
	if _, err := e.get(service, account); !errors.Is(err, keyring.ErrNotFound) {
		return errStorage
	}
	return nil
}
