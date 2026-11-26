// The usage text of commands not migrated to the command registry yet.

package cli

import "github.com/ryanhelms/agentflow/internal/command"

// usageHeader opens the usage text, before the first section.
const usageHeader = "agentflow links git work to your configured task, knowledge and code providers.\n\nUsage: agentflow <command> [flags]\n"

// legacyUsage holds the usage blocks of the commands that are not registered
// in the command registry yet, in today's order. renderUsage skips a block
// whose Path a registered Spec or Verb binds: the registered block, with the
// same Section and Order, replaces it, so the text stays byte for byte the
// same. Blocks that share a Path are skipped together. A Path that no Spec
// can bind (it holds a flag or an underscore) keeps its block until the
// phase 3 cleanup.
var legacyUsage = []command.UsageBlock{
	{Path: "ui", Section: command.SectionSetup, Order: 100, Text: "  ui [--screen NAME] [--role ROLE] [--no-open] [--port N] [--foreground] [directory]\n                                 open or reuse the local repository console"},
	{Path: "ui", Section: command.SectionSetup, Order: 200, Text: "  ui --status | --stop           inspect or stop the shared local UI daemon"},
	{Path: "init", Section: command.SectionSetup, Order: 300, Text: "  init [--force]                 write .agentflow/, hooks, workflow, client configs and release setup"},
	{Path: "init", Section: command.SectionSetup, Order: 400, Text: "  init --detect-providers        show local selection evidence and supported choices as JSON"},
	{Path: "init", Section: command.SectionSetup, Order: 500, Text: "  init [--guided|--non-interactive] [--provider ROLE=ID ...]\n                                 review and persist provider choices; the terminal guides by default"},
	{Path: "init", Section: command.SectionSetup, Order: 600, Text: "  init --guidance-only           refresh the managed AGENTS.md block"},
	{Path: "remove", Section: command.SectionSetup, Order: 700, Text: "  remove [--dry-run] [--force] [--keep-releases]\n                                 undo init: delete agentflow's files, strip its entries from\n                                 AGENTS.md and client configs, stop automatic init here"},
	{Path: "pin", Section: command.SectionSetup, Order: 800, Text: "  pin [--check]                  pin .github/workflows/agentflow.yml to this agentflow release\n                                 (review the change in a PR, like any dependency bump)"},
	{Path: "install", Section: command.SectionSetup, Order: 900, Text: "  install [--client all|NAME]    configure AI clients (claude, codex, grok, cursor, vscode, gemini)"},
	{Path: "setup", Section: command.SectionSetup, Order: 1000, Text: "  setup [plan|verify] [--companion ID] [--client NAME] [--json]   inspect provider and client readiness"},
	{Path: "setup providers", Section: command.SectionSetup, Order: 1100, Text: "  setup providers [--explain] [--json]   list every provider each role accepts and which one is selected;\n                                 --explain shows effective settings and the layer that set each"},
	{Path: "setup apply", Section: command.SectionSetup, Order: 1200, Text: "  setup apply [--install-gh | --client NAME] [--yes]   install pinned gh or register a project adapter"},
	{Path: "setup apply", Section: command.SectionSetup, Order: 1300, Text: "  setup apply --companion ID [--install | --client NAME] [--yes]\n                                 install or register one declared MCP companion"},
	{Path: "setup login", Section: command.SectionSetup, Order: 1400, Text: "  setup login --provider NAME [--client NAME] [--server NAME] [--register [--client-id ID] | --public-client-id ID --team-id TEAM]\n                                 start sign-in through a provider CLI or MCP client\n                                 use --client agentflow for Jira, Confluence, Asana or an approved Slack app"},
	{Path: "setup logout", Section: command.SectionSetup, Order: 1500, Text: "  setup logout --provider NAME --client agentflow\n                                 disconnect the selected local Agentflow enrollment"},
	{Path: "doctor --providers", Section: command.SectionSetup, Order: 1600, Text: "  doctor --providers [--client NAME]   verify provider and client readiness"},
	{Path: "login", Section: command.SectionSetup, Order: 1700, Text: "  login [--email E] [--token-stdin]   store your Atlassian API token in the OS keychain"},
	{Path: "logout", Section: command.SectionSetup, Order: 1800, Text: "  logout                         remove the legacy Atlassian API-token login"},
	{Path: "doctor", Section: command.SectionSetup, Order: 1900, Text: "  doctor [--fix]                 check binary, hooks, auth, client config and API access"},
	{Path: "work", Section: command.SectionWork, Order: 2000, Text: "  work                           list work from the selected task provider"},
	{Path: "task create", Section: command.SectionWork, Order: 2100, Text: "  task create [--summary S] [--project P] [--type T] [--description D] [--link-to REF]\n              [--template NAME --template-revision REV] [--dry-run] [--yes]"},
	{Path: "task link", Section: command.SectionWork, Order: 2200, Text: "  task link KEY --url URL [--title T] [--yes]   add a web link to a Jira issue"},
	{Path: "start", Section: command.SectionWork, Order: 2300, Text: "  start KEY [--worktree]         branch or worktree for KEY; apply supported start effects.\n                                 Worktrees go to <worktree.root>/<repo>/<branch> (default ~/.worktrees)\n                                 with a git-excluded .agentflow/worktree.yml and, if configured, go.work"},
	{Path: "worktree go-work", Section: command.SectionWork, Order: 2400, Text: "  worktree go-work               regenerate go.work in this worktree from worktree.go_work"},
	{Path: "finish", Section: command.SectionWork, Order: 2500, Text: "  finish [BRANCH] [--dry-run] [--yes]\n                                 after the PR merges: remove the worktree, the local and the remote branch"},
	{Path: "sweep", Section: command.SectionWork, Order: 2600, Text: "  sweep [--all-repos] [--dry-run] [--stale-days N] [--json] [--yes]"},
	{Path: "delegate", Section: command.SectionWork, Order: 2700, Text: "  delegate --repo NAME [--agent claude|codex] [--start] [--dry-run] [--json] [--yes] TASK\n                                 hand TASK to a session in another repository: a running tmux\n                                 session there, or a new one in its checkout\n                                 finish merged worktrees; report dirty, unpushed, stale and orphaned ones"},
	{Path: "promote", Section: command.SectionWork, Order: 2800, Text: "  promote [--from develop] [--to main] [--dry-run] [--yes]\n                                 open the develop -> main release PR (merge commit); people only, not agents"},
	{Path: "context", Section: command.SectionWork, Order: 2900, Text: "  context [KEY]                  ticket bundle for the current branch"},
	{Path: "ac", Section: command.SectionWork, Order: 3000, Text: "  ac [list] [KEY] [--json]       acceptance criteria checklist with local done state"},
	{Path: "ac done", Section: command.SectionWork, Order: 3100, Text: "  ac done|undo ITEM [--key KEY] [--evidence E] [--post] [--yes]\n                                 tick or untick a criterion; --post comments progress"},
	{Path: "scope", Section: command.SectionWork, Order: 3200, Text: "  scope [--json]                 changes and new TODOs that look outside the ticket"},
	{Path: "comment", Section: command.SectionWork, Order: 3300, Text: "  comment [KEY] TEXT [--yes]     comment on the task"},
	{Path: "collisions", Section: command.SectionWork, Order: 3400, Text: "  collisions [KEY] [--json]      other PRs on this task or touching your files"},
	{Path: "config", Section: command.SectionWork, Order: 3500, Text: "  config OP [--input FILE|-]     inspect or prepare repository console settings from JSON"},
	{Path: "config publish", Section: command.SectionWork, Order: 3600, Text: "  config publish [--input FILE|-]   publish the exact approved repository change"},
	{Path: "config save_personal", Section: command.SectionWork, Order: 3700, Text: "  config save_personal [--input FILE|-]   save the exact approved preferences"},
	{Path: "policy explain", Section: command.SectionWork, Order: 3800, Text: "  policy explain [ACTION]          show autonomy, repository ceilings and review windows"},
	{Path: "policy status", Section: command.SectionWork, Order: 3900, Text: "  policy status ID                inspect a shared approval"},
	{Path: "policy approve", Section: command.SectionWork, Order: 4000, Text: "  policy approve ID --confirm-token TOKEN   record a reviewed approval for its worker"},
	{Path: "push", Section: command.SectionWork, Order: 4100, Text: "  push [--prepare | --confirm-token TOKEN] [--yes]   publish the current feature branch"},
	{Path: "pr mark-ready", Section: command.SectionWork, Order: 4200, Text: "  pr mark-ready [--prepare | --confirm-token TOKEN] [--yes]   mark the current draft pull request ready for review"},
	{Path: "commit", Section: command.SectionWork, Order: 4300, Text: "  commit [-m MSG] [--path PATH] [--prepare | --confirm-token TOKEN] [--split] [--dry-run] [--yes]   guided commit using the configured workflow"},
	{Path: "preview", Section: command.SectionWork, Order: 4400, Text: "  preview [MSG]                  configured task effects of a message or of unpushed commits"},
	{Path: "transition", Section: command.SectionWork, Order: 4500, Text: "  transition [KEY] EVENT [--dry-run] [--yes]      e.g. pr.merged or \"#qa\""},
	{Path: "secops triage", Section: command.SectionWork, Order: 4600, Text: "  secops triage [--source REF] [--since RFC3339] [--apply] [--yes]\n                                 preview or create tasks from configured security events"},
	{Path: "pr", Section: command.SectionWork, Order: 4700, Text: "  pr [--check] [--run] [--title T] [--draft] [--prepare | --confirm-token TOKEN] [--yes]   readiness check, then open the PR"},
	{Path: "review", Section: command.SectionWork, Order: 4800, Text: "  review [threads] [--json]      unresolved review threads on this branch's PR"},
	{Path: "review reply", Section: command.SectionWork, Order: 4900, Text: "  review reply THREAD_ID BODY [--yes]   reply on a review thread"},
	{Path: "status", Section: command.SectionWork, Order: 5000, Text: "  status                         PR, checks, deployments and ticket in one view"},
	{Path: "standup", Section: command.SectionWork, Order: 5100, Text: "  standup [--since 24h] [--json] activity to draft a standup"},
	{Path: "events", Section: command.SectionWork, Order: 5200, Text: "  events [--since CURSOR] [--type PATTERN] [--topic T] [--limit N] [--all-repos] [--follow] [--json]\n                                 git, configuration and user-level events recorded by the console\n                                 daemon, for this repository and global; --all-repos reads every\n                                 repository the daemon serves; --follow starts the daemon and\n                                 prints new events until interrupted"},
	{Path: "eod post", Section: command.SectionWork, Order: 5300, Text: "  eod post KEY=NOTE [KEY=NOTE...] [--yes]   post end-of-day progress comments"},
	{Path: "notify", Section: command.SectionWork, Order: 5400, Text: "  notify SIGNAL --operation-id ID [--title T] [--body TEXT] [--links JSON] [--dry-run] [--automation] [--yes]"},
	{Path: "docs get", Section: command.SectionWork, Order: 5500, Text: "  docs get REF [--json]          read one document by qualified reference"},
	{Path: "docs search", Section: command.SectionWork, Order: 5600, Text: "  docs search QUERY [--space S]  search the selected knowledge base"},
	{Path: "docs publish", Section: command.SectionWork, Order: 5700, Text: "  docs publish REF --title T --body TEXT --operation-id ID\n               [--expected-revision REV] [--links JSON] [--type TYPE] [--description TEXT] [--tags JSON] [--yes]   publish a knowledge document"},
	{Path: "adr new", Section: command.SectionWork, Order: 5800, Text: "  adr new TITLE                  draft docs/adr/NNNN-title.md"},
	{Path: "release init", Section: command.SectionWork, Order: 5900, Text: "  release init [--strategy trunk|gitflow] [--force]   detect deployable units, write\n                                 .agentflow/releases.yml and the release-please files"},
	{Path: "release sync", Section: command.SectionWork, Order: 6000, Text: "  release sync                   regenerate release-please files from releases.yml"},
	{Path: "release plan", Section: command.SectionWork, Order: 6100, Text: "  release plan [--unit U] [--json]   next version of each unit on this branch"},
	{Path: "release version", Section: command.SectionWork, Order: 6200, Text: "  release version --unit U       print the version a build of this branch gets"},
	{Path: "release preview", Section: command.SectionWork, Order: 6300, Text: "  release preview [--since TAG]  unreleased commits by type"},
	{Path: "release get", Section: command.SectionWork, Order: 6400, Text: "  release get TAG                inspect a published release from the selected provider"},
	{Path: "release publish", Section: command.SectionWork, Order: 6500, Text: "  release publish TAG --revision SHA [--title T] [--notes TEXT] [--dry-run] [--yes]\n                                 explicitly publish a stable release at a commit"},
	{Path: "keys", Section: command.SectionWork, Order: 6600, Text: "  keys --range A..B              Jira keys in a commit range"},
	{Path: "lint-msg", Section: command.SectionWork, Order: 6700, Text: "  lint-msg FILE | --github-event validate a commit file or runner PR and its task references"},
	{Path: "--native-client", Section: command.SectionRuntime, Order: 6800, Text: "  --native-client codex COMMAND  reuse the configured Codex MCP for supported writes"},
	{Path: "--native-client", Section: command.SectionRuntime, Order: 6900, Text: "  --native-client claude-desktop COMMAND\n                                 use Jira for comment, start, transition or task create\n                                 use Asana for comment, completion via transition or task create\n                                 Jira comments and unlinked creates can fall back to the signed-in Teamwork Graph CLI"},
	{Path: "--native-client", Section: command.SectionRuntime, Order: 7000, Text: "  --native-client claude-desktop notify SIGNAL --operation-id ID\n                                 use the separately enrolled Slack MCP connection"},
	{Path: "mcp", Section: command.SectionRuntime, Order: 7100, Text: "  mcp [--client NAME] [--ui-only] [--auto-open]  MCP server on stdio"},
	{Path: "hook", Section: command.SectionRuntime, Order: 7200, Text: "  hook commit-msg FILE | pre-push [REMOTE URL] | post-commit | session-start | pre-bash | stop   [--client NAME]"},
	{Path: "update", Section: command.SectionRuntime, Order: 7300, Text: "  update [--latest-stable | --rollback] [--quiet]  update the binary or restore its previous version"},
	{Path: "version", Section: command.SectionRuntime, Order: 7400, Text: "  version                        build, channel and update status"},
}
