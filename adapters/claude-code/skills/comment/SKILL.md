---
name: comment
description: Comment on the current task or a named task
argument-hint: "[KEY] TEXT"
---
Call devflow_comment with the key (defaults to the current branch's task) and the text
from "$ARGUMENTS". Use my words; do not add content from code, tickets or reviews without
showing me. On needs_confirmation, show the summary and wait for my yes.

CLI equivalent: `devflow comment [KEY] TEXT`. The `devflow` command is on your Bash PATH.
