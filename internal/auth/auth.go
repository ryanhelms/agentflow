// Package auth stores and loads the developer's own credentials. Atlassian
// credentials live in the OS keychain; GitHub reuses `gh auth token`.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/nscaledev/devflow/internal/managedgh"
)

const service = "devflow"

// Atlassian is a site credential. Token is an API token (basic auth) or an
// OAuth access token when Bearer is true.
type Atlassian struct {
	Site   string `json:"site"`
	Email  string `json:"email"`
	Token  string `json:"token"`
	Bearer bool   `json:"bearer,omitempty"`
}

// ErrNoCredentials is returned when the developer has not run `devflow login`.
var ErrNoCredentials = errors.New("no Atlassian credentials: run `devflow login`")

// SaveAtlassian stores a credential for its site in the OS keychain.
func SaveAtlassian(c Atlassian) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return keyring.Set(service, "atlassian:"+c.Site, string(b))
}

// LoadAtlassian returns the credential for site. JIRA_USER/JIRA_TOKEN env vars
// take precedence (used in CI and for testing).
func LoadAtlassian(site string) (Atlassian, error) {
	if u, t := os.Getenv("JIRA_USER"), os.Getenv("JIRA_TOKEN"); u != "" && t != "" {
		return Atlassian{Site: site, Email: u, Token: t}, nil
	}
	s, err := keyring.Get(service, "atlassian:"+site)
	if errors.Is(err, keyring.ErrNotFound) {
		return Atlassian{}, ErrNoCredentials
	}
	if err != nil {
		return Atlassian{}, fmt.Errorf("keychain: %w", err)
	}
	var c Atlassian
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return Atlassian{}, err
	}
	return c, nil
}

// DeleteAtlassian removes a stored credential.
func DeleteAtlassian(site string) error {
	err := keyring.Delete(service, "atlassian:"+site)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// GitHubToken returns GH_TOKEN/GITHUB_TOKEN or the token from the gh CLI.
// For provider operations, repo names the workspace for managed gh resolution.
func GitHubToken(ctx context.Context, repo string) (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := os.Getenv(k); t != "" {
			return t, nil
		}
	}
	command := "gh"
	if repo != "" {
		selection, err := managedgh.Inspect(ctx, repo)
		if err != nil {
			return "", err
		}
		if selection.Command == "" {
			return "", errors.New(selection.Detail)
		}
		command = selection.Command
	}
	cmd := exec.CommandContext(ctx, command, "auth", "token")
	cmd.Dir = repo
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("GitHub auth: run `gh auth login` (%s)", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
