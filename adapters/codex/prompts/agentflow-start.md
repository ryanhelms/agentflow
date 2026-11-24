---
description: Pick a Jira ticket and start work on it with agentflow
argument-hint: "[JIRA-KEY]"
---
If $ARGUMENTS contains a Jira key use it, otherwise call agentflow_my_work and ask me which ticket.
Call agentflow_collisions, then agentflow_start (wait for my confirmation if asked), then agentflow_context,
and give me a three-line brief: goal, acceptance criteria, next status.
