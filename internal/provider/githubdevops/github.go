// Package githubdevops implements the GitHub checks, deployments and releases
// capabilities using an injected GitHub client.
package githubdevops

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/provider"
)

const instance = "github"

// Adapter uses the API host and credentials of its client for all repositories.
type Adapter struct {
	client *gh.Client
}

var (
	_ provider.Checks      = (*Adapter)(nil)
	_ provider.Deployments = (*Adapter)(nil)
	_ provider.Releases    = (*Adapter)(nil)
)

// New creates an adapter without discovering credentials or a Git remote.
func New(client *gh.Client) *Adapter {
	return &Adapter{client: client}
}

func repositoryPath(repo provider.RepositoryRef) (string, error) {
	owner, name, ok := strings.Cut(repo.Path, "/")
	if !ok || !validSegment(owner) || !validSegment(name) || strings.Contains(name, "/") {
		return "", fmt.Errorf("invalid github repository %q, want owner/name", repo.Path)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func validSegment(value string) bool {
	return strings.TrimSpace(value) != "" && value != "." && value != ".."
}

// The GitHub client currently exposes HTTP status codes in its error prefix.
func githubReadError(err error, path string) error {
	if err != nil && strings.HasPrefix(err.Error(), "github GET "+path+": HTTP 404:") {
		return errors.Join(provider.ErrNotFound, err)
	}
	return err
}
