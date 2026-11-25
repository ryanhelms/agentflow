# Repository console

Run `devflow ui` from the checkout you want to manage, or ask your local AI
harness to open a devflow screen. One daemon runs under your OS account. It
serves the configured repositories directly on the loopback interface.

## Open a screen from chat

Ask Agent Commander **“Open the task management provider editor for this
repo.”** Its coordinator calls `devflow_ui_open` in the worktree selected for
the chat. This opens **Providers → Task management**. You can also ask for
Overview, Configuration or Handoff. The tool defaults to Providers and accepts
an optional provider role.

```json
{"screen":"providers","role":"task_management"}
```

Use `devflow ui --screen providers --role task_management` for the same
destination from a terminal. An authorized harness opens the browser locally.
Chat receives the screen, role and reuse status. Agent Commander registers
`devflow mcp --ui-only --client agent-commander --auto-open` for its lead
agent. The capability check omits `--auto-open` and exposes only
`devflow_ui_open`.

## Repository addresses and tabs

The daemon assigns one hostname to each local clone. The first clone of
devflow might be `http://devflow.localhost/`. A linked worktree gets a
distinct path under the same hostname. A second clone with the same name
gets a hostname with a short suffix. The daemon keeps the mapping in the
developer’s private profile, so it survives restarts. No repository file or
system DNS entry is needed. Browsers must support loopback `.localhost`
subdomains.

On first launch devflow tries port 80 so the usual address has no visible port. When it
cannot bind that port, it selects an available loopback port and shows it in
the URL. On subsequent starts it tries the remembered port first to keep
bookmarks and browser drafts at the same address. Use `--port N` to
choose a specific port when starting the daemon. `devflow ui --status`
reports the listening port. `devflow ui --stop` stops the daemon for all
clones. The next eligible launch starts it again. An ordinary `devflow ui`
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
for recovery and requires you to review the new source before publishing.

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
`devflow ui --no-open` to request a fresh one-time link for a specific browser.
Keep that link on your machine. Private browsing and clearing site data
remove browser-local credentials and drafts. A console started with v0.43.0
predates the daemon. Its first launch after upgrading opens the new address.

## Follow an effective setting back to its source

Open **Providers**, select a role, then select a setting. For example, open
task management to see which provider receives task operations, what its search
limit is, and which source supplied it. Devflow uses the same resolver as the
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
scope selector are tracked in [issue #96](https://github.com/nscaledev/devflow/issues/96).
See [provider definitions and precedence](providers.md#provider-definitions).

## Set approval preferences and ceilings

Open **Autonomy** to inspect the rule, source and review window for each action.
Use **Find an action** to look up a name such as `commit`. Actions with both local
and remote provider routes show a rule for each class. Mandatory or conditional
review requirements appear alongside the action.

Under **Personal preferences**, choose **Automatic** for **Create a local
commit** and choose **Save preferences** to apply that choice for your user.
The repository ceilings still apply. The personal form saves immediately to
the Devflow user configuration. Restart an existing Devflow MCP server to use
the updated settings.

Under **Repository ceilings**, choose a class or an action, then choose
**Stage repository settings**. **Review staged configuration** opens the shared
source editor with the change alongside any other staged edits to the same file.
The effective rules on the Autonomy page then reflect that proposed configuration.
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
   registration files appear with their ownership. Use `devflow init` to set
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
At publication devflow checks GitHub Actions and the rules on both branches.
When Actions is active and both branches are confirmed to be unprotected and
free of applicable rules, the publisher requests `[skip actions]` for eligible
push and pull request workflows. Otherwise it uses normal triggers. Other
event types, branch creation and external CI can still run.
[GitHub Actions skip behavior](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/skip-workflow-runs)

## Hand off work

**Overview** reports the branch, revision, changed paths, configuration
problems and recognized workflow pin. **Check activity** adds recent open
GitHub PRs, their review and merge states, and dependencies between visible
PRs. If Agent Commander is running locally, it also lists pending worker
questions from this exact worktree. Review the question and confirm your
answer to send it to the worker. These operations use the permissions of the
Agent Commander connection that opened the worktree. A remembered browser can
continue editing configuration after that connection expires. To reconnect
worker activity, follow the instruction on Overview to open a fresh local
link from the intended worktree. **Handoff** provides a copyable summary for
the next developer or agent. The local configuration editor also works with
Agent Commander disconnected.
