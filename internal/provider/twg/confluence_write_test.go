package twg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/confluence"
)

func TestConfluenceTWGPublishAndReplay(t *testing.T) {
	for _, lose := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_ack_%t", lose), func(t *testing.T) {
			ctx := t.Context()
			c := newTestConfluence(t, "DOC")
			ref := c.ref("DOC", "55")
			draft := provider.DocumentDraft{Title: "--title stays a value", Body: "# Literal\n<script> & quotes"}
			opts := provider.WriteOptions{OperationID: "stable", ExpectedRevision: "7"}
			replacement, err := confluence.NewReplacement(confluence.PageURL(testSite, ref.ID), ref, draft, opts)
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "state")
			receivedBody := filepath.Join(t.TempDir(), "body-path")
			message := replacement.Successor.Message
			before := `{"data":{"id":"55","contentType":"page","status":"current","title":"Old","version":{"number":7},
				"metadata":{"spaceKey":"DOC","spaceId":"100"},"snapshotToken":"opaque-7"}}`
			after := fmt.Sprintf(`{"data":{"id":"55","contentType":"page","status":"current","title":%q,"version":{"number":8,"message":%q},
				"spaceKey":"DOC","spaceId":"100","snapshotToken":"opaque-8"}}`, draft.Title, message)
			later := `{"data":{"id":"55","contentType":"page","status":"current","title":"Later edit","version":{"number":9,"message":"later"},
				"spaceKey":"DOC","spaceId":"100","snapshotToken":"opaque-9"}}`
			history := fmt.Sprintf(`{"data":{"contentId":"55","versions":[{"number":9,"message":"later"},{"number":8,"message":%q}]}}`, message)
			ack := printJSON(`{"data":{"contentId":"55","snapshotToken":"opaque-8"}}`)
			if lose {
				ack = "exit 36"
			}
			fakeTWG(t, `
test "$1" = confluence
test "$2" = content
case "$3" in
get)
  test "$4 $5 $6 $7 $8 $9 ${10} ${11}" = '55 --detail full --format html --include-metadata --site https://example.atlassian.net'
  case "$(/bin/cat `+shellQuote(state)+` 2>/dev/null || true)" in
  later) `+printJSON(later)+` ;;
  delivered) `+printJSON(after)+` ;;
  *) `+printJSON(before)+` ;;
  esac ;;
update)
  test "$4 $5 $6 $7" = '55 --snapshot-token=opaque-7 --title=--title stays a value --body-file'
  test "$(/bin/cat "$8")" = '<p># Literal<br />&lt;script&gt; &amp; quotes</p>'
  test "$9 ${10} ${11} ${12} ${13} ${14} ${15} ${16} ${17} ${18}" = `+shellQuote("--format html --ack-body-formats --version-message "+message+" --content-type page --yes --site "+testSite)+`
  test ! -e `+shellQuote(state)+`
  printf '%s' "$8" > `+shellQuote(receivedBody)+`
  printf '%s' delivered > `+shellQuote(state)+`
  `+ack+`
  ;;
versions)
  test "$4 $5 $6 $7 $8 $9 ${10}" = 'list --id 55 --limit 100 --site https://example.atlassian.net'
  `+printJSON(history)+` ;;
*) exit 99 ;;
esac`)

			prepared, err := c.PreparePublish(ctx, ref, draft, opts)
			if err != nil || prepared.Intent().Site != testSite || prepared.Intent().SpaceID != "100" || prepared.Intent().PayloadSHA256 == "" {
				t.Fatalf("prepare = %+v, %v", prepared.Intent(), err)
			}
			if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prepare changed page state: %v", err)
			}
			res, err := prepared.Apply(ctx)
			if lose && err == nil || !lose && (err != nil || res != replacement.Result(provider.Applied)) {
				t.Fatalf("publication = %+v, %v", res, err)
			}
			name, err := os.ReadFile(receivedBody)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(string(name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement file should have been removed: %v", err)
			}
			res, err = c.Publish(ctx, ref, draft, opts)
			if err != nil || res != replacement.Result(provider.AlreadyApplied) {
				t.Fatalf("immediate replay = %+v, %v", res, err)
			}
			if err := os.WriteFile(state, []byte("later"), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err = c.Publish(ctx, ref, draft, opts)
			if err != nil || res != replacement.Result(provider.AlreadyApplied) {
				t.Fatalf("historical replay = %+v, %v", res, err)
			}
			draft.Body += " changed"
			if _, err := c.Publish(ctx, ref, draft, opts); !errors.Is(err, provider.ErrConflict) {
				t.Fatalf("changed operation = %v", err)
			}
		})
	}
}

func TestConfluenceTWGRejectsWrongSiteOrSnapshot(t *testing.T) {
	for _, fields := range []string{
		`"url":"https://foreign.atlassian.net/wiki/spaces/DOC/pages/55","snapshotToken":"opaque"`,
		`"snapshotToken":""`,
	} {
		trace := fakeTWG(t, printJSON(`{"data":{"id":"55","contentType":"page","status":"current","title":"Old","version":{"number":7},
			"metadata":{"spaceKey":"DOC","spaceId":"100"},`+fields+`}}`))
		c := newTestConfluence(t, "DOC")
		_, err := c.Publish(t.Context(), c.ref("DOC", "55"), provider.DocumentDraft{Title: "New"},
			provider.WriteOptions{OperationID: "stable", ExpectedRevision: "7"})
		if !errors.Is(err, ErrFailure) {
			t.Fatalf("unsafe preflight = %v", err)
		}
		assertArgs(t, trace, "confluence", "content", "get", "55", "--detail", "full", "--format", "html", "--include-metadata")
	}
}
