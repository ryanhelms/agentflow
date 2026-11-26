---
name: pin
description: Update this repo's agentflow workflows to the installed agentflow release and open a PR
---
Pin this repo's agentflow caller workflow (`.github/workflows/agentflow.yml`) to the agentflow
release that is installed here. The `agentflow` command is on your Bash PATH (the plugin's
`bin/`); it keeps itself on the latest verified release.

1. Run `agentflow update --quiet`, then `agentflow pin --check` and tell me which release the
   workflow is pinned to and which one is installed. Stop if it is already up to date.
2. Link the release notes for the versions in between:
   `gh release list --repo ryanhelms/agentflow --limit 10`, and point out any breaking
   (`!`) changes to the reusable workflows.
3. Run `agentflow pin`, show `git diff .github/workflows/agentflow.yml`, then commit it on a
   new branch as `chore: pin agentflow workflows to vX.Y.Z` (use the repo's Jira key as
   the scope when commits need one) and open a PR. Do not merge it.
