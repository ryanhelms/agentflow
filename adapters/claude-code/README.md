# agentflow for Claude Code

A Claude Code plugin in the `agentflow` marketplace (this repository). It provides:

- MCP server `agentflow` (all `agentflow_*` tools)
- Skill `agentflow` with the commit, PR, confirmation and untrusted-content rules
- A skill per CLI command, run as `/agentflow:<name>`: `init`, `remove`, `pin`, `install`,
  `login`, `logout`, `doctor`, `ai`, `work`, `start`, `context`, `commit`, `preview`, `transition`, `pr`,
  `review`, `status`, `standup`, `docs`, `adr`, `release`, `update`, `version`. `init`,
  `remove`, `install`, `login`, `logout` and `update` run only when you invoke them.
- `bin/agentflow`, on Claude's Bash PATH: runs the installed agentflow or installs the latest
  verified release, so the skills work before a repo is set up
- Hooks: `SessionStart` (ticket context; in an unset repo owned by an org in `init.orgs`,
  runs `agentflow init` automatically, see the main README), `PreToolUse` on Bash (blocks raw `git commit`
  with an invalid message, force-push, pushes to protected branches and `gh pr merge`), `Stop` (reminds about uncommitted work and open criteria)

To install it for yourself (Claude Code 2.1.275 or later):

```
/plugin install agentflow --marketplace ryanhelms/agentflow
```

Repos turn it on for everyone in `.claude/settings.json` (written by `agentflow init`):

```json
{
  "extraKnownMarketplaces": {
    "agentflow": { "source": { "source": "github", "repo": "ryanhelms/agentflow" } }
  },
  "enabledPlugins": { "agentflow@agentflow": true },
  "includeCoAuthoredBy": false
}
```

`bin/launch` runs the repo's `.agentflow/bin/agentflow`, then the installed binary, then
`bin/agentflow` to install one, so Claude Code uses the same self-updating binary as every
other client. `bin/agentflow` is a copy of `templates/repo/.agentflow/bin/agentflow`; a test
keeps them in sync. Hook and settings formats can change between
Claude Code versions; `agentflow doctor` checks them.
