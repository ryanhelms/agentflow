# devflow: changes since PR #2 (handoff for another Claude session)

Repo: `nscaledev/devflow` (GitHub, internal). Everything below is merged to `main`; this
file only summarises it. Snapshot taken 2026-11-20, `main` at `f827e0a`, latest release
**v0.3.1**.

## 1. Sync this machine first

```bash
cd <your devflow checkout>
git switch main && git pull --ff-only
git fetch --tags --force          # v1 was deleted; v0 is the floating tag now
git tag -d v1 2>/dev/null || true # remove a stale local v1 if you have one
make install-hooks                # builds ./devflow, enables .githooks (commit-msg + pre-push)
```

Then **start a new Claude Code session in the repo** so it loads `CLAUDE.md` →
`AGENTS.md`, `.claude/rules/`, and the hooks and permissions in `.claude/settings.json`.

Verify:

```bash
git log --oneline -1               # f827e0a chore(release): v0.3.1 (or newer)
git rev-parse --short 'v0^{}'      # same commit as v0.3.1
grep -o 'devflow:version -->`[^`]*' README.md   # v0.3.1
```

## 2. What changed, PR by PR

| PR | Merge | Release | Summary |
| --- | --- | --- | --- |
| #2 | `4722478` | v0.1.0 | devflow's own releases became single-branch: version from commit types, no Release PR. Fixed floating tags (e.g. `v1`) being read as the last release. |
| #3 | `61e3712` | – | Enterprise README, `AGENTS.md`, `CLAUDE.md` = `@AGENTS.md` only (edits blocked + CI check). |
| #4 | `6bf1efb` | v0.2.0 | Generated `CHANGELOG.md`; this repo enforces devflow's commit convention; new `commit.require_key` option. |
| #5 | `bae531b` | v0.3.0 | Dropped beta channel and 24 h promotion; releases are stable immediately; floating tag `v1` → `v0`. Breaking: consumers use `@v0`. |
| #6 | `0cadb28` | – | Rule: README.md (incl. status area) must stay current; release bot updates the version; CI warnings. |
| #7 | `c871e27` | v0.3.1 | PR titles checked as the future squash commit (≤64 chars); pre-push hook; Claude hook in this repo; CI checks `<title> (#N)`. |

### PR #2: single-branch releases for devflow itself
- `.github/workflows/release.yml`: on push to `main`, `bin/next-version.sh` computes the
  next version from Conventional Commits since the last `vX.Y.Z` tag
  (`fix`/`perf` → patch, `feat` → minor, `!`/`BREAKING CHANGE:` → major, minor before 1.0).
  No bump → no release. Tag is removed again if the build fails.
- Removed `release-please-config.json` and `.release-please-manifest.json` from this repo.
  (release-please is still what devflow offers *other* repos via `semver-release.yml`.)
- Fix: only full `X.Y.Z` tags count when finding the last release, in
  `bin/next-version.sh`, `internal/app/release.go` and `semver-release.yml`; bash 3.2
  empty-array fix.

### PR #3: README, AGENTS.md, CLAUDE.md
- `README.md` rewritten (status, capabilities, Mermaid architecture, getting started,
  day-to-day use, versioning, AI clients, security, operations, own releases, layout).
- `AGENTS.md`: agent instructions for this repo, incl. a table separating **what devflow
  offers other repos** from **how devflow itself is released**, commands, conventions,
  compatibility contracts, keep-in-sync list.
- `CLAUDE.md` contains exactly `@AGENTS.md`. `.claude/settings.json` denies Edit/Write on
  any `CLAUDE.md`; `scripts/check-agent-docs.sh` (CI + `make agent-docs`) fails otherwise.
- `CODEOWNERS` covers `AGENTS.md`, `CLAUDE.md`, `.claude/`.

### PR #4: changelog and commit convention in this repo
- `.devflow/config.yml` (this repo only): devflow's commit rules with
  `require_key: false` (Jira keys optional until devflow has a Jira project).
- Product: new `commit.require_key` option (default `true`), so `type(scope): ...` /
  `type: ...` pass when keys are optional. `internal/commit`, `internal/config`.
- `.githooks/commit-msg` checks commits with the devflow built from the checkout.
- CI `convention` job: PR title + every PR commit; fails PRs that edit `CHANGELOG.md`.
- `scripts/changelog.sh` generates release sections (breaking, features, fixes,
  performance, reverts; docs/test/ci/chore hidden; idempotent).
- `release.yml` now: generate changelog → commit `chore(release): vX.Y.Z` → tag that
  commit → GoReleaser publishes with the same notes. `CHANGELOG.md` bootstrapped with
  v0.1.0; Claude denied edits to it.

### PR #5: stable releases, `v0` floating tag (breaking)
- `.goreleaser.yaml`: `prerelease: "false"`, `make_latest: "true"`.
- `promote.yml` deleted. `release.yml` moves the floating major tag after each release
  (`v0` while 0.x, `v1` from 1.0.0).
- Every `nscaledev/devflow/...@v1` reference became `@v0`: caller workflow template,
  `actions/setup` in all reusable workflows, docs, default `flow_map`
  (`nscaledev/devflow@v0:flow-map.yml`). `update.channel: beta` removed from the template.
- Safety net is `policy/versions.json` (block a version) + `devflow update --rollback`.
- Done manually after merge: v0.1.0 and v0.2.0 marked non-prerelease; remote `v1` tag
  deleted.

### PR #6: README must stay current
- `AGENTS.md` rule "Keep README.md current", mirrored in
  `.claude/rules/readme-up-to-date.md`.
- README status table has a **Current release** row; the version sits between
  `<!-- devflow:version -->` markers and `release.yml` updates it in the
  `chore(release)` commit. Never edit it by hand.
- CI (warnings only): reader-visible files changed without README; status marker
  missing/stale; rollout or blueprint changed without README.
- CI now also re-runs when a PR title is edited (`pull_request: types: [..., edited]`).

### PR #7: check PR titles before they become commits
- `internal/commit.ValidatePRTitle`: title must fit 72 with ` (#1234)` added → **≤64**.
- devflow's pre-bash hook checks `gh pr create/edit --title` and
  `gh pr merge --subject`; `devflow pr` uses the same rule.
- New `devflow hook pre-bash --convention-only` (skips the never-list). This repo uses it
  in Claude Code via `.claude/settings.json` → `.githooks/claude-pre-bash`, because
  maintainers ask agents to merge here. Consumer repos keep the full hook.
- `.githooks/pre-push` checks every pushed commit (catches `--no-verify`).
- CI checks the exact squash subject `<title> (#<number>)`.

## 3. Rules now in effect (read AGENTS.md for the full text)

- Never edit `CLAUDE.md` or `CHANGELOG.md`, or the README version marker.
- Commit messages and PR titles: `type[(scope)][!]: imperative summary`, lowercase start,
  no trailing period, commits ≤72 chars, **PR titles ≤64**. Jira key as scope when there
  is a ticket; optional in this repo only.
- Squash-merge PRs. The PR title is the commit on `main`, decides the version bump and is
  the changelog entry. `docs`/`ci`/`chore`/`test`/`refactor` release nothing.
- Update README.md (including the status area) in the same PR as any reader-visible
  change.
- Never push to `main`, force-push, move or delete tags, or merge, unless the user asks.
- No AI attribution trailers. No secrets.
- Keep "what devflow offers other repos" separate from "how devflow is released".

## 4. Current state

- Releases: v0.1.0, v0.2.0, v0.3.0, **v0.3.1 (Latest)**; all stable. Floating tag `v0`
  = v0.3.1. No `v1` tag.
- Consuming repos reference `nscaledev/devflow/...@v0`. **No team uses devflow yet.**
- Blueprint: published artifact https://claude.ai/artifact/Fv9hznrx6BoWbCiMgDSrwj
  (source `docs/artifact/nscale-delivery-loop.html`; rebuild `docs/blueprint.html` with
  `scripts/build-blueprint.sh`). Republish the artifact from that source after edits.

## 5. Still open

- Architecture approval by Kyle Redelinghuys and Vishal Srini (gates rollout P0).
- Devflow GitHub App (`vars.DEVFLOW_APP_ID`, `secrets.DEVFLOW_APP_PRIVATE_KEY`): needed
  by consumer repos' `semver-release` and `auto-issues`, not by devflow's own releases.
- Jira project for devflow work; then remove `require_key: false` in
  `.devflow/config.yml`.
- Pilot team nomination; real Jira/Confluence testing after a pilot developer runs
  `devflow login`.
- Listing the Claude Code plugin in `nscale-marketplace` (agreed, not done).
- If `main` gets branch protection, allow `github-actions` to push (release commit).
