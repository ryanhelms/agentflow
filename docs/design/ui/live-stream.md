# Live stream patterns for the console

Use these patterns for every console screen that shows data from the event stream
(`GET /w/<id>/api/events`, [ADR](../../knowledge/engineering/decisions/event-driven-platform.md)).
The overview is the first screen that uses them.

## Use the shared stream, never open your own

`static/event-worker.js` holds one stream per browser and shares it with every tab.

```js
const live = AgentflowEvents.connect({
  endpoint: new URL("api/events", location.href).href,
  credential: () => session,          // posted to the worker, never put in a URL
  topics: () => ["*/" + worktreeID + "/git.**"],
  unauthorized: renewSession,         // the existing 401 flow
  onStatus(status) {},                // {state, reason}
  onSnapshot({cursor, states}) {},    // replace everything you show
  onDelta({states, event}) {}         // apply the changed states; event feeds timelines
});
live.start();   // when the console session opens
live.stop();    // on the reconnect screen and on pagehide
live.resync();  // behind the Resync control
```

- A state is `{key, projection, repo, worktree, state}`. `state: null` means the key is gone.
- A screen adds its topics to `topics()`. The worker reconnects with the union of all tabs' topics.
- Read values defensively: ignore unknown fields and treat missing ones as empty.
- The stream runs in the tab itself when `SharedWorker` is missing, fails to start, or cannot
  reach the console. Screens do not need to know which mode is active.

## Connection states

One indicator sits in the top bar beside **Resync** (`#stream-state`, class `stream-state`).
Each state has a label, a colour and its own mark shape, so it never depends on colour alone.

| State | Label | Mark | Token | When |
| --- | --- | --- | --- | --- |
| `connecting` | Connecting | hollow circle, pulsing | `--text-muted` | first connection, before the first snapshot |
| `live` | Live | filled circle | `--ok` | a snapshot arrived on the current connection |
| `reconnecting` | Reconnecting | hollow circle, pulsing | `--warn` | the stream dropped; fewer than 3 failed attempts |
| `resyncing` | Resyncing | half-filled circle, turning | `--warn` | the server sent `resync`, or you pressed **Resync** |
| `offline` | Offline | hollow square | `--bad` | 3 failed attempts, the browser is offline, or the server refused (404, 403, 429) |

Rules:

- Keep the last values on screen in every state. Never blank a screen because the stream dropped.
- Show a note only when the reader must know the values may be stale: `offline`, and `live`
  with no state for this screen (`.live-note`, amber keyline). `reconnecting` and `resyncing`
  are short, so the indicator alone is enough.
- The worker retries by itself (250 ms doubling to 10 s). The only manual control is **Resync**:
  it reloads the checkout and asks for a snapshot without a cursor.

## Show a live change

1. Replace the value in place. Do not rebuild the surrounding element; rebuilding moves focus
   and restarts the highlight on values that did not change.
2. Add `live-changed` to the element whose text changed and remove it after 2 s
   (`showFact` in `app.js` does both). The class fades a background from `--live-flash`.
   Set `--live-flash` and `--live-mark` on a container whose ground differs, as `.checkout` does.
3. Highlight only a change from one live value to another. Do not highlight the first snapshot,
   a requested **Resync**, or values from `api/state`.

## Accessibility

- Changes are announced through one polite live region, `#live-announcer`, with one sentence
  per update ("Branch changed to feature/x. Changes changed to 2 unstaged."). Call `announce()`;
  do not add more live regions.
- Announce a connection change only when it goes `offline` and when it returns to `live` from
  `offline`. Short reconnects stay silent.
- The indicator is text, so screen readers read it in place. **Resync** is a button and names
  the indicator through `aria-describedby`.
- With `prefers-reduced-motion: reduce` the marks do not animate and a changed value gets a
  static 1 px outline (`--live-mark`) for the same 2 s. Forced colours use `Highlight`.
- Light and dark use the same tokens from `nscale-tokens.css`; add no raw colours.

## Empty and error states

| Case | What the screen shows |
| --- | --- |
| No state for this screen in a live snapshot | The loaded values and a note: "The live stream has no data for this worktree yet." |
| Offline, values received earlier | The last values and "Live updates are offline. … The console keeps retrying." |
| Endpoint missing (404) | "This console does not serve live updates. Use Resync to reload the checkout." Retrying stops. |
| Too many streams (429) | "Too many live streams are open for this console. …" Retrying continues. |
| Session ended (401) | No stream state: the existing session flow renews the session or shows the reconnect screen. |
| Configuration changed on disk while you have unsaved edits | A notice asks you to save or discard, then **Resync**. Without edits the console reloads by itself. |

## Tests

`TestEventStreamBrowser` covers frame parsing, the store and the worker. `TestLiveOverviewBrowser`
drives the overview. Both use the scripted endpoint in `internal/webui/stream_fixture_test.go`
and need `AGENTFLOW_TEST_CHROME`.
