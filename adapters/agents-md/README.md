# AGENTS.md block

`devflow install` inserts `AGENTS.md` between the `devflow:start` / `devflow:end` markers in
the repo's `AGENTS.md` (creating it if needed) and replaces the block on later installs.
Codex, Cursor, Copilot and other AGENTS.md-aware clients read it directly; Gemini CLI and
Grok CLI are pointed at it from their own config. Client support for AGENTS.md varies by version.
