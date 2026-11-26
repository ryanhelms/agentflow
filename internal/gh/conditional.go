package gh

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/watch"
)

// GetConditional performs a GET of path, sending If-None-Match when etag is
// not empty, and decodes a 200 response into out. It works the same through
// the HTTP and the gh CLI backends, so it authenticates like every other
// call. The returned meta reports the response's ETag and the headers a
// poller must honour: the primary rate limit, X-Poll-Interval and
// Retry-After.
//
//   - A 304 returns meta.NotModified with a nil error and leaves out
//     untouched. meta.ETag is then the response's ETag, or etag when GitHub
//     sent none.
//   - A failed response returns the error together with the meta of its
//     headers. A rate-limited one matches ErrRateLimited, and
//     meta.RetryAfter and meta.RateLimit say how long to wait.
//   - A failure before any response arrived returns a zero meta.
//
// A 304 does not count against GitHub's primary rate limit for
// authenticated requests.
func (c *Client) GetConditional(ctx context.Context, path, etag string, out any) (watch.GitHubMeta, error) {
	var hdr map[string]string
	if etag != "" {
		hdr = map[string]string{"If-None-Match": etag}
	}
	h, err := c.do(ctx, http.MethodGet, path, nil, out, hdr)
	meta := conditionalMeta(h, time.Now())
	if errors.Is(err, ErrNotModified) {
		meta.NotModified = true
		if meta.ETag == "" {
			meta.ETag = etag
		}
		return meta, nil
	}
	return meta, err
}

// conditionalMeta reads the headers GetConditional reports. h may be nil.
func conditionalMeta(h http.Header, now time.Time) watch.GitHubMeta {
	var m watch.GitHubMeta
	if h == nil {
		return m
	}
	m.ETag = h.Get("ETag")
	m.RateLimit.Limit = headerInt(h, "X-RateLimit-Limit")
	m.RateLimit.Remaining = headerInt(h, "X-RateLimit-Remaining")
	if reset := headerInt(h, "X-RateLimit-Reset"); reset > 0 {
		m.RateLimit.Reset = time.Unix(int64(reset), 0)
	}
	if secs := headerInt(h, "X-Poll-Interval"); secs > 0 {
		m.PollInterval = time.Duration(secs) * time.Second
	}
	m.RetryAfter = retryAfter(h.Get("Retry-After"), now)
	return m
}

func headerInt(h http.Header, name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get(name)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// retryAfter parses a Retry-After value: delay seconds or an HTTP date.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return max(0, time.Duration(secs)*time.Second)
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(0, at.Sub(now))
	}
	return 0
}

// Classify sorts an error from a GitHub request into the kinds the GitHub
// poller treats differently (watch.GitHubOptions.Classify).
func Classify(err error) watch.GitHubFailure {
	switch {
	case err == nil:
		return watch.FailureOther
	case errors.Is(err, ErrRateLimited):
		return watch.FailureRateLimited
	case errors.Is(err, ErrAuthentication), errors.Is(err, ErrPermission), errors.Is(err, ErrCLIUnavailable):
		return watch.FailureAuth
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
		return watch.FailureNotFound
	}
	return watch.FailureOther
}
