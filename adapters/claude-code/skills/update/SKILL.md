---
name: update
description: Update devflow to the latest release now, or roll back one version
argument-hint: "[--rollback]"
disable-model-invocation: true
---
The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

Run `devflow version`, then `devflow update $ARGUMENTS`, then `devflow version` again, and
tell me the old and new version. Updates are checksum- and attestation-verified; if
verification fails, report it and don't retry.
