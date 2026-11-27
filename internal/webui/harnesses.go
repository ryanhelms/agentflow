package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// The Harnesses page's two requests (lane 6.2; ADR "Phase 6 harnesses
// projection", Requests from the page). Both sit behind the console's
// session, origin and request-header checks (server.handler) and answer
// 202 with no harness data: results arrive over the stream.
//
//   - POST /api/harnesses/check wakes the daemon source (trigger focus). It
//     starts a harness only under the daemon's own rules. At most one pass
//     every 10 s per console; more answer 202 and do nothing.
//   - POST /api/harnesses/probe runs the ai.harnesses command through an
//     Invoker with probe: true and client repository-console, which the
//     command treats as a person. At most one a minute per console (429
//     beyond). The source then runs a pass (trigger probe).

// Console limits.
const (
	harnessCheckEvery = 10 * time.Second
	harnessProbeEvery = time.Minute
	harnessProbeIDs   = 64
	// HarnessesCommand is the Spec the probe endpoint invokes (lane 6.3).
	HarnessesCommand = "ai.harnesses"
	harnessesClient  = "repository-console"
)

var harnessIDRule = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// harnessCommands is the registry the probe endpoint looks the command up
// in; tests replace it.
var harnessCommands = func() *command.Registry { return command.Default }

// harnessLimits are the per-console rate limits, by server.
var harnessLimits struct {
	mu    sync.Mutex
	check map[*server]time.Time
	probe map[*server]time.Time
}

// allowHarness reports whether a request of kind may run now, and records
// it when it may.
func allowHarness(s *server, probe bool, now time.Time) bool {
	harnessLimits.mu.Lock()
	defer harnessLimits.mu.Unlock()
	if harnessLimits.check == nil {
		harnessLimits.check, harnessLimits.probe = map[*server]time.Time{}, map[*server]time.Time{}
	}
	m, every := harnessLimits.check, harnessCheckEvery
	if probe {
		m, every = harnessLimits.probe, harnessProbeEvery
	}
	if last, ok := m[s]; ok && now.Sub(last) < every {
		return false
	}
	m[s] = now
	return true
}

var harnessNow = time.Now

func (s *server) harnessesCheck(w http.ResponseWriter, r *http.Request) {
	if !readHarnessBody(w, r, nil) {
		return
	}
	rt := currentHarnesses()
	if rt.source != nil && allowHarness(s, false, harnessNow()) {
		rt.source.Trigger(watch.HarnessTriggerFocus)
	}
	reply(w, http.StatusAccepted, struct{}{})
}

type harnessProbeRequest struct {
	IDs []string `json:"ids,omitempty"`
}

func (s *server) harnessesProbe(w http.ResponseWriter, r *http.Request) {
	var in harnessProbeRequest
	if !readHarnessBody(w, r, &in) {
		return
	}
	if len(in.IDs) > harnessProbeIDs {
		problem(w, http.StatusBadRequest, "too many harness IDs")
		return
	}
	seen := map[string]bool{}
	for _, id := range in.IDs {
		if !harnessIDRule.MatchString(id) || seen[id] {
			problem(w, http.StatusBadRequest, "invalid harness ID")
			return
		}
		seen[id] = true
	}
	rt := currentHarnesses()
	if rt.source == nil {
		problem(w, http.StatusServiceUnavailable, "harness detection is not running in this console")
		return
	}
	cmd, ok := harnessCommands().Lookup(HarnessesCommand)
	jc, isJSON := cmd.(command.JSONCommand)
	if !ok || !isJSON {
		problem(w, http.StatusServiceUnavailable, "harness probes are not available in this build")
		return
	}
	if !allowHarness(s, true, harnessNow()) {
		problem(w, http.StatusTooManyRequests, "wait a minute between probes")
		return
	}
	ids := in.IDs
	if len(ids) == 0 {
		if rep, ok := rt.source.Last(); ok {
			for _, h := range rep.Harnesses {
				if h.State != harness.StateNotInstalled {
					ids = append(ids, h.ID)
				}
			}
		}
	}
	input, _ := json.Marshal(map[string]any{"probe": true, "ids": in.IDs})
	if len(in.IDs) == 0 {
		input = []byte(`{"probe":true}`)
	}
	done := markProbing(ids)
	dir := s.repo.Dir
	ctx := rt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer rt.source.Trigger(watch.HarnessTriggerProbe)
		defer done()
		a, err := app.Open(ctx, dir, harnessesClient)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentflow: harness probe: cannot open the repository")
			return
		}
		inv := command.NewInvoker(command.Options{Auditor: command.AuditLog{}})
		res := jc.InvokeJSON(ctx, inv, a, input, "", command.Origin{Source: "daemon", Client: harnessesClient})
		if res.Err != nil && !errors.Is(res.Err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "agentflow: harness probe failed")
		}
	}()
	reply(w, http.StatusAccepted, struct{}{})
}

// readHarnessBody accepts an empty body or one JSON object. v nil means the
// object must be empty ({}).
func readHarnessBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		problem(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return true
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		problem(w, http.StatusUnsupportedMediaType, "expected application/json")
		return false
	}
	if v == nil {
		v = &struct{}{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		problem(w, http.StatusBadRequest, "expected a single request")
		return false
	}
	return true
}
