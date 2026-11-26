# The Plugins page

The Plugins page (`#plugins`) shows the plugins installed for your user: what each one
is, whether it runs, what it adds and what it may do. The timeline shows the notes plugins
attach to operations. The contract is in the
[ADR](../../knowledge/engineering/decisions/event-driven-platform.md), section "Phase 5".
The page follows [live stream patterns](live-stream.md) for everything it shows.

| File | Holds |
| --- | --- |
| `static/plugins.js` | the page: the state reader, the cards, the countdown, and its wiring |
| `static/plugins.css` | every rule of the page, tokens only |
| `static/index.html` | the rail entry and `#page-plugins` |
| `static/style.css` | the rail entry's icon and count |
| `static/timeline.js`, `static/timeline.css` | plugin notes under a timeline operation |

## The page sends no request

Every value is the repository's `plugins` state on the event stream (`PluginsState` in
`internal/webui/projection_plugins_state.go`). Nothing polls, and the page has no endpoint.

| Action | How |
| --- | --- |
| Enable or turn off a plugin | a link to its Settings group, `#settings?group=plugins.<id>`. The setting `personal:plugins.<id>.enabled` is saved through the Settings review. |
| Install | the command `agentflow plugin add PATH`, shown with a **Copy** button |
| Remove | the command `agentflow plugin remove <id>`, on the card under **Installed files and removal** |

The page never has an install, remove or enable button of its own (ADR, phase 5 decision 3).

## A plugin runs as you

A standing note sits above the cards on every state of the page: "A plugin runs as you and
is not sandboxed." It says that a process plugin can read your files and use the network,
and that the permissions on a card limit what a plugin does through Agentflow, not what its
program does by itself. Do not hide it or move it below the cards.

## One plugin, one card

Cards are in the server's order (by ID). A card is updated in place, so focus, open details
and running highlights survive an update.

| Part | Shows |
| --- | --- |
| Head | the name with a state mark, and the state tag |
| Facts | the ID, the version, the tier (its meaning is the tooltip) and "Running since 14:02" |
| Description | the plugin's own description |
| Detail | one sentence on what the state means and what to do, when there is something to say |
| Enabled | **Enabled** or **Turned off**, "Turned off in this repository", and the Settings link: **Enable in Settings** (primary) or **Open its settings** |
| What it adds | each handler with the events it reacts to, the commands it runs and whether it adds timeline notes; each permission rule with its deny message |
| What it may do | the granted permissions: receive events, publish events, run commands |
| Since the daemon started | events delivered, failed deliveries, events dropped, commands run, annotations added |
| Recent failed deliveries | the plugin's dead letters, newest first: event type, reason, time, event ID |
| Installed files and removal | the content hash, the install time and the remove command |

Dead letters of a plugin that is no longer installed are listed in their own panel below
the cards. The state holds the 20 newest letters of all plugins.

## States

The tag's text carries the state; the tone, the card's left keyline and the mark's shape
repeat it.

| State | Tag | Tone | Mark | Detail |
| --- | --- | --- | --- | --- |
| `installed` | Not enabled | neutral | hollow square | "Installed and turned off. It does nothing until you enable it." |
| `starting` | Starting | neutral | hollow circle, pulsing | "Agentflow is starting it." |
| `running` | Running | `--ok` | filled circle | none, or that it is turned off in this repository |
| `degraded` | Degraded | `--warn` | half-filled circle | "Running, but it missed health checks. Agentflow restarts it if this continues." |
| `backoff` | Restarting | `--warn` | hollow circle, pulsing | "Its program exited. Restarts in 41 s (at 14:02:10). Restart 3 of 5 in 10 minutes." |
| `failed` | Failed | `--bad` | filled square | why, and what starts it again |
| `stopping`, `stopped` | Stopping, Stopped | neutral | hollow | "Turned off." or "Stopped with the daemon." |
| empty (no host) | Not running | neutral | hollow square | "The plugin host is not running, so this plugin does not run." |

The restart countdown follows "Countdowns" in `live-stream.md`: one timer, once a second,
only while a plugin waits to restart and the page is open. A tick is never highlighted or
announced.

Lifecycle and dead letter codes are fixed identifiers. `codes` and `letterCodes` in
`plugins.js` hold a clause for each known code; an unknown code that looks like an
identifier is shown as "reason code x", anything else as "the reason is not readable". Add
a clause when a lane adds a code.

## Plugin text is text

A plugin's name, description, extension names, deny messages and annotation text are
untrusted. They are set with `textContent`, cut to one line, stripped of control characters
and bounded. Event patterns, event types and command names are shown only when they look
like identifiers. The Settings link is built from the plugin's ID, never from a field of the
state, and only for an ID of the manifest's form.

## Timeline annotations

`TimelineOperation.Annotations` are shown under the operation's steps, oldest first, at most
five. Each note starts with "Note from plugin <id>" or "Warning from plugin <id>", so its
text is never read as Agentflow's own. A URL becomes a link only when it is an absolute
`https` address without credentials; the link text is its host and the plugin's ID
("ci.example.com (link from plugin timeline-notes)"). A new note is highlighted like a new
step and is not announced.

## Page states

| Case | The page shows |
| --- | --- |
| Before the first snapshot | "Waiting for live updates…" |
| No plugins | the panel **No plugins installed** with the install command |
| The plugin host did not start (`host: false`) | an error notice, and the installed plugins as **Not running** |
| Offline with values | the last values and a `.live-note` |
| Live, but no `plugins` state | the last values and "The live stream has no plugin state for this repository yet." |

## The rail entry

The **Plugins** entry shows how many plugins are degraded, restarting or failed, as a number
with a sentence for screen readers (", 2 plugins need attention"). It follows the stream on
every page.

## Accessibility

- Each card is a list item named by its plugin's name; its parts have headings.
- The Settings control is a link, **Copy** is a button, and the details are native
  `<details>`. Everything works from the keyboard, and focus is always visible.
- When the card that holds focus is removed, focus moves to the page title.
- Changes are announced through `#live-announcer` while the page is open: a plugin that is
  installed, removed, enabled, running, degraded, restarting, failed or stopped, and new
  failed deliveries. At most three changes, then "and 2 more changes".
- State never depends on colour alone. Reduced motion and forced colours are handled as in
  `live-stream.md`.

## Wiring

This lane owns `plugins.js` but not `app.js`, so the page attaches itself at the end of
`plugins.js`. Each block there is marked `WIRING` and names the change that replaces it.

| Block | What it does now | Replace with |
| --- | --- | --- |
| Announcements | calls `app.js`'s `announce()` when it is loaded | pass `announce` to `createPlugins` from `app.js` |
| The stream | `plugins.js` loads before `app.js` and wraps `AgentflowEvents.connect`, so the one console stream also feeds the page. It adds no topic. | a `plugins` entry in `app.js`'s `onSnapshot` and `onDelta` |
| The route | `app.js` shows the overview for `#plugins`; `plugins.js` shows its own page instead and marks its rail entry | a `plugins` screen in `app.js` and `destination.go` |

Until the route is in `app.js` and `destination.go`, `agentflow ui --screen plugins` does
not exist, and the reconnect screen offers the overview when the session ends on this page.

## Tests and the fixture

`go test ./internal/webui -run TestPluginsBrowser` runs `testdata/plugins/plugins-test.js`
in a browser (set `AGENTFLOW_TEST_CHROME`) against a scripted stream. It covers the cards,
every page state, live changes, the countdown, the Settings link, the commands, hostile
plugin text, that the page sends no request, and the timeline annotations. To look at the
page without a backend:

```bash
AGENTFLOW_PLUGINS_DEMO=127.0.0.1:41734 go test ./internal/webui -run TestPluginsDemo -timeout 0
```

Then open `http://127.0.0.1:41734/w/first/`. Add `?view=empty` for no plugins or
`?view=host` for a daemon without a plugin host. `testdata/plugins/demo.js` writes the
frames.
