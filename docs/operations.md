# devflow operations

## Org secrets and variables

Set at the `nscaledev` org level, visible to repos that use devflow. The caller
workflow passes each reusable workflow only the secrets it uses (no `secrets: inherit`).

| Name | Kind | Purpose |
|---|---|---|
| `JIRA_BASE` | variable | `https://nscale.atlassian.net` |
| `CONFLUENCE_BASE` | variable | Same site URL (pages live under `/wiki`) |
| `JIRA_USER` | secret | `svc-devflow` account email |
| `JIRA_TOKEN` | secret | Its scoped API token (Jira + Confluence) |
| `SLACK_WEBHOOK_URL` | secret | Default webhook; override per repo for team channels |
| `ANTHROPIC_API_KEY` | secret | AI assist (P9), limited to opted-in repos |
| `DEVFLOW_APP_ID` | variable | devflow GitHub App id |
| `DEVFLOW_APP_PRIVATE_KEY` | secret | devflow GitHub App private key |
| `JIRA_AC_FIELD` | variable (optional) | Custom field id for acceptance criteria, e.g. `customfield_10050` |
| `DEVFLOW_ENABLED` | repo variable | `true` to enable transitions; `false` is the kill switch |

## Actions access

`nscaledev/devflow` is an internal repository. In its **Settings › Actions › General ›
Access**, choose *Accessible from repositories in the nscaledev organization* so other repos
can call its reusable workflows and actions.

## GitHub App

Create an org-owned GitHub App named **devflow** (no webhook):

- Repository permissions: Contents read & write, Pull requests read & write,
  Dependabot alerts read, Metadata read.
- Install it on `nscaledev/devflow` and on repos that use `auto-issues`.
- Store the app id in `DEVFLOW_APP_ID` and a private key in `DEVFLOW_APP_PRIVATE_KEY`.

Consuming repos' `semver-release` workflow uses it to create and auto-merge their
release-please Release PR, because PRs merged with `GITHUB_TOKEN` don't trigger other
workflows. devflow's own releases don't need it: they tag the merge commit directly with
`GITHUB_TOKEN`.

## Atlassian service account

1. Create `svc-devflow@nscale.com` as an Atlassian service account (or a managed user).
2. Give it access to the opted-in Jira projects (Browse, Transition, Comment, Create, Edit,
   Manage versions) and Confluence spaces (View, Add pages, Edit).
3. Create a scoped API token with Jira and Confluence read/write scopes. Store it as `JIRA_TOKEN`.
4. Rotate every 90 days: create a new token, update the secret, run any devflow workflow
   with `workflow_dispatch` to confirm, then revoke the old token.

## Developer Atlassian login

`devflow login` stores each developer's own Atlassian API token in the OS keychain
(macOS Keychain, Secret Service, Windows Credential Manager), so Jira and Confluence
history shows the real person. Developers create the token at
https://id.atlassian.com/manage-profile/security/api-tokens and paste it once;
`devflow login --token-stdin` reads it from stdin for scripted setups. `devflow logout`
removes it.

### Planned: OAuth 2.0 (3LO)

To replace personal API tokens with a browser login, register an OAuth 2.0 integration
named **devflow** in developer.atlassian.com › Console with Jira and Confluence read/write
work scopes plus `offline_access`, and a loopback callback such as
`http://127.0.0.1:47823/callback`. Revoking the app would cut off every install at once.
This is not implemented in the binary yet; `internal/auth` already supports bearer tokens.

## Releases

Releases are automatic and published as stable (see CONTRIBUTING.md). Each release also
moves the floating major tag that consuming repos use: `v0` while devflow is 0.x, `v1`
from 1.0.0 (`nscaledev/devflow/...@v0` for reusable workflows and actions, and
`nscaledev/devflow@v0:flow-map.yml`). To move it by hand:

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
4. Repos needing an immediate workaround can set `update.pin` in `.devflow/config.yml`.

## Troubleshooting

- `devflow doctor` checks the binary, hooks, auth, client config and API reachability.
- Logs: `~/.devflow/audit.log` (actions) and the job summary of each workflow run.
- Transitions do nothing: confirm the project is in `flow-map.yml`, `DEVFLOW_ENABLED` isn't
  `false`, and the target status is reachable from the current one in that workflow.
