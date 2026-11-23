---
name: review
description: Work through unresolved review comments on the current PR
---
For issue-linked reviews, use `devflow_context` to load the task and its linked
design decisions. Read a decision by its exact reference with `devflow_docs_get`.

1. Call devflow_review_threads and list the unresolved threads.
2. Fix them one at a time with convention commits using devflow_commit_prepare
   and devflow_commit. Use devflow_push when asked to update the remote branch.
3. Reply on each thread with devflow_review_reply, linking the fixing commit.
   Follow the effective review_reply policy and the approval response in AGENTS.md.

`devflow review` lists threads with their IDs. Use `devflow review --json` for structured output
and `devflow review reply THREAD_ID BODY` to reply.
