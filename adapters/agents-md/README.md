# AGENTS.md block

`agentflow init` and `agentflow install` insert the shared workflow guidance between
the `agentflow:start` and `agentflow:end` markers in the repo's `AGENTS.md`.
`agentflow init --guidance-only` refreshes that block for an existing repository.
Codex, Cursor, Copilot and other AGENTS.md-aware clients read it directly; Gemini CLI and
Grok CLI are pointed at it from their own config. Client support for AGENTS.md varies by version.
