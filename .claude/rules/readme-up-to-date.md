# Keep README.md up to date

The same rule is in AGENTS.md ("Keep README.md current"); AGENTS.md is the source of truth.

Every change that a reader of README.md would notice must update README.md in the same
PR. That includes new or removed commands, MCP tools, workflows or actions, changed
setup or install steps, config options, supported clients, release or versioning
behaviour, security guarantees, status (current version, approval), and links to docs.

The status area at the top of README.md (Status, Current release, Owner, Architecture,
Support) must always be true. The release workflow writes the current version between the
`<!-- agentflow:version -->` markers; never edit that by hand. Update the Status row yourself
whenever approval, the rollout phase or adoption changes.

Before you finish a task:

1. Read the README sections that cover what you changed.
2. Update anything that is now wrong or missing, in the README's existing style.
3. If nothing a reader would notice changed, say so in the PR description.

Never let the README describe behaviour the code no longer has.
