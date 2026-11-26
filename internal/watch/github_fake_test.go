package watch_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/gh"
)

// ---- fake clock ----

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	created int
	last    time.Duration
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Timer(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created++
	c.last = d
	tm := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		tm.ch <- c.now
		return tm.ch, func() {}
	}
	c.timers = append(c.timers, tm)
	return tm.ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, t := range c.timers {
			if t == tm {
				c.timers = append(c.timers[:i], c.timers[i+1:]...)
				return
			}
		}
	}
}

// Advance moves time forward and fires every timer that is due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	rest := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
			continue
		}
		rest = append(rest, t)
	}
	c.timers = rest
}

func (c *fakeClock) stats() (created int, last time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created, c.last
}

// ---- recording publisher ----

type recorder struct {
	t      *testing.T
	mu     sync.Mutex
	events []bus.Event
	health []bus.Event // gh.health.changed, kept apart from the data events
}

// Publish checks e against the envelope rules the real bus applies, then
// records it.
func (r *recorder) Publish(_ context.Context, e bus.Event) (bus.Event, error) {
	e = e.Normalize(time.Now())
	if err := e.Validate(); err != nil {
		r.t.Errorf("invalid %s event: %v", e.Type, err)
		return e, err
	}
	if len(e.Payload) > 16<<10 {
		r.t.Errorf("%s payload is %d bytes", e.Type, len(e.Payload))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Type == bus.TypeGHHealthChanged {
		r.health = append(r.health, e)
		return e, nil
	}
	r.events = append(r.events, e)
	return e, nil
}

// take returns the data events (all but gh.health.changed) since the last
// call.
func (r *recorder) take() []bus.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

// takeHealth returns the gh.health.changed payloads since the last call.
func (r *recorder) takeHealth() []bus.GHHealthChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []bus.GHHealthChanged
	for _, e := range r.health {
		var p bus.GHHealthChanged
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			r.t.Errorf("health payload: %v", err)
		}
		if e.Worktree != "" || !e.Time.Equal(e.Time.UTC()) {
			r.t.Errorf("health event %+v", e)
		}
		out = append(out, p)
	}
	r.health = nil
	return out
}

// ---- fake GitHub ----

// fakeGitHub is a small GitHub REST and GraphQL server with ETag support. It
// logs every request as "<route> <status>", so a test can count the requests
// of one poll cycle.
type fakeGitHub struct {
	t    *testing.T
	slug string
	srv  *httptest.Server

	mu       sync.Mutex
	pulls    []map[string]any            // open pull requests, most recently updated first
	closed   map[string][]map[string]any // closed lookup by head label
	runs     map[string][]map[string]any // check runs by SHA
	statuses map[string][]map[string]any // commit statuses by SHA
	issues   map[int]map[string]any
	graphql  map[int][2]string // review decision and merge state by pull request
	gqlFail  bool
	fail     map[string]int // route -> status to answer with
	headers  map[string]string
	failHdr  map[string]string
	log      []string
}

func newFakeGitHub(t *testing.T, slug string) *fakeGitHub {
	f := &fakeGitHub{
		t: t, slug: slug,
		closed:   map[string][]map[string]any{},
		runs:     map[string][]map[string]any{},
		statuses: map[string][]map[string]any{},
		issues:   map[int]map[string]any{},
		graphql:  map[int][2]string{},
		fail:     map[string]int{},
		headers:  map[string]string{},
		failHdr:  map[string]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// client returns a real gh client pointed at the fake.
func (f *fakeGitHub) client() *gh.Client {
	c := gh.New("test-token")
	c.API = f.srv.URL
	return c
}

var (
	reCheckRuns = regexp.MustCompile(`^/repos/[^/]+/[^/]+/commits/([0-9a-f]+)/check-runs$`)
	reStatus    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/commits/([0-9a-f]+)/status$`)
	reIssue     = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/(\d+)$`)
	reAlias     = regexp.MustCompile(`p(\d+):pullRequest`)
)

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	q := r.URL.Query()
	var key string
	var body any
	switch {
	case r.Method == http.MethodPost && path == "/graphql":
		key = "graphql"
	case path == "/repos/"+f.slug+"/pulls" && q.Get("state") == "open":
		key, body = "pulls", f.pulls
		if body.([]map[string]any) == nil {
			body = []map[string]any{}
		}
	case path == "/repos/"+f.slug+"/pulls" && q.Get("state") == "closed":
		label := q.Get("head")
		key, body = "closed:"+label, f.closed[label]
		if body.([]map[string]any) == nil {
			body = []map[string]any{}
		}
	case reCheckRuns.MatchString(path):
		sha := reCheckRuns.FindStringSubmatch(path)[1]
		key, body = "runs:"+sha[:4], map[string]any{"total_count": len(f.runs[sha]), "check_runs": orEmpty(f.runs[sha])}
	case reStatus.MatchString(path):
		sha := reStatus.FindStringSubmatch(path)[1]
		key, body = "status:"+sha[:4], map[string]any{"state": "pending", "statuses": orEmpty(f.statuses[sha])}
	case reIssue.MatchString(path):
		n, _ := strconv.Atoi(reIssue.FindStringSubmatch(path)[1])
		key = "issue:" + strconv.Itoa(n)
		issue, ok := f.issues[n]
		if !ok {
			f.respond(w, key, http.StatusNotFound, f.headers, []byte(`{"message":"Not Found"}`))
			return
		}
		body = issue
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
		return
	}
	if status, ok := f.fail[key]; ok && status != 0 {
		hdr := map[string]string{}
		for k, v := range f.headers {
			hdr[k] = v
		}
		for k, v := range f.failHdr {
			hdr[k] = v
		}
		f.respond(w, key, status, hdr, []byte(`{"message":"fixture failure"}`))
		return
	}
	if key == "graphql" {
		f.serveGraphQL(w, r)
		return
	}
	data, _ := json.Marshal(body)
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))[:14] + `"`
	hdr := map[string]string{"ETag": etag}
	for k, v := range f.headers {
		hdr[k] = v
	}
	if r.Header.Get("If-None-Match") == etag {
		f.respond(w, key, http.StatusNotModified, hdr, nil)
		return
	}
	f.respond(w, key, http.StatusOK, hdr, data)
}

func orEmpty(in []map[string]any) []map[string]any {
	if in == nil {
		return []map[string]any{}
	}
	return in
}

func (f *fakeGitHub) serveGraphQL(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(raw, &req)
	var nums []string
	nodes := map[string]any{}
	for _, m := range reAlias.FindAllStringSubmatch(req.Query, -1) {
		nums = append(nums, m[1])
		n, _ := strconv.Atoi(m[1])
		if v, ok := f.graphql[n]; ok {
			node := map[string]any{"reviewDecision": nil, "mergeStateStatus": v[1]}
			if v[0] != "" {
				node["reviewDecision"] = v[0]
			}
			nodes["p"+m[1]] = node
		} else {
			nodes["p"+m[1]] = nil
		}
	}
	sort.Strings(nums)
	key := "graphql:" + strings.Join(nums, ",")
	if f.gqlFail {
		f.respond(w, key, http.StatusOK, nil, []byte(`{"data":null,"errors":[{"type":"INTERNAL","message":"boom"}]}`))
		return
	}
	data, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": nodes}})
	f.respond(w, key, http.StatusOK, nil, data)
}

func (f *fakeGitHub) respond(w http.ResponseWriter, key string, status int, hdr map[string]string, body []byte) {
	f.log = append(f.log, fmt.Sprintf("%s %d", key, status))
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// take returns the requests since the last call.
func (f *fakeGitHub) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.log
	f.log = nil
	return out
}

func (f *fakeGitHub) set(fn func(f *fakeGitHub)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// ---- fixtures ----

func sha(c string) string { return strings.Repeat(c, 40) }

func pullFixture(slug string, n int, branch, head string, updated time.Time) map[string]any {
	owner, _, _ := strings.Cut(slug, "/")
	return map[string]any{
		"number": n, "title": fmt.Sprintf("Pull %d", n), "state": "open", "draft": false,
		"html_url":   fmt.Sprintf("https://github.com/%s/pull/%d", slug, n),
		"updated_at": updated.UTC().Format(time.RFC3339),
		"merged_at":  nil,
		"user":       map[string]any{"login": "dev"},
		"head": map[string]any{
			"ref": branch, "sha": head, "label": owner + ":" + branch,
			"repo": map[string]any{"full_name": slug},
		},
		"base": map[string]any{"ref": "main"},
	}
}

func runFixture(name, status, conclusion string) map[string]any {
	r := map[string]any{"name": name, "status": status, "conclusion": nil}
	if conclusion != "" {
		r["conclusion"] = conclusion
	}
	return r
}

func issueFixture(slug string, n int, state string, updated time.Time) map[string]any {
	return map[string]any{
		"number": n, "title": fmt.Sprintf("Issue %d", n), "state": state, "state_reason": nil,
		"html_url":   fmt.Sprintf("https://github.com/%s/issues/%d", slug, n),
		"updated_at": updated.UTC().Format(time.RFC3339),
		"labels":     []map[string]any{{"name": "bug"}},
		"assignees":  []map[string]any{{"login": "dev"}},
	}
}
