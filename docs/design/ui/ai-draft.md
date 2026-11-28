# The draft view

The draft view is the **Draft with AI** panel at the top of the AI sessions page (`#ai`).
In it you request a draft, read what would be sent, send it, and read the candidates. It
applies nothing. The contract is in the
[ADR](../../knowledge/engineering/decisions/event-driven-platform.md), section "Phase 8":
"AI projection and console routes" gives the state and the requests, "draft review UI" the
states, and "owner decisions" the three rules below. The view follows
[live stream patterns](live-stream.md) and the look of the [AI sessions page](ai-panel.md).

| File | Holds |
| --- | --- |
| `static/ai-draft.js` | the view: the readers, the features, the request, the list of what would be sent, the drafts and the candidates |
| `static/ai-panel.js` | one line: it creates the view as `drafts` and hands it the helpers that clean text |
| `static/index.html` | `#ai-draft` and the script tag |
| `static/app.js` | feeds the view the `ai` state, the stream's state and the page change; `request` puts a refusal's body on the error |
| `static/style.css` | every rule of the view (`ai-draft-`), tokens only |

## Three rules from the owner

| Rule | How the view keeps it |
| --- | --- |
| The console starts drafts and applies nothing | No button applies a draft. Each candidate shows its command to copy into a terminal |
| Your click on the list of what will be sent is the confirmation | Nothing is sent to get the list. The list is one box, in the warning tone, that takes focus. **Send** is its last control |
| Nothing is sent for a feature without a harness | A feature that is off has no **Draft** button. It says why, and links to the AI settings |

## Everything shown comes from the stream

The features and the drafts are `AIState.Features` and `AIState.Drafts` of the repository's
`ai` state (`internal/webui/projection_ai_state.go`). Nothing polls. The view sends three
requests:

| Request | When | Body | Answer |
| --- | --- | --- | --- |
| `POST api/ai/draft` | **Show what would be sent** | `{"feature", "input"}` | `200 {"confirm", "manifest"}`, or `202 {}` when your autonomy settings start drafts without asking |
| `POST api/ai/draft/confirm` | **Send** | `{"confirm"}` | `202 {}` |
| `POST api/ai/cancel` | **Cancel draft** | `{"session"}` | `202 {}` |

Only the first answer carries data: the manifest, which lists what would be sent and never
holds it. The draft itself always arrives over the stream. The page never sees a confirm
token or a prompt.

A refused request answers `409 {"code", "message", "fix"}`. The view shows "Nothing was
sent.", then the console's message and fix, as text.

## Model and repository text is never trusted

The drafted text comes from a model that anything it read can steer. Paths, task titles,
template headings and review text come from the repository and from other people. So:

- All of it is set with `textContent`. Nothing is read as HTML, Markdown is shown as its
  source, and no address becomes a link.
- It sits in a box of its own. The model's words are in a dashed box under **Written by the
  model. Agentflow checked its form, not whether it is right or true.** Names from the
  repository are in a bordered code box. Text in a box cannot pass for a heading, a button
  or a note of the page, and a long text scrolls inside its box.
- Agentflow's own words (headings, labels, state names, the command) never contain it,
  except the command, where it is quoted (below).
- A hidden character that Agentflow wrote as `<U+XXXX>` is drawn as a mark, as on the
  permission cards. One that arrives as itself shows as `�`, and that text is not offered
  to copy.

The helpers that clean text are those of `ai-panel.js`, handed over in `kit`, so both
views treat hidden characters the same way.

## The features

One card for each feature of the state, in its order.

| State | Tag | The card shows |
| --- | --- | --- |
| `available` | Ready | what the draft is for, "Its harness is Claude Agent." and **Draft** |
| `feature_off` | Off | "Off. No harness is chosen for this feature, so nothing can be sent for it.", the fix and **Open the AI settings** |
| `not_ready`, `repo_disabled`, `feature_off_account` | Not ready | the reason in words, the fix and the link |
| a code this console does not know | Not ready | "Not available. Reason code `<code>`." and the fix |

A state without `features` reads "This console's daemon does not offer drafts." An empty
list reads "This build of Agentflow has no draft feature yet."

## Asking for a draft

**Draft** opens the request under the cards and moves focus to its heading.

| Feature | Inputs |
| --- | --- |
| Commit message, PR title and body, Acceptance-criteria check | the hint |
| Ticket | the description (required, at most 16,384 bytes) and the hint. Agentflow uses your default task template |
| Review replies | one thread ID (optional; empty means every open thread) and the hint |

The hint is optional, at most 2,048 bytes. Both limits are counted in bytes as you type.
**Show what would be sent** asks the console for the list. The text under the inputs says
that nothing is sent until you choose **Send**, and that a draft starts at once when your
autonomy settings run `ai.draft` without asking. In that case the view says so, and the
draft's own **What this draft sent** holds the list.

## The list of what would be sent

The list is the one place where you let data leave the computer, so it is built to be read
before it is answered. It is a box with a thick border in the warning tone. From top to
bottom:

| Part | Shows |
| --- | --- |
| Heading and lead | **Read what would be sent**. "Nothing has been sent yet. This is everything a commit message draft would send from this computer. Send starts the draft. Discard sends nothing." |
| **Where it goes** | the harness with its ID and version, the program, "The harness passes it to its model provider. The provider and the harness may keep it, under their own terms.", the worktree, the branch, the time limit, and what happens if a part changes |
| What is held back | one sentence, before the table: "4 files are left out: the path is sent, the content is not. 2 values that look like tokens are replaced with [redacted]. 1 part is cut to fit." |
| **What would be sent** | a table: each part, where it is from and its size. Under a part's name: how much was cut, how many values were replaced, and each file left out with its reason |
| Items | when the draft has targets: their names, as text from the task, the template or reviewers |
| Your hint, your description | your own words in full, as they are sent: hidden characters written out and secrets redacted |
| **Never sent, by any draft** | the fixed list, closed |
| **Prompt fingerprint** | the prompt's SHA-256, closed |
| Time, buttons, status | "Send within 4 min 54 s. After that, nothing is sent.", **Discard** and **Send to Claude Agent** |

- **Discard** and **Send** have the same weight and sit at opposite ends. At narrow widths
  they stack, **Discard** first.
- **Send answers only when it has stood still for 1.2 s.** A click on a button that just
  appeared or moved does nothing and reads "The button just appeared. Read the list, then
  choose again." The view looks where the button is once a second, so a move that is over
  refuses nothing.
- When the five minutes run out, the view says so once, **Send** goes away, **Discard**
  reads **Back to the request**, and focus moves there if it was on **Send**.
- **Send** is absent, and the box says why, when the page cannot show every part (a part
  without a size or a name, more than 32 parts, no harness, no confirmation), and when the
  five minutes ran out.
- **Discard** returns to the request with your inputs. The console's confirmation expires
  by itself.
- If the context changed since you saw the list, the console sends nothing
  (`operation_changed`). The view returns to the request with its message, and your inputs
  are still there.
- While live updates are offline: "You can still send, but the draft shows here only when
  they return."

After **Send** the view reads "Sent to Claude Agent. The draft appears under Drafts when it
starts." The draft arrives over the stream, is selected, and takes focus if focus was
still on the panel's heading.

## The drafts

Drafts come newest first, at most 20, in a list like the session list. The view chooses a
draft only when you have none: the first one that still runs, else the newest. A live
update never changes the draft you are reading.

| State | Tag | Tone | You can |
| --- | --- | --- | --- |
| `starting` | Starting | `--info` | **Cancel draft** |
| `running` | Writing | `--info` | **Cancel draft** |
| `cancelling` | Cancelling | `--warn` | wait |
| `ready` | Ready to read | `--ok` | read each candidate, copy its text or its command |
| `invalid` | Not usable | `--warn` | read the issues and the model's answer, copy the answer |
| `failed` | Failed | `--bad` | read the reason, **Request a new draft** |
| `cancelled` | Cancelled | neutral | **Request a new draft** |
| a state this console does not know | Unknown state | neutral | read the reason code |

A failed draft says why in words, for example "The harness needs a sign-in." Add a row to
`failures` in `ai-draft.js` when a lane adds a code; an unknown one shows as "Reason code
`<code>`."

**Cancel draft** sends at once: a draft changes nothing, so there is no second question.

Every draft shows who requested it, the harness, the worktree, the branch, the times, its
ID, and a closed **What this draft sent** with the same list as above. It also shows the
draft's warnings, and "The harness asked 2 times for a file, a command or a tool. A draft
gets none, so Agentflow refused."

## A candidate

| Part, top to bottom | Shows | Whose words |
| --- | --- | --- |
| Heading | "Commit message", "Review reply", … | Agentflow's |
| For | the thread, criterion or section it is for | the repository's or a reviewer's |
| **Written by the model. …** | each drafted field: its name, its size, **Copy text** and the text in a box that scrolls and takes focus | the model's |
| **Look at this before you use it** | the warnings: a mention, a link | Agentflow's |
| **To use it, copy this command and run it in bash, zsh or fish** | the command, **Copy command**, and what running it does (below) | Agentflow's |

- **The command** is the candidate's `cli`, quoted for bash, zsh and fish. A word of plain
  characters is written as it is, unless it starts with `=` or `%`, which zsh and fish
  expand. Any other word is written in pieces: text in single quotes, and each run of
  single quotes and backslashes in double quotes with the backslash doubled (`'` becomes
  `"'"`, `\` becomes `"\\"`). No quote or backslash is left inside single quotes, because
  fish reads `\'` and `\\` there as escapes. So `$(…)`, backticks, line breaks and
  `x\'; touch p; echo '` in the drafted text stay text in all three shells.
- **On Windows no command is offered.** PowerShell and cmd read quotes in other ways, and
  PowerShell takes curly quotes as quote marks, so no line is safe to paste. The card says
  so and shows the command's form, for example `agentflow commit --prepare -m MESSAGE`.
  **Copy text** stays on each field.
- **What running it does** is worded for the operation. `commit.prepare` and `pr.prepare`:
  "This command only prepares: it shows what it would do and changes nothing." Every other
  operation: "This command runs under your autonomy settings, and may act without asking
  you first." Both end with "This page runs nothing."
- The command is not offered when the candidate has issues: **Agentflow does not offer this
  candidate** lists them. A text Agentflow took out reads "Agentflow took this text out."
- The command is also not offered when the page cannot show all of it as it is: a first
  word that is not `agentflow`, more than 64 words or 70,000 characters, or a hidden
  character. The card then shows `agentflow ai status <id>` to read the draft in a terminal.
- What the model reported and Agentflow did not make a candidate of (a criterion that is
  not met) is listed after the candidates.

## Page states

| Case | The view shows |
| --- | --- |
| Before the first state | nothing; the page reads "Waiting for live updates…" |
| No draft yet | the features and "No draft has been requested in this repository yet." |
| Offline with values | the last values. The list and a draft that still runs say that the page may be behind |
| A draft that is no longer kept | the view moves to another draft; focus goes to the panel's heading |
| A feature that goes away while you fill in its request | the request closes and the view says so |

## Accessibility

- The panel is a section named by its `h2`. The features are a list; each card has an `h3`.
  The request, the list and the selected draft are named by their `h3`; a draft's parts
  have `h4` and a candidate `h5`.
- Focus follows what you asked for: the request's heading after **Draft**, the list's
  heading after **Show what would be sent**, the feature's button after **Close**, the
  draft's heading when you choose a draft. Focus never moves by itself, except from the
  panel's heading to the draft you just sent.
- The table of parts has a caption, column headers and a row header for each part.
- Status lines are `role="status"`. Announced through `#live-announcer`: the list ("Nothing
  has been sent yet. Read what this draft would send, then choose Send or Discard."), a
  draft that started and a change of state. A draft you sent from this page is announced
  on every page when it finishes.
- Text boxes that scroll take focus and have a name. Every action is a `button`.
- State never depends on colour alone. Light and dark share one set of rules, on tokens
  only. Forced colours keep every border.
- Below 65rem the list of drafts moves above the draft. Below 38rem the two buttons stack
  and a file left out shows its reason under its path.

## In app.js

`AgentflowAI.createAI` creates the view as `aiPage.drafts`. `renderRepository("ai")` calls
`aiPage.drafts.show`, `renderStream` calls `aiPage.drafts.status` and `showPage` calls
`aiPage.drafts.tick()`. The stream's topics do not change: a draft publishes every
`ai.session.*` event, and the host writes the result before the session ends, so the `ai`
state that follows `ai.session.ended` holds it.

## Tests and the fixture

`go test ./internal/webui -run TestAIDraftBrowser` runs `testdata/ai-draft/ai-draft-test.js`
in a browser (set `AGENTFLOW_TEST_CHROME`) against a scripted stream and fake endpoints. It
covers the readers, the shell quoting (each line is read back by a real bash, zsh and fish
when they are installed, as arguments of `printf`), Windows, the end of the time to send, each feature state, the request and its limits,
refusals, the list and what can be sent, a button that just appeared, Discard, a manifest
that changed, a draft that starts at once, Cancel, the candidates with hostile text, every
draft state, a lost connection, and that the page sends nothing but the three requests. No
test starts a harness, a session or a draft. To look at the view without a backend:

```bash
AGENTFLOW_AI_DRAFT_DEMO=127.0.0.1:41737 go test ./internal/webui -run TestAIDraftDemo -timeout 0
```

Then open `http://127.0.0.1:41737/w/first/`. Add `?view=empty` for no draft, `?view=off`
for every feature off, or `?view=auto` for a console that starts drafts without asking.
`testdata/ai-draft/demo.js` writes the frames and answers **Send** and **Cancel draft**.
