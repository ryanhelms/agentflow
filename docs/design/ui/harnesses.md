# The Harnesses page

The Harnesses page (`#harnesses`) shows the ACP harnesses Agentflow found on this computer:
which are ready, which need attention, what to do about each one and whether you are signed
in. The contract is in the
[ADR](../../knowledge/engineering/decisions/event-driven-platform.md), section "Phase 6".
The page follows [live stream patterns](live-stream.md) for everything it shows.

| File | Holds |
| --- | --- |
| `static/harnesses.js` | the page: the state reader, the groups and cards, the probe and check requests |
| `static/index.html` | the rail entry, `#page-harnesses` and the script tag |
| `static/style.css` | every rule of the page (`hn-`), tokens only, and the rail entry's icon |

## Everything shown comes from the stream

Every value is the repository's `harnesses` state on the event stream (`HarnessesState` in
`internal/webui/projection_harnesses_state.go`): the daemon's last detection report. Nothing
polls. The page sends two requests, and neither answer carries harness data:

| Request | When | Body |
| --- | --- | --- |
| `POST api/harnesses/probe` | **Probe now** in the header, or on one card | `{}` for every harness found, `{"ids":["<id>"]}` for one card |
| `POST api/harnesses/check` | the window gets focus or becomes visible again while the page is open, at most once every 10 s | `{}` |

The page edits nothing. Automatic probes, the sign-in check and harness overrides are on the
Settings page, group `ai`; the header links there (`#settings?group=ai`). The page never
installs a harness and never runs a command: fix and sign-in commands are shown to copy.

## The header

The **Detection** panel is above the groups.

| Part | Shows |
| --- | --- |
| Summary | "8 harnesses found · 3 ready · 5 need attention. Checked at 14:02." |
| **Probe now** | probes every harness found; the line beside it says what a probe is doing |
| Registry copy | the pinned registry commit (12 characters; the full commit is the tooltip), its date and the number of agents |
| Automatic probes | **On** or **Off**, what that means, and the Settings link |
| Problems | definitions Agentflow refused: the ID, the layer, the message and the code |
| PATH entries skipped | folders of PATH that Agentflow does not search, each with its reason, in a closed `<details>` |

## Two groups

| Group | Holds |
| --- | --- |
| **Ready** | `native` and `adapter_installed` with a probe that answered (`probe.code` `ok`, status `ok` or `cached`) |
| **Needs attention** | every other harness in the state, including one that was found and not probed yet |

Cards keep the server's order inside a group. `not_installed` is not part of the state; one
that arrives anyway is left out. A card is built once and then updated in place. When a
probe moves it to the other group the same card moves, so focus, open details and running
highlights survive.

## One harness, one card

| Part | Shows |
| --- | --- |
| Head | the kind icon, the name with a state mark, **Probing** while it is probed, and the state tag |
| Facts | the ID, the kind (its meaning is the tooltip), "Version 0.71.1" or "Version not reported", and "Registry lists 0.72.0" when it differs |
| Description | the registry's description |
| Runs, Found at, Base CLI | the command and its arguments, the path (`~/…`), and for an adapter its base CLI and where it is |
| Fix | Agentflow's text for the state, the command with **Copy**, and the link. A ready harness has none |
| Sign-in | the verdict, when it was checked, and **How to sign in** with each method |
| Notes | `registry_env_ignored`, `quarantined`, or a note's own text |
| Detection details | the state and its code, the probe result and time, the layers that define the harness, the ACP protocol version and agent, what it supports and what it does not |
| **Probe now** | probes this harness (not shown for an adapter that is not installed) |

The page draws its own icon per kind, in CSS: a prompt in a window for `native`, a part
attached to a window for `adapter`. No registry icon is loaded, and the page loads no image.

## States

The tag's text carries the state; the tone, the card's left keyline and the mark's shape
repeat it.

| State, probe | Tag | Tone | Mark |
| --- | --- | --- | --- |
| `native` or `adapter_installed`, probe answered | Ready | `--ok` | filled circle |
| found, probe `none` | Not checked yet | neutral | hollow circle |
| found, probe `refused` | Not probed | neutral | hollow circle |
| found, probe `failed` (`timeout`, `exited`, `launch`) | Probe failed | `--warn` | half-filled circle |
| `needs_auth` | Sign-in needed | `--warn` | half-filled circle |
| `adapter_available` | Adapter not installed | `--warn` | half-filled circle |
| `incompatible` | Incompatible | `--bad` | filled square |
| a state this console does not know | Unknown state | neutral | hollow circle |

The fix text is Agentflow's own template from the report (`fix.text`). When a report sends
none for a harness that is not ready, `meaning()` in `harnesses.js` supplies one sentence
for the state and code. Add a sentence there, and a row to `probeCodes` and `authCodes`,
when a lane adds a code.

## Sign-in: signed out is not "not known"

The sign-in check answers one of three things, and the page keeps them apart by label,
sentence and mark shape:

| Verdict | Label | Mark | Sentence |
| --- | --- | --- | --- |
| `signed_in` | Signed in | filled circle, `--ok` | none |
| `signed_out` | Signed out | hollow square, `--warn` | "Its own status command says you are not signed in." |
| `unknown` | Sign-in not known | dotted circle | why, from the code: no verified check, an environment variable may sign you in, checks are off, and so on |

"Sign-in not known" never reads as a fault. For `env_credential` the sentence is "You may be
signed in with an environment variable; agentflow cannot check that." When the fix already
says it, it is not repeated.

**How to sign in** lists the harness's sign-in methods. A `terminal` method shows its command
with **Copy**. A method marked `unsafe`, or whose command is not one plain line, shows no
command and says so. An `agent` method says to start the harness and use its own login. The
list is open when the harness needs a sign-in and closed otherwise; your own toggle stays
until that changes. The section is not shown for an adapter that is not installed.

## Probe now

A probe starts each harness once, sends ACP `initialize`, and runs the harness's verified
sign-in status command where there is one. The console allows one probe a minute.

1. The click sends the request. The line beside the header button reads "Probe requested.
   The results arrive here."
2. When the state lists IDs in `probing`, their cards show the **Probing** tag and the line
   reads "Probing Cursor…" or "Probing 3 harnesses…".
3. The result arrives as a new state. Then the line counts down: "You can probe again in
   42 s."

While a probe runs or the minute has not passed, every probe button has `aria-disabled`
and stays in the tab order; pressing one announces the line instead of sending a request.
The countdown follows "Countdowns" in `live-stream.md`: one timer, once a second, only
while it counts and the page is open. When the console refuses a probe, its message shows
as text under the button and the buttons work again.

## State text is text

Names, descriptions, fix text, sign-in methods, paths, notes, problems and skipped folders
come from the registry, from harnesses and from files on disk. They are set with
`textContent`, cut to one line, stripped of control and bidirectional characters and
bounded. Backticks in fix text mark a command name; the span becomes a `<code>` element
whose content is again text.

A command is offered to copy only when it is one line of at most 512 characters without
control or bidirectional characters, exactly as sent; otherwise it is not shown. A fix link
is made only from an absolute `https` address without credentials; its text is the address
itself, and it opens in a new tab. Only an ID of the form `^[a-z][a-z0-9-]{0,63}$` is sent in
a probe request.

## Page states

| Case | The page shows |
| --- | --- |
| Before the first snapshot | "Waiting for live updates…" |
| Live, but no `harnesses` state | "The live stream has no harness state for this repository yet. …" |
| The daemon runs no detection (`detector: false`) | an error notice with `agentflow ai harnesses`; no probe button, no check request |
| Nothing found | the panel **No ACP harnesses found on your PATH** with `agentflow ai harnesses --all` and `agentflow ai doctor` |
| Nothing ready, or nothing needs attention | a sentence in that group |
| Automatic probes off | **Off** in the header; unprobed harnesses read **Not checked yet** or **Not probed** with their fix |
| A probe is running | **Probing** on its cards and the line beside **Probe now** |
| Offline with values | the last values and a `.live-note` |

## Accessibility

- The groups are sections with `h2` headings; each card is a list item named by its `h3`;
  sign-in is an `h4`. Details are native `<details>`, **Copy** and **Probe now** are
  buttons, and Settings is a link.
- A command box can take focus, so a keyboard user can scroll a long command.
- When the card that holds focus is removed, focus moves to the page title. A card that
  moves between the groups keeps focus.
- Changes are announced through `#live-announcer` while the page is open: a harness found
  or gone, a new state or sign-in verdict, a finished probe, and the end of detection. At
  most three changes, then "and 2 more changes". The first snapshot is not announced.
- State never depends on colour alone. Reduced motion and forced colours are handled as in
  `live-stream.md`. At narrow widths the facts stack and a command wraps under its button.

## In app.js

`app.js` creates the page and feeds it like the other repository panels:

```js
const harnessesPage = AgentflowHarnesses.createHarnesses({
  root: document, request, announce, settingsRoute: groupRoute,
  visible: () => !$("console-shell").hidden && pages[destination().screen] === "harnesses"
});
```

It also needs `harnesses` in `screenNames` and `pages`, `showRepository("harnesses")` from
`onSnapshot` and `onDelta`, `harnessesPage.show` in `renderRepository`,
`harnessesPage.status` in `renderStream`, `harnessesPage.tick()` in `showPage`, and the
reset on **Resync**. The `harnesses` state has no topic: it rides in the repository's frames
(ADR, "Phase 6 harnesses projection").

`app.js` is not one of the files of the lane that built this page (ADR, phase 6 ownership),
so these lines are listed in `harnessesWiring` in `internal/webui/harnesses_browser_test.go`,
and the fixture serves `app.js` with them applied. The rail entry is `hidden` in
`index.html` and `createHarnesses` shows it, so the entry appears only in a console whose
`app.js` creates the page. `agentflow ui --screen harnesses` also needs `harnesses` in
`internal/webui/destination.go`. When `app.js` has the lines, `wireHarnesses` serves it
unchanged.

## Tests and the fixture

`go test ./internal/webui -run TestHarnessesBrowser` runs
`testdata/harnesses/harnesses-test.js` in a browser (set `AGENTFLOW_TEST_CHROME`) against a
scripted stream and fake endpoints. It covers the reader, both groups, every state and
sign-in verdict, the fix and sign-in commands and **Copy**, the probe and check requests
and their limits, a card that moves and keeps focus, hostile state text, every page state,
and that nothing is sent while the page only shows. No test starts a harness. To look at
the page without a backend:

```bash
AGENTFLOW_HARNESSES_DEMO=127.0.0.1:41735 go test ./internal/webui -run TestHarnessesDemo -timeout 0
```

Then open `http://127.0.0.1:41735/w/first/`. Add `?view=empty` for nothing found,
`?view=off` for a daemon without detection, `?view=manual` for automatic probes off, or
`?view=probing` for a probe in progress. `testdata/harnesses/demo.js` writes the frames and
answers **Probe now**.
