# AI drafts

A draft is text a harness writes for one existing agentflow operation: a commit
message, a PR title and body, a ticket, acceptance-criteria checks, or replies to
review threads. A draft is only a candidate. Agentflow never applies it: you, or the
agent that asked, run the operation with its fields, through that operation's normal
checks and confirmation.

Drafts are off until you route a feature to a harness. Each feature is off on its
own, and a feature that is off sends nothing anywhere.

## Commands

| Task | CLI | MCP tool |
| --- | --- | --- |
| See what a draft would send, without sending it | `agentflow ai draft FEATURE --dry-run [--show-prompt]` | `agentflow_ai_draft` with `dry_run: true` |
| Make a draft (always confirmed first) | `agentflow ai draft FEATURE [inputs] [--yes] [--json]` | `agentflow_ai_draft` |
| See a finished draft | `agentflow ai status SESSION` | `agentflow_ai_status` with `session` |

`FEATURE` is one of `commit`, `pr`, `ticket`, `ac` and `reply`. The inputs belong to
one feature each: `--path P` (commit), `--text TEXT` or `--text -`, `--template`,
`--template-revision`, `--project`, `--type` and `--link-to` (ticket), `--key KEY`
(ac) and `--thread ID` (reply). `--hint TEXT` works for every feature. An input that
belongs to another feature is refused (`usage`).

```text
agentflow ai draft commit --dry-run           # the manifest: what would be sent
agentflow ai draft commit --dry-run --show-prompt   # also the exact prompt, on your terminal only
agentflow ai draft commit                     # asks you to confirm, then waits for the draft
agentflow ai draft pr --json                  # the draft as JSON
```

`--ai` on `commit`, `pr`, `ac`, `task create` and `review reply` makes a draft and
then runs that command's normal flow after you accept it. It is for a person at a
terminal only. The feature drafts arrive in later releases; until a feature is in the
build, `ai draft FEATURE` refuses `unknown_feature`.

## Turn a feature on

Set the harness for each feature you want, in Settings or with `agentflow config`:

| Key | Default | What it does |
| --- | --- | --- |
| `personal:ai.features.commit.harness` | off | Harness for commit message drafts |
| `personal:ai.features.pr.harness` | off | Harness for PR drafts |
| `personal:ai.features.ticket.harness` | off | Harness for ticket drafts |
| `personal:ai.features.ac.harness` | off | Harness for acceptance-criteria checks |
| `personal:ai.features.reply.harness` | off | Harness for review reply drafts |
| `personal:ai.drafts.max_duration` | 3m (30s to 15m) | How long a draft may run |

These keys are personal: a repository cannot set them. Setting or changing a feature's
harness is a loosening change, because it starts sending repository and task text to a
model provider, so an agent's change to it needs your confirmation. The harness must be
ready (`agentflow ai harnesses`). When `AGENTFLOW_HOME` points somewhere other than
your account's home, a feature runs only when both homes name the same harness
(`feature_off_account`), and the lower time limit holds.

The autonomy action is `ai.draft` (class `remote`). You may set it to `auto` for
yourself. An agent's draft always needs your confirmation, whatever the setting.

## What a draft sends

Before anything is sent, agentflow shows the manifest and asks you to confirm. The
manifest lists:

- the feature, the harness (ID, name, version, path), the worktree and branch, and who
  asked;
- each part of the context: its kind, label, source, size, what was cut or redacted,
  and each file left out with the reason;
- the targets (for example the criteria to check), and your hint and a ticket's text
  in full, exactly as they are sent;
- the prompt's total size and SHA-256, the time limit, and what is never sent.

The manifest never holds the diff, a task description or a review body. Only
`--show-prompt` shows the exact prompt, on your own terminal; no tool returns it.

Agentflow filters the context before it builds the prompt:

- it leaves out generated, binary, lock and secret-looking files (by every name a
  diff gives them: quoted, renamed or deleted), and paths outside the worktree;
- it leaves out a file's changes when its diff header cannot be read, for example
  when git writes paths without the `a/` and `b/` prefixes, and the changes of a
  combined diff (`diff --cc`, written for a conflict or a merge);
- when the draft's diffs delete or rename away a secret-looking file, it leaves out
  every file those diffs add, because git can show a moved file as a delete and an
  add;
- it shows hidden and control characters as visible codes;
- it redacts token shapes it knows;
- it cuts each part to its size limit, and refuses a prompt larger than the limit
  (`too_large`);
- it refuses the draft (`secret_in_context`) when a secret is still found after
  redaction, naming the part but not the match.

The prompt marks every piece of repository, task and review text as data to describe,
not instructions to follow.

The confirmation binds to the prompt. If the context changes between the
confirmation and the start (for example a new staged change), the draft is refused
`operation_changed` and nothing is sent.

Never sent: environment values, credentials and tokens agentflow holds, the agentflow
home and the session store, `.git` contents other than what the diff shows, remote
URLs, whole files, files outside the worktree, other worktrees and repositories,
approval tokens and IDs, your configuration, and author e-mail addresses.

## How a draft runs

A draft is an AI session with a draft profile:

- The harness starts in an empty private folder inside the session store, not in your
  worktree, and gets no agentflow MCP server.
- Every file, terminal and permission request it sends agentflow is denied
  (`draft_profile`); nothing asks you.
- A tool call of any kind other than thinking ends the draft (`draft_tool`).
- The answer is kept in memory up to 256 KiB; a longer answer ends the draft
  (`answer_too_large`).
- The prompt file is deleted when the turn ends.
- A draft may run in the primary checkout, because it changes nothing there.
- If your worktree changed while the draft ran, the draft carries the warning
  `worktree_changed`. A harness's own tools are outside agentflow's checks, as for
  every session.

Limits: one running draft per worktree (`limit_draft_worktree`) and 30 drafts an hour
(`limit_draft_rate`). Drafts count towards the running-session limit. Agentflow keeps
finished drafts for 24 hours, at most 20, apart from sessions.

An AI session cannot start a draft (`nested`).

## What comes back

The draft's state is `ready`, `invalid` (the answer broke a rule; the answer is kept so
you can read it), `failed` (with a code) or `cancelled`. Each candidate names its
target, its fields, its issues and warnings, and the tool input or command that uses it.
A candidate never carries a confirmation token.

Agentflow reads exactly one JSON object from the answer and ignores any key it does not
know (`unknown_field` warning). Fields that agentflow fixes, such as a commit's paths,
always come from your request, never from the answer. Each text field is cleaned and
cut to its limit. A secret in a drafted field empties that candidate
(`secret_in_draft`), and an answer that looks like it holds a secret is never shown.
The session transcript records only how much text the harness sent; its words are
kept in the draft result alone. A mention or link in text that would be posted gives a warning.

The text output prefixes every drafted line with `| `, so drafted text cannot look like
agentflow's own lines. Its "use it" line is quoted so that bash, zsh and fish read every
word as text. On Windows no paste-ready line is printed: use the candidate's fields as
the command's own flags, or read its `cli` words with `--json`. A draft that is not `ready` exits with status 1.

## In the console

The console's AI panel lists drafts apart from sessions and shows which features are
available. **Draft** shows the manifest; **Send** confirms it. The console keeps the
confirmation token itself and gives the page only a short-lived confirmation ID, used
once. The draft arrives over the event stream (`ai.draft.finished`). Events carry IDs,
codes and counts only: no drafted text.

## Not verified

Whether the Claude Code and Codex ACP adapters start in a folder that is not a git
repository is not settled by their published documentation. A harness that refuses
ends the draft with `launch` or `protocol`; there is no fallback to the worktree.
