# Live stream patterns for the console

Use these patterns for every console screen that shows data from the event stream
(`GET /w/<id>/api/events`, [ADR](../../knowledge/engineering/decisions/event-driven-platform.md)).
The overview's checkout card was the first to use them. The activity, approvals and
handoff panels, the operation timeline and tab navigation follow in "Live panels",
"Timeline" and "Tab navigation" below.

## Use the shared stream, never open your own

`static/event-worker.js` holds one stream per browser and shares it with every tab.

```js
const live = AgentflowEvents.connect({
  endpoint: new URL("api/events", location.href).href,
  credential: () => session,          // posted to the worker, never put in a URL
  topics: () => ["*/" + worktreeID + "/git.**"],
  tab: () => tabs.id,                 // lists this tab in the stream's tab= (see "Tab navigation")
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
| `offline` | Offline | hollow square | `--bad` | 3 failed attempts, the browser is offline, the server refused (404, 403, 429), or it has no event store yet (503, keeps retrying) |

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
  do not add more live regions. Messages from the same update are joined, so one panel's
  change never replaces another's.
- Announce a connection change only when it goes `offline` and when it returns to `live` from
  `offline`. Short reconnects stay silent.
- The indicator is text, so screen readers read it in place. **Resync** is a button and names
  the indicator through `aria-describedby`.
- With `prefers-reduced-motion: reduce` the marks do not animate and a changed value gets a
  static 1 px outline (`--live-mark`) for the same 2 s. Forced colours use `Highlight`.
- Light and dark use the same tokens from `agentflow-tokens.css`; add no raw colours.

## Empty and error states

| Case | What the screen shows |
| --- | --- |
| No state for this screen in a live snapshot | The loaded values and a note: "The live stream has no data for this worktree yet." |
| Offline, values received earlier | The last values and "Live updates are offline. … The console keeps retrying." |
| Endpoint missing (404) | "This console does not serve live updates. Use Resync to reload the checkout." Retrying stops. |
| Too many streams (429) | "Too many live streams are open for this console. …" Retrying continues. |
| No event store yet (503) | "Live updates are not available for this worktree yet. The console keeps retrying." Retrying continues. |
| Session ended (401) | No stream state: the existing session flow renews the session or shows the reconnect screen. |
| Configuration changed on disk while you have unsaved edits | A notice asks you to save or discard, then **Resync**. Without edits the console reloads by itself. |

## Live panels

`static/live-panels.js` renders two repository-wide states (`ActivityState` and
`ApprovalsState` in `internal/webui/projection_states.go`). `app.js` passes each state to
`panels.showActivity(raw, flash)` or `panels.showApprovals(raw, flash)`; `flash` follows rule 3
of "Show a live change". No panel has a refresh button. **Resync** is the only manual control.

| Panel | Where | Shows |
| --- | --- | --- |
| Approvals | Overview, beside **Needs attention** | **Open**: requests that wait, oldest first, with a countdown. **Recent outcomes**: used, cancelled and expired requests of the last 24 hours. |
| Activity | Overview, full width | GitHub health in the heading; **Pull requests**, **Issues**, **Recent commits and hooks** in three columns (one column below 65 rem). |
| Handoff | Handoff screen | The checkout brief, open worker requests and the live pull request lines (`ActivityState.handoff`). |

### Rows

Lists are keyed (`reconcile` in `live-panels.js`): a row that stays is updated in place, a new
row is inserted where the server orders it, a row that leaves is removed. Every value of a row
is a `[data-fact]` element set through `fact()`, which is what gets highlighted.

- **Pull request**: `#number title` (link, new tab), one readiness tag, then facts: checks,
  review, `head into base`, `Depends on #n (its readiness)`, worktree ("This worktree" or
  "Worktree id"), author. Failed check names follow on their own line in `--bad`.
- **Issue**: `#number title`, state tag, worktree, labels, assignees.
- **Recent**: a commit shows its subject, time, short OID, branch, type and "breaking change".
  A hook shows "pre-bash hook blocked: repository boundary", its duration and client.
  Only blocked and failed hooks appear. Hook reasons are fixed codes; show their label, never free text.
- **Approval**: action name, state tag, countdown, request time, class, client, rule and
  operation ID. An outcome row drops the countdown, rule and ID.

### One readiness tag per pull request

`pullReadiness` picks the most blocking fact, in this order:

| Tag | Tone | When |
| --- | --- | --- |
| Draft | neutral | `draft` |
| Conflicts | bad | `merge_state` is `dirty` |
| Checks failing | bad | `ci` is `failure` |
| Changes requested | warn | `review_decision` is `changes_requested` |
| Checks running | warn | `ci` is `pending` |
| Review required | warn | `review_decision` is `review_required` |
| Behind base | warn | `merge_state` is `behind` |
| Blocked | warn | `merge_state` is `blocked` |
| Ready to merge | good | `merge_state` is `clean` or `has_hooks` |
| Open | neutral | anything else |

Tags take their tone from `data-state` (`good`, `warn`, `bad`). The label carries the meaning;
the colour repeats it.

### State marks

A `.state-mark` sits before a fact whose state matters at a glance. Its shape carries the
state, as the stream indicator's does: a filled circle is fine, a hollow shape waits, a
square has stopped.

| Mark | Token | Used for |
| --- | --- | --- |
| filled circle | `--ok` | checks passing, GitHub up to date |
| filled circle | `--text-muted` | a commit |
| hollow circle, pulsing | `--warn` | checks running |
| hollow circle | `--warn` | an approval that waits |
| half-filled circle | `--warn` | GitHub rate limited, an approval with a minute or less left |
| hollow square | `--warn` | a blocked hook |
| filled square | `--bad` | checks failing, a failed hook, GitHub unavailable |
| hollow circle | `--text-muted` | no checks, GitHub not connected or unknown |

### Countdowns

An approval shows "Expires in 4 min 05 s", then "Expires in 32 s" with the half-filled mark
in the last minute, then the tag **Expired**. A rate-limited GitHub shows "retries at 14:32
(in 1 min 20 s)". One timer runs once a second while something counts down and stops
otherwise. A tick is never highlighted and never announced. The clock announces two moments
only: "expires in 1 minute" and "expired". Digits use tabular numbers so the text does not shift.

### What is announced

One sentence group per update, at most three changes, then "and 2 more changes".

| Change | Sentence |
| --- | --- |
| A pull request appears or leaves | "Pull request #41 opened: title." / "Pull request #41 is no longer open." |
| Its readiness, checks or review change | "Pull request #41: checks failing." |
| An issue closes or reopens | "Issue #37 closed." |
| A commit or a stopped hook is recorded | "New commit on main: subject." / "pre-bash hook blocked: repository boundary." |
| GitHub health changes | "GitHub is rate limited. Retries at 14:32." / "GitHub updates restored." |
| An approval is requested, approved or ends | "Approval requested: Push a branch. Expires in 4 min 50 s." / "Approval for Push a branch cancelled." |
| The brief changes while the Handoff screen is open | "The repository brief changed." |

### Panel states

| Case | Activity | Approvals |
| --- | --- | --- |
| Before the first snapshot | "Waiting for live updates…" | the same |
| Live, lists empty | "No open pull requests." / "No issues are bound to this repository’s worktrees." / "No commits or stopped hooks recorded yet." | "Nothing is waiting for approval." / "No approvals ended in the last 24 hours." |
| GitHub not connected (`disabled`) | "GitHub not connected" and a note; the pull request and issue lists say "GitHub is not connected." | not affected |
| GitHub unavailable or rate limited | the health label, the retry time when known, and "… show the last values it read." The lists stay. | not affected |
| Offline with values | the values and a `.live-note`: "Live updates are offline. Activity shows the last values it received. The console keeps retrying." | the same, for approvals |
| Offline before any value | "Live updates are offline. Activity appears when they return. …" | the same |
| Live, but no state for the repository | the last values and "The live stream has no activity for this repository yet." | "… no approvals …" |
| Requested **Resync** | the values stay until the new snapshot replaces them, without highlights | the same |

The Handoff screen shows a `.live-note` while offline, and "Updated live at 14:32." under the
brief after a live change.

### Focus

A row that is updated keeps focus. When the row that holds focus is removed, focus moves to
the panel heading (`tabindex="-1"`), so a keyboard user is never dropped at the top of the page.

### Worker requests

Agent Commander's worker questions are not on the stream yet. They load from
`GET api/activity` with the checkout and on **Resync**, only for a console that a Commander
session opened, and only the Commander items of the answer are shown. A navigation request
clears them; the note in **Needs attention** then says to use **Resync**.

## Timeline

`static/timeline.js` and `static/timeline.css` render one repository-wide state,
`TimelineState` (`internal/webui/projection_timeline_state.go`). The `timeline` projection
(`projection_timeline.go`) builds it from the repository's stored `op.*` and `approval.*`
events. `app.js` adds `*/*/op.**` to its topics and passes the state to
`timeline.show(raw, flash)` and the stream's state to `timeline.status(...)`; nothing else
in `app.js` knows about the panel. The panel is on the Overview, full width, below **Activity**.

### One operation, one row

The server groups events by operation ID and orders operations newest first. The panel never
reorders them. A row is keyed by the operation ID, so a step that arrives changes the row in
place.

| Part | Shows |
| --- | --- |
| Title and tag | The operation's name (the action's name from the Autonomy screen when it has one, otherwise the operation name in words), a state mark, and one state tag. |
| Subject | The commit subject, PR title or task summary, as text. |
| Facts | Origin ("From the command line", "From claude-code (agent)", "From the console"), worktree ("This worktree", "Worktree id", "Whole repository"), task, branch, "Started 14:02, last step 14:05", class. |
| Reason | Only for **Failed** and **Suggested only**: one sentence in words (see "Failure codes"). |
| Approval | Only while the operation waits or is approved but not applied: what it waits for, when the approval expires, and **Show in Approvals**. |
| Steps | Every event of the operation, oldest first, each with a mark, a label and its time to the second. One line that wraps; one step per line below 38 rem. "Earlier steps are not shown." leads the list when the server cut it at 12. |
| Links | What the operation touched: commit, branch, pull request, task, document, release. A link opens in a new tab only when the state carries an `https` address; otherwise the name is plain text. A task or branch that is linked is not repeated as a fact. |
| Operation ID | The ID in full with **Copy ID**. The same ID is on the operation's line in `~/.agentflow/audit.log` and in `agentflow events`; a browser cannot open that file, so the panel gives the ID to search for. |

### States

`state` is the operation's latest state. Each has one tag and one mark; the mark follows the
"State marks" rule above (filled is done, hollow waits, a square has stopped).

| `state` | Tag | Tone | Mark | Filter group |
| --- | --- | --- | --- | --- |
| `requested` | In progress | neutral | hollow circle, pulsing, `--text-muted` | In progress |
| `requested`, no result for 10 minutes | No result recorded | neutral | hollow circle | In progress |
| `awaiting_approval` | Waiting for approval | warn | hollow circle, `--warn` | Waiting for approval |
| `approved` | Approved, not applied yet | good | hollow circle, `--ok` | In progress |
| `prepared` | Ready to apply | neutral | hollow circle, `--ok` | In progress |
| `applied` | Applied | good | filled circle, `--ok` | Applied |
| `failed` | Failed | bad | filled square, `--bad` | Failed |
| `suggested` | Suggested only | neutral | hollow square | Suggested only |
| `cancelled` | Cancelled | neutral | hollow square | Cancelled or expired |
| `expired` | Approval expired | bad | hollow square, `--bad` | Cancelled or expired |
| anything else | Unknown state | neutral | hollow square | none; shown only under "All states" |

A cancellation reaches the timeline through `approval.resolved`, and an expiry through
`approval.expired`. Approval events come from a file watcher and can arrive after the
`op.*` event of the call that used the approval, so the projection lets them change the
state only where they can still be true: they never undo **Applied**, and an approval
granted after a failed call leaves it **Failed**.

Step labels: Requested, Requested again (a call that resumes the operation), Waiting for
approval (`op.awaiting_approval`), Approval requested (`approval.requested`), Approved,
Approval used, Cancelled, Approval expired, Prepared, Applied, Suggested, not run, and
"Failed: " with the short reason.

### Failure codes

`op.failed` and `op.suggested` carry a fixed code, never error text. The panel shows a
sentence for each known code and its short form on the step ("Failed: request changed").
An unknown code that looks like an identifier is shown as "Reason code: name."; anything
else as "not readable". Add a sentence to `codes` in `timeline.js` when a lane adds a code.

| Code | Sentence |
| --- | --- |
| `approval_expired` | The approval expired before the operation ran. Ask for a fresh preview. |
| `operation_changed` | The request changed after it was approved, or the policy became stricter. Ask for a fresh preview. |
| `approval_consumed` | The approval was already used. Check the earlier result before you retry. |
| `approval_cancelled` | The approval was cancelled. |
| `approval_pending` | The operation was resumed before a person approved it. |
| `invalid_token` | The confirmation token was not valid. |
| `cancelled`, `timeout` | The operation was cancelled before it finished. / The operation timed out. |
| `native_read_required` | The agent has to read from its own provider connection first, then run the operation again. |
| `invalid`, `blocked` | The input was not valid, so nothing ran. / A check blocked the operation, so nothing ran. |
| `unexpected_result`, `unknown_status` | The operation returned a result (a status) Agentflow did not expect. |
| `error` | The operation returned an error. The terminal or agent session that ran it shows the details. |

### Filters

Three selects sit above the list. They filter in the browser; the state carries every
worktree's operations.

| Filter | Choices | Default |
| --- | --- | --- |
| Worktree | This worktree (with operations of the whole repository), All worktrees, and each other worktree the state names | This worktree |
| State | All states, and the filter groups of "States" | All states |
| Origin | All origins, Command line, Agents (MCP), Console when the state has one, and each agent by name when there are several | All origins |

- A filter change is not a live change: nothing is highlighted, and the result count is
  announced once ("4 operations · 7 operations in the repository.").
- **Clear filters** appears when a filter differs from its default and moves focus to the
  first select.
- The list shows 10 rows. **Show 10 more** adds the next 10; when the last rows are shown
  the button goes and focus moves to the panel heading.
- A choice that the state no longer names (a worktree that was removed) stays selected
  until the reader changes it.

### Show in Approvals

A waiting operation is answered in the session that asked, and the Approvals panel holds
its countdown, rule and ID. **Show in Approvals** is a button, not a link: the console's
address fragment names the screen. It scrolls the request into view, highlights its row
for 2 s, moves focus to the Approvals heading and announces "Approvals. The request is
highlighted." When the request is no longer open it says so instead.

### Live changes and announcements

- A new operation is inserted at the top and its row is highlighted. A step that arrives
  highlights the step and the values that changed (tag, reason), not the row.
- The timeline announces results only, one sentence each, at most three per update:
  "claude-code: Push a branch applied: Push issue-57.", "Command line: Create a local
  commit failed: blocked by a check.", "… was suggested, not run." A request and its
  approval are announced by the Approvals panel, so the timeline stays silent for them.
- An operation that the filters hide is neither shown nor announced.
- The first snapshot and a requested **Resync** are never highlighted or announced.
- The pulsing mark stops under `prefers-reduced-motion: reduce`; highlights follow the
  shared rule.

### Timeline states of the panel

| Case | What the panel shows |
| --- | --- |
| Before the first snapshot | "Waiting for live updates…" |
| Live, no operations | "No operations recorded in the last 7 days. Run agentflow commit, agentflow pr or an agent tool, and its steps appear here." |
| Filters match nothing | "No operations match these filters." and **Clear filters** |
| More than 50 operations, or history older than the store's replay window | "Older operations are not shown. Run `agentflow events` to list them." |
| Offline with values | the values and a `.live-note`: "Live updates are offline. The timeline shows the last operations it received. The console keeps retrying." |
| Offline before any value | "Live updates are offline. The timeline appears when they return. …" |
| Live, but no state for the repository | the last values and "The live stream has no timeline for this repository yet." |

## Tab navigation

A console tab is asked to show a screen through the stream (`static/tab-navigation.js`).

1. On start the tab posts `api/tabs/attach {"tab": "<32 hex>"}` with its session. The
   `X-Agentflow-Commander` header carries its capability when it has one. The capability never
   goes to the worker or into an address.
2. `AgentflowEvents.connect({tab: () => tabs.id, ...})` lists the ID in the shared stream's
   `tab=` parameters. The worker sends the sorted IDs of all its tabs, at most 16.
3. `app.js` passes the `tabs` state of its own worktree to `tabs.receive(state)`. The tab acts
   only on a `navigation` whose `tab` is its own ID, once per navigation `id`.
4. It answers with `api/tabs/ack {tab, id, handled | blocked}` and changes the screen only
   after the console accepted the answer.
5. A state that does not list the tab means the console dropped it: the tab attaches again.
   `tabs.refresh()` attaches again after the Commander capability changes.
6. `tabs.stop()` posts `api/tabs/leave` (with `keepalive`) on the reconnect screen and on `pagehide`.

Attach is retried from 250 ms doubling to 10 s after 429 or a network error, uses the renewed
session after 401, and stops after 400, 403 or 404. A console without a live stream cannot
deliver a navigation; a request for a screen then opens a new tab.

## Tests

`TestEventStreamBrowser` covers frame parsing, the store and the worker. `TestLiveOverviewBrowser`
drives the overview. `TestLivePanelsBrowser` drives the activity, approvals and handoff panels
through every state above. `TestTabFlowBrowser` covers attach, the stream's tab list,
navigation, acknowledgement and leave. `TestTimelineBrowser` drives the timeline: rows, steps,
links, failure codes, filters, paging, the jump to Approvals and every panel state.
`TestConsoleConnectionBrowser` runs navigation against the real tab hub. All use the fixtures in `internal/webui/stream_fixture_test.go` and need
`AGENTFLOW_TEST_CHROME`.
