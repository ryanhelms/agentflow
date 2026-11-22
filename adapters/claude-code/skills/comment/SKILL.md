---
name: comment
description: Comment on the current task or a named task
argument-hint: "[KEY] TEXT"
---
Call devflow_comment with the key (defaults to the current branch's task) and the text
from "$ARGUMENTS". Use my words; do not add content from code, tickets or reviews without
showing me. On needs_confirmation, show the summary and wait for my yes.

On the Desktop Rovo route, report the verified `comment_id` on success. If the
outcome is uncertain, stop and inspect the issue with the user before requesting
another approval. A fresh approval can post the same text again.

CLI equivalent: `devflow comment [KEY] TEXT`. For the Desktop Rovo enrollment, use
`devflow --native-client claude-desktop comment [KEY] TEXT`. The `devflow` command is on your Bash PATH.
