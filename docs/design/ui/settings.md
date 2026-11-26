# The Settings page

The Settings page (`#settings`) edits every option of the two `config.yml` layers with a
structured control. Edits collect in one draft, are checked as you make them, and are saved
together after a review. The contract is in the
[ADR](../../knowledge/engineering/decisions/event-driven-platform.md), section "Phase 4".
The page follows [live stream patterns](live-stream.md) for everything it shows from the
stream.

| File | Holds |
| --- | --- |
| `static/settings-widgets.js` | the widget registry: one factory per `FieldMeta.widget` |
| `static/settings.js` | the page: draft, live checks, conflicts, review, undo, filters |
| `static/settings.css` | every rule of the page, tokens only |
| `static/index.html` | `#page-settings` and the review dialog `#settings-review` |

## Requests and the stream

| What | How |
| --- | --- |
| Current values, revisions, problems, the last save, the branch | the worktree's `settings` state on the event stream (`SettingsState`) |
| The field catalog | `GET api/settings/catalog`, once when the page first opens and again when `catalog_revision` changes |
| Live checks | `POST api/settings/preview` with the draft's `ChangeSet`, 250 ms after the last edit |
| Save | `POST api/settings/save`, first without and then with `approval` |
| Undo, Redo | `POST api/settings/undo`, first without and then with `approval` |

Nothing polls. A new state while a draft is open starts one more check, because the
revisions the draft is based on changed.

## Controls

`AgentflowSettingsWidgets.create(field, options)` builds the control for a catalog field
(Factory Method). Every control has the same interface: `node`, `main` (the element that
carries the label and the description), `set(value)`, `get()`, `focus()` and `disable(flag)`.
It reports each edit through `options.change` as `{value}`, `{inherit: true}` or
`{incomplete: "why"}`.

| Widget | Control |
| --- | --- |
| `toggle` | a switch (`role="switch"`) |
| `select`, `provider-picker` | a native select with the schema's `enum`; the first choice is "Inherit" |
| `stepper` | a number with − and + and the field's `min`, `max` and `step` |
| `duration` | an amount and a unit, saved as a Go duration (`5m`); checks `min_duration` and `max_duration` |
| `chips` | a list of entries with a remove button each; an `enum` becomes a select, an integer list takes numbers |
| `kv-table` | a table with a key column and one cell per value or column; column keys are property paths (`native.channel_id`); more than three columns stack into labelled cells |
| `list` | a table with one row per item, with move up, move down and remove |
| `branch-picker` | a text input that offers the checkout's branches, or chips when `picker.multiple` |
| `file-picker` | a path input that offers the registered files matching `picker.glob`; a repository path may not leave the repository |
| `credential-ref` | the name of an environment variable; anything that is not a variable name is refused, so a pasted secret never becomes an edit |
| `text` | a text input with the schema's suggestions |

Rules:

- A field with a structured kind never gets a free-text editor. A value no control can edit
  (a map of free objects) is shown read-only with a pointer to the Configuration page.
- Add a widget kind with `AgentflowSettingsWidgets.register(kind, factory)`. A field
  without metadata takes the first control that suits its schema (`derive`).
- A cell of a table is built by the same registry, so a map of lists holds chips and a map
  of maps holds a nested table.
- An inherited value shows as a placeholder in a typed control, as "Inherit (value)" in a
  select, and as the value itself in a switch, chips or a table.

## A field row

Left: the label (`<label for>`), the description, the layer tag (Personal or Repository,
with the provider), when a change applies, and the field ID in mono. Right: the control,
then:

1. **Source**: "Set in the repository file.", "Not set here. Uses the default: confirm.",
   or "Not in use" for a value of a provider the role does not select.
2. **Ceiling**: when `capped_by` is set, the ceiling's label and the effective value, with
   an amber keyline.
3. **State**: the tag "Edited · Saves as a draft pull request" (where the change will go),
   **Revert** (drop the edit) and **Inherit** (an `unset` edit).
4. **Conflict**: "Changed outside this page: it is now X. Your edit sets Y." with
   **Keep my edit** and **Use the new value**.
5. **Diagnostics** of the last check: a filled square for an error, a hollow diamond for a
   warning, a hollow circle for a note. The control gets `aria-invalid` on an error.

The row's left keyline shows its state: blue for edited, amber for a conflict, red for an
error. A table control takes the full row width.

Only the selected provider's settings show. Choosing another provider in the draft shows
that provider's settings at once. Advanced fields are hidden until **Show advanced** is
ticked, a search finds them, or they are set.

## The Save bar

The bar sticks to the bottom of the window. It appears with the first edit and stays
until the draft is saved or discarded.

- "3 unsaved changes", then the check result ("Checking…", "1 conflict · 1 error ·
  2 warnings", "No problems found"), then where the changes go ("2 to your personal file ·
  1 to a draft pull request").
- **Discard** asks once, clears the draft and moves focus to the page title.
- **Review and save** opens the review.
- The second row is the last save, from `last_save` on the stream: who saved how many
  settings and when, the publication link, and **Undo**, or **Redo** when the record is
  itself an undo. When the save cannot be undone the row says why.

## The review

A modal dialog. It shows, from the last preview:

- each change with its label, ID, destination, value before and after, effective value
  and when it applies;
- a tick for every entry of `confirm`. A loosening change has an amber ground and the tag
  "Loosens policy". **Save** stays disabled until every tick is set;
- other settings whose effective value changes (`effects`);
- for repository changes: the mode (draft pull request by default, this worktree, or the
  default branch), the title and task reference, the destination, and the diff of
  `.agentflow/config.yml`. The worktree choice is disabled on a protected branch;
- a note that personal settings go to `~/.agentflow/config.yml`. Personal YAML is never
  shown.

**Save** sends the change set without `approval`. The console answers with a request to
confirm; the dialog shows its reason and the button becomes **Confirm and save**, which
sends the same change set with the token. Any change to the draft, the mode or the title
drops the token. A refusal comes back as a preview and its diagnostics show in the dialog.
Undo uses the same dialog: it shows what the undo puts back and asks for **Confirm and
undo**. When some settings changed since the save, **Undo the other settings** retries
with `skip_conflicts`.

## Outside edits

When a new state arrives, each edit's `expect` is compared with the field's layer value:

- unchanged: nothing happens;
- the layer now holds what the edit wants: the edit is dropped;
- anything else: the row shows the conflict. **Keep my edit** sets `expect` to the new
  value; **Use the new value** drops the edit.

A field that is not in the draft updates in place with the `live-changed` highlight and
one announcement ("Subject length changed to 80.").

## Search, filters and groups

- Search matches every word against the label, the description, the ID and the provider.
- **Modified only** shows the fields in the draft or set in a layer file.
- The group list comes from the catalog. A group is an address: `#providers`,
  `#providers?role=task_management`, `#autonomy`, and `#settings?group=<id>` for the
  others. The rail's **Providers** and **Autonomy** entries are these addresses, so
  `agentflow ui --screen providers` and old links open the Settings page.

## The draft in the browser

The draft is stored per worktree in `localStorage` under `agentflow.settings:<path>`:
the edits with their `expect`, and the repository mode, title and task reference. It
survives a reload and a session change. A sensitive value is never stored. A draft cannot
authorize a write: every save needs a fresh confirmation.

## Accessibility

- Every control is named by its visible label; its description, source and diagnostics
  are its `aria-describedby`. Table cells are named "column, row".
- Everything works from the keyboard with native controls; focus is always visible.
  Removing a chip or a row moves focus to its neighbour.
- Changes are announced through `#live-announcer`: the first edit, each check result that
  differs from the last, conflicts, outside edits, filter results and saves.
- State never depends on colour alone: the tag text, the diagnostic mark's shape and the
  sentence carry it. Reduced motion and forced colours are handled as in `live-stream.md`.

## Tests and the fixture

`go test ./internal/webui -run Settings` runs `testdata/settings/settings-test.js` in a
browser (set `AGENTFLOW_TEST_CHROME`) against a scripted stream and fake endpoints.
`testdata/settings/catalog.json` is `config.Catalog()` and `config.Groups()` as JSON;
regenerate it when the catalog changes. To look at the page without a backend:

```bash
AGENTFLOW_SETTINGS_DEMO=127.0.0.1:41733 go test ./internal/webui -run TestSettingsDemo -timeout 0
```

Then open `http://127.0.0.1:41733/w/first/`. `testdata/settings/demo.js` answers the
requests in the page.
