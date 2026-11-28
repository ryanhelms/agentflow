---
type: Decision
title: Provider contracts and repository configuration
description: How Agentflow selects providers, resolves configuration, and preserves stable references.
tags: [architecture, providers]
x-agentflow-ref:
  scope: engineering
  id: provider-architecture
x-agentflow-operations:
    provider-pilot-adr-issue-25: a85433bd0efdb7cf295801ae753690c153534a18a99fd9ffa7f4422a2c5d9f39
    provider-pilot-adr-issue-25-verified-checkout: 7eb56be3fddae81068c3768e4ec28d793adce4136fddd38fd0da3787d3750e4d
---
# Provider contracts and repository configuration

## Status

Accepted for the agentflow pilot. Consumer rollout follows the compatibility canary and rollback rehearsal.

## Context

Agentflow coordinates local Git, task management, knowledge, code hosting, and delivery automation. Existing repositories use Jira, Confluence, and GitHub. Teams need to select services independently while keeping the same CLI, MCP, and workflow operations.

## Decision

Agentflow owns typed contracts for the objects it reads and writes. Providers translate those objects to their native APIs or file formats. Agentflow resolves code hosting from Git remotes and keeps Task Management, Knowledge Base, DevOps, SecOps, and Notifications in separate configuration sections. DevOps checks, deployments, and releases can use separate providers.

Schemas define defaults and validate overrides for each selected provider. Agentflow resolves a task template before creation and retains its revision for later operations when the provider supports that metadata. Qualified references preserve identity across branches, commits, pull requests, and provider instances.

FileSystem stores tasks and documents as Markdown with front matter. Repository adds Git publication, revision checks, and support for review through a pull request. Each runtime plans its provider tooling independently. CLI and MCP use the same setup and operation policies.

## Consequences

Existing Jira and Confluence selections retain their compatibility path. GitHub Issues and Repository knowledge serve the agentflow pilot. The delivery plan separates the product's GoReleaser workflow from the release-please workflow used by consumer repositories.

The pilot checks repository-scoped references, repeatable setup, approval boundaries, and one issue transition after an authorized merge. Consumer rollout also needs recorded replay and rollback results.

## Pilot verification

The initial ADR publication opened PR #47 and returned the same PR on retry. The first merge workflow stopped at document verification because the pull request's recorded base SHA predated JSON document search. It made no change to issue #25.

The workflow builds agentflow at the reviewed merge commit and verifies this ADR before applying the enrolled issue's transition. The runner record and its replay are tracked on issue #25.