# Clear the Devflow CodeQL findings before the Agentflow cutover

Fix the 37 open CodeQL findings on the current Devflow default branch, carry the fixes into the Agentflow rename, and verify a fresh scan of each. The failure on PR #121 surfaced 18 annotations from this baseline. The scope below includes all 37 findings. [Default-branch alerts](https://github.com/nscaledev/devflow/security/code-scanning?query=branch%3Amain+tool%3ACodeQL+is%3Aopen) · [Rename PR alerts](https://github.com/nscaledev/devflow/security/code-scanning?query=pr%3A121+tool%3ACodeQL+is%3Aopen) · [Failing check](https://github.com/nscaledev/devflow/runs/138667151374)

## Alert coverage

Numbers are GitHub code-scanning alert IDs. First-party action and runner-environment alerts were reidentified on the rename branch.

| Work | Findings | On `main` | On PR #121 |
| --- | ---: | --- | --- |
| T-01 · Blueprint DOM rendering | 2 high | 57, 58 | 57, 58 |
| T-02 · Slack webhook detection | 1 high | 18 | 18 |
| T-03 · MCP launcher arguments and paths | 2 medium | 59, 60 | 59, 60 |
| T-04 · Setup action `GITHUB_ENV` and `GITHUB_PATH` | 2 medium | 1, 2 | 80, 81 |
| T-05 · Checkout and release trust boundaries | 4 medium | 19, 49, 50, 62 | 19, 49, 50, 62 |
| T-06 · First-party setup references | 13 medium | 46–48, 51–56, 63–66 | 67–79 |
| T-07 · Third-party action references | 13 medium | 12, 15, 17, 20–27, 31, 61 | 12, 15, 17, 20–27, 31, 61 |
| **Total** | **37** | **3 high, 34 medium** | **3 high, 34 medium** |

## Application and documentation fixes

These three changes have disjoint source files and can be reviewed independently on the current Devflow default branch.

- **T-01 · Render the commit tester as DOM nodes.** In `docs/artifact/nscale-delivery-loop.html`, replace HTML-string rendering of parsed input in both the results and the status message with DOM construction and text nodes. Build intentional line breaks, badges, and inline code as elements. Regenerate `docs/blueprint.html` with `scripts/build-blueprint.sh`. Verify valid and rejected subjects, project keys, transitions, and smart-commit formatting in a browser. Inputs containing markup characters should appear as literal text in both files. [CodeQL DOM rendering rule](https://codeql.github.com/codeql-query-help/javascript/js-xss-through-dom/)
- **T-02 · Validate Slack webhook candidates.** In `internal/app/secrets.go`, find URL candidates within the text being scanned, then validate each candidate’s scheme, exact hostname, and service path. Keep detection of a webhook embedded in a staged diff line or display text. Add cases for legitimate URLs embedded in quotes and prose, lookalike domains and paths, and the existing redaction behavior. Exercise both staged-diff and display-scanning entry points. [CodeQL regex rule](https://codeql.github.com/codeql-query-help/go/go-regex-missing-regexp-anchor/)
- **T-03 · Launch the MCP server with a fixed program and separate arguments.** Change `adapters/plugin-mcp.cjs` and the descriptor generator together. For Codex, package both platform launchers as build-time data and materialize the appropriate launcher in a private temporary location. Invoke POSIX by script file and Windows through PowerShell’s `-File` interface. Keep the client allowlist, working directory, stdio, artifact verification, process lifecycle, and cleanup. Regenerate the Codex descriptor and test installed Codex and a packaged host on POSIX and Windows. Verify ordinary paths with spaces and that path characters remain data throughout launch. [CodeQL command-line rule](https://codeql.github.com/codeql-query-help/javascript/js-indirect-command-line-injection/) · [CodeQL environment rule](https://codeql.github.com/codeql-query-help/javascript/js-shell-command-injection-from-environment/)

## GitHub Actions fixes

- **T-04 · Export only runner-owned paths.** In `actions/setup/action.yml`, prepare a private runner-owned location for the verified action scripts, flow map, and optional binary. Derive exported paths from that location and validate that each is canonical, contained, and single-line before writing to the runner command files. Keep `PATH`, `FLOW_MAP`, and the product root and update settings available to callers. Verify script-only and binary modes, repeat invocation, and normal paths containing spaces. [CodeQL PATH rule](https://codeql.github.com/codeql-query-help/actions/actions-envpath-injection-medium/) · [CodeQL environment rule](https://codeql.github.com/codeql-query-help/actions/actions-envvar-injection-medium/)
- **T-05 · Separate configuration reads, builds, and publishing by trust.** Apply the following rules in the four reported workflows. Use least-privilege permissions per job and disable persisted checkout credentials wherever they are unnecessary. A valid revision must come from the expected repository and an approved source for that event. [CodeQL checkout rule](https://codeql.github.com/codeql-query-help/actions/actions-untrusted-checkout-medium/)

  | Workflow | Change and acceptance check |
  | --- | --- |
  | `auto-issues.yml` and `slack-notify.yml` | Resolve the caller repository’s trusted default-branch config through GitHub and read only the expected config data at the resolved SHA. Exercise successful issue and notification paths, including caller events where a PR head supplies different config. |
  | `transitions.yml` | Preserve the event’s reviewed base revision where that is the policy source. Verify the base repository and ref before reading the config at that exact revision. Exercise same-repo PRs, fork PRs, pushes, and a base branch that advances after the event. |
  | `semver-release.yml` | Bind the release and tag to the exact reviewed commit that will be built. Build with read-only access and place publishing credentials in a separate job that validates the artifact’s repository, commit, build run, and digest. Validate the binary, image, and chart paths with a release canary. An unexpected revision or artifact must stop publication. |

- **T-06 · Resolve nested setup at the same commit as its caller.** Rehearse GitHub Cloud’s `uses: $/actions/setup` in a private cross-repository reusable workflow and in the composite ship action. It resolves against the owning workflow or action at its running SHA and needs no extra checkout. The installed actionlint rejected this syntax in a minimal fixture. Check maintained linter support and verify acceptance by GitHub Actions and CodeQL in the canary before choosing it. If tooling support is missing, first land a compatible hardened setup revision on the current default branch, then reference its durable full SHA in a follow-up change. Verify that the pinned code supports both sides of the Devflow-to-Agentflow cutover before adopting that fallback. Existing externally supported action names and major tags remain available. [GitHub self-repository action references](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#example-using-an-action-in-the-same-repository-as-the-workflow-at-the-running-commit-recommended)
- **T-07 · Pin flagged third-party actions to verified commits.** Resolve each of the 13 affected calls to the full commit SHA of a reviewed release in the upstream repository, keeping the release version in a comment. Retain the existing inputs and supported major versions. Update the workflow pinning guidance in `AGENTS.md` to document immutable pins. Run the normal CI, release snapshot, and affected reusable workflows using those pins. [CodeQL action pinning rule](https://codeql.github.com/codeql-query-help/actions/actions-unpinned-tag/)

## Sequence and proof of closure

1. Start T-01 through T-03 independently. In parallel, implement T-04 and test whether the production tooling accepts same-commit references. Complete T-05 before the pin sweep so the overlapping workflow files have one set of edits to review. Apply T-06 to the hardened setup action, then finish T-07 on the resulting workflow files.
2. Land the reviewed remediation on the current Devflow default branch through normal PRs. Run `make check`, `go test ./internal/setup/`, the Claude Code `make e2e` gate where applicable, and the POSIX and Windows launcher and installer checks. Rehearse a private consumer workflow, the composite ship action, and release publication with attestation and rollback. Use the current signed Devflow release as the upgrade baseline if remediation publishes a newer patch.
3. Reconcile the fixes into PR #121 and rerun Actions, Go, and JavaScript/TypeScript CodeQL on its exact head. Require a passing CodeQL check, map every alert ID in the table to a fixed source location, and confirm the corresponding findings are closed by fresh scans of default branch and the rename branch. Check that the fixes introduce no new alerts and keep the existing rules enabled.
4. Verify MCP Gateway’s `key-check / validate` on a candidate with the tested workflow and action references. Return to the [Agentflow cutover plan](plan.md) for approval and live execution once the checks and review are clear.
