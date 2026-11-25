---
name: update
description: Update agentflow to the latest release now, or roll back one version
argument-hint: "[--latest-stable | --rollback]"
disable-model-invocation: true
---
Use `sh "${CLAUDE_PLUGIN_ROOT}/bin/agentflow"` for the `agentflow` commands below.
Each invocation resolves the active verified binary, including after updating.

Run `agentflow version`, then `agentflow update $ARGUMENTS`, then `agentflow version` again, and
tell me the old and new version. Updates are checksum- and attestation-verified; if
verification fails, report it and don't retry.

For a project update to the latest stable release, use the manager skill. It
also verifies the active binary and updates the project's workflow pins.
