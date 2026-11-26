---
name: promote
description: Explain how the user opens the develop to main release PR
---
`agentflow promote` opens the release pull request from the integration branch (`develop`)
into `main`, to be merged with a merge commit. Only the user runs it: agentflow refuses it
inside an AI agent.

Do not run it yourself. Tell me to run this in my own terminal:

```sh
agentflow promote --dry-run   # check what it opens
agentflow promote
```
