---
name: pin
description: Update this repo's agentflow workflows to the installed agentflow release and open a PR
---
Pin this repo's agentflow caller workflow (`.github/workflows/agentflow.yml`) to the agentflow
release that is installed here, and replace stock launcher and hook files from older releases
(`.agentflow/bin/agentflow`, `.agentflow/bin/agentflow.ps1`, `.githooks/commit-msg`,
`.githooks/pre-push`). The `agentflow` command is on your Bash PATH (the plugin's `bin/`); it
keeps itself on the latest verified release.

1. Run `agentflow update --quiet`, then `agentflow pin --check` and tell me which release the
   workflow is pinned to, which one is installed, and which launcher files are stock files from
   an older release. Stop if nothing is behind. Pass on any warning about a changed file or a
   link as it is: pin leaves those alone, and I decide whether to replace them by hand.
2. Link the release notes for the versions in between:
   `gh release list --repo ryanhelms/agentflow --limit 10`, and point out any breaking
   (`!`) changes to the reusable workflows.
3. Run `agentflow pin`, show
   `git diff -- .github/workflows/agentflow.yml .agentflow/bin .githooks`, then commit those
   files on a new branch as `chore: pin agentflow workflows to vX.Y.Z` (use the repo's Jira
   key as the scope when commits need one) and open a PR. Do not merge it.
