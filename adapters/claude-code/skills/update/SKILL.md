---
name: update
description: Update devflow to the latest release now, or roll back one version
argument-hint: "[--latest-stable | --rollback]"
disable-model-invocation: true
---
Use `sh "${CLAUDE_PLUGIN_ROOT}/bin/devflow"` for the `devflow` commands below.
Each invocation resolves the active verified binary, including after updating.

Run `devflow version`, then `devflow update $ARGUMENTS`, then `devflow version` again, and
tell me the old and new version. Updates are checksum- and attestation-verified; if
verification fails, report it and don't retry.

For a project update to the latest stable release, use the manager skill. It
also verifies the active binary and updates the project's workflow pins.
