# Repository boundary: one session, one repository

An agent session works only in the repository it runs in. It never closes, creates,
merges, pushes or otherwise changes another repository. To get work done in another
repository, it hands the task to a session there with `agentflow delegate`.

This came from a real incident. A coordinator in agent-commander ran
`gh pr merge 5 -R ryanhelms/agentflow`. `policy.coordinator_merge` looked up PR 5 in the
current folder's repository (agent-commander, base `develop`), so it allowed the merge,
and agentflow PRs 5 and 6 were merged into agentflow `main`.

## What the rule allows and refuses

The session's repository is its **home**. agentflow identifies it by the Git common
directory, so every linked worktree of the home repository counts as home too. The
GitHub `owner/name` of `origin` identifies it for `gh`.

| Command | Home repository | Another repository |
| --- | --- | --- |
| `git` reads: `status`, `log`, `show`, `diff`, `blame`, `grep`, `rev-parse`, `ls-files`, `branch`/`tag` listing, `remote -v`, `config --get`, `worktree list`, ... | allowed | allowed |
| Any other `git` subcommand: `commit`, `push`, `merge`, `rebase`, `reset`, `checkout`, `switch`, `fetch`, `pull`, `branch -D`, `tag v1`, `worktree add`, ... | allowed (the never-list still applies) | **refused** |
| `git push` to a URL of a different GitHub repository | **refused** | **refused** |
| `gh` reads: `view`, `list`, `diff`, `checks`, `status`, `search`, `browse`, `clone`, `download`, `watch` | allowed | allowed |
| Any other `gh` command: `pr merge`, `pr create`, `pr close`, `issue create`, `release create`, `run rerun`, `workflow run`, ... | allowed (the never-list still applies) | **refused** |
| `gh api` with GET (no `-X`, no `-f`/`-F`/`--input`) | allowed | allowed |
| `gh api repos/OWNER/NAME/...` with another method or fields | allowed | **refused** |

agentflow finds the target repository of a command from:

- `git -C DIR`, `git --git-dir DIR`, `git --work-tree DIR`, and the `GIT_DIR` and
  `GIT_WORK_TREE` variables;
- `cd DIR` and `pushd DIR` earlier in the same command line, including `(cd DIR && ...)`;
- `gh -R`/`--repo`, the `GH_REPO` variable, and `github.com/OWNER/NAME/...` URLs in the
  arguments (`gh pr merge https://github.com/OWNER/NAME/pull/5`);
- `gh api repos/OWNER/NAME/...`; `{owner}/{repo}` placeholders mean the current folder's
  repository;
- `bash -c`, `sh -c` and `zsh -c` scripts, and the commands inside `$(...)`, backquotes,
  `<(...)` and `>(...)`, which are checked the same way.

Reads stay allowed because a coordinator needs to see the state of other repositories
(a PR's checks, a branch's log) to decide what to delegate. Reads change nothing there.
`git fetch` counts as a write: it updates the other checkout's refs. Use
`gh -R OWNER/NAME pr view` or `gh api` to read remote state instead.

A session that does not run in a Git repository has no home and is not restricted.

## Where it is enforced

1. **The pre-bash hook** (Claude Code plugin, `agentflow hook pre-bash`). It checks
   every Bash command before it runs, in every repository, including agentflow's own
   convention-only mode. The home is `CLAUDE_PROJECT_DIR`, then `AGENTFLOW_SESSION_REPO`,
   then the hook's folder.
2. **The git hooks.** The session-start hook writes
   `AGENTFLOW_SESSION_REPO=<home common Git directory>` to `CLAUDE_ENV_FILE`, so every
   command the session runs carries it. `agentflow delegate` sets it for the sessions
   it starts. The pre-commit hook refuses a commit, and the pre-push hook refuses a
   push, when the repository they run in has a different common Git directory. This
   catches what a command parser cannot see, such as a script that changes folder.
   A person who inherited the variable in a shell can clear it with
   `unset AGENTFLOW_SESSION_REPO`.
3. **`agentflow sweep --all-repos`** finishes merged worktrees of the home repository
   only. It reports the other repositories' merged worktrees as `other_repo`, with the
   command to run there.

The pre-push hook also refuses a push to a URL that is not one of the repository's
configured remotes.

`policy.coordinator_merge` now looks up the PR's base in the PR's own repository: it
passes the command's `-R`/`--repo`, `GH_REPO` or PR URL on to `gh pr view`. With the
boundary in place, a merge in another repository is refused before that lookup runs.

Limits. A parser cannot see everything: a script file, a Makefile target or an alias
can still reach another folder. The git hooks cover commits and pushes in those cases.
`gh api graphql` mutations name objects by node ID, so the hook cannot tell which
repository they change; they are allowed. Codex has no pre-bash hook, so a Codex
session relies on the git hooks (when `AGENTFLOW_SESSION_REPO` is set) and on the
AGENTS.md rule.

## Delegating to another repository

```bash
agentflow delegate --repo agentflow "Fix the merge-base lookup and open a PR into main"
```

MCP: `agentflow_delegate` with `repo` and `task`.

`delegate` finds the repository's primary checkout in `worktree.checkouts` in
`~/.agentflow/config.yml` (default `~/GitHub/<name>`), then:

1. **A running tmux session.** It looks for a tmux pane in that checkout or one of its
   worktrees: first a pane agentflow started (tagged `@agentflow_repo`), then any pane
   running `claude`, `codex` or `node`. It types the task into that pane and presses
   Enter.
2. **The native harness.** When no pane is found and the caller runs in a client with
   its own session tools (Claude Code desktop exposes `CLAUDE_CODE_MESSAGING_SOCKET`),
   it returns `status: native` with the checkout. The agent then uses its client's
   tools: message a session whose folder is that checkout, or start one there.
3. **A new session.** Otherwise, or with `--start`, it starts a detached tmux session in
   the checkout running the client (`claude` or `codex`, from `--client` or the caller)
   with the task as its first prompt, and sets `AGENTFLOW_SESSION_REPO` for it.

The task is prefixed with `[agentflow delegate from <home>]` so the receiving session
knows who asked. Sending or starting goes through the usual confirmation flow
(`needs_confirmation`, `--yes`). `--dry-run` shows what would happen.

agentflow has no session-provider contract (providers cover tasks, knowledge and
notifications), so delegation lives in `internal/delegate` with a small transport
interface for tmux and a hand-off result for native harnesses.

## Plan

This is the plan this change follows (issue ryanhelms/agentflow#8).

1. `internal/boundary`: parse a shell command into simple commands, follow `cd`,
   `-C`, `--git-dir`, `-R`, `GH_REPO`, URLs and `gh api` paths, classify reads and
   writes, and compare the target with the home repository.
2. Pre-bash: run the boundary check before the never-list, always, with a refusal that
   says how to delegate. Fix `MergeBase` to pass the PR's repository to `gh pr view`.
3. Git hooks: session-start exports `AGENTFLOW_SESSION_REPO`; pre-commit and pre-push
   compare it with their repository. Sweep applies only to the home repository.
4. `agentflow delegate` and `agentflow_delegate` over `internal/delegate`, with the
   confirmation flow, a plugin skill, the parity row and usage text.
5. Tests for each refusal, each allowed case and the delegate paths; this document,
   the AGENTS.md managed block, the README and the blueprint capability table.
