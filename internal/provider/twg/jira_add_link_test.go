package twg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
)

const (
	addURL   = "https://docs.example.test/releases/%2F?a=1&b=2#v1"
	addTitle = "Release 'v1' & notes"
	addID    = "devflow:link:abc123"
	addAck   = `{"data":{"success":true,"issue":{"idOrKey":"ENG-42"},"result":{"id":10001},"weblink":{"url":"` + addURL + `","title":"` + addTitle + `","globalId":"` + addID + `"}}}`
)

func TestJiraAddRemoteLink(t *testing.T) {
	t.Setenv("TWG_SITE", "elsewhere.example.test")
	t.Setenv("TWG_PROFILE", "elsewhere")
	t.Setenv("TWG_TOKEN", "fixture")
	trace := fakeTWG(t, `test "$CI" = true
test -z "${TWG_TOKEN+x}${TWG_SITE+x}${TWG_PROFILE+x}"
`+printJSON(addAck))
	j := newTestJira(t, "ENG")
	if err := j.AddRemoteLink(t.Context(), j.ref("eng", "eng-42"), addURL, addTitle, addID); err != nil {
		t.Fatal(err)
	}
	assertArgs(t, trace, "jira", "workitem", "link", "weblink", "--id", "ENG-42", "--url", addURL, "--title", addTitle, "--global-id", addID)
}

func TestJiraAddRemoteLinkBeforeDispatch(t *testing.T) {
	trace := fakeTWG(t, "exit 9")
	j := newTestJira(t, "ENG")
	ref := j.ref("ENG", "ENG-42")
	for _, invalid := range []provider.Ref{
		{Instance: "other", Scope: ref.Scope, ID: ref.ID},
		j.ref("OPS", "OPS-42"), j.ref("ENG", "OPS-42"), j.ref("ENG", "ENG-0"),
	} {
		err := j.AddRemoteLink(t.Context(), invalid, addURL, addTitle, addID)
		if !errors.Is(err, ErrAccessDenied) || errors.Is(err, native.ErrWriteUncertain) {
			t.Fatalf("invalid ref %v: %v", invalid, err)
		}
	}
	for _, args := range [][3]string{
		{"", addTitle, addID}, {addURL, " " + addTitle, addID},
		{addURL, addTitle, ""}, {addURL, addTitle, addID + "\uFEFF"},
	} {
		err := j.AddRemoteLink(t.Context(), ref, args[0], args[1], args[2])
		if err == nil || errors.Is(err, native.ErrWriteUncertain) {
			t.Fatalf("invalid link arguments: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := j.AddRemoteLink(ctx, ref, addURL, addTitle, addID)
	if !errors.Is(err, context.Canceled) || errors.Is(err, native.ErrWriteUncertain) {
		t.Fatalf("already canceled: %v", err)
	}
	if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no invocation, stat error = %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	err = j.AddRemoteLink(t.Context(), ref, addURL, addTitle, addID)
	if !errors.Is(err, ErrToolAbsent) || errors.Is(err, native.ErrWriteUncertain) {
		t.Fatalf("missing twg: %v", err)
	}
}

func TestJiraAddRemoteLinkUncertain(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         error
	}{
		{"nonzero after acknowledgement", printJSON(addAck) + "\nexit 3", ErrFailure},
		{"permission", printJSON(`{"error":{"code":"ACCESS_DENIED","message":"fixture-secret"}}`) + " >&2\nexit 1", ErrAccessDenied},
		{"empty", "exit 0", ErrFailure},
		{"malformed", printJSON("fixture-secret"), ErrFailure},
		{"partial", printJSON(strings.Replace(addAck, `{"data"`, `{"partial":true,"data"`, 1)), ErrFailure},
		{"failed", printJSON(strings.Replace(addAck, `"success":true`, `"success":false`, 1)), ErrFailure},
		{"wrong issue", printJSON(strings.Replace(addAck, "ENG-42", "ENG-43", 1)), ErrFailure},
		{"wrong url", printJSON(strings.Replace(addAck, addURL, addURL+"x", 1)), ErrFailure},
		{"wrong title", printJSON(strings.Replace(addAck, addTitle, "Other", 1)), ErrFailure},
		{"missing global id", printJSON(strings.Replace(addAck, `,"globalId":"`+addID+`"`, "", 1)), ErrFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeTWG(t, tc.script)
			j := newTestJira(t, "ENG")
			err := j.AddRemoteLink(t.Context(), j.ref("ENG", "ENG-42"), addURL, addTitle, addID)
			if !errors.Is(err, native.ErrWriteUncertain) || !errors.Is(err, tc.want) || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestJiraAddRemoteLinkCanceledAfterStart(t *testing.T) {
	trace := fakeTWG(t, "exec /bin/sleep 10")
	j := newTestJira(t, "ENG")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- j.AddRemoteLink(ctx, j.ref("ENG", "ENG-42"), addURL, addTitle, addID)
	}()
	for {
		if _, err := os.Stat(trace); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("command returned before fixture started: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, native.ErrWriteUncertain) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled after start: %v", err)
	}
}
