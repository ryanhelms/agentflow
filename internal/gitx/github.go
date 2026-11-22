package gitx

import (
	"errors"
	"regexp"
	"strings"
)

var (
	githubOwnerPattern = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)
	githubRepoPattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)
)

// ValidateGitHubSlug checks that slug is an owner/name, suitable for use in
// public GitHub repository URLs.
func ValidateGitHubSlug(slug string) error {
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || len(owner) > 39 || !githubOwnerPattern.MatchString(owner) ||
		!githubRepoPattern.MatchString(name) || name == "." || name == ".." {
		return errors.New("expected a GitHub repository in owner/name format")
	}
	return nil
}
