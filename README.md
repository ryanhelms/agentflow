# devflow

devflow joins GitHub, Jira and Confluence into one delivery loop for Nscale engineering.
One commit message moves the ticket, updates the docs and tells the team.

It has two halves that share one status map (`flow-map.yml`):

- **Reusable GitHub Actions** react to code events (branch, PR, merge, deploy, release)
  and update Jira and Confluence.
- **The `devflow` developer plugin**, a single Go binary that is a CLI, an MCP server and
  a set of git hooks. It works inside Claude Code, Codex CLI, Grok CLI, Cursor, Copilot,
  Gemini CLI and a plain terminal, so the agent knows the ticket and writes the commit
  correctly the first time.

The full architecture and rollout plan is in [docs/blueprint.html](docs/blueprint.html).

## Commit convention

```
<type>(<JIRA-KEY>[,<KEY>])[!]: <imperative summary> [#verb]

[body: why, not what]

[Refs: PLAT-456]
[Devflow: skip]
```

`type` is one of `feat fix perf refactor docs test build ci chore revert`. Verbs `#start`,
`#review`, `#qa`, `#done` and `#block` move tickets through `flow-map.yml`. Jira Smart
Commits still handle `#comment` and `#time`. devflow adds no AI attribution trailers.

## Turn it on in a repo

From the repo root, with `gh` logged in:

```bash
gh release download --repo nscaledev/devflow --pattern 'devflow_*_darwin_arm64.tar.gz' -O - | tar -xz devflow
./devflow init && rm ./devflow
```

`devflow init` writes the files in [`templates/repo/`](templates/repo/): the launcher,
the `commit-msg` hook, the caller workflow, the Jira deployment mapping and config for
every supported AI client. You can also copy that folder by hand. Commit the result.

Then set the repo variable `DEVFLOW_ENABLED=true` and check the org secrets listed in
[docs/operations.md](docs/operations.md).

## Developer setup

Nothing to install by hand. Open the repo in any supported client: the launcher in
`.devflow/bin/devflow` installs the latest verified release into `~/.cache/devflow`, and
the binary keeps itself up to date after that.

```bash
devflow login          # stores your Atlassian API token in the OS keychain, reuses gh auth
git config core.hooksPath .githooks
devflow doctor         # checks binary, hooks, auth and client config
```

## Client support

| Layer | Claude Code | Codex CLI | Grok CLI | Cursor · Copilot · Gemini | Terminal |
|---|---|---|---|---|---|
| Tools | Plugin MCP | MCP | MCP | MCP | CLI |
| Instructions | Plugin skill | `AGENTS.md` | Instructions file | `AGENTS.md` / rules | – |
| Commands | `/devflow:*` | Custom prompts | Where supported | Where supported | Subcommands |
| Hooks | SessionStart, PreToolUse, Stop | git hooks | git hooks | git hooks | git hooks |

Enforcement is the same everywhere: the `commit-msg` hook locally and the PR key check
in CI. Client config paths change between versions; `devflow doctor` reports any client it
cannot configure.

## Semantic versioning for your repos

Every deployable asset is versioned with SemVer and shipped as a GitHub Release. `devflow
init` (or `devflow release init`) detects the repo's deployable units and branching
strategy and writes `.devflow/releases.yml` plus the release-please files. Each merge to
the default branch with `feat`/`fix`/`!` commits publishes a release per changed unit
(`v1.4.0`, or `api-v1.4.0` in monorepos) and attaches its binaries, image, Helm chart or
package. Other branches build prerelease versions (`1.5.0-rc.2+sha`). Production deploys
pass `actions/release-guard` only for published releases or an approved hotfix. Details:
[docs/releases.md](docs/releases.md).

## devflow's own releases and updates

Releases are automatic. Every merge to `main` containing a `feat` or `fix` commit makes
release-please open a Release PR, which is auto-merged once checks pass. GoReleaser
then builds five platforms, `checksums.txt` and build provenance attestations.

New releases ship as **beta** (pre-release) for pilot repos and are promoted to
**stable** after 24 hours unless an issue labelled `release-blocker` is open.

Installs check for updates at most every 6 hours, never blocking a command. Updates are
verified, swapped in atomically, and the previous version is kept for
`devflow update --rollback`. [`policy/versions.json`](policy/versions.json) can force a
minimum version or block a bad release everywhere.

## Repository layout

```
cmd/devflow/              CLI entry point (cli, mcp, hook subcommands)
internal/                 commit engine, flow-map resolver, Atlassian and GitHub clients,
                          policy and autonomy, context engine, self-update, auth
adapters/                 per-client plugin files (Claude Code plugin, Codex, Grok, ...)
templates/repo/           files `devflow init` writes into a consuming repo
.github/workflows/        CI, release, promote and reusable workflows for other repos
actions/ship/             composite action: move shipped tickets after a deploy
bin/                      shell helpers used by the workflows
flow-map.yml              canonical events -> each project's Jira statuses
policy/versions.json      minimum and blocked versions
jira-automation/          Jira Automation rule specs and exports
confluence-templates/     shared design doc, postmortem, release notes, sprint report
docs/                     blueprint, rollout checklist, operations runbook
```

## Development

```bash
make build      # ./devflow
make check      # vet, test, lint, shellcheck, actionlint
make snapshot   # local GoReleaser build
```

See [CONTRIBUTING.md](CONTRIBUTING.md).
