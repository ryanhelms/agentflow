package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

type confluenceFixture struct {
	title, space     string
	version, receipt atlassian.PageVersion
	responseStatus   int
	writes           int
}

func (f *confluenceFixture) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "wiki-user" || pass != "wiki-fixture" || r.URL.Host != "docs.example" {
			t.Fatalf("unexpected Confluence identity or site: %q at %s", user, r.URL)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /wiki/api/v2/pages/42":
			if r.URL.Query().Get("body-format") != "atlas_doc_format" {
				t.Fatalf("body format: %s", r.URL)
			}
			fmt.Fprintf(w, `{"id":"42","status":"current","title":%q,"spaceId":"100","ownerId":"me","parentId":"20","labels":{"results":[{"name":"runbook"}]},"version":{"number":%d,"message":%q},"body":{"atlas_doc_format":{"value":"{\"type\":\"doc\",\"content\":[{\"type\":\"text\",\"text\":\"Page text\"}]}"}}}`, f.title, f.version.Number, f.version.Message)
		case "GET /wiki/api/v2/spaces/100":
			fmt.Fprintf(w, `{"id":"100","key":%q}`, f.space)
		case "GET /wiki/api/v2/pages/42/versions/8":
			if f.receipt.Number == 0 {
				http.NotFound(w, r)
				return
			}
			if err := json.NewEncoder(w).Encode(f.receipt); err != nil {
				t.Fatal(err)
			}
		case "PUT /wiki/api/v2/pages/42":
			f.writes++
			var update struct {
				ID, Title, Status string
				Body              struct{ Representation, Value string }
				Version           atlassian.PageVersion
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields() // Moving the page or overwriting metadata is a regression.
			if err := decoder.Decode(&update); err != nil {
				t.Fatal(err)
			}
			if update.ID != "42" || update.Title != "Quota" || update.Status != "current" || update.Version.Number != f.version.Number+1 ||
				!strings.HasPrefix(update.Version.Message, "agentflow docs publish ") ||
				update.Body.Representation != "storage" || update.Body.Value != "<p># Quota<br />&lt;ac:structured-macro/&gt; &amp; limits</p>" {
				t.Fatalf("unexpected update: %+v", update)
			}
			if f.responseStatus == http.StatusConflict {
				http.Error(w, "another edit won", f.responseStatus)
				return
			}
			f.version, f.receipt, f.title = update.Version, update.Version, update.Title
			if f.responseStatus != 0 {
				http.Error(w, "lost upstream response", f.responseStatus)
				return
			}
			fmt.Fprintf(w, `{"id":"42","spaceId":"100","status":"current","title":%q,"version":{"number":%d,"message":%q}}`, f.title, f.version.Number, f.version.Message)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
	}
}

func newConfluenceFixture(t *testing.T) (*App, *confluenceFixture, DocsPublishInput) {
	t.Helper()
	f := &confluenceFixture{title: "Design", space: "ENG", version: atlassian.PageVersion{Number: 7}}
	a := legacyApp(t, f.handler(t))
	a.Cfg.Providers.TaskManagement = config.Selection{Provider: "none"}
	in := DocsPublishInput{Ref: provider.Ref{Instance: "confluence", Scope: "ENG", ID: "42"},
		Title: "Quota", Body: "# Quota\n<ac:structured-macro/> & limits", OperationID: "quota-1", ExpectedRevision: "7"}
	return a, f, in
}

func TestConfluenceLookupAndApprovedReplacement(t *testing.T) {
	ctx := t.Context()
	a, f, in := newConfluenceFixture(t)
	const pageURL = "https://docs.example/wiki/pages/viewpage.action?pageId=42"
	doc, err := a.DocsGet(ctx, in.Ref)
	if err != nil || doc.Ref != in.Ref || doc.Title != "Design" || doc.Revision != "7" || doc.Body != "Page text" || doc.URL != pageURL {
		t.Fatalf("lookup: %+v, %v", doc, err)
	}
	var tc TicketContext
	if err := a.addTaskDocuments(ctx, &tc, provider.Task{Links: []provider.Link{{Ref: &in.Ref, URL: "https://stale.example", Title: "Old"}}}); err != nil {
		t.Fatal(err)
	}
	if len(tc.Warnings) != 0 || len(tc.Docs) != 1 || tc.Docs[0].URL != pageURL || tc.Docs[0].Title != doc.Title || tc.Docs[0].Revision != doc.Revision {
		t.Fatalf("linked page: %+v, %v", tc.Docs, tc.Warnings)
	}
	a.Gate.Autonomy.Remote = config.Suggest
	v, err := a.DocsPublish(ctx, in, "")
	if err != nil || v.(*Pending).Status != "suggest" || f.writes != 0 {
		t.Fatalf("suggest: %+v, %v", v, err)
	}
	a.Gate.Autonomy.Remote = config.Confirm
	v, err = a.DocsPublish(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	summary := v.(*Pending).Summary.(map[string]any)
	if summary["site"] != "https://docs.example" || !reflect.DeepEqual(summary["document"], in) {
		t.Fatalf("approval: %+v", summary)
	}
	for _, mutate := range []func(*DocsPublishInput){
		func(i *DocsPublishInput) { i.Ref.Scope = "OTHER" },
		func(i *DocsPublishInput) { i.Ref.ID = "43" },
		func(i *DocsPublishInput) { i.ExpectedRevision = "8" },
		func(i *DocsPublishInput) { i.Title += "!" },
		func(i *DocsPublishInput) { i.Body += "!" },
		func(i *DocsPublishInput) { i.OperationID += "!" },
	} {
		changed := in
		mutate(&changed)
		if _, err := a.DocsPublish(ctx, changed, token); !errors.Is(err, policy.ErrToken) {
			t.Fatalf("retargeted approval: %+v, %v", changed, err)
		}
	}
	settings := a.Cfg.Providers.KnowledgeBase.Settings.(*config.ConfluenceSettings)
	settings.Site = "https://other.example"
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, native.ErrDenied) || f.writes != 0 {
		t.Fatalf("changed site: %v, writes %d", err, f.writes)
	}
	settings.Site = "https://docs.example"
	v, err = a.DocsPublish(ctx, in, token)
	want := provider.WriteResult{Ref: in.Ref, URL: pageURL, Revision: "8", Status: provider.Applied}
	if err != nil || v != want || f.writes != 1 {
		t.Fatalf("replacement: %+v, %v, writes %d", v, err, f.writes)
	}
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("reused confirmation: %v", err)
	}
}

func TestConfluenceReplayAfterUncertainWrite(t *testing.T) {
	ctx := t.Context()
	a, f, in := newConfluenceFixture(t)
	a.Gate.Autonomy.Remote = config.Auto
	f.responseStatus = http.StatusBadGateway
	if _, err := a.DocsPublish(ctx, in, ""); err == nil || f.writes != 1 {
		t.Fatalf("lost response: %v, writes %d", err, f.writes)
	}
	checkReplay := func() {
		t.Helper()
		v, err := a.DocsPublish(ctx, in, "")
		if err != nil || v.(provider.WriteResult).Status != provider.AlreadyApplied || v.(provider.WriteResult).Revision != "8" || f.writes != 1 {
			t.Fatalf("replay: %+v, %v, writes %d", v, err, f.writes)
		}
	}
	checkReplay()
	f.version = atlassian.PageVersion{Number: 9, Message: "later edit"}
	checkReplay()
	changed := in
	changed.Body += "!"
	if _, err := a.DocsPublish(ctx, changed, ""); !errors.Is(err, provider.ErrConflict) || f.writes != 1 {
		t.Fatalf("different body matched earlier receipt: %v", err)
	}
	f.receipt = atlassian.PageVersion{}
	if _, err := a.DocsPublish(ctx, in, ""); err == nil || !strings.Contains(err.Error(), "cannot verify earlier Confluence version 8") || f.writes != 1 {
		t.Fatalf("missing receipt: %v, writes %d", err, f.writes)
	}
}

func TestConfluenceWritePreconditions(t *testing.T) {
	ctx := t.Context()
	a, f, in := newConfluenceFixture(t)
	a.Gate.Autonomy.Remote = config.Auto
	invalid := in
	invalid.Ref.ID = "../42"
	if _, err := a.DocsPublish(ctx, invalid, ""); err == nil {
		t.Fatal("accepted a nonnumeric page ID")
	}
	invalid = in
	invalid.Links = []provider.Link{{URL: "https://example.org"}}
	if _, err := a.DocsPublish(ctx, invalid, ""); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("structured links: %v", err)
	}
	invalid = in
	invalid.ExpectedRevision = ""
	if _, err := a.DocsPublish(ctx, invalid, ""); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("create: %v", err)
	}
	invalid.ExpectedRevision = "-1"
	if _, err := a.DocsPublish(ctx, invalid, ""); err == nil {
		t.Fatal("accepted a negative revision")
	}
	f.space = "OTHER"
	if _, err := a.DocsGet(ctx, in.Ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("foreign space lookup: %v", err)
	}
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("foreign space update: %v", err)
	}
	f.space = "ENG"
	f.version = atlassian.PageVersion{Number: 8, Message: "someone else's edit"}
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrConflict) || f.writes != 0 {
		t.Fatalf("stale version: %v, writes %d", err, f.writes)
	}
	f.version.Number = 7
	f.responseStatus = http.StatusConflict
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrConflict) || f.writes != 1 {
		t.Fatalf("concurrent update: %v, attempts %d", err, f.writes)
	}
}
