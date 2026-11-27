# AI harnesses

A harness is an AI coding agent that speaks the
[Agent Client Protocol](https://agentclientprotocol.com) (ACP), such as Gemini
CLI, or an ACP adapter for one, such as the adapter for Claude Code. Agentflow
finds the harnesses installed on your machine and says what to do about each one.

Agentflow never installs a harness, never downloads one and never starts a session.

## List them, or check why one is missing

```text
agentflow ai harnesses [--probe] [--all] [--id ID]... [--json]
agentflow ai doctor [--probe] [--json]
```

| Command | MCP tool | Shows |
| --- | --- | --- |
| `ai harnesses` | `agentflow_ai_harnesses` (`probe`, `all`, `ids`) | each harness's ID, state, version and command, then its fix |
| `ai doctor` | `agentflow_ai_doctor` (`probe`) | checks marked `ok`, `warn` or `fail`, each with its fix, and the same report |

Both only read. Run them inside a git repository, as other agentflow commands.
In Claude Code, use `/agentflow:ai`.

## States

| State | Meaning |
| --- | --- |
| `native` | Found. The product's own CLI speaks ACP. |
| `adapter_installed` | Found. A separate ACP adapter for a CLI. |
| `adapter_available` | The CLI is installed, but its ACP adapter is not. |
| `needs_auth` | Found, and it needs sign-in. |
| `incompatible` | Found, but it does not speak ACP version 1, or it is too old. |
| `not_installed` | Nothing found. Listed only with `--all`. |

Finding a harness does not prove it works. The probe status says that:

| Probe status | Meaning |
| --- | --- |
| `none` | Found on your PATH; nothing has run for this version of the file. |
| `ok` | It answered with ACP version 1. |
| `cached` | An earlier result for the same file; the code says what it was. |
| `failed` | It timed out, exited, could not start or did not answer like an ACP agent. |
| `refused` | Agentflow did not run it; the code says why. |

Each harness that needs something carries a fix: a sentence, and often a command or
a link. **Agentflow shows the command and never runs it.**

## What a probe runs

A probe starts only a program that you put on your PATH or named in your own
settings:

1. Its version command (3 seconds).
2. One ACP `initialize` request (5 seconds by default). No other ACP request is sent.
3. Its own sign-in status command, only where agentflow has a verified one.

The first two run with a scrubbed environment: no tokens, no keys, no session IDs,
and an empty `HOME` under the agentflow home.

### The sign-in check uses your real home folder

| Harness | Runs |
| --- | --- |
| `claude-acp` | `claude auth status` |
| `codex-acp` | `codex login status` |

This is the only command agentflow runs with your real `HOME`. It runs in an empty
directory, without credential variables. Agentflow keeps only the answer (signed
in, signed out or unknown) and the time, never the command's output. You can add a
status command for another harness in your own overrides; a repository, a plugin
and the registry cannot.

Other harnesses show sign-in `unknown` with their own ways to sign in. `unknown`
does not mean signed out. If a variable such as `ANTHROPIC_API_KEY` is set,
agentflow reports `unknown`, not signed out, because you may sign in with it.

## Who may ask for a probe

| Who asks | When it probes |
| --- | --- |
| You, in a terminal outside an AI agent | always |
| You, with **Probe now** in the console | always |
| An AI agent: any MCP call, or the command line inside an agent session | only while automatic probes are on |
| A script or CI job with no terminal on standard input | the same as an agent |
| The console daemon | only while automatic probes are on, when a harness changes |

A refused request returns `probe_refused: needs_person` with the cached results.

A harness that a plugin defines is different: agentflow never starts it on its own
or for an agent, whatever the settings say. Only your own probe runs it (probe
`refused`, code `plugin_launch`, until you do). `agentflow plugin add` shows the
exact command, arguments and version command it would run, and a plugin's arguments
may not hold an absolute path, `..`, `~`, a `:` (URLs, `host:path`, drive letters)
or a value starting with `@`. If your own override sets the command, the arguments
and the version arguments (or `no_version`) again, the harness is yours and follows
the rules above.

Two settings control this. Both are **on by default**. Change them on the Settings
page (group **AI harnesses**) or from the terminal:

```text
agentflow config set ai.harnesses.auto_probe=false
agentflow config set ai.harnesses.sign_in_check=false
```

| Setting | When off |
| --- | --- |
| `ai.harnesses.auto_probe` | Agentflow only looks at files until you ask for a probe. |
| `ai.harnesses.sign_in_check` | Agentflow never runs a sign-in status command, whoever asks. |

## Where agentflow looks

Agentflow searches the PATH of the process that detects: your shell's for the
command line, the daemon's for the console. It skips what another party could have
put in reach, and `ai doctor` lists each one:

| Skip reason | What was skipped |
| --- | --- |
| `relative` | an empty, `.` or relative PATH entry |
| `inside_repository` | a directory inside a worktree of a repository agentflow is working in |
| `node_modules` | a project's `node_modules` |
| `unsafe_permissions` | a directory or file that others can change, or that another user owns |
| `shim` | a Windows `.cmd` or `.bat` file |

Package runners, interpreters, shells and downloaders (`npx`, `uvx`, `node`,
`python`, `bash`, `curl` and others) are never run.

## Where definitions come from

Later layers win, field by field:

1. A pinned copy of the official ACP registry, built into agentflow. It is never
   fetched while agentflow runs.
2. Agentflow's own corrections to it.
3. Enabled declarative plugins, which can only add new harnesses.
4. Your personal settings, which can change any harness or add one.

A repository cannot add or change a harness. An `ai` section in a repository's
`.agentflow/config.yml` is ignored.

When two built-in entries find the same program (the legacy `kimi` entry and
`kimi-code` both install `kimi`), agentflow lists it once, as the entry its own
corrections name. Entries you or a plugin defined are always listed.

An override's `config_env` names directory variables passed to the sign-in
check, such as `CODEX_HOME`. Agentflow refuses a name that looks like a
credential (`TOKEN`, `SECRET`, `PASSWORD`, a `_KEY` ending and so on), one that
changes how programs are found or loaded (`PATH`, `LD_*`, `NODE_OPTIONS` and
so on), and any `AGENTFLOW_*` name.

```yaml
# your agentflow config.yml
ai:
  harnesses:
    disabled: [kimi]
    poll_interval: 30s
    probe_timeout: 5s
    overrides:
      gemini:
        args: [--acp]
      my-agent:
        name: My agent
        command: my-agent
        args: [acp]
```

## `ai doctor` checks

`ai doctor` exits 1 when a check fails, so a script can use it. The MCP tool returns
the report either way.

| Check | `warn` or `fail` when |
| --- | --- |
| `registry` | warn: the pinned registry copy is more than 120 days old |
| `home` | fail: the agentflow home is unsafe, so your overrides and plugin harnesses are ignored |
| `path` | warn: PATH entries or files were skipped |
| `definitions` | fail: a definition of yours or of a plugin was refused |
| `cache` | warn: the probe cache cannot be read, or others can reach it |
| `daemon` | warn: no console daemon runs, so nothing updates live |
| `auto_probe` | warn: automatic probes are off and some harness is not probed |
| `sign_in` | warn: a harness needs sign-in, or sign-in checks are off |
| `harnesses` | warn: no harness is installed and confirmed to speak ACP |

## What is untrusted

Registry text, a plugin's definitions and everything a harness answers are data.
Agentflow shortens them, removes control characters and shows them as text. Fix
text comes only from agentflow's own templates. A sign-in command that would be
unsafe to paste into a shell is not shown. Events carry no paths, arguments or
harness text.

The full contract is the "Phase 6" section of the
[event-driven platform decision](knowledge/engineering/decisions/event-driven-platform.md#phase-6).
