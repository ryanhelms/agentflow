---
name: pin
description: Update this repo's devflow workflows to the installed devflow release and open a PR
---
Pin this repo's devflow caller workflow (`.github/workflows/devflow.yml`) to the devflow
release that is installed here. The `devflow` command is on your Bash PATH (the plugin's
`bin/`); it keeps itself on the latest verified release.

1. Run `devflow update --quiet`, then `devflow pin --check` and tell me which release the
   workflow is pinned to and which one is installed. Stop if it is already up to date.
2. Link the release notes for the versions in between:
   `gh release list --repo nscaledev/devflow --limit 10`, and point out any breaking
   (`!`) changes to the reusable workflows.
3. Run `devflow pin`, show `git diff .github/workflows/devflow.yml`, then commit it on a
   new branch as `chore: pin devflow workflows to vX.Y.Z` (use the repo's Jira key as
   the scope when commits need one) and open a PR. Do not merge it.
