# Repository console

Run `devflow ui` in a Git worktree. Devflow opens a local browser tab for that
checkout. You can also pass a directory, use `--port N` for a specific loopback
port, or use `--no-open` to open the printed URL yourself. Keep that URL on
your machine. Its launch code is single-use. Refreshing the same tab preserves
the session. Stop the process with Ctrl+C.

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

Stage a typed value to set an inline repository override. Remove the inline
override to inherit the next applicable value. **Stage switch** selects a new
provider for the role and removes its previous inline settings. Return to
**Providers** to configure the staged selection in the same proposal. Preview the
resulting defaults, definitions and source diff before publishing. A switch
routes future operations once the new configuration is in use. Existing provider
records stay at their source. Automatic data migration and a per-repository
scope selector are tracked in [issue #96](https://github.com/nscaledev/devflow/issues/96).
See [provider definitions and precedence](providers.md#provider-definitions).

## Edit and publish a repository file

1. Open **Configuration** and select a file. The inventory includes existing
   repository settings, provider definitions, release units, flow maps, task
   templates, document mappings and deployment environments. Workflow and client
   registration files appear with their ownership. Use `devflow init` to set
   up a repository that does not yet have configuration.
2. Edit the source, or use a draft prepared by the provider controls. Enter a
   conventional commit title and, if appropriate, a qualified task reference.
   Each proposal applies to one file. Keep credentials in their credential
   store. The editor withholds sources that appear to contain credentials.
3. Preview the patch, effective changes and destination. The effective preview
   reflects this workstation. A concurrent edit, a different draft choice, or
   a changed review requires a fresh confirmation. Browser drafts live in the
   current page until publication, discard or reload. Use **Download patch**
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
answer to send it to the worker. **Handoff** provides a copyable summary for
the next developer or agent. The local configuration editor also works with
Agent Commander disconnected.
