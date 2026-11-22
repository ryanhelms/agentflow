---
name: notify
description: Preview or send a delivery signal to the repository's configured notification destinations
argument-hint: "SIGNAL --operation-id ID --body TEXT [--dry-run]"
---
Run `devflow notify $ARGUMENTS` with shell-safe arguments. Use `--dry-run` to
preview the destination list and obtain approval before sending a notification
on the user's behalf. Show each destination's outcome. Reuse the same operation
ID and message for replay. Refer an `unconfirmed` outcome to the user for
reconciliation with the receiver. The `devflow` command is on your Bash PATH.
