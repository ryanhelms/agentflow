# agentflow operations

## Prepare and apply an operation

Use `agentflow policy explain ACTION` in the worktree to inspect the personal
preference, repository ceiling, mandatory review rules and effective approval
window. Local and remote class defaults are set in `~/.agentflow/config.yml`.
Repository ceilings live in `.agentflow/config.yml`. See the
[configuration examples](../README.md#control-agent-approvals).

To prepare a commit without staging files or moving the branch, supply the
message and paths you intend to commit. Omit `--path` to use the staged index.

```sh
agentflow commit --prepare -m "fix(PLAT-123): restore the project path" --path internal/setup/path.go
```

Agentflow runs the repository's configured commit hooks. It also uses Git's
`reference-transaction` hook to verify the approved ref, parents, tree and
message before Git updates the branch. If your configuration sets
`hook.reference-transaction.enabled=false`, Agentflow stops during preparation.

Use `agentflow pr --prepare --draft --title TITLE` to inspect the publication
candidate before pushing or opening the draft. Both preparations return the
same candidate the corresponding apply command checks, including the Git tree
and parent for commits or the head SHA, destinations and PR details for publication.

For a standalone branch push, use `agentflow push --prepare` or
`agentflow_push_prepare`. For an existing draft PR, use
`agentflow pr mark-ready --prepare` or `agentflow_pr_mark_ready_prepare`.
Repeat the CLI command with `--confirm-token TOKEN` to apply either preview.

| Response | Next step |
| --- | --- |
| `prepared` | The policy permits automatic execution. Apply the candidate with the returned token. |
| `needs_confirmation` | The owner shows the summary once. After explicit approval, apply with the returned token or grant approval to a waiting worker. |
| `awaiting_approval` | Another worker owns the prompt. Read `agentflow policy status ID` while waiting for its decision. |
| `suggest` | Present the suggested commands for the developer to run. |

If the owner receives `approval.previously_issued` for the same operation,
reuse the existing question when the user has already seen it.

To apply a reviewed commit, repeat the message and paths with
`agentflow commit --confirm-token TOKEN`. For a reviewed PR, repeat its arguments
with `agentflow pr --confirm-token TOKEN`. A changed candidate or an expired
approval requires a new preview.

A tighter review window shortens an existing approval when the active gate
loads the new settings. Raising the window applies to new approvals.

An operation bound to a native provider connection must also use that exact
authenticated connection. Its approval expires at the earlier of the configured
review deadline and the connection's review deadline. If the provider session
ends or the account changes, prepare a fresh operation in the active session.
Approvals issued by an older version without the shared approval journal need
a fresh preview after upgrading.

For a handoff, the owner can record the user's decision with
`agentflow policy approve ID --confirm-token TOKEN`. The worker reads the status
and submits the same action with the approved operation ID as its confirmation
token. Recording approval and applying it are separate steps. Only one caller
can claim that operation. The MCP equivalents are `agentflow_approval_status` and
`agentflow_approval_approve`. Share the operation ID for coordination and keep
the owner's token private.

A declined interactive prompt or an unanswered prompt at end of input cancels
its request. A worker stops waiting when the reported state is `cancelled`,
`consumed` or `expired`.

## PR publication and its branch push

A PR preview covers the source repository and branch, the exact commit,
the destination repository and base branch, and the PR fields. Use
`agentflow pr --prepare --draft --title TITLE` to preview a draft, or
`agentflow pr --prepare --draft=false --title TITLE` to preview a ready PR.
The MCP equivalent is `agentflow_pr_prepare` with `draft` set to `true` or `false`.

By default, the stricter of the PR and push decisions governs publication.
A repository can keep `policy.max_autonomy.remote: confirm` and set
`policy.allow_auto_pr_publication: true` to permit automatic execution of
this combined operation. Publish the opt-in on the target default branch and
update your checkout before using it. A feature branch proposing the opt-in
follows the existing policy when opening its own PR. Personal preferences must
allow both actions. An explicit action ceiling can tighten either action, and
a repository-wide `suggest` ceiling still applies. Agentflow checks the target’s
current default-branch revision again on apply. Passing reviewers or labels adds
GitHub writes to the operation, so the usual remote ceiling applies to the
entire request. If your checkout lacks the current default-branch
revision, fetch the target remote and prepare the PR again.
[Example configuration](../README.md#control-agent-approvals)

| Operation | Personal actions that must permit automatic execution | Applies the PR opt-in |
| --- | --- | --- |
| Open a draft PR and push its branch | `pr_open_draft`, `push` | Yes |
| Open a ready PR and push its branch | `pr_open`, `push` | Yes |
| Push a branch on its own | `push` | No |
| Mark an existing draft ready | `pr_ready_for_review` | No |

With the example configuration, both PR previews return `prepared` and
`agentflow push --prepare` returns `needs_confirmation`. Run the previews
in the same worktree to see the decisions before sending any changes.
Agentflow rejects publication from a protected source branch.

## Org secrets and variables

Set at the `nscaledev` org level, visible to repos that use agentflow. The caller
workflow passes each reusable workflow only the secrets it uses (no `secrets: inherit`).

| Name | Kind | Purpose |
|---|---|---|
| `JIRA_BASE` | variable | `https://nscale.atlassian.net` |
| `CONFLUENCE_BASE` | variable | Same site URL (pages live under `/wiki`) |
| `JIRA_USER` | secret | `svc-agentflow` account email |
| `JIRA_TOKEN` | secret | Its scoped API token (Jira + Confluence) |
| `SLACK_WEBHOOK_URL` | secret | Default webhook; override per repo for team channels |
| `ANTHROPIC_API_KEY` | secret | AI assist (P9), limited to opted-in repos |
| `AGENTFLOW_APP_ID` | variable (optional) | agentflow GitHub App id |
| `AGENTFLOW_APP_PRIVATE_KEY` | secret (optional) | agentflow GitHub App private key |
| `JIRA_AC_FIELD` | variable (optional) | Custom field id for acceptance criteria, e.g. `customfield_10050` |
| `AGENTFLOW_ENABLED` | repo variable | `true` to enable transitions; `false` is the kill switch |

## Actions access

`nscaledev/agentflow` is an internal repository. In its **Settings › Actions › General ›
Access**, choose *Accessible from repositories in the nscaledev organization* so other repos
can call its reusable workflows and actions.

Without org-level secrets and variables, a repo admin can set the same names on one
repo (Settings › Secrets and variables › Actions) for a pilot.

## GitHub App (optional)

Nothing requires the App. Without it: Dependabot alerts are not copied to Jira (code
scanning alerts still are), and in repos whose branch rules require checks, someone
closes and reopens each Release PR so its checks run. To remove those two gaps, create an org-owned GitHub App named **agentflow** (no webhook):

- Repository permissions: Contents read & write, Pull requests read & write,
  Dependabot alerts read, Metadata read.
- Install it on `nscaledev/agentflow` and on repos that use `auto-issues`.
- Store the app id in `AGENTFLOW_APP_ID` and a private key in `AGENTFLOW_APP_PRIVATE_KEY`.

Consuming repos' `semver-release` workflow then opens their release-please Release PR
with the App, so its checks run (GitHub runs none for PRs opened with `GITHUB_TOKEN`).
agentflow's own releases never use it: they tag with the release deploy key.

## Atlassian service account

1. Create `svc-agentflow@nscale.com` as an Atlassian service account (or a managed user).
2. Give it access to the opted-in Jira projects (Browse, Transition, Comment, Create, Edit,
   Manage versions) and Confluence spaces (View, Add pages, Edit).
3. Create a scoped API token with Jira and Confluence read/write scopes. Store it as `JIRA_TOKEN`.
4. Rotate every 90 days: create a new token, update the secret, run any agentflow workflow
   with `workflow_dispatch` to confirm, then revoke the old token.

## Developer Atlassian login

`agentflow login` stores each developer's own Atlassian API token in the OS keychain
(macOS Keychain, Secret Service, Windows Credential Manager), so Jira and Confluence
history shows the real person. Developers create the token at
https://id.atlassian.com/manage-profile/security/api-tokens and paste it once;
`agentflow login --token-stdin` reads it from stdin for scripted setups. `agentflow logout`
removes it.

### Planned: OAuth 2.0 (3LO)

To replace personal API tokens with a browser login, register an OAuth 2.0 integration
named **agentflow** in developer.atlassian.com › Console with Jira and Confluence read/write
work scopes plus `offline_access`, and a loopback callback such as
`http://127.0.0.1:47823/callback`. Revoking the app would cut off every install at once.
This is not implemented in the binary yet; `internal/auth` already supports bearer tokens.

## Releases

Releases are automatic and published as stable (see CONTRIBUTING.md). Each release also
moves the floating major tag that consuming repos use: `v0` while agentflow is 0.x, `v1`
from 1.0.0 (`nscaledev/agentflow/...@v0` for reusable workflows and actions, and
`nscaledev/agentflow@v0:flow-map.yml`).

`v*` tags are protected by the repository ruleset **Protect release tags**: nobody can
create, move or delete them except repository admins and the release workflow, which
pushes tags with a write deploy key ("release workflow: tags (ruleset bypass)", private
key in the `RELEASE_DEPLOY_KEY` secret). GitHub Actions itself can't be a ruleset bypass
actor. To rotate the key: generate a new ed25519 key pair, replace the deploy key and the
secret, and delete the old key. Once the agentflow GitHub App exists, it can replace the
deploy key as the bypass actor.

To move the floating tag by hand (repository admins only):

```bash
git fetch --tags && git tag -f v0 vX.Y.Z && git push -f origin v0
```

### Block a release

1. Add the bad version to `blocked` in `policy/versions.json` and raise `min_version` to
   the fixed version (or the last good one). Merge.
2. Every install reads the policy on start. A blocked install updates before it runs again,
   or stops with the policy `message` if it can't.
3. If no fixed version exists yet, mark the bad release as pre-release so `latest` points at
   the previous one: `gh release edit vX.Y.Z --prerelease --latest=false` and set
   `gh release edit vPREV --latest`, and move the floating tag back:
   `git tag -f v0 vPREV && git push -f origin v0`.
4. Repos needing an immediate workaround can set `update.pin` in `.agentflow/config.yml`.

## Provider setup and migration

Use `agentflow setup plan --json` to inspect the selected roles and companion
requirements in the current runtime. Perform sign-in in the desktop client,
terminal, or runner that will make the calls. Use `agentflow setup verify` or
`agentflow doctor --providers` to verify that runtime's readiness.

Keep the pinned caller workflow and effective provider settings as the
baseline for the [migration rehearsal](rollout.md#provider-migration-rehearsal).
Use durable receipts where replay spans runner processes. Before assigning a
live writer, verify the proposal and recovery path for each destination.

## Troubleshooting

- `agentflow doctor` checks the binary, hooks, auth, client config and API reachability.
- Logs: `~/.agentflow/audit.log` (actions) and the job summary of each workflow run.
- Transitions do nothing: confirm the project is in `flow-map.yml`, `AGENTFLOW_ENABLED` isn't
  `false`, and the target status is reachable from the current one in that workflow.
