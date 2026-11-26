# Repository console

Run `agentflow ui` from the checkout you want to manage, or ask your local AI
harness to open a agentflow screen. One daemon runs under your OS account. It
serves the configured repositories directly on the loopback interface.

## Open a screen from chat

Ask Agent Commander **“Open the task management provider editor for this
repo.”** Its coordinator calls `agentflow_ui_open` in the worktree selected for
the chat. This opens **Providers → Task management**. You can also ask for
Overview, Autonomy, Configuration or Handoff. The tool defaults to Providers and accepts
an optional provider role.

```json
{"screen":"providers","role":"task_management"}
```

Use `agentflow ui --screen providers --role task_management` for the same
destination from a terminal. An authorized harness opens the browser locally.
Chat receives the screen, role and reuse status. Agent Commander registers
`agentflow mcp --ui-only --client agent-commander --auto-open` for its lead
agent. The capability check omits `--auto-open` and exposes only
`agentflow_ui_open`.

## Repository addresses and tabs

The daemon assigns one hostname to each local clone. The first clone of
agentflow might be `http://agentflow.localhost/`. A linked worktree gets a
distinct path under the same hostname. A second clone with the same name
gets a hostname with a short suffix. The daemon keeps the mapping in the
developer’s private profile, so it survives restarts. No repository file or
system DNS entry is needed. Browsers must support loopback `.localhost`
subdomains.

On first launch agentflow tries port 80 so the usual address has no visible port. When it
cannot bind that port, it selects an available loopback port and shows it in
the URL. On subsequent starts it tries the remembered port first to keep
bookmarks and browser drafts at the same address. Use `--port N` to
choose a specific port when starting the daemon. `agentflow ui --status`
reports the listening port. `agentflow ui --stop` stops the daemon for all
clones. The next eligible launch starts it again. An ordinary `agentflow ui`
returns to your shell and leaves the daemon running when the terminal closes.
Use `--foreground` to supervise a new daemon from the terminal and stop it
with Ctrl+C.

Recognized local desktop and interactive terminal sessions open one tab per
clone. Headless sessions, CI jobs and delegated workers skip automatic opening.
Opening another chat leaves
a live tab on its current screen. An explicit open request directs the tab to
the requested screen and worktree. Bringing the tab to the foreground depends
on the browser. Dirty worktrees retain their own staged files and unfinished
editor controls on this browser. For example, you can leave a provider list
half-edited, work in another checkout, and return to the open field and its
unsubmitted rows. The source selection, field search, and publication inputs
are also restored. If the underlying file changes, the editor keeps the draft
for recovery and requires you to review the new source before publishing. Save or discard personal or
repository Autonomy form edits before switching worktrees.

To try this with two linked worktrees, open the provider editor from the chat
for the first one and type into a list row. Leave the field open. Ask the chat
for the second worktree to open its configuration editor, then ask the first
chat to reopen its provider editor. The tab returns to the original checkout
with the unfinished row still editable. Restoring a draft never stages or
publishes it.

## Reopen a bookmark or reconnect

After an authorized launch, this browser can revisit registered worktrees of
the same clone for 30 days. The initial one-time link goes straight to the
browser. A fresh profile, expired authorization or a different origin can show
the reconnect screen. Copy the suggested prompt into the chat for that
worktree, or run the displayed command from its terminal. Use
`agentflow ui --no-open` to request a fresh one-time link for a specific browser.
Keep that link on your machine. Private browsing and clearing site data
remove browser-local credentials and drafts. A console started with v0.43.0
predates the daemon. Its first launch after upgrading opens the new address.

The repository name and branch stay visible while you navigate. Under
**Appearance**, choose **System**, **Light** or **Dark**. The console remembers
your choice for this browser origin when browser storage is available.

## Configure Agentflow from chat

Use a Agentflow MCP connection for the worktree you want to configure. If the
tools are missing after upgrading Agentflow, reconnect the MCP server or start a
fresh client session in that worktree. For a Codex checkout without a Agentflow
registration, run `agentflow setup plan --client codex` and review the project
registration with `agentflow setup apply --client codex`. Open a fresh session
after registration. [Client setup](../README.md#set-up-your-machine-engineers)

| Ask in chat | What Agentflow does |
| --- | --- |
| “Which setting controls local commits here, and where does it come from?” | Shows your preference, the repository ceilings and the effective rule. |
| “Set my personal commit preference to automatic. Keep the five-minute window.” | Previews the change to your user configuration. On approval, saves it on this workstation. |
| “Change the task provider’s search limit to 25 and open a draft PR.” | Reads the repository settings, stages the typed value, and shows its source diff, effective changes and publication destination before requesting approval. |
| “Show the release units and preview this change to their configuration.” | Reads the registered release configuration by path and previews your proposed source against its current revision. |
| “Check which configured provider connections are ready.” | Runs the checkout’s provider readiness checks and reports connections that need attention. |

`agentflow_config` handles inspection and preparation. `agentflow_config_apply` saves
the reviewed personal change or publishes the reviewed repository file.
The CLI exposes the same operations through `agentflow config OP`. Pass the
remaining fields as a JSON object on standard input or with `--input FILE`.
List the sources and read the repository configuration with these commands.

```sh
agentflow config list
printf '%s\n' '{"path":".agentflow/config.yml"}' | agentflow config read
```

### Change settings as one save

Every option in the personal (`~/.agentflow/config.yml`) and repository
(`.agentflow/config.yml`) layers is a field with an ID `<layer>:<path>`, for
example `personal:autonomy.local` or `repo:task_management.settings.repository`.
List them, with each value and where it comes from:

```sh
agentflow config fields --modified
agentflow config fields --group autonomy --json
```

Change several fields as one save. A path without its layer works when only one
layer has it.

```sh
agentflow config set --dry-run autonomy.local=auto repo:commit.subject_max=60
agentflow config set --unset approval_window autonomy.local=auto
agentflow config undo
```

- A value is literal text for a text field, a comma-separated list for a list of
  text (unless it starts with `[`), and JSON otherwise (`true`, `3`, `{"a": "b"}`).
- `--dry-run` runs the live checks and shows each change, its diagnostics and
  the repository diff. Nothing is written.
- A save is checked again, gated once and always confirmed, also under `auto`.
  Loosening edits (a higher autonomy level, a longer approval window, automatic
  PR publication, coordinator merges, fewer protected branches) are listed first.
- A save that changes `autonomy.*` or `approval_window`, or loosens policy, is
  gated by `console.autonomy` as well as `console.settings`; the stricter rule
  wins.
- The repository diff is left out while any sensitive field is set in the
  file, before or after the save.
- All layers are written or none. If a layer file changes between the
  confirmation and the save, the save fails with `operation_changed`; preview
  again.
- Personal edits apply at once: running consoles and MCP servers reload on the
  `config.layer.changed` event. Provider selections apply after agent sessions
  restart.
- Repository edits go to a draft pull request (default) or `--mode direct`,
  through the same publication rules as `agentflow_config_apply`, without touching
  the worktree. `--mode worktree` writes the file in this worktree instead, and is
  refused on a protected branch.
- `agentflow config undo` reverses the last save in this repository; fields
  changed since conflict unless you pass `--skip-conflicts`. A field whose pull
  request is still open cannot be undone: close the pull request. A second undo
  redoes the save. A `--mode worktree` save is undone only from the worktree
  that wrote it. When GitHub cannot be reached to check the pull request, the
  undo reverts the personal part alone and says so.
- Personal YAML never leaves your machine: previews and the console show field
  values only.

Agents use `agentflow_config_fields`, `agentflow_config_preview`,
`agentflow_config_set` and `agentflow_config_undo` with the same rules. The
console's Settings page calls the same commands through
`GET /api/settings/catalog`, `POST /api/settings/preview`, `POST /api/settings/save`
and `POST /api/settings/undo`, and gets live values from the `settings` stream
projection. Request bodies are JSON of at most 1 MiB. A policy refusal answers
409 with its `code`; any other failure answers 422 with code `settings_failed`
and a fixed message, and the detail goes to the console's log.

### Change a personal preference

Call `agentflow_config` with `{"operation":"autonomy"}` to read the effective
rules and the current `user_revision`. To make local commits automatic, use
the returned revision in a second call to `agentflow_config`.

```json
{
  "operation": "preview_personal",
  "user_revision": "<revision from autonomy>",
  "patch": {"actions": {"commit": "auto"}}
}
```

Review the before and after values. Once the developer approves, call
`agentflow_config_apply` with the same revision and patch and the token from the
preview.

```json
{
  "operation": "save_personal",
  "user_revision": "<revision from autonomy>",
  "patch": {"actions": {"commit": "auto"}},
  "confirm_token": "<token from preview_personal>"
}
```

A patch can change `local`, `remote`, `window` and individual `actions`.
Omitted fields keep their existing values. For example,
`{"actions":{"commit":""}}` returns the commit setting to inheritance.
Agentflow uses the current on-disk policy to review the change. If another editor
updates the personal file, read the new revision and preview your patch again.

### Publish a repository change

1. Call `agentflow_config` with `{"operation":"list"}` to see the registered
   files and provider settings. Use `read` with `path: ".agentflow/config.yml"`
   for the current source and digest. Managed and credential-bearing sources
   appear in the inventory with their contents withheld.
2. For a checkout using GitHub Issues for task management, the following
   `agentflow_config` call sets the search limit to 25. Substitute the source
   and digest from `read`. `value` accepts the JSON type from the setting's
   schema.

   ```json
   {
     "operation": "compose",
     "path": ".agentflow/config.yml",
     "base_digest": "<digest from read>",
     "content": "<source from read>",
     "role": "task_management",
     "edit": "set",
     "setting": "search.limit",
     "value": 25
   }
   ```

3. Preview the result with `agentflow_config`. Copy the edited `content` returned
   by `compose` and retain the original digest through successive edits.

   ```json
   {
     "operation": "preview",
     "path": ".agentflow/config.yml",
     "base_digest": "<digest from read>",
     "content": "<edited source from compose>",
     "title": "chore: adjust issue search limit",
     "mode": "draft"
   }
   ```

4. Review the diff, effective settings and publication destination. After
   approval, call `agentflow_config_apply` with `operation: "publish"`, the same
   path, digest, source, title and mode, and the returned `confirm_token`.
   Supply a conventional title that follows the checkout's commit rules. If the
   proposal includes a `task_ref`, include it in both calls. The default mode is
   `draft`. Request `direct` explicitly to apply the reviewed change to the
   default branch through GitHub's checks and review rules.

Use `compose` with `edit: "remove"` to inherit a provider setting, or
`edit: "select"` to stage a provider switch. Use `stage_autonomy` with a
`patch` to stage repository ceilings. For example, send this to `agentflow_config`
to enable automatic PR publication for eligible personal preferences.

```json
{
  "operation": "stage_autonomy",
  "base_digest": "<digest from read>",
  "content": "<current configuration source>",
  "patch": {"allow_auto_pr_publication": true}
}
```

Preview and publish the returned source through the same repository review
flow. This field belongs to the repository. The personal `push`,
`pr_open_draft` and `pr_open` choices determine which PRs a developer
can publish automatically. [PR publication policy](operations.md#pr-publication-and-its-branch-push)

For other registered editable sources,
supply the complete edited file to `preview`. Each repository publication
covers one file. Use `readiness` to check the providers configured in the
current checkout.

For an approval handoff, the prompt owner records consent for the exact
preview. Another worker can apply that same request with the approved
operation ID as `confirm_token`. A change to the reviewed inputs or an expired
request requires a fresh preview. [Approval handoff](operations.md#prepare-and-apply-an-operation)

## Follow an effective setting back to its source

> **Providers** and **Autonomy** are now groups of the **Settings** page
> ([design](design/ui/settings.md)): each setting has its own control, edits are
> saved together from one Save bar, and **Check connections** is at the end of
> that page. The staging steps in this section and the next describe the earlier
> pages and are being replaced with the settings commands.

Open **Providers**, select a role, then select a setting. For example, open
task management to see which provider receives task operations, what its search
limit is, and which source supplied it. Agentflow uses the same resolver as the
CLI. Starting with compiled defaults and applicable legacy values, it applies
release definitions, repository provider definitions, inline repository
settings, then user provider definitions. A user override can therefore hide a
team change on this workstation. The proposed file diff remains visible even
when the effective value stays the same.

Use **Check connections** to run the existing headless setup checks against
the current checkout. The results identify which providers are ready, what
needs attention, and which connections need verification inside the agent
client. Run the check again after updating your checkout.

For a list such as GitHub Issues `labels`, choose **Edit**, then **Add item**.
Enter one label per row. Move rows to change the order, or remove an individual
entry. Other lists use the control for their item type. For example, a
security findings list offers supported finding types, and webhook status
codes use numeric inputs. Map editors let you add or rename keys and edit
each value. Branch and Git remote fields offer names from the local checkout.

Stage a value to set an inline repository override. Remove the inline
override to inherit the next applicable value. **Stage switch** selects a new
provider for the role and removes its previous inline settings. Return to
**Providers** to configure the staged selection in the same proposal. Preview the
resulting defaults, definitions and source diff before publishing. A switch
routes future operations once the new configuration is in use. Existing provider
records stay at their source. Automatic data migration and a per-repository
scope selector are tracked in [issue #96](https://github.com/nscaledev/agentflow/issues/96).
See [provider definitions and precedence](providers.md#provider-definitions).

## Set approval preferences and ceilings

Open **Autonomy** to inspect the rule, source and review window for each action.
Use **Find an action** to look up a name such as `commit`. Actions with both local
and remote provider routes show a rule for each class. Mandatory or conditional
review requirements appear alongside the action.

Under **Personal preferences**, choose **Automatic** for **Create a local
commit** and choose **Save preferences** to apply that choice for your user.
The repository ceilings still apply. The personal form saves immediately to
the Agentflow user configuration. Restart an existing Agentflow MCP server to use
the updated settings.

Under **Repository ceilings**, choose a class or an action, then choose
**Stage repository settings**. **Review staged configuration** opens the shared
source editor with the change alongside any other staged edits to the same file.
The effective rules on the Autonomy page then reflect that proposed configuration.
To permit automatic publication of a draft or ready PR with its branch push,
select **Allow automatic PR publication** under **Repository ceilings**.
Preview and publish the change through the repository review flow below.

Choose **Inherit** to remove a class or action setting. Leave **Approval window**
empty to inherit the duration, or enter a duration from `1m` to `1h`.
The personal window defaults to five minutes. A repository can set a shorter
maximum. **Discard form edits** restores the selected scope to its last loaded
or staged values.

## Edit and publish a repository file

1. Open **Configuration** and select a file. The inventory includes existing
   repository settings, provider definitions, release units, flow maps, task
   templates, document mappings and deployment environments. Workflow and client
   registration files appear with their ownership. Use `agentflow init` to set
   up a repository that does not yet have configuration.
2. Use **Fields** to find a setting, edit it with its typed control and stage
   it. For a task template, for example, you can edit the ordered list of
   sections and their headings. Use **Source** for direct YAML or JSON edits.
   **Suggest at cursor** or Ctrl+Space offers completions where the editor can
   identify the field. Both views use the same draft. Enter a
   conventional commit title and, if appropriate, a qualified task reference.
   Each proposal applies to one file. Keep credentials in their credential
   store. The editor withholds sources that appear to contain credentials.
3. Preview the patch, effective changes and destination. The effective preview
   reflects this workstation. A concurrent edit, a different draft choice, or
   a changed review requires a fresh confirmation. Browser drafts are saved locally for the worktree
   until publication or discard. Use **Download patch**
   in the preview to save a diff for your usual Git review process.
4. Choose a draft PR for team review, or confirm **Apply to default branch** for
   immediate delivery through GitHub. The publisher verifies the default
   branch and relevant source inputs, creates or recovers a dedicated branch
   and PR, and requests a squash merge when GitHub reports the PR is ready.
   When checks or reviews are outstanding, it requests native auto-merge if
   the repository enables it. Follow the PR link to see what is pending.
   Uncommitted work in the checkout stays local. After merge, update your
   checkout using your normal Git workflow to pick up the team change.

Publication requires matching source and target repositories on github.com,
access to the default branch configuration, and GitHub credentials with the
appropriate branch and pull request permissions. The current repository and
user autonomy settings apply. A repository with remote actions set to
`suggest` can still preview a change. Existing branch rules and required
checks govern when it can merge. The preview lists local workflow triggers.
At publication agentflow checks GitHub Actions and the rules on both branches.
When Actions is active and both branches are confirmed to be unprotected and
free of applicable rules, the publisher requests `[skip actions]` for eligible
push and pull request workflows. Otherwise it uses normal triggers. Other
event types, branch creation and external CI can still run.
[GitHub Actions skip behavior](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/skip-workflow-runs)

## Hand off work

**Overview** reports the branch, revision, changed paths, configuration
problems and recognized workflow pin. The branch, revision, changes and
configuration problems update on their own while the console is open; the
indicator beside **Resync** shows whether those updates are live, reconnecting,
resyncing or offline. **Resync** reloads the checkout and restarts the live
updates.

**Activity** lists the repository's open GitHub PRs with their checks, review
and merge readiness and the PR each depends on, the issues bound to its
worktrees, and recent commits and blocked or failed hooks. It also shows
GitHub's state: up to date, not connected, unavailable, or rate limited with
the time Agentflow retries. **Approvals** lists the requests that wait for an
answer with the time each has left, and the outcomes of the last 24 hours.
Answer an approval in the agent session or terminal that asked for it. Both
panels update on their own; a changed value is highlighted for two seconds and
announced to screen readers.

**Timeline** is a live feed of the operations run in the repository from the
command line, from agents and from the console. Each operation is one entry
with its steps and their times: requested, waiting for approval, approved, then
applied, failed, suggested only, cancelled or expired. An entry names who ran
it, its worktree, task and branch, and links to the commit, pull request or task
it touched. A failed operation says why in a sentence. An operation that waits
has **Show in Approvals**, which moves to its request. Filter the feed by
worktree, state and origin; it starts with this worktree. **Copy ID** copies the
operation ID, which is on the operation's line in `~/.agentflow/audit.log` and in
`agentflow events`. The feed keeps the 50 newest operations of the last 7 days.
It fills as Agentflow's commands start to record `op.*` events; a command that
does not record them yet does not appear.

If Agent Commander opened the console, **Needs attention** also lists pending
worker questions from this exact worktree. They load with the checkout and
again on **Resync**. Review the question and confirm your
answer to send it to the worker. These operations use the permissions of the
Agent Commander connection that opened the worktree. A remembered browser can
continue editing configuration after that connection expires. To reconnect
worker activity, follow the instruction on Overview to open a fresh local
link from the intended worktree. **Handoff** provides a copyable summary for
the next developer or agent; its pull requests follow the live updates. The local configuration editor also works with
Agent Commander disconnected.

## Update the console design tokens

The Go binary embeds the published `package/tokens.css` from
`@nscaledev/design-tokens@0.1.0` as `internal/webui/static/nscale-tokens.css`.
This keeps both themes available offline. The Nscale Design System MCP's
`list_design_packages` tool reports the package version and archive SHA-256.

To reproduce the checked-in asset, run these commands from the repository root
with GitHub Packages read access. You can also use the same archive from the
design-system plugin's `vendor/` directory.

```bash
token_dir=$(mktemp -d)
npm pack @nscaledev/design-tokens@0.1.0 --registry=https://npm.pkg.github.com --pack-destination="$token_dir"
token_archive="$token_dir/nscaledev-design-tokens-0.1.0.tgz"
printf '%s  %s\n' cf7592b43e4a0fe0f2fdee54213b7b23b5267203e9866733cd6abcc1f4435362 "$token_archive" | shasum -a 256 -c - &&
  tar -xOzf "$token_archive" package/tokens.css > internal/webui/static/nscale-tokens.css
```

For a package upgrade, update the version and archive checksum together,
replace the stylesheet, and check the console in light and dark mode.
