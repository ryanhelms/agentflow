# devflow for Claude Code

A Claude Code plugin in the `nscale` marketplace (this repository). It provides:

- MCP server `devflow` (all `devflow_*` tools)
- Skill `devflow` with the commit, PR, confirmation and untrusted-content rules
- Commands `/devflow:start`, `/devflow:commit`, `/devflow:pr`, `/devflow:review`,
  `/devflow:status`, `/devflow:standup`
- Hooks: `SessionStart` (ticket context), `PreToolUse` on Bash (blocks raw `git commit`
  with an invalid message), `Stop` (reminds about uncommitted work and open criteria)

Repos turn it on in `.claude/settings.json` (written by `devflow init`):

```json
{
  "extraKnownMarketplaces": {
    "nscale": { "source": { "source": "github", "repo": "nscaledev/devflow" } }
  },
  "enabledPlugins": { "devflow@nscale": true },
  "includeCoAuthoredBy": false
}
```

`bin/launch` runs the repo's `.devflow/bin/devflow`, so Claude Code uses the same
self-updating binary as every other client. Hook and settings formats can change between
Claude Code versions; `devflow doctor` checks them.
