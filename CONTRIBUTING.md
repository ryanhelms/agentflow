# Contributing to devflow

## Commits

This repo follows the same commit convention devflow enforces in the repos that use it,
checked by `.githooks/commit-msg` (`make install-hooks`) and by the `convention` CI job on
every PR title and commit (`make install-hooks` also adds a `pre-push` check). PR titles
are checked as the squash commit they become, `<title> (#123)`, so keep them to 64
characters. Jira keys are optional here until devflow has a Jira project
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
- Changes to the reusable workflows reach every repo through its `devflow pin` PR (and
  `actions/setup` changes reach them at once through `@v0`). Keep them backwards
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
   floating major tag (`v0` while devflow is 0.x) moves to it. Repos that pin devflow by SHA pick it up with
   `devflow pin`, which the Claude Code session hook suggests once the pin is behind.

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

### Website

From the repository root, start the launch and documentation website with
Docker Compose v2.22 or later. The container includes Node, Vite, and the Nscale
site fonts.

```sh
docker compose up --build --wait website
```

Open [the local website](http://127.0.0.1:5173). Edit the HTML, JavaScript, or
CSS in `website/src/` with your usual editor. Vite applies CSS and JavaScript
updates in the open tab and refreshes the page for HTML edits. The install
commands and documentation version index live in `website/src/main.js`.

| Task | Command |
| --- | --- |
| Follow Vite logs and errors | `docker compose logs -f website` |
| Check that the website builds for production | `docker compose exec website npm run build` |
| Automatically rebuild the image when dependencies or Vite configuration change | `docker compose up --build --watch website` |
| Stop the local environment | `docker compose down` |

Source edits work with either startup command. With the detached server, rerun
`docker compose up --build --wait website` after changing the package files,
Vite configuration, or Dockerfile. To add a dependency, use npm from
`website/` and include the updated `package-lock.json` with the change.

Set `DEVFLOW_WEBSITE_PORT=5174` in your shell or a root Compose `.env` file to
use another host port. If Docker Desktop or WSL misses file changes, set
`VITE_USE_POLLING=1` the same way. Run the startup command again to apply
either setting. The website binds to the loopback address on the host.

For development directly with Node 24, run `npm ci` and `npm run dev` in
`website/`. Run `npm run build` to write a static site to `website/dist/`;
`npm run preview` serves that build locally.

## Testing the Claude Code plugin

`make e2e` (or `scripts/e2e-claude.sh`) builds devflow, creates a throwaway repo with
`devflow init`, and drives `claude -p` with the plugin loaded from `adapters/claude-code`.
It checks that the MCP server connects with every tool, the skills and
SessionStart context load, `/devflow:commit` makes a convention commit through the
confirmation flow, and the PreToolUse hook blocks invalid commits, pushes to `main`,
force-pushes and `gh pr merge`. It needs a logged-in `claude` CLI and `gh auth`, and it
uses your Claude account. `KEEP=1` keeps the temp directory.
