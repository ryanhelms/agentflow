---
name: manager
description: Install and initialize Agentflow in an existing Git project, update its binary and workflow pins to the latest stable release, or remove its project integration. Use when the user requests one of these lifecycle operations.
argument-hint: "<install | update | remove> [checkout path]"
---

# Agentflow project manager

Use the employee's requested operation and destination. Treat “delete Agentflow
from this repo” as `remove`. If given a project name, resolve it against known
local workspaces. If it matches several checkouts, ask which path to use. For
all three operations, resolve the root with `git -C <path> rev-parse --show-toplevel`.
Re-prompt for a missing, invalid or bare repository. Use the existing checkout,
including linked worktrees. Work from that root. Report the resolved path.

Use the verified launcher supplied by the installed plugin, or the official
release installer from `nscaledev/agentflow`. The GitHub URL identifies the
Agentflow distribution. The destination path identifies the employee's project.
Use structured argument lists for paths, including paths with spaces. Run GitHub
authentication through the user's signed-in client.

In Claude Code, expand each `agentflow` invocation below to
`sh "${CLAUDE_PLUGIN_ROOT}/bin/agentflow"` with the given arguments. The installed
plugin also lives two directories above this skill's directory. Other hosts'
manager entry supplies their equivalent absolute launcher path. Keep the
working directory in the target checkout throughout.

## Install

1. Bootstrap the verified binary with the installed plugin launcher (`agentflow version`
   in Claude Code) or use the signed-in GitHub CLI to download the release's
   platform installer. An agent that can present an interactive terminal can run
   `bash install.sh --repo <path>` or `./install.ps1 -Repo <path>` and let the user
   answer setup. Completing the installer includes initialization.
2. When conducting setup in chat, run `agentflow init --detect-providers` from the
   target root before scaffolding. Display detected providers and the specific
   evidence for each. Identify saved choices. Allow overrides and ask about
   unresolved roles, including the option to disable a role where supported.
   On repeat runs reuse recorded choices. For an older verified binary, upgrade
   to the release that provides detection before touching project files.
3. Apply the reviewed selections together with `agentflow init --non-interactive`
   and one `--provider ROLE=ID` argument per chosen role. Use the catalog's IDs.
   Keep credentials in the provider's login flow. Do not force-overwrite files.
4. Verify the choices with `agentflow setup providers --explain` and inspect
   `agentflow setup plan --client NAME` for the actual host. Follow the setup
   skill for any requested sign-in or registration. Report the installed version,
   repo root, changed files and any remaining sign-in or verification step.

## Update

1. Record the current version through the verified launcher. Resolve the target
   with `gh release view --repo nscaledev/agentflow --json tagName --jq .tagName`.
2. Run `agentflow update --latest-stable` from the project root. Respect
   project pins, major tracks and version blocks. Surface a conflict so the
   employee can decide whether to change project policy. On older versions
   that lack this flag, run their verified `agentflow update` and check the
   resulting version against the requested stable tag.
3. Invoke the launcher again to check the active version. Once it matches, run
   `agentflow pin` then `agentflow pin --check` for the repo's caller workflow. Report
   the old and new versions, changed pins and any client restart needed.
   A verification or pin failure means the update is incomplete.

## Remove

1. Show `git status --short` and `agentflow remove --dry-run` in the target repo.
   Distinguish generated assets, edits to shared files, pre-existing files and
   entries the remover flags for review. For an older installation without a
   receipt, check Git history and local changes for pre-existing release files.
   Use `--keep-releases` if their ownership is uncertain. Preview the same flags
   that will be applied.
2. After the user approves the displayed removal, run `agentflow remove` with
   those flags. Preserve existing project instructions, authored records and
   unrelated changes. Where a changed file or registration might contain
   user work, show the specific residual and ask how to handle it. Limit
   deletion to files whose Agentflow ownership can be established.
3. Check the resulting diff and status. Verify that the Agentflow block in
   `AGENTS.md`, generated launchers, caller workflow and managed client
   registrations are gone. Report preserved work and any remaining Agentflow
   integration precisely. The per-clone opt-out prevents the next session
   from re-enrolling the project. Other projects continue using the shared
   plugin and binary cache.
