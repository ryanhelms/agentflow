package twg

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/nscaledev/devflow/internal/provider"
)

var (
	siteHost     = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)
	jiraProject  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	jiraIssueKey = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*-[1-9][0-9]*$`)
	spaceKey     = regexp.MustCompile(`^~?[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	pageID       = regexp.MustCompile(`^[1-9][0-9]*$`)
	queryEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

type binding struct {
	instance string
	site     string
	scopes   []string
	valid    *regexp.Regexp
}

func newBinding(instance, site string, scopes []string, valid *regexp.Regexp) (binding, error) {
	ref := provider.Ref{Instance: instance, Scope: "scope", ID: "id"}
	if err := ref.Validate(); err != nil {
		return binding{}, err
	}
	u, err := url.Parse(site)
	if err != nil || u.Scheme != "https" || !siteHost.MatchString(u.Host) || u.User != nil ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") || u.ForceQuery || u.RawQuery != "" || strings.Contains(site, "#") {
		return binding{}, errors.New("twg requires an https site origin without credentials, port, path, query or fragment")
	}
	for _, scope := range scopes {
		if !valid.MatchString(scope) {
			return binding{}, errors.New("invalid configured twg scope")
		}
	}
	return binding{instance: instance, site: "https://" + strings.ToLower(u.Host), scopes: slices.Clone(scopes), valid: valid}, nil
}

func (b binding) ref(scope, id string) provider.Ref {
	return provider.Ref{Instance: b.instance, Scope: scope, ID: id}
}

func (b binding) checkRef(ref provider.Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Instance != b.instance {
		return fmt.Errorf("reference belongs to a different provider instance: %w", ErrAccessDenied)
	}
	_, err := b.queryScopes(ref.Scope)
	return err
}

func (b binding) queryScopes(scope string) ([]string, error) {
	if scope == "" {
		return b.scopes, nil
	}
	if !b.valid.MatchString(scope) || (len(b.scopes) > 0 && !slices.Contains(b.scopes, scope)) {
		return nil, fmt.Errorf("scope is outside the twg provider binding: %w", ErrAccessDenied)
	}
	return []string{scope}, nil
}

func (b binding) sameSiteURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" ||
		(u.IsAbs() && (u.Scheme != "https" || !strings.EqualFold("https://"+u.Host, b.site))) ||
		(!u.IsAbs() && (u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//"))) {
		return nil, fmt.Errorf("result url does not belong to the configured site: %w", ErrFailure)
	}
	return u, nil
}

func queryLiteral(value string) string {
	return `"` + queryEscaper.Replace(value) + `"`
}

func scopeClause(field string, scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	if len(scopes) == 1 {
		return field + " = " + queryLiteral(scopes[0])
	}
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		values = append(values, queryLiteral(scope))
	}
	return field + " in (" + strings.Join(values, ", ") + ")"
}

func textFilter(query, field, text string) (string, error) {
	if strings.ContainsFunc(text, unicode.IsControl) {
		return "", errors.New("twg query text contains a control character")
	}
	if text != "" {
		query = andFilter(query, field+" ~ "+queryLiteral(text))
	}
	return query, nil
}

func andFilter(query, filter string) string {
	if query == "" {
		return filter
	}
	if filter == "" {
		return query
	}
	return query + " AND " + filter
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func queryLimit(value, fallback, ceiling int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, ceiling)
}
