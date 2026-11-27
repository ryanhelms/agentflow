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
  and others) always ask you.
- Anything outside the worktree, in `.git` or in a nested repository is denied.
- A command that names the agentflow home, the console registry or the session
  store is denied, whatever your rules say.

A request waits up to 5 minutes (`ai.sessions.ask_timeout`) and is then denied.
`ai status` lists the waiting requests, each with its `ai respond` command.

### Who may answer

| Who | Allow once | Reject | Cancel |
| --- | --- | --- | --- |
| You, in the console's AI panel | yes | yes | yes |
| You, at the terminal running `ai run` | yes | yes | yes (Ctrl-C) |
| You, `ai respond --allow` in your own terminal | yes | yes | `ai cancel` |
| An AI agent: `agentflow_ai_respond`, or `ai respond` inside an agent | **no** | yes | yes |
| The session itself | no | no | no |

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
session ends. Nothing is written to the repository's event store except session
IDs, codes and counts.
