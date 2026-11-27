# The AI sessions page

The AI sessions page (`#ai`) shows this repository's ACP sessions as they run. On it you
answer what an agent asks and cancel a session. It starts no session. The contract is in
the [ADR](../../knowledge/engineering/decisions/event-driven-platform.md), section
"Phase 7": "AI projection" gives the state, "AI panel" the page, and "who may start a
session and who may answer" the rules. The page follows
[live stream patterns](live-stream.md) for everything it shows.

| File | Holds |
| --- | --- |
| `static/ai-panel.js` | the page: the state reader, the session list, the selected session, the permission cards, the two requests |
| `static/index.html` | the rail entry, `#page-ai` and the script tag |
| `static/app.js` | creates the page, subscribes to `*/*/ai.session.**` and feeds the page the `ai` state |
| `static/style.css` | every rule of the page (`ai-`), tokens only, and the rail entry's icon |

## Everything shown comes from the stream

Every value is the repository's `ai` state on the event stream (`AIState` in
`internal/webui/projection_ai_state.go`). Nothing polls and the page has no refresh
button. The page sends two requests, and neither answer carries session data:

| Request | When | Body |
| --- | --- | --- |
| `POST api/ai/respond` | **Allow once** or **Reject** on a permission card | `{"session", "ask", "decision": "allow" or "reject", "fingerprint"}` |
| `POST api/ai/cancel` | **Cancel the session**, after the second question | `{"session"}` |

The result of both arrives as a new state. The page has no form, no input and no button
that starts a session (owner decision 10): the panel **Start a session in a terminal**
shows `agentflow ai run --harness HARNESS "PROMPT"` to copy.

## Agent text is never trusted

A session's messages, thoughts, tool call titles, option names, paths, command lines,
diffs and terminal output come from a model that anything it read can steer. So:

- All of it is set with `textContent`. Nothing is read as HTML, and Markdown is shown as
  its source.
- No address becomes a link. A reader who wants to open one copies it.
- Control characters and the characters that reorder text are removed from one-line text
  and shown as `�` in text of several lines, although the session store already removes
  them.
- The page loads nothing an agent names: no image, no icon, no frame.
- Agentflow's own words (headings, labels, reasons, state names) never contain agent text.

The page runs under the console's content security policy. It uses no inline style and no
inline script.

## The session list

Sessions come newest first, at most 20. Each row is a button and shows the harness name,
the state tag, "Started by you" or "Started by an agent" with the time, the branch, the
worktree (`~/…`) and, in the warning tone, "2 requests wait for your answer".

The page chooses a session only when you have none: the first one that waits for an
answer, else the first one that still runs, else the newest. After that the choice is
yours: a live update never changes the session you are reading. The row of the selected
session has `aria-current="true"`. The rail entry carries the number of waiting requests,
with its sentence for screen readers.

The summary line reads "3 sessions · 1 running · 2 requests wait for your answer. Your
user runs 1 of at most 2 sessions on this computer."

## States

The tag's text carries the state; the tone and the mark's shape repeat it.

| State, code | Tag | Tone | Mark |
| --- | --- | --- | --- |
| `starting`, `initializing`, `creating` | Starting | `--info` | hollow circle, pulsing |
| `running` | Running | `--info` | hollow circle, pulsing |
| `waiting` | Waiting for you | `--warn` | half-filled circle |
| `cancelling` | Cancelling | `--warn` | half-filled circle |
| `ended`, `end_turn` | Finished | `--ok` | filled circle |
| `ended`, `cancelled` | Cancelled | neutral | hollow circle |
| `ended`, `max_tokens`, `max_turn_requests`, `limit_duration`, `limit_idle`, `limit_output` | Stopped | `--warn` | half-filled circle |
| `ended`, `refusal` | Refused | `--warn` | half-filled circle |
| `ended`, `unasked` | Stopped | `--bad` | filled square |
| `ended`, `launch`, `protocol`, `protocol_version`, `timeout`, `auth_required`, `exited`, `host_lost` | Failed | `--bad` | filled square |
| `ended`, a code this console does not know | Ended | neutral | hollow circle |

A sentence under the session's name says what the state means, for example "The harness
needs a sign-in. Sign in to it, then start the session again." An unknown code is shown as
"Reason code `<code>`." Add a row to `endings` in `ai-panel.js` when a lane adds a code.

## The selected session

From top to bottom:

| Part | Shows |
| --- | --- |
| Head | the harness name, the state tag and the state's sentence |
| Requests | the permission cards, while requests wait (below) |
| Facts | who started it, the limits of a session an agent started, the worktree and the branch. **Session details** holds the ID, the harness ID, the home folder and the times |
| **Cancel session** | while the session can be cancelled (below) |
| Prompt | the prompt as text, in a box that scrolls |
| The agent's plan | each entry with **Not started**, **In progress** or **Done** and its mark; "2 of 4 done" |
| What the agent did | the stream of items (below) |
| Commands Agentflow ran for the agent | terminals no tool call shows: the command line in full, the result and the output |
| Changed files | once the session ended: the files that differed from the last commit, and a sentence that a harness can change files without telling Agentflow |
| Totals | context tokens, the cost "reported by the harness. Agentflow does not check it.", tool calls, and requests asked, allowed and rejected |

The requests sit above everything that grows, so new output never moves them.

## Permission cards

A card is the one place where a person allows something only a person may allow, so it is
built to be read before it is answered.

| Part | Shows | Whose words |
| --- | --- | --- |
| Heading | the kind of request: "Write a file", "Read a file", "Run a command", "Permission to change files", "Permission to fetch from the network" … | Agentflow's |
| **What Agentflow checked** | the command line; the file or files; the folder it runs in; the names of the variables it sets (never values); the worktree; why it asks | Agentflow's, with the paths and the command as Agentflow resolved them |
| Effect | who does it once allowed: Agentflow itself for a file or a command, or "the harness does this with its own tools. Agentflow passes your answer on and cannot check what the harness then does." | Agentflow's |
| **Written by the agent. Agentflow did not check this text.** | the agent's title, and its names for its choices with each one's kind | the agent's |
| Time | "Answer within 4 min 05 s. After that, Agentflow rejects it." | Agentflow's |
| Buttons | **Reject** and **Allow once**, nothing else | Agentflow's |

How the card keeps the two apart:

- Agentflow's facts are in a solid box with upright text. The agent's words are in a
  dashed box, in italics, in quotation marks, under their label. The label is text, so it
  does not depend on the look.
- The agent's title is one paragraph of at most 600 characters. It cannot add a heading, a
  button, a line break or a link, and it never appears in the heading or among the facts.
- An option name such as "Yes, always allow" is shown with its kind, and the card says
  that Agentflow answers only with "allow once" or "reject", whatever a name says.

How the card shows the whole request:

- A path or a command line wraps, keeps its spaces and never scrolls sideways or ends in
  "…", because the hidden part could be the part that matters.
- When the page cannot show a value in full and as it is (too long, or with a control or
  text-reordering character), the card has no **Allow once** and says why. The value shows
  with `�` in place of the character.
- **Allow once** is also absent when `can_allow` is false ("the agent offered no 'allow
  once' choice"), when the kind is one this console does not know, and when the request
  arrived without its fingerprint.

How the card avoids an answer by accident:

- **Reject** and **Allow once** sit at opposite ends of the card. At narrow widths they
  stack, **Reject** first, with a gap.
- A card's buttons wait 1.2 s after any card appears or goes away. A card that goes away
  moves the ones below it, so a click aimed at one card could land on another. A waiting
  button has `aria-disabled`, stays in the tab order, and says "Read the request first.
  The buttons work in a moment." when pressed.
- New cards go below the ones already there. Focus never moves to a card by itself.
- Each button's accessible name says what it answers: "Allow once: Write a file,
  .claude/settings.json".
- After a click both buttons wait and the card reads "Allow once sent. Waiting for the
  session to confirm." The card goes away when the state no longer lists the request. If
  the console refuses the answer, its message shows as text and the buttons work again.
- A request whose time ran out cannot be answered: "The time to answer ran out. Agentflow
  rejects the request."
- The answer carries the request's fingerprint, so the console refuses an answer to a
  request that changed.

The countdown follows "Countdowns" in `live-stream.md`: one timer, once a second, only
while a card counts and the page is open. The last minute shows in the warning tone.

## Cancel

**Cancel session** opens a question in place: "Cancel this session? The agent stops, and
requests that wait for you are rejected. Files it already changed stay as they are."
Focus lands on **Keep it running**. **Cancel the session** sends the request and the line
reads "Cancel sent. The session stops within a few seconds." The button is not shown once
the session is cancelling or has ended.

## What the agent did

The stream draws the session's items, oldest first, in a box that scrolls by itself.

| Item | Shows |
| --- | --- |
| `message` | the text, with its line breaks. Pieces in a row are one text, because an agent sends a message in pieces |
| `thought` | a closed `<details>` named **Thought** |
| `tool` | one card per tool call, at its first item; later items update the card |
| `plan` | "Plan: The plan changed." The plan itself is in its own part |
| `ask`, `answer`, `terminal`, `file`, `mode`, `info` | a label and the item's text |
| `unasked`, `limit` | the same, in the warning or the error tone |
| `usage`, `prompt` | nothing here: they have their own parts |
| a kind this console does not know | "Update" and the item's text |

A tool call card shows the kind (**Read**, **Edit**, **Delete**, **Move**, **Search**,
**Run**, **Think**, **Fetch**, **Switch mode**, **Tool**), the status tag, "The agent's
description:" with the title in italics, the locations, the command with its result when
Agentflow ran one for it, and a closed **Changes to `<path>`** for each diff. **Asked
first** marks a call that went through a request. **Ran without asking** marks one the
harness ran with its own tools although Agentflow's checks would have asked: the card
says that Agentflow could not stop it and to review the worktree.

A diff is built when it is opened: unchanged lines, `-` lines taken out and `+` lines put
in, with long unchanged stretches counted and left out. The sign is text, so the change
does not depend on colour. Terminal output is built when opened too.

**Staying quick.** Each update rebuilds only the blocks whose content changed; the others
keep their nodes, their open details and focus. Diffs and outputs cost nothing while
closed. The page draws at most 400 items and 100 tool calls.

**Following.** While you are at the end of the box, new output keeps the end in view.
When you scroll up or open a diff, the box stays where it is and **Show the latest (3 new
updates)** appears under it. When older items are no longer kept, the part shows
`agentflow ai status <id> --follow` to copy.

## Page states

| Case | The page shows |
| --- | --- |
| Before the first snapshot | "Waiting for live updates…" |
| Live, but no `ai` state | "The live stream has no AI session state for this repository yet. …" |
| No session | "No session has run in this repository yet." and the command to copy |
| Offline with values | the last values, a `.live-note`, and on each card "Live updates are offline, so this request may already be answered." |
| A session that is no longer kept | the page moves to another session; focus goes to the page title |

## Accessibility

- The list is a list of buttons. The selected session is an `article` named by its `h2`;
  its parts are sections with `h3` headings; each card is a list item named by its `h4`.
- The stream box, the prompt, a diff and an output can take focus, so the keyboard can
  scroll them. Each has a name.
- Details are native `<details>`. Every action is a `button`.
- Announced through `#live-announcer`: a new request, on every page ("Claude Agent asks
  for your permission: write a file. Answer it on the AI sessions page"); and while the
  page is open a session that started and a change of state. At most three changes, then
  "and 2 more changes". The stream of output is not announced: it would never stop.
- When a card, a row or the selected session that holds focus goes away, focus moves to
  the session's name or the page title.
- State never depends on colour alone. Reduced motion and forced colours are handled as
  in `live-stream.md`. Light and dark share one set of rules, on tokens only.
- Below 65rem the list moves above the session. Below 38rem facts stack, and the two
  answer buttons stack with a gap.

## In app.js

```js
const aiPage = AgentflowAI.createAI({
  root: document, request, announce,
  visible: () => !$("console-shell").hidden && pages[destination().screen] === "ai"
});
```

`ai` is in `screenNames` and `pages`. `liveTopics` includes `*/*/ai.session.**`: the `ai`
projection follows those events, and the stream sends a projection's state only to a
stream that follows a matching topic. The state arrives through `showRepository("ai")`
from `onSnapshot` and `onDelta`, `renderRepository` calls `aiPage.show`, `renderStream`
calls `aiPage.status`, `showPage` calls `aiPage.tick()`, and **Resync** clears the stored
state.

`ai` joins the console's screens in `internal/webui/destination.go` (lane 7.3), so
`agentflow ui --screen ai` opens the page.

## Tests and the fixture

`go test ./internal/webui -run TestAIBrowser` runs `testdata/ai/ai-test.js` in a browser
(set `AGENTFLOW_TEST_CHROME`) against a scripted stream and fake endpoints. It covers the
reader, the list and the selection, the cards and what may be allowed, both requests and
their limits, the wait after a card appears or moves, hostile agent text, the stream with
200 items, diffs and output, Cancel, every page state, and that the page sends nothing
but the two requests. No test starts a harness or a session. To look at the page without
a backend:

```bash
AGENTFLOW_AI_DEMO=127.0.0.1:41736 go test ./internal/webui -run TestAIDemo -timeout 0
```

Then open `http://127.0.0.1:41736/w/first/`. Add `?view=stream` to watch output arrive,
`?view=ended` for a finished session, or `?view=empty` for no session.
`testdata/ai/demo.js` writes the frames and answers **Allow once**, **Reject** and
**Cancel**.
