---
name: review
description: Work through unresolved review comments on the current PR
---
1. Call devflow_review_threads and list the unresolved threads.
2. Fix them one at a time with convention commits via devflow_commit.
3. Reply on each thread with devflow_review_reply, linking the fixing commit. Ask before posting.

CLI equivalent: `devflow review`.
