# Security

devflow runs on every engineer's machine and in CI, with access to Jira, Confluence and
GitHub as that engineer. Treat it as security-sensitive code.

## Reporting

Report suspected vulnerabilities privately in the `#security` Slack channel or to the
Security team. Do not open a public issue or PR that describes the problem.

To stop a bad release from running anywhere, follow
[docs/operations.md › Block a release](docs/operations.md#block-a-release).

## Design guarantees

- **Release integrity.** Releases are built only by `.github/workflows/release.yml` in
  `nscaledev/devflow`. Each archive is listed in `checksums.txt` and has a GitHub build
  provenance attestation. The launcher and the self-updater install a build only when
  both the SHA-256 and `gh attestation verify --repo nscaledev/devflow` pass.
- **Credentials.** Developer tokens are stored in the OS keychain (macOS Keychain, Secret
  Service, Windows Credential Manager). They are never written to config files, logs or
  MCP responses.
- **Untrusted content.** Jira, Confluence and GitHub text is returned to AI agents inside
  labelled reference blocks. Agents are told not to follow instructions in it, and remote
  writes whose payload came from that text always require confirmation.
- **Never list.** At every autonomy level devflow refuses to force-push, push to
  protected branches, merge PRs, delete remote branches, or act on tickets as another user.
- **Audit.** Every local and remote action is appended to `~/.devflow/audit.log`.
