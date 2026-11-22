package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
)

type confluenceMCPFixture struct {
	t                     *testing.T
	connection, snapshot  string
	space, title          string
	version               int64
	currentMessage        string
	successorMessage      string
	prepareErr, writeErr  error
	ack                   json.RawMessage
	prepares, reads, puts int
}

func (f *confluenceMCPFixture) Prepare(_ context.Context, call native.Call) (native.WriteBinding, error) {
	f.prepares++
	if call.Provider != "confluence" || call.Server != "atlassian" || call.Tool != "updateConfluenceContent" ||
		call.Arguments["cloudId"] != "https://docs.example" {
		f.t.Fatalf("unexpected native update capability: %#v", call)
	}
	return native.WriteBinding{Connection: f.connection}, f.prepareErr
}

func (f *confluenceMCPFixture) Rebind(ctx context.Context, binding native.WriteBinding, call native.Call) (native.WriteBinding, error) {
	if binding.Connection != f.connection || call.Arguments["snapshotToken"] != f.snapshot {
		f.t.Fatalf("unexpected confluence rebind: %#v, %#v", binding, call)
	}
	return f.Prepare(ctx, call)
}

func (f *confluenceMCPFixture) ReadBound(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	f.reads++
	if binding.Connection != f.connection || call.Provider != "confluence" || call.Server != "atlassian" ||
		call.Arguments["cloudId"] != "https://docs.example" {
		f.t.Fatalf("unexpected bound read: %#v, %#v", binding, call)
	}
	switch call.Tool {
	case "getConfluenceContent":
		if call.Arguments["content_id"] != "42" || call.Arguments["include_metadata"] != true {
			f.t.Fatalf("unexpected page request: %#v", call)
		}
		return f.page(), nil
	case "executeRead":
		want := map[string]any{"name": "getConfluenceContentVersion", "cloudId": "https://docs.example",
			"inputs": map[string]any{"contentId": "42", "versionNumber": int64(8)}}
		if !reflect.DeepEqual(call.Arguments, want) {
			f.t.Fatalf("unexpected version request: %#v", call)
		}
		return json.RawMessage(fmt.Sprintf(`{"data":{"number":8,"message":%q}}`, f.successorMessage)), nil
	default:
		f.t.Fatalf("unexpected native read: %#v", call)
		return nil, nil
	}
}

func (f *confluenceMCPFixture) Write(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	f.puts++
	if binding.Connection != f.connection {
		f.t.Fatalf("write had unexpected binding: %#v", binding)
	}
	message, _ := call.Arguments["versionMessage"].(string)
	want := map[string]any{
		"cloudId": "https://docs.example", "contentId": "42", "contentType": "page", "title": "Quota",
		"snapshotToken": f.snapshot, "versionMessage": message,
		"body": map[string]any{"format": "html", "value": "<p># Quota<br />&lt;ac:structured-macro/&gt; &amp; limits</p>"},
	}
	if !strings.HasPrefix(message, "devflow docs publish ") || !reflect.DeepEqual(call.Arguments, want) {
		f.t.Fatalf("unexpected native write: %#v", call)
	}
	f.version, f.title, f.currentMessage, f.successorMessage = f.version+1, "Quota", message, message
	if f.writeErr != nil || f.ack != nil {
		return f.ack, f.writeErr
	}
	return f.page(), nil
}

func (f *confluenceMCPFixture) page() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"structuredContent":{"contentId":"42","contentType":"page","status":"current","title":%q,
		"metadata":{"spaceId":"100","spaceKey":%q},"snapshotToken":%q,"version":{"number":%d,"message":%q}}}`,
		f.title, f.space, f.snapshot, f.version, f.currentMessage))
}

func newConfluenceMCPFixture(t *testing.T) (*App, *confluenceFixture, *confluenceMCPFixture, DocsPublishInput) {
	t.Helper()
	a, legacy, in := newConfluenceFixture(t)
	host := &confluenceMCPFixture{t: t, connection: "route-a", snapshot: "snapshot-7", space: "ENG", title: "Design", version: 7}
	return a, legacy, host, in
}

func TestConfluenceNativeConfirmationBindsSnapshotAndRoute(t *testing.T) {
	a, rest, host, in := newConfluenceMCPFixture(t)
	a.Gate.Autonomy.Remote = config.Confirm
	ctx := native.WithWriteHost(t.Context(), host)
	v, err := a.DocsPublish(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	summary := v.(*Pending).Summary.(map[string]any)
	if summary["backend"] != "mcp" || summary["space_id"] != "100" ||
		summary["payload_sha256"] == "" || host.puts+rest.writes != 0 || host.prepares != 2 {
		t.Fatalf("approval = %+v, host = %+v", summary, host)
	}
	host.snapshot = "different-snapshot-7"
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed snapshot accepted: %v", err)
	}
	host.snapshot = "snapshot-7"
	host.connection = "route-b"
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed route accepted: %v", err)
	}
	host.connection = "route-a"
	if _, err := a.DocsPublish(t.Context(), in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed backend accepted: %v", err)
	}
	if host.puts+rest.writes != 0 {
		t.Fatal("changed approval dispatched a write")
	}
	v, err = a.DocsPublish(ctx, in, token)
	want := provider.WriteResult{Ref: in.Ref, Revision: "8", Status: provider.Applied, URL: "https://docs.example/wiki/pages/viewpage.action?pageId=42"}
	if err != nil || v != want || host.puts != 1 || rest.writes != 0 {
		t.Fatalf("publication: %+v, %v", v, err)
	}
}

func TestConfluenceNativeAmbiguousWriteAndHistoricalReplay(t *testing.T) {
	for _, setup := range []struct {
		name string
		err  error
		ack  json.RawMessage
	}{
		{name: "transport error", err: errors.New("lost upstream response")},
		{name: "unavailable after dispatch", err: native.ErrUnavailable},
		{name: "tool error", ack: json.RawMessage(`{"isError":true}`)},
		{name: "foreign acknowledgement", ack: json.RawMessage(`{"contentId":"43"}`)},
	} {
		t.Run(setup.name, func(t *testing.T) {
			a, rest, host, in := newConfluenceMCPFixture(t)
			a.Gate.Autonomy.Remote = config.Auto
			host.writeErr, host.ack = setup.err, setup.ack
			ctx := native.WithWriteHost(t.Context(), host)
			if _, err := a.DocsPublish(ctx, in, ""); err == nil || host.puts != 1 || rest.writes != 0 {
				t.Fatalf("ambiguous delivery: %v, writes %d MCP and %d REST", err, host.puts, rest.writes)
			}
			host.version, host.currentMessage = 9, "later edit"
			v, err := a.DocsPublish(ctx, in, "")
			if err != nil || v.(provider.WriteResult).Status != provider.AlreadyApplied || v.(provider.WriteResult).Revision != "8" || host.puts != 1 {
				t.Fatalf("historical replay: %+v, %v, %d attempts", v, err, host.puts)
			}
			in.OperationID = "other-operation"
			if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrConflict) || host.puts != 1 {
				t.Fatalf("unmatched receipt: %v, %d attempts", err, host.puts)
			}
		})
	}
}

func TestConfluenceNativeVerifiesMinimalAcknowledgement(t *testing.T) {
	a, rest, host, in := newConfluenceMCPFixture(t)
	a.Gate.Autonomy.Remote = config.Auto
	host.ack = json.RawMessage(`{"contentId":"42","operationPerformed":"UPDATED","snapshotToken":"new"}`)
	v, err := a.DocsPublish(native.WithWriteHost(t.Context(), host), in, "")
	if err != nil || v.(provider.WriteResult).Status != provider.Applied || host.reads != 2 || host.puts != 1 || rest.writes != 0 {
		t.Fatalf("read-back: %+v, %v, reads %d", v, err, host.reads)
	}
}

func TestConfluenceNativeUnavailableAndDenied(t *testing.T) {
	a, rest, host, in := newConfluenceMCPFixture(t)
	a.Gate.Autonomy.Remote = config.Auto
	ctx := native.WithWriteHost(t.Context(), host)
	host.space = "OTHER"
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrNotFound) || host.puts+rest.writes != 0 {
		t.Fatalf("wrong space: %v", err)
	}
	host.space = "ENG"
	for _, err := range []error{native.ErrDenied, native.ErrSignIn} {
		host.prepareErr = err
		if _, got := a.DocsPublish(ctx, in, ""); !errors.Is(got, err) || host.puts+rest.writes != 0 {
			t.Fatalf("preflight denial: %v", got)
		}
	}
	host.prepareErr = native.ErrUnavailable
	v, err := a.DocsPublish(ctx, in, "")
	if err != nil || v.(provider.WriteResult).Status != provider.Applied || host.puts != 0 || rest.writes != 1 {
		t.Fatalf("missing native capability: %+v, %v", v, err)
	}
}

func TestConfluenceNativeReconcilesRESTReceipt(t *testing.T) {
	a, rest, host, in := newConfluenceMCPFixture(t)
	a.Gate.Autonomy.Remote = config.Auto
	if _, err := a.DocsPublish(t.Context(), in, ""); err != nil {
		t.Fatal(err)
	}
	host.version, host.title, host.currentMessage = rest.version.Number, rest.title, rest.version.Message
	v, err := a.DocsPublish(native.WithWriteHost(t.Context(), host), in, "")
	if err != nil || v.(provider.WriteResult).Status != provider.AlreadyApplied || host.puts != 0 || rest.writes != 1 {
		t.Fatalf("cross-backend receipt = %+v, %v", v, err)
	}
}

func TestConfluenceCLIRequiresApprovalForItsOwnRoute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("twg fixture uses /bin/sh")
	}
	a, rest, host, in := newConfluenceMCPFixture(t)
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "JIRA_USER", "JIRA_TOKEN", "CONFLUENCE_USER", "CONFLUENCE_TOKEN"} {
		t.Setenv(name, "")
	}
	a.Gate.Autonomy.Remote = config.Confirm
	host.prepareErr = native.ErrUnavailable
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	exe := filepath.Join(dir, "twg")
	script := `#!/bin/sh
set -eu
test "$1 $2 $3 $4" = 'confluence content get 42'
printf '%s\n' '{"data":{"id":"42","contentType":"page","title":"Design","status":"current","spaceKey":"ENG","spaceId":"100","version":{"number":7},"snapshotToken":"snapshot-7"}}'
`
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := WithConfluenceCLI(native.WithWriteHost(t.Context(), host))
	v, err := a.DocsPublish(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	summary := v.(*Pending).Summary.(map[string]any)
	if summary["backend"] != "twg" || summary["twg_intent"] == nil {
		t.Fatalf("fallback approval = %+v", summary)
	}
	host.prepareErr = nil
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) || host.puts+rest.writes != 0 {
		t.Fatalf("fallback approval used for another backend: %v", err)
	}
}
