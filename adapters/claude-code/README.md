# devflow for Claude Code

A Claude Code plugin in the `nscale` marketplace (this repository). It provides:

- MCP server `devflow` (all `devflow_*` tools)
- Skill `devflow` with the commit, PR, confirmation and untrusted-content rules
- A skill per CLI command, run as `/devflow:<name>`: `init`, `remove`, `install`, `login`,
  `logout`, `doctor`, `work`, `start`, `context`, `commit`, `preview`, `transition`, `pr`,
  `review`, `status`, `standup`, `docs`, `adr`, `release`, `update`, `version`. `init`,
  `remove`, `install`, `login`, `logout` and `update` run only when you invoke them.
- `bin/devflow`, on Claude's Bash PATH: runs the installed devflow or installs the latest
  verified release, so the skills work before a repo is set up
- Hooks: `SessionStart` (ticket context; in an unset repo owned by an org in `init.orgs`,
  runs `devflow init` automatically, see the main README), `PreToolUse` on Bash (blocks raw `git commit`
  with an invalid message, force-push, pushes to protected branches and `gh pr merge`), `Stop` (reminds about uncommitted work and open criteria)

To install it for yourself (Claude Code 2.1.275 or later):

```
/plugin install devflow --marketplace nscaledev/devflow
```

Repos turn it on for everyone in `.claude/settings.json` (written by `devflow init`):

```json
{
  "extraKnownMarketplaces": {
    "nscale": { "source": { "source": "github", "repo": "nscaledev/devflow" } }
  },
  "enabledPlugins": { "devflow@nscale": true },
  "includeCoAuthoredBy": false
}
```

`bin/launch` runs the repo's `.devflow/bin/devflow`, then the installed binary, then
`bin/devflow` to install one, so Claude Code uses the same self-updating binary as every
other client. `bin/devflow` is a copy of `templates/repo/.devflow/bin/devflow`; a test
keeps them in sync. Hook and settings formats can change between
Claude Code versions; `devflow doctor` checks them.
