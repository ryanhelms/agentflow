# OKF rollout in the Agentflow repository

Agentflow will use GitHub Issues for its work and a Git-backed Open Knowledge
Format (OKF) bundle for its knowledge. FileSystem and Repository knowledge
providers will share OKF v0.3 document handling. The Repository provider will
continue to publish Agentflow changes through pull requests. This builds on
the issue-to-ADR pilot.

Each implementation task uses `engineering-task@1` and records its
objective, scope, dependencies, acceptance criteria, and verification.

| Task | Depends on |
| --- | --- |
| Compatible reader and signed release | Rollout scope |
| Default OKF writer | Verified signed compatible reader release |
| Exact task-to-knowledge links | Compatible reader |
| Bundle migration and seed docs | Signed compatible reader and OKF writer |
| Reviewed merge handling | Exact links and migrated bundle |
| Repository dogfood and rollback | Writer, links, migration, and merge handling |

## Release order

1. **Ship the compatible reader.** Add OKF concept discovery to FileSystem
   and Repository knowledge alongside existing Agentflow Markdown. Verify the
   signed release against both formats through their normal read entry points.
   Verify that the same release reads the current Agentflow ADR using the
   committed configuration. Record the tag and verification before applying
   any live bundle changes.
2. **Ship the OKF writer and exact task links.** After the coordinator verifies
   the signed compatible reader release, integrate the writer. Default new
   file-backed documents to OKF through a knowledge-only setting. Retain an
   explicit legacy format option and preserve each existing document's format
   until its reviewed migration. Accept a stable document reference on the
   GitHub issue and expose exact lookup in CLI and MCP.
3. **Migrate and seed the bundle.** Use one reviewed change to move the pilot
   ADR into `docs/knowledge/`. Add a bundle index, task workflow guide, and
   publication runbook. Update the selected Repository directory and the
   pilot verification together. Verify the live configuration from a fresh
   checkout after merge.
4. **Complete a real delivery.** Preview a GitHub task transition using
   trusted base configuration. Approve a scoped issue write for an eligible
   Agentflow PR merge, verify one closure, then replay. Capture links to the
   task, knowledge concept, PR, release, and workflow evidence. Rehearse
   restoring the prior bundle layout and caller configuration in isolation.

## Document behavior

Follow the [OKF v0.3 specification](https://github.com/open-knowledge-format/open-knowledge-format/blob/527e81aff55992be21507e97004286789126f88c/SPEC.md).
The bundle root index identifies the version. The providers discover nested
concepts and recognize reserved index and log filenames. Human and agent
readers can follow standard Markdown links within the bundle. Broken links
can be reported without preventing unrelated concepts from being read.

Managed concepts keep their Agentflow scope and ID across a move. OKF's own
concept ID follows the path. A reviewed move repairs affected Markdown links.
Publication checks the expected revision and uses an operation ID to reconcile
replays. Migration carries prior operation receipts and their matching
semantics. The knowledge format choice applies to FileSystem and Repository
knowledge. File-backed tasks and the GitHub Wiki provider have separate
compatibility requirements.

## Evidence and rollback

The attested [v0.38.0 reader](https://github.com/ryanhelms/agentflow/releases/tag/v0.38.0)
was installed and verified against mixed FileSystem and Git-backed Repository
fixtures and the live legacy ADR.
The attested [v0.39.0 writer](https://github.com/ryanhelms/agentflow/releases/tag/v0.39.0)
passed OKF publication, legacy override, exact lookup, and replay checks before
the Agentflow bundle moved.

Run `make check` for each change and `make e2e` for MCP, hook, Claude adapter,
or initializer changes. Exercise FileSystem and Repository through the
published reader, then verify the bundle through CLI and the regular Claude
Code Desktop workspace after cutover. Use the saved native sign-ins for
interactive checks. Verify the workflow's permissions and its opt-out with an
isolated replay before enabling the scoped writer.

To rehearse rollback, disable the scoped merge writer, revert the bundle and
provider-directory change together in a throwaway checkout, and read the
restored ADR with the compatible release. Check that the task reference and
receipts are still traceable. Reconcile any completed remote effects using
their original operation IDs.
