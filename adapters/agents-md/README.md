# AGENTS.md block

`devflow init` and `devflow install` insert the shared workflow guidance between
the `devflow:start` and `devflow:end` markers in the repo's `AGENTS.md`.
`devflow init --guidance-only` refreshes that block for an existing repository.
Codex, Cursor, Copilot and other AGENTS.md-aware clients read it directly; Gemini CLI and
Grok CLI are pointed at it from their own config. Client support for AGENTS.md varies by version.
