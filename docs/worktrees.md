# Task worktrees

Each task gets one GitHub issue, one linked Git worktree and one pull request into the
integration branch (`develop`). A coordinator merges the PR. The worktree and its branches
are removed after the merge. `main` only receives release promotions.

The agentflow repository itself uses a `develop` integration branch while the ACP platform
program runs ([plans/acp-platform/](plans/acp-platform/)). Program PRs target `develop`,
only the coordinator merges them. At a passing phase gate the coordinator tags the
`develop` commit `develop-pN`, and the `auto-promote` workflow merges it into `main` and
releases it if CI passed and nothing is breaking. Breaking changes still wait for the owner
to run `agentflow promote`.

## The lifecycle

| Step | Who | Command |
| --- | --- | --- |
| 1. Create or pick the issue | agent or developer | `agentflow task create`, `agentflow work` |
| 2. Create the worktree | agent or developer, from the primary checkout | `agentflow start <issue> --worktree` |
| 3. Work, test and commit | agent, in the worktree only | `agentflow commit` |
| 4. Open the PR into `develop` | agent | `agentflow pr` |
| 5. Merge the PR | coordinator | `gh pr merge` (needs `policy.coordinator_merge`) |
| 6. Remove the worktree and branches | agent or developer | `agentflow finish` |
| 7. Clean up everything merged | anyone | `agentflow sweep` |
| 8. Release `develop` to `main` | the developer only | `agentflow promote` |

## Where worktrees go

Every task provider (GitHub Issues, Jira, Asana, repository tasks) uses the same path:

```
<root>/<repo>/<branch>
```

- `<root>` is `worktree.root` in `.agentflow/config.yml`, else `worktree.root` in
  `~/.agentflow/config.yml`, else `~/.worktrees`.
- `<repo>` is the last path component of `origin`, or the primary checkout's directory name.
- A branch with slashes nests: `feat/x` becomes `<root>/<repo>/feat/x`.

`agentflow start --worktree` prints the path and the `cd` command to start your agent there.
It also writes `.agentflow/worktree.yml` with the issue, branch, base, base commit, repository
and creation time. The file is listed in `.git/info/exclude`, so it is never committed.

## go.work for multi-repository Go work

Configure the `go.work` written into each new worktree:

```yaml
worktree:
  go_work:
    use: [".", "multiplayer"]
    siblings:
      agent-acp.local/sdks/go: { repo: agent-acp, path: sdks/go }
      agent-acp.local/transport: { repo: agent-acp-transport, path: . }
```

- `use` lists module directories in this repository.
- The `go` line is the highest `go` directive among the used modules.
- Each sibling becomes `replace <module> => <absolute path>`. The path is, in order:
  1. the sibling's worktree for the same branch, `<root>/<sibling repo>/<branch>`
  2. the sibling's worktree for the same issue, found by its `.agentflow/worktree.yml`
  3. the sibling's primary checkout: `worktree.checkouts.<repo>` in `~/.agentflow/config.yml`,
     default `~/GitHub/<repo>`
- `go.work` and `go.work.sum` are added to `.git/info/exclude`. The file is shared by the
  primary checkout and all its worktrees.

Run `agentflow worktree go-work` in a worktree to regenerate it, for example after creating
the sibling's worktree.

```yaml
# ~/.agentflow/config.yml
worktree:
  checkouts:
    agent-acp: ~/src/agent-acp
```

## finish

`agentflow finish [BRANCH] [--dry-run] [--yes]` resolves the worktree from the current
directory, or from the branch name. It refuses when:

- the worktree has uncommitted changes
- the branch has commits that are on no `origin` branch and not part of the merged PR head
- the branch is protected or is the default branch
- the PR is not merged

The PR counts as merged when `gh pr view <branch> --json state,mergedAt,baseRefName,headRefOid`
says so. Without a PR, the branch must have advanced past its starting commit and be an
ancestor of `origin/<default>` after a fetch.

Then it runs `git worktree remove`, deletes the local branch, runs `git worktree prune`,
deletes the remote branch if it still exists, and prints the primary checkout path. Running
it again reports `already_finished`. The confirmation flow is the same as other agentflow
writes (`finish` action, remote class).

## sweep

`agentflow sweep [--all-repos] [--dry-run] [--stale-days N] [--json] [--yes]` inspects every
linked worktree of the repository, including Claude Code's `<repo>/.claude/worktrees/`.
`--all-repos` also reports every repository with a worktree under `<root>` and every entry in
`worktree.checkouts`, but finishes only this repository's worktrees: a session changes only
its own repository ([repo-boundary.md](repo-boundary.md)). Merged worktrees elsewhere show as
`other_repo`, with the `agentflow delegate` command that finishes them from a session there.

| State | Meaning | Sweep does |
| --- | --- | --- |
| `would_finish` / `finished` | merged; finish's checks pass | finishes it (not with `--dry-run`) |
| `dirty` | uncommitted changes | reports |
| `unpushed` | commits on no `origin` branch | reports |
| `stale` | no commits for more than N days (default 7) | reports |
| `orphaned` | the directory is gone | reports; `git worktree prune` clears the entry |
| `other_repo` | merged, in another repository (`--all-repos`) | reports; finish it from a session there |
| `detached`, `protected`, `active` | nothing to do | reports |

It then runs `git worktree prune` and `git remote set-head origin -a`. Sweep never deletes a
worktree that is not merged.

The Claude Code session-start hook runs an offline `sweep --dry-run` (no fetch, no `gh`, at
most 3 seconds) and adds a one-line summary when something needs attention. When the session
starts in a primary checkout, it also tells the agent to start a worktree before changing
anything.

## promote

`agentflow promote [--from develop] [--to main] [--dry-run] [--yes]` opens the release PR
from the integration branch into `main`. The body asks for a merge commit, not a squash. If
an open promotion PR exists, it returns that one.

It refuses when an AI agent runs it (`CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, any `CODEX_*`
variable other than `CODEX_HOME`, or agentflow's Codex host marker). Set
`AGENTFLOW_ALLOW_PROMOTE=1` to run it from such an environment yourself.

## Enforcement with local hooks

Private repositories on GitHub Free have no branch rulesets, so `agentflow init` installs
local hooks next to `commit-msg` and sets `core.hooksPath=.githooks`:

- `pre-push` blocks pushes to `policy.protected_branches`. It uses the installed agentflow
  (`agentflow hook pre-push`), or reads `.agentflow/config.yml` itself.
  `AGENTFLOW_ALLOW_PROMOTE=1` allows a push to `main` or `master` only.
- `pre-commit` refuses commits in the primary checkout and tells you to run
  `agentflow start <issue> --worktree`. `AGENTFLOW_ALLOW_PRIMARY=1` bypasses it, for example to
  commit the files `agentflow init` wrote. A repository with no commits is allowed.

The Claude Code PreToolUse hook keeps the never-list. With `policy.coordinator_merge: true` it
allows `gh pr merge` when the PR's base, read with `gh pr view`, is not `main` or `master`. The
command must be a single `gh pr merge` with no shell chaining. The never-list also holds
`agentflow plugin add`, `remove` and `approve` with a plugin source or ID. It does not find
every form of such a command: see [What the hook catches](plugins.md#what-the-hook-catches).
`--convention-only` skips the rest of the never-list and checks messages and titles. The
plugin rule and the [repository boundary](repo-boundary.md) still apply in that mode.
