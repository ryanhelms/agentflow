package githubissues

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
)

func TestIssueTemplateEnvelope(t *testing.T) {
	ref := provider.TaskTemplateRef{Name: "A: -->\n雪", Revision: "01"}
	// Exact wire format, including punctuation and multibyte UTF-8.
	header := "<!-- agentflow:github-issues:template:v1:413a202d2d3e0ae99baa:3031 -->"
	if got := templateMarker(ref, ""); got != header {
		t.Fatalf("header = %q", got)
	}
	for _, op := range []string{"", strings.Repeat("a", 64)} {
		marker := templateMarker(ref, op)
		for _, desc := range []string{"", " \r\nuser text\n", header, header + "\n\nlooks managed", bodyEscape + "ordinary", markerPrefix + op + markerSuffix} {
			plain, pin, err := issueBody(markedBody(desc, marker))
			if err != nil || pin == nil || *pin != ref || plain != desc {
				t.Fatalf("templated %q: plain=%q pin=%v err=%v", desc, plain, pin, err)
			}
			plain, pin, err = issueBody(markedBody(desc, ""))
			if err != nil || pin != nil || plain != desc {
				t.Fatalf("untemplated %q: plain=%q pin=%v err=%v", desc, plain, pin, err)
			}
		}
	}
	for _, desc := range []string{"text " + header, "intro\n" + header, " " + header, "```\n" + header + "\n```"} {
		plain, pin, err := issueBody(desc)
		if err != nil || plain != desc || pin != nil {
			t.Fatalf("recognized embedded marker: %q, %v, %v", plain, pin, err)
		}
	}
	legacyMarker := markerPrefix + strings.Repeat("b", 64) + markerSuffix
	if plain, pin, err := issueBody(legacyMarker + markerGap + header); err != nil || pin != nil || plain != header {
		t.Fatalf("reinterpreted legacy description: %q, %v, %v", plain, pin, err)
	}
	for _, bad := range []string{header + " extra", header + "\nuser", strings.Replace(header, "3031", "031", 1),
		strings.Replace(header, "3031", "20", 1), strings.Replace(header, "3031", "3031:zz", 1), strings.Replace(header, "3031", "ff", 1)} {
		if _, _, err := issueBody(bad); err == nil {
			t.Fatalf("accepted malformed header %q", bad)
		}
	}
}

func TestTemplateCreateReplayAndUpdate(t *testing.T) {
	ref := provider.TaskTemplateRef{Name: "work", Revision: "01"}
	desc := "## Objective\nKeep this\r\n\n"
	var saved issue
	const path = backlogPath + "/27"
	var creates, comments, patches int
	a, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET " + backlogPath:
			items := []issue{}
			if saved.Number != 0 {
				items = append(items, saved)
			}
			writeJSON(t, w, items)
		case "POST " + backlogPath:
			creates++
			var in struct {
				Title  string
				Body   string
				Labels []string
			}
			if !readJSON(t, w, r, &in) {
				return
			}
			if !slices.Equal(in.Labels, []string{"planned"}) {
				t.Errorf("create labels = %v", in.Labels)
			}
			saved = issue{Number: 27, Title: in.Title, Body: in.Body, State: "open", Labels: []issueLabel{{Name: "planned"}}}
			writeJSON(t, w, saved)
		case "GET " + path:
			writeJSON(t, w, saved)
		case "POST " + path + "/comments":
			comments++
			writeJSON(t, w, comment{ID: 1})
		case "PATCH " + path:
			var in map[string]string
			if !readJSON(t, w, r, &in) {
				return
			}
			if len(in) != 1 || in["state"] != "closed" {
				t.Errorf("unsafe patch: %v", in)
			}
			patches++
			saved.State = in["state"]
			writeJSON(t, w, saved)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	d := provider.TaskDraft{Template: &ref, Title: "Review", Description: desc, Labels: []string{"planned"}}
	opts := provider.WriteOptions{OperationID: "event"}
	result, err := a.Create(t.Context(), d, opts)
	if err != nil || result.Status != provider.Applied {
		t.Fatalf("create: %+v %v", result, err)
	}
	replay, err := a.Create(t.Context(), d, opts)
	if err != nil || replay.Status != provider.AlreadyApplied || creates != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	task, err := a.Get(t.Context(), result.Ref)
	if err != nil || task.Template == nil || *task.Template != ref || task.Description != desc || !slices.Equal(task.Labels, d.Labels) {
		t.Fatalf("get: %+v %v", task, err)
	}
	body := saved.Body
	check := provider.WriteOptions{CheckTask: func(current provider.Task) error {
		if current.Template == nil || *current.Template != ref || current.Description != desc || !slices.Equal(current.Labels, d.Labels) {
			return provider.ErrConflict
		}
		return nil
	}}
	closeIssue := func(o provider.WriteOptions) error {
		_, err := a.Transition(t.Context(), result.Ref, provider.TaskState{ID: "closed"}, o)
		return err
	}
	post := func(o provider.WriteOptions) error {
		_, err := a.Comment(t.Context(), result.Ref, "progress", o)
		return err
	}
	for _, update := range []func(provider.WriteOptions) error{post, closeIssue} {
		if err := update(provider.WriteOptions{}); err == nil {
			t.Fatal("updated templated issue without validation")
		}
		saved.Labels = nil
		if err := update(check); !errors.Is(err, provider.ErrConflict) {
			t.Fatalf("stale preflight: %v", err)
		}
		saved.Labels = []issueLabel{{Name: "planned"}}
		if err := update(check); err != nil {
			t.Fatal(err)
		}
	}
	if comments != 1 || patches != 1 || saved.Body != body {
		t.Fatalf("comments=%d patches=%d body=%q", comments, patches, saved.Body)
	}
}

func TestTemplateCreateRequiresConfirmedMarker(t *testing.T) {
	a, requests := testAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, issue{Number: 27, State: "open", Body: "user text"})
	})
	d := provider.TaskDraft{Title: "Review", Description: "user text", Template: &provider.TaskTemplateRef{Name: "work", Revision: "01"}}
	result, err := a.Create(t.Context(), d, provider.WriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not confirm") || result.Status != "" {
		t.Fatalf("missing stored pin: %+v %v", result, err)
	}
	d.Template.Revision = "\xff"
	if _, err := a.Create(t.Context(), d, provider.WriteOptions{}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid pin: %v", err)
	}
	requests("POST " + backlogPath)
}
