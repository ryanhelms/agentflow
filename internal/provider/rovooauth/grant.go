package rovooauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"
)

// Each OS user has a single active grant for this issuer, resource and Jira scope profile.
const (
	service   = "devflow.rovooauth.v2.jira.1"
	account   = issuer + " " + Resource
	maxRecord = 2560 // Windows Credential Manager's blob limit.
)

type grant struct {
	Generation string `json:"generation"`
	ClientID   string `json:"client_id"`
	Refresh    string `json:"refresh_token"`
}

type accessGrant struct {
	generation string
	token      string
	expiry     time.Time
}

func userLockDir() (string, error) {
	// Every process using this OS user's credential store must share the lock,
	// regardless of checkout, DEVFLOW_HOME or XDG configuration.
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		return "", errLock
	}
	return filepath.Join(u.HomeDir, ".devflow-rovo"), nil
}

func (e *enrollment) lock(ctx context.Context) (*os.File, error) {
	dir, err := e.lockDir()
	if err != nil || os.MkdirAll(dir, 0700) != nil {
		return nil, errLock
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errLock
	}
	f, err := os.OpenFile(filepath.Join(dir, "grant.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errLock
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, errors.Join(errLock, err)
		}
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, errLock
		}
		if ok {
			return f, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (e *enrollment) disconnect(ctx context.Context) error {
	lock, err := e.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	e.cached.Store(nil)
	head, err := e.currentHead(false)
	if errors.Is(err, ErrNotEnrolled) {
		return nil
	}
	if err != nil {
		return err
	}
	// Verify the spent marker and finish journaled chunk retirement before
	// removing the primary entry. Only a clean absence means unenrolled.
	if head != "{}" {
		if err := e.replace(head, "{}"); err != nil {
			return err
		}
	}
	return e.discard(account)
}

func (e *enrollment) boundToken(ctx context.Context) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock, err := e.lock(ctx)
	if err != nil {
		return "", "", err
	}
	defer lock.Close()
	head, err := e.currentHead(false)
	if err != nil {
		return "", "", err
	}
	g, err := e.readGrant(head)
	if err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if a := e.cached.Load(); a != nil && a.generation == g.Generation && time.Until(a.expiry) > 30*time.Second {
		return a.token, g.Generation, nil
	}
	// Spend before exchange. A crash or ambiguous response may have rotated
	// the remote refresh token. Subsequent processes must enroll again.
	if err := e.replace(head, "{}"); err != nil {
		return "", "", err
	}
	t, err := e.exchange(ctx, g.ClientID, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}})
	if err != nil {
		return "", "", errors.Join(ErrLoginRequired, err)
	}
	if t.Refresh != "" {
		g.Refresh = t.Refresh
	}
	if err := e.save("{}", g, t); err != nil {
		return "", "", err
	}
	return t.Access, g.Generation, nil
}

func (e *enrollment) save(head string, g grant, t *tokenResponse) error {
	if !opaque(g.Refresh) {
		return errToken
	}
	raw, err := json.Marshal(g) //nolint:gosec // This private record goes only to the OS keyring.
	if err != nil {
		return errToken
	}
	if err := e.replace(head, string(raw)); err != nil {
		return err
	}
	e.cached.Store(&accessGrant{generation: g.Generation, token: t.Access, expiry: t.expiry})
	return nil
}
