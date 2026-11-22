# Semantic versioning and GitHub Releases

Every deployable asset at Nscale (apps, services, CLIs, images, Helm charts, libraries
and modules) is versioned with [Semantic Versioning 2.0](https://semver.org) and shipped
as a GitHub Release. The GitHub Release is the system of record: production deploys
reference a release, the task follow-up previews Jira fix versions, and Confluence release notes link to it.

## Rules

| Rule | How devflow enforces it |
| --- | --- |
| Versions come from commits, never by hand | release-please reads Conventional Commits: `fix`/`perf` → patch, `feat` → minor, `!` or `BREAKING CHANGE:` → major. Before 1.0.0 a breaking change bumps the minor version. |
| Releases are fully automatic | `semver-release.yml` runs on every push to the default branch, auto-merges the Release PR, tags and publishes the release, then builds and attaches artifacts. |
| One version per deployable unit | Units are listed in `.devflow/releases.yml`. Monorepo tags carry the unit (`api-v1.4.0`); single-unit repos use `v1.4.0`. A commit only bumps the units whose path it touches. |
| Branches build prerelease versions | Builds off the default branch are versioned `X.Y.Z-<id>.<n>+<sha>`, following the repo's branching strategy (below). They sort before the release they lead to. |
| Production deploys only releases | `actions/release-guard` fails a production deploy unless the ref is a published, non-prerelease GitHub Release, or an approved hotfix. |
| Hotfixes still end in a release | A hotfix may deploy through the `production-hotfix` environment (required reviewers = the approval). The guard opens a tracking issue; merging the `fix(...)` to the default branch publishes the patch release, which is then redeployed. |

## Branching strategy

`devflow release init` detects the strategy (a `develop` branch means GitFlow) and
records it in `.devflow/releases.yml`. Stable releases only ever come from the default
branch.

| Branch | trunk | gitflow | Example |
| --- | --- | --- | --- |
| default (`main`) | release `X.Y.Z` | release `X.Y.Z` | `1.5.0` |
| `develop` | `pr` prerelease | `alpha` prerelease | `1.5.0-alpha.7+3f2c1ab` |
| `release/*` | `rc` prerelease | `rc` prerelease | `1.5.0-rc.2+9a1b2c3` |
| `hotfix/*` | `hotfix` prerelease | `hotfix` prerelease | `1.4.1-hotfix.1+c0ffee1` |
| anything else (PRs) | `pr` prerelease | `pr` prerelease | `1.5.0-pr.3+abc1234` |

`n` is the number of the unit's commits since its last release; `+sha` is build
metadata, which SemVer ignores for ordering.

## Asset types

| `builds:` | What a release produces |
| --- | --- |
| `binary` | GoReleaser archives per platform plus `checksums.txt`, attached to the release, with build provenance attestations. The unit needs a `.goreleaser.yaml`; set `release: disable: true` there, since devflow uploads. |
| `image` | Image pushed to `ghcr.io/<owner>/<unit>` (or `image:`), tagged `X.Y.Z`, `X.Y`, `X` (from 1.0.0) and the short SHA, with an attestation in the registry. `VERSION` is passed as a build arg. |
| `helm` | Chart packaged with `version` and `appVersion` set to the release, pushed to `oci://ghcr.io/<owner>/charts` and attached. release-please also updates `Chart.yaml` in the Release PR. |
| `library` | `publish: go` adds the `path/vX.Y.Z` tag Go needs for submodules; `npm` publishes to GitHub Packages (or `NPM_TOKEN`); `pypi` builds and, with `vars.PYPI_PUBLISH=true`, publishes with trusted publishing; `terraform` is tag-only. |

## Setup

```bash
devflow release init             # detect units + strategy; writes .devflow/releases.yml,
                                 # release-please-config.json, .release-please-manifest.json
devflow release plan             # each unit: current → next version, bump, build version
devflow release version --unit api   # the version this branch builds as
devflow release sync             # after editing releases.yml
```

`devflow init` runs `release init` for you. Existing tags (`v1.2.0`, `api-v1.2.0`) seed the
manifest, so adopting devflow doesn't restart versions. The caller workflow written by
`devflow init` runs `semver-release.yml` on pushes to the default branch.

`.devflow/releases.yml`:

```yaml
strategy: trunk            # or gitflow
units:
  - name: api
    path: services/api
    release_type: go       # release-please type: go, node, python, helm, terraform-module, simple
    builds: [image, helm]
    chart: chart           # relative to path
  - name: cli
    path: cli
    release_type: go
    builds: [binary]
  - name: quota
    path: libs/quota
    release_type: go
    builds: [library]
    publish: go
```

## Using versions in your own workflows

Tag staging builds with the branch version:

```yaml
- uses: actions/checkout@v4
  with: { fetch-depth: 0 }
- id: version
  uses: nscaledev/devflow/actions/version@v0
  with: { unit: api }
- run: docker build -t ghcr.io/nscaledev/api:${{ steps.version.outputs.version }} services/api
```

Guard production deploys:

```yaml
deploy-production:
  environment: production          # or production-hotfix for an approved hotfix
  runs-on: ubuntu-latest
  steps:
    - uses: nscaledev/devflow/actions/release-guard@v0
      with:
        environment: production
        ref: ${{ github.ref_name }}  # the release tag being deployed, e.g. api-v1.4.0
    - run: ./scripts/deploy.sh production
```

Give `production-hotfix` required reviewers in the repo's environment settings; that
review is the hotfix approval.

## Requirements

- Nothing beyond the workflow's own token. Release notes run as a job after the release
  job, so they don't depend on release events. GitHub runs no checks on a Release PR
  opened with that token: if branch rules require checks, close and reopen the Release PR
  (or click "Update branch") once per release, and it merges itself when they pass. The
  optional devflow GitHub App (`vars.DEVFLOW_APP_ID`, `secrets.DEVFLOW_APP_PRIVATE_KEY`)
  removes that step.
- Repo setting "Allow auto-merge", or no required checks on the default branch.
- `packages: write` for images, charts and npm packages (granted by the caller workflow).
