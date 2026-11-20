# Contributing to devflow

## Commits

This repo follows the same commit convention devflow enforces in the repos that use it,
checked by `.githooks/commit-msg` (`make install-hooks`) and by the `convention` CI job on
every PR title and commit. Jira keys are optional here until devflow has a Jira project
(`commit.require_key: false` in `.devflow/config.yml`). The version and the changelog come
straight from the commit types:

```
feat(PLAT-123): add per-tenant quota preview
fix(PLAT-130)!: rename commit_context output fields
```

- `fix` releases a patch, `feat` a minor version, `!` or a `BREAKING CHANGE:` footer a
  major version.
- Every commit needs a Jira key. Run `make install-hooks` once to get the local check.
- No AI attribution trailers.

## AI agents

Agents follow [AGENTS.md](AGENTS.md). `CLAUDE.md` contains only `@AGENTS.md`: never edit it.
Claude Code is denied edits to it by `.claude/settings.json`, and CI fails if any
`CLAUDE.md` contains anything else (`make agent-docs`).

## Pull requests

- CI must pass: `go vet`, `go test -race`, golangci-lint, shellcheck, actionlint and a
  GoReleaser snapshot build.
- Changes to `flow-map.yml` need the owning team lead's approval (see `CODEOWNERS`).
- Changes to the reusable workflows are used by every repo on `@v0`. Keep them backwards
  compatible within a major version.

## How releases happen

devflow releases from a single branch. There is no Release PR and no version-bump commit.

1. Merging to `main` runs `.github/workflows/release.yml`.
2. `bin/next-version.sh` reads the commits since the last `vX.Y.Z` tag. If any is a
   `feat`, `fix`/`perf` or breaking change, the merge commit itself is tagged with the
   next version. Otherwise nothing is released.
3. `scripts/changelog.sh` generates the release's `CHANGELOG.md` section from those
   commits (features, fixes, performance, reverts and breaking changes), the workflow
   commits it as `chore(release): vX.Y.Z` and tags that commit.
4. GoReleaser builds five platforms and `checksums.txt`, and publishes the GitHub Release
   with the same notes. Build provenance is attested.
5. The release is published as a normal, stable release and becomes "Latest". The
   floating major tag (`v0` while devflow is 0.x) moves to it, so repos using
   `nscaledev/devflow/...@v0` get it straight away.

Use squash merges with a convention PR title: the title becomes the commit on `main`,
decides the bump and is the changelog entry. Never edit `CHANGELOG.md` by hand; CI fails
PRs that change it.

## Local development

```bash
make build         # build ./devflow
make test          # unit tests
make check         # everything CI runs (needs golangci-lint, shellcheck, actionlint)
make snapshot      # goreleaser --snapshot --clean
```

## Testing the Claude Code plugin

`make e2e` (or `scripts/e2e-claude.sh`) builds devflow, creates a throwaway repo with
`devflow init`, and drives `claude -p` with the plugin loaded from `adapters/claude-code`.
It checks that the MCP server connects with every tool, the slash commands and
SessionStart context load, `/devflow:commit` makes a convention commit through the
confirmation flow, and the PreToolUse hook blocks invalid commits, pushes to `main`,
force-pushes and `gh pr merge`. It needs a logged-in `claude` CLI and `gh auth`, and it
uses your Claude account. `KEEP=1` keeps the temp directory.
