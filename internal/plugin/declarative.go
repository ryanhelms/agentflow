package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// MaxTemplateBytes caps one rendered template string.
const MaxTemplateBytes = 64 << 10

func init() {
	registerTier(manifest.TierDeclarative, func(h *Host) Factory { return declarativeFactory{now: h.o.Now} })
}

type declarativeFactory struct{ now func() time.Time }

func (declarativeFactory) Tier() manifest.Tier { return manifest.TierDeclarative }

func (f declarativeFactory) New(p manifest.Installed) (Instance, error) {
	return &declarative{m: p.Manifest, now: f.now, done: make(chan struct{})}, nil
}

// declarative is the declarative tier's Instance: a small Interpreter of
// the manifest's handler rules. No plugin code runs. Each event is handled
// under recover and DeclarativeBudget (actions excluded); a panic is a
// crashed dead letter, and CrashLoopCount of them within CrashLoopWindow
// end the instance as failed.
type declarative struct {
	m    manifest.Manifest
	env  Env
	now  func() time.Time
	done chan struct{}

	mu      sync.Mutex
	panics  []time.Time
	err     error
	stopped bool
}

func (d *declarative) Start(ctx context.Context, env Env) error {
	d.env = env
	var on []string
	for _, h := range d.m.Extensions.Handler {
		for _, p := range h.On {
			if !contains(on, p) {
				on = append(on, p)
			}
		}
	}
	_, err := env.Subscribe(ctx, on)
	return err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (d *declarative) Settings(context.Context, map[string]any) error { return nil }

func (d *declarative) Stop(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	return nil
}

func (d *declarative) Done() <-chan struct{} { return d.done }

func (d *declarative) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// plannedAction is an action rendered from its templates, ready to run.
type plannedAction struct {
	invoke   *InvokeRequest
	annotate *PublishRequest
}

// Deliver runs every handler whose on matches e.
func (d *declarative) Deliver(ctx context.Context, e bus.Event) error {
	if e.Type == bus.TypeStreamOverflow {
		return nil
	}
	plans, err := d.plan(e)
	if err != nil {
		return err
	}
	for _, actions := range plans {
		for _, a := range actions {
			if err := d.run(ctx, a); err != nil {
				return coded(err, ReasonAction, false)
			}
		}
	}
	return nil
}

// plan evaluates conditions and renders actions under recover and the
// budget.
func (d *declarative) plan(e bus.Event) (plans [][]plannedAction, err error) {
	start := d.now()
	defer func() {
		if r := recover(); r != nil {
			plans, err = nil, d.crashed()
		}
	}()
	doc, err := eventDoc(e)
	if err != nil {
		return nil, coded(err, ReasonAction, false)
	}
	settings := d.env.Settings()
	for _, h := range d.m.Extensions.Handler {
		if !matchesOn(h.On, e.Type) || !conditionsHold(h.When, doc) {
			continue
		}
		var actions []plannedAction
		for _, a := range h.Actions {
			pa, err := render(a, e, doc, settings)
			if err != nil {
				return nil, coded(err, ReasonAction, false)
			}
			actions = append(actions, pa)
		}
		plans = append(plans, actions)
		if d.now().Sub(start) > DeclarativeBudget {
			return nil, coded(errors.New("declarative budget exceeded"), bus.PluginCodeTimeout, false)
		}
	}
	if d.now().Sub(start) > DeclarativeBudget {
		return nil, coded(errors.New("declarative budget exceeded"), bus.PluginCodeTimeout, false)
	}
	return plans, nil
}

// crashed records a panic and ends the instance after too many.
func (d *declarative) crashed() error {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.panics = append(d.panics, now)
	d.panics = slices.DeleteFunc(d.panics, func(t time.Time) bool { return now.Sub(t) >= CrashLoopWindow })
	err := coded(errors.New("declarative handler panicked"), bus.PluginCodeCrashed, false)
	if len(d.panics) >= CrashLoopCount && d.err == nil {
		d.err = coded(errors.New("declarative handler keeps panicking"), bus.PluginCodeCrashed, true)
		close(d.done)
	}
	return err
}

func (d *declarative) run(ctx context.Context, a plannedAction) error {
	if a.invoke != nil {
		res, err := d.env.Invoke(ctx, *a.invoke)
		if err != nil {
			return err
		}
		if res.Outcome == string(command.OutcomeFailed) {
			return fmt.Errorf("invoke %s failed", a.invoke.Command)
		}
		return nil
	}
	_, err := d.env.Publish(ctx, *a.annotate)
	return err
}

func matchesOn(on []string, t bus.Type) bool {
	for _, p := range on {
		if bus.Pattern(p).Match(t) {
			return true
		}
	}
	return false
}

// eventDoc is e's envelope as JSON values, for field paths.
func eventDoc(e bus.Event) (map[string]any, error) {
	doc := map[string]any{"type": string(e.Type), "repo": string(e.Repo), "worktree": string(e.Worktree), "source": e.Source,
		"correlation_id": e.CorrelationID}
	if len(e.Payload) > 0 {
		var p any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, err
		}
		doc["payload"] = p
	}
	return doc, nil
}

// field returns the value at a dot path, nil when missing.
func field(doc map[string]any, path string) any {
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	if s, ok := cur.(string); ok && s == "" && !strings.HasPrefix(path, "payload.") {
		return nil // an empty envelope field is missing
	}
	return cur
}

// conditionsHold evaluates every condition (Message Filter).
func conditionsHold(when []manifest.Condition, doc map[string]any) bool {
	for _, c := range when {
		v := field(doc, c.Field)
		switch {
		case c.Exists != nil:
			if (v != nil) != *c.Exists {
				return false
			}
		case c.In != nil:
			found := false
			for _, x := range c.In {
				if equalJSON(v, x) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case c.NotEquals != nil:
			if equalJSON(v, c.NotEquals) {
				return false
			}
		default:
			if !equalJSON(v, c.Equals) {
				return false
			}
		}
	}
	return true
}

// equalJSON compares a and b as JSON values (YAML integers equal JSON
// numbers).
func equalJSON(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func normalize(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return v
	}
	return out
}

// render turns an action's templates into a request.
func render(a manifest.Action, e bus.Event, doc map[string]any, settings map[string]any) (plannedAction, error) {
	if a.Invoke != nil {
		input, err := renderValue(a.Invoke.Input, doc, settings)
		if err != nil {
			return plannedAction{}, err
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return plannedAction{}, err
		}
		return plannedAction{invoke: &InvokeRequest{Command: a.Invoke.Command, Input: raw, Cause: e.ID}}, nil
	}
	if a.Annotate == nil {
		return plannedAction{}, errors.New("an action without a kind")
	}
	text, err := renderString(a.Annotate.Text, doc, settings, true)
	if err != nil {
		return plannedAction{}, err
	}
	link, err := renderString(a.Annotate.URL, doc, settings, true)
	if err != nil {
		return plannedAction{}, err
	}
	raw, err := json.Marshal(bus.PluginAnnotationAdded{OperationID: e.CorrelationID, Text: text.(string), URL: link.(string), Level: a.Annotate.Level})
	if err != nil {
		return plannedAction{}, err
	}
	return plannedAction{annotate: &PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: raw, Cause: e.ID}}, nil
}

func renderValue(v any, doc, settings map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		return renderString(x, doc, settings, false)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			r, err := renderValue(item, doc, settings)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			r, err := renderValue(item, doc, settings)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// renderString renders one template string. A string that is exactly one
// placeholder keeps the value's JSON type unless asText.
func renderString(s string, doc, settings map[string]any, asText bool) (any, error) {
	if s == "" {
		return "", nil
	}
	if !asText && strings.HasPrefix(s, manifest.TemplateOpen) && strings.HasSuffix(s, manifest.TemplateClose) &&
		strings.Count(s, manifest.TemplateOpen) == 1 {
		v, err := placeholder(strings.TrimSpace(s[2:len(s)-2]), doc, settings)
		if err != nil {
			return nil, err
		}
		if str, ok := v.(string); ok && len(str) > MaxTemplateBytes {
			return nil, errors.New("rendered template too long")
		}
		return v, nil
	}
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, manifest.TemplateOpen)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		j := strings.Index(rest[i:], manifest.TemplateClose)
		if j < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		v, err := placeholder(strings.TrimSpace(rest[i+2:i+j]), doc, settings)
		if err != nil {
			return nil, err
		}
		b.WriteString(text(v))
		if b.Len() > MaxTemplateBytes {
			return nil, errors.New("rendered template too long")
		}
		rest = rest[i+j+2:]
	}
	return b.String(), nil
}

func placeholder(name string, doc, settings map[string]any) (any, error) {
	switch {
	case strings.HasPrefix(name, "event."):
		return field(doc, strings.TrimPrefix(name, "event.")), nil
	case strings.HasPrefix(name, "settings."):
		return normalize(settings[strings.TrimPrefix(name, "settings.")]), nil
	}
	return nil, fmt.Errorf("unknown placeholder")
}

// text is v inserted into a string: a list as comma-separated text.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = text(item)
		}
		return strings.Join(parts, ", ")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
