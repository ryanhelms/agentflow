# AI sessions

An AI session runs one prompt through an installed ACP harness (see
[AI harnesses](harnesses.md)) in your task worktree. Agentflow starts the harness,
streams what it does, and checks every file and command request the harness
sends to agentflow. When a request needs a person, the session waits for your
answer.

A session uses your own sign-in for the harness and can change files in the
worktree. Read [What agentflow cannot control](#what-agentflow-cannot-control)
before you start one.

## Commands

```text
agentflow ai run --harness ID [--detach] [--yes] [--json] PROMPT
agentflow ai status [SESSION] [--follow] [--since SEQ] [--json]
agentflow ai respond SESSION ASK (--allow | --reject) [--json]
agentflow ai cancel SESSION [--yes] [--json]
```

| Command | MCP tool | Does |
| --- | --- | --- |
| `ai run` | `agentflow_ai_run` (`harness`, `prompt`, `confirm_token`) | starts a session for one prompt; always asks you to confirm first |
| `ai status` | `agentflow_ai_status` (`session`, `since`) | lists sessions, or shows one session's transcript from a sequence number |
| `ai respond` | `agentflow_ai_respond` (`session`, `ask`, `decision`) | answers a request the session is waiting on |
| `ai cancel` | `agentflow_ai_cancel` (`session`, `confirm_token`) | stops a session |

`PROMPT` may be `-` to read it from standard input. In Claude Code, use
`/agentflow:ai`.

`ai status`, `ai respond` and `ai cancel` see only the sessions of the
repository you run them in (any of its worktrees). A session of another
repository answers like an unknown ID. Outside a repository they refuse
(`not_worktree`). The console's AI panel answers and cancels only the sessions
of the repository it shows.

### Where a session runs

A session always runs in the worktree of the process that starts it: the
directory where you run `ai run`, or the directory of the MCP server for
`agentflow_ai_run`. No flag, tool input, file or variable chooses another
directory or repository. To run a session somewhere else, start agentflow there.

`ai run` refuses in the primary checkout (`primary_checkout`). Start a task
worktree first:

```text
agentflow start <task> --worktree
```

### Foreground and detached

| Started by | Runs | Output |
| --- | --- | --- |
| You, `ai run` in a terminal | in that terminal | the agent's messages, tool calls and plan as they arrive; requests are asked as `Allow once? [y/N]`; Ctrl-C cancels, a second Ctrl-C stops it |
| `ai run --detach`, `agentflow_ai_run`, or `ai run` inside an AI agent | in the background | the session ID; follow it with `ai status SESSION --follow` or the console's AI panel |

`ai status --follow` reads the session store directly, so it needs no console.

### Before a session starts

`ai run` shows a summary and asks you to confirm, also with `autonomy` set to
`auto`. The summary lists:

- the harness: ID, name, version, command, path, arguments and identity;
- the worktree and branch;
- who starts it, and whether its requests are capped at `confirm`;
- the home folder kind and the name of every environment variable passed (never
  a value), with the harness's own sign-in variables called out;
- whether agentflow's read-only MCP server is passed, and its tools;
- the limits;
- the control files (such as `.claude/settings.json` or `AGENTS.md`) that differ
  from the default branch or are untracked, as a warning: the harness loads them
  when it starts;
- the prompt.

`ai run` refuses, with the code and the fix, when:

| Code | Why |
| --- | --- |
| `nested` | it runs inside an AI session: a session can never start a session |
| `agent_start_off` | an agent asked, and you turned off **Let agents start sessions** (`ai.sessions.agent_start`) |
| `repo_disabled` | the repository turned sessions off (`ai.sessions.disabled`) |
| `primary_checkout` | it runs in the primary checkout |
| `not_ready` | the harness is not ready; the fix says what to do |
| `plugin_launch` | an agent asked for a harness a plugin chose how to start |
| `unsafe_environment`, `home_unsafe` | your home, a configuration folder or the agentflow home is not safe to use |
| `limit_running`, `limit_worktree`, `limit_rate` | too many sessions are running, one already runs in this worktree, or more than 10 started in the last hour |

## Requests and answers

When the harness asks agentflow to read or write a file, run a command, or allow
one of its own tool calls, agentflow decides in this order: the never-list, the
worktree boundary, your permission rules, then your autonomy policy. When nothing
allows the request, it is denied.

- Reads inside the worktree follow your policy.
- Writes and commands follow your policy, but a session an agent started asks you
  for every request except reads inside the worktree.
- Writes to control files (`.agentflow/`, `.claude/`, `.github/workflows/`,
  `AGENTS.md` and others) and reads of secret files (`.env`, `*.pem`, `id_rsa*`
  and others) always ask you. Agentflow checks both the path the harness named
  and the path it leads to through links, so `notes.txt` linked to `.env`, or
  `docs/ci.yml` under a `docs` link to `.github/workflows`, asks as the real
  file does. A rule allows a path only when every form of it matches.
- On macOS and Windows, a path with a non-ASCII character in any name below the
  worktree always asks you (`unicode_name`): those file systems open, for
  example, `id_rſa` as `id_rsa`, so no list can tell what it names.
- Right before a read or write, agentflow checks again that the path still
  leads where it did. If it changed, the request is denied (`changed`).
- Anything outside the worktree, in `.git` or in a nested repository is denied.
- A command that names the agentflow home, the console registry or the session
  store is denied, whatever your rules say. Agentflow compares the names and the
  files they open, so a link or a folded spelling counts too. It is still a text
  check: a path built while the command runs gets past it.
- A command that sets any environment variable of its own always asks you, whatever
  your rules say (`env`): a value such as `GIT_EXTERNAL_DIFF`, `PAGER` or
  `GOFLAGS` can make an allowed program run other code. The request shows each
  name and value and the program it starts.
- The session `PATH`, for the harness and its commands, keeps only entries
  outside every worktree of the session's repository and of every repository the
  console serves, so a repository cannot put a program in front of `go` or `npm`.
  When agentflow cannot read which repositories the console serves, it does not
  start the session (`unsafe_environment`).

A request waits up to 5 minutes (`ai.sessions.ask_timeout`) and is then denied.
`ai status` lists the waiting requests, each with its `ai respond` command.

A command request shows the command line, the program it starts (its full
path), the folder it runs in and each environment variable with its value.
Characters that hide or change what you read (zero-width, direction marks,
line separators, unusual spaces) are shown as `<U+XXXX>`. After you allow it,
agentflow resolves the folder and the program again; if either changed, the
command is denied (`changed`). The values are kept only in the request's own
file, as a write's change is (below).

### What a write request shows

A write request shows the change: the path (and where it leads, when a link
moves it), the file's current text and the new text. In a terminal, every line
of the file's text starts with `old| ` or `new| `, so no line of the file can
pass for agentflow's own markers. **Allow once** is offered only when you can see the whole change:

- The request holds at most 256 KiB of each side. A larger or binary change
  shows only **Reject** (`preview_too_large`).
- The console's AI panel offers **Allow once** only up to 70,000 characters or
  4,000 lines a side. Allow a longer change from your terminal with
  `ai respond --allow`, which prints it in full.
- If the file changes after you saw the request, the write is denied
  (`changed`).

The change is kept only in the request's own file (readable only by you). It is
not redacted, so you judge the real content, and it is deleted when the request
is answered, expires or the session ends. It never reaches the transcript, an
event, a log or the audit.

Every allow names the request as you saw it (its fingerprint). An allow for a
request that changed since is refused.

### Who may answer

| Who | Allow once | Reject | Cancel |
| --- | --- | --- | --- |
| You, in the console's AI panel | yes | yes | yes, at once |
| You, at the terminal running `ai run` | yes | yes | yes (Ctrl-C) |
| You, `ai respond --allow` in your own terminal | yes | yes | `ai cancel` |
| An AI agent: `agentflow_ai_respond`, or `ai respond` inside an agent | **no** | yes | yes |
| The session itself | no | no | no |

**Cancel session** in the console stops the session at once: your click is the
confirmation, even when you set `autonomy.actions.ai.cancel` to `suggest`,
because stopping is a safety action. `ai cancel` and `agentflow_ai_cancel`
follow your autonomy policy, so they may ask first. No autonomy setting decides
who may answer: the rules above do (there is no `ai.respond` action).

`ai respond --allow` shows the whole request and asks y/N. It refuses inside an
AI agent and without a terminal, and it ignores `--yes`. The pre-bash hook blocks
agents from running it. `agentflow_ai_respond` accepts only `reject`. Agentflow
only ever chooses an "allow once" option for a harness, never "allow always".

## Agentflow's MCP server in a session

With **Give the harness agentflow's read-only tools** on
(`ai.sessions.agentflow_mcp`, the default), the harness gets one MCP server:
`agentflow mcp --session ID`. It has only these read-only tools:
`agentflow_context`, `agentflow_scope_check`, `agentflow_ac_checklist`,
`agentflow_commit_validate`, `agentflow_docs_search`, `agentflow_docs_get` and
`agentflow_policy_explain`. It has no `agentflow_ai_*` tool, and its client
(`ai-session:<id>`) can read only: every write and every approval is refused
(`session_read_only`). No environment value is passed to it.

Inside a session, every process has `AGENTFLOW_ACP_SESSION` set. Agentflow then
treats the process as the session's own client: it can read, and it cannot
write, start a session or answer a request.

This variable is one layer, not a boundary: the harness's own shell, which
agentflow cannot see, can unset it. A harness that then runs `agentflow ai run`
counts as an ordinary agent. What bounds it is the limits below, which
agentflow counts in the session store under its lock whatever the caller's
environment says, and the `confirm` ceiling of an agent's session.

## Limits

| Limit | Default | Setting |
| --- | --- | --- |
| Sessions at once | 2 (1 to 4) | `ai.sessions.max_running` |
| Sessions per worktree | 1 | |
| Starts | 10 an hour | |
| Session time | 30 minutes (1 minute to 4 hours) | `ai.sessions.max_duration` |
| Idle time | 10 minutes (1 minute to 1 hour) | `ai.sessions.idle_timeout` |
| Time to answer a request | 5 minutes (30 seconds to 30 minutes) | `ai.sessions.ask_timeout` |
| Requests waiting | 4 per session | |
| Requests | 30 a minute per session | |
| Terminals | 4 at once, 64 per session | |
| What the harness sends as messages and updates | 64 MiB; past it the session ends `limit_output` | |
| Tool calls followed | 4,096 per session; past it the session ends `limit_output` | |

### Your own settings hold whatever `AGENTFLOW_HOME` says

A caller can set `AGENTFLOW_HOME` to a fresh folder. When it is not your
account's own agentflow home (`~/.agentflow`, found from your account, not from
`HOME`), your settings there still hold, and can only make a session stricter:

- **Let agents start sessions** off, the isolated home, `unasked: cancel` and
  the read-only MCP server off win.
- The session limits and the time limits take the lower value.
- A harness you turned off stays off.
- Every autonomy level takes the lower one.
- Your `deny` and `ask` rules apply first; your `allow` rules there never apply
  on their own.
- The running, per-worktree and hourly limits count the sessions of both homes.

Such a session is still not shown in your console, which reads your own home.

The harness may report a cost. Agentflow shows it as reported by the harness and
does not enforce it; the time limits bound what a session can spend.

## What agentflow cannot control

- **The harness's own tools.** A harness can run its own shell or edit files
  without asking agentflow. Agentflow cannot see or stop that. It reports tool
  calls the harness ran without asking, cancels the session when one names a place
  outside the worktree, and lists the changed files when the session ends. Every
  change is in your worktree, as a diff to review.
- **Your home folder.** The harness runs with your real home, so it can use your
  git credentials, SSH keys and cloud profiles. Agentflow passes only a listed
  environment: no tokens, keys, `SSH_AUTH_SOCK`, proxy variables or `AGENTFLOW_*`
  variables, except the harness's own verified sign-in variables.
- **Processes of your own user.** A program that deliberately reads agentflow's
  files can imitate you. The checks stop agent use and prompt injection, not a
  hostile program you installed.
- **A program that fakes a terminal.** "Only a person may allow" means a person
  at a terminal, with no variable that marks an agent. That stops ordinary agent
  behaviour. It does not stop a program running as you that deliberately opens a
  pseudo-terminal and removes those variables: it can allow its own session's
  requests and start sessions as you, and the record says a person did it.
- **Processes that leave their group.** When a session ends, agentflow kills each
  terminal's process group. A process that moved to a group of its own (for
  example with `setsid`, or a daemon) keeps running.
- **A host that crashed.** The next `ai status` ends the session `host_lost`,
  kills the harness's and the terminals' process groups when it can show they
  are still the same processes (Linux and macOS), and deletes the waiting
  requests, the session's temporary folder and its answer key.
- **An agent that skips the confirmation.** An agent's `ai run` asks the agent to
  show you the summary. An agent that applies the confirmation itself can start a
  session without you. That session still asks you for every write and command,
  runs only in that agent's worktree, and cannot start another session. Turn off
  **Let agents start sessions** to stop agents from starting sessions at all.

## Where sessions are kept

Sessions live in the agentflow home, under `ai/sessions/<id>/`, readable only by
you. A transcript can hold secrets despite best-effort redaction. Agentflow keeps
ended sessions for 7 days (`ai.sessions.keep_days`), at most 50 sessions and
200 MiB. Turn off `ai.sessions.keep_transcripts` to delete the transcript when a
session ends. The waiting requests, the temporary folder and the answer key are
always deleted when a session ends. Nothing is written to the repository's event store except session
IDs, codes and counts.
