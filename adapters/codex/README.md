# Codex CLI

- `config.toml` → `.codex/config.toml` in the repo (trusted projects) or merged into
  `~/.codex/config.toml` by `devflow install --client codex`.
- `prompts/*.md` → copied to `~/.codex/prompts/`; run them as `/prompts:devflow-commit` etc.
- Instructions come from the repo's `AGENTS.md` block.

Codex has no lifecycle hooks for devflow to use, so enforcement relies on the git
`commit-msg` hook and CI. Config locations vary by Codex version; `devflow doctor` checks them.
