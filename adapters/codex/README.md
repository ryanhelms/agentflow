# Codex CLI

- `config.toml` → `.codex/config.toml` in the repo (trusted projects) or merged into
  `~/.codex/config.toml` by `agentflow install --client codex`.
- `prompts/*.md` → copied to `~/.codex/prompts/`; run them as `/prompts:agentflow-commit` etc.
- Instructions come from the repo's `AGENTS.md` block.

Codex has no lifecycle hooks for agentflow to use, so enforcement relies on the git
`commit-msg` hook and CI. Config locations vary by Codex version; `agentflow doctor` checks them.
