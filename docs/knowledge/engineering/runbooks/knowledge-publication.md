---
type: Runbook
title: Publish a knowledge document
description: Publish a revision-checked Open Knowledge Format concept through a reviewed PR.
tags: [knowledge, okf, publication]
x-devflow-ref: {scope: engineering, id: knowledge-publication}
x-devflow-operations:
    devflow-okf-publication-runbook-conflicts-v1: c299b078ab1f52380335198087264d950b76c5e033b9ba317bdda125afa30a67
---
# Publish a knowledge document

The Devflow bundle is rooted at `docs/knowledge/`. Start from its [index](../../index.md)
or use `devflow docs search QUERY --space engineering`. Use `devflow docs get REF --json`
to read the exact file and its revision. New Repository knowledge documents use OKF.

For an example publication, supply a qualified reference, title, Markdown body,
and an operation ID that identifies this approved request.

```sh
devflow docs publish repository/engineering/example-policy \
  --title 'Example policy' --body 'The approved policy and its rationale.' \
  --operation-id example-policy-1
```

For an update, also supply `--expected-revision` with the revision just read.
Review the preview, then approve it. Repository publication returns a review PR.
Read the document again after the PR merges to verify the new base revision.

Reuse the same operation ID and document inputs for an interrupted request.
Reconcile the prior receipt or review PR before starting a different operation.
If the expected revision is stale, reread the document and review a new draft.

Managed documents keep their Devflow scope and ID in `x-devflow-ref`. Keep that
field when moving a concept and update the bundle index and affected Markdown
links in the same reviewed change. Verify the same reference can still be
opened from its linked task.

See [the task workflow](../guides/task-workflow.md) and
[provider architecture](../decisions/provider-architecture.md).

## Diagnose publication conflicts

- If Devflow reports a stale revision, run `devflow docs get REF --json` again. Compare the latest body with your draft before choosing a new operation ID and confirming the update.
- If the same operation ID returns an existing PR, open that PR to check its head and review state. After merge, replaying the original request should report `already_applied` and return the current document revision.
- If discovery reports duplicate references, check the bundle for two files with the same `x-devflow-ref`. Review the correction in Git and verify the exact reference before retrying publication.
