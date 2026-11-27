# Plugins

A plugin adds behaviour to agentflow without changing agentflow. It can react to
events, such as a failing CI run, and it can deny operations you choose to block.
This page is for two readers: you, if you add plugins to your own machine, and
you, if you write them.

Plugins belong to you, not to a repository. You install them into your agentflow
home and turn them on in your personal settings. A repository can turn a plugin
off for itself. It can never install one, turn one on, or make one stronger. A
cloned repository runs no plugin code.

Plugins run inside the repository console (`agentflow ui`). With no console
running, no plugin handler runs. Permission rules are the one exception: they
apply in every agentflow process that runs a command.

## Read this first: a plugin runs as you

A plugin is code from its author.

| Kind | What runs | What it can reach |
| --- | --- | --- |
| Declarative (YAML) | Nothing of the plugin's own. agentflow reads the rules. | Only what its permissions list. |
| Process (a program) | The program, as your user account. | Everything your user can read, and the network. |

agentflow starts a process plugin with a clean environment: no tokens, no cloud
keys, no session IDs, and a `HOME` inside the plugin's own data directory. **That
is not a sandbox.** The program can still read your files and open network
connections. Add a program plugin only if you trust its author and have read its
code.

What agentflow does guarantee, for every plugin:

- It refuses to install or remove one from an agent session or a
  non-interactive shell. See [Add and remove plugins
  yourself](#add-and-remove-plugins-yourself) for what that does not stop.
- It never downloads anything. `plugin add` reads a local path.
- It shows every permission before it installs, and always asks, whatever your
  approval settings say.
- It checks the installed files against their hash before every start. A changed
  copy never runs.
- A plugin never sees a confirmation token. Its writes wait for you.
- A plugin starts turned off.

## Add, remove and approve plugins yourself

Run `plugin add`, `plugin remove` and `plugin approve` in your own terminal.
Installing a program plugin lets it run as you, removing a plugin can end a rule
that denies operations, and approving lets a plugin's write run. Agentflow
therefore does three things:

- The three commands refuse an agent session: one that sets `CLAUDECODE`,
  `CLAUDE_CODE_SESSION_ID`, `AGENTFLOW_CODEX_HOST_ACTIVE` or a `CODEX_` variable
  other than `CODEX_HOME`.
- They refuse a non-interactive shell, with no terminal on standard
  input, such as a script, a pipe or a CI job.
- The Claude Code pre-bash hook blocks an agent from running them. See
  [What the hook catches](#what-the-hook-catches). This rule also holds in
  `--convention-only` mode, which switches off the rest of the never-list, because
  plugins belong to you and not to a repository. Codex has no such hook.

The refusals say `run this in your own terminal`. There is no override flag or
variable. `--yes` is accepted and ignored: the question is always asked.

### What the hook catches

The hook reads the command line before it runs. It blocks `plugin add`, `remove`
and `approve` when they name a plugin source or an ID, so `plugin add --help`
runs. It finds `agentflow`:

- by name, by the path of a repository launcher or a cached build, through the
  plugin's `bin/launch` launcher, and as `go run ./cmd/agentflow`;
- in the command given to `sh -c`, `bash -c`, `bash -lc`, `zsh -c`, `eval`,
  `script`, `expect -c`, `cmd /c` or `powershell -Command`;
- behind a simple prefix, such as `env` (with assignments, `-i` or `-u NAME`),
  `sudo`, `nohup`, `nice`, `time`, `setsid`, `xargs` or `timeout 30`.

It does not find every form. For example, it misses:

- a prefix with an option that takes a separate value, such as `sudo -u me`,
  `timeout -k 5 30` or `nice -n 5`;
- a shell with such an option before `-c`, such as `bash -o pipefail -c`;
- a command that another program starts, such as `watch`, `find -exec` or `ssh`;
- a command that is built while it runs: a program named through a variable, a
  line or here-document fed to a shell, or one written to a file and run from
  there.

A command that only mentions the words, such as a search or a commit message,
runs as usual.

These checks stop mistakes and ordinary automation. They are not a security
boundary: a process that runs as you can still get around them on purpose, for
example by clearing the variables and creating its own terminal, or by writing the
files in your agentflow home directly.

## Add a plugin

```text
agentflow plugin add PATH [--json]
```

Run the plugin commands from inside a Git repository: agentflow opens it to read
your approval settings. `PATH` is a local directory that holds
`agentflow-plugin.yml`, or, for a declarative plugin, a single YAML file. A web address, a `git@` address or a
`file:` address is refused. Build a program plugin before you add it: agentflow
never runs `go build`, `npm`, `pip` or `git`.

The command reads the files once and shows:

- the plugin's ID, name, version, tier and content hash;
- the program to run, for a process plugin;
- every extension it adds, and every permission it asks for: the events it may
  receive, the events it may publish and the commands it may run;
- the version it replaces, for an upgrade.

Type `y` to install. Neither `--yes` nor your approval settings skip this
question: `plugin.install` asks even when your autonomy is `auto`.

The confirmation is tied to the files you were shown. If a file changes between
the preview and your answer, the install fails with `operation_changed`: run
the command again.

Some paths are refused and nothing is written:

- a symbolic link, a device, a socket or a pipe, in the plugin or as `PATH`;
- more than 256 files, or more than 16 MiB;
- a `.git` directory;
- a manifest that does not validate, or one that uses an extension point agentflow
  has reserved for later;
- a process plugin whose program is missing or not executable.

### What `plugin add` writes

Everything goes under your agentflow home (`~/.agentflow` by default):

```text
~/.agentflow/plugins/
  installed.json          the list of installed plugins
  <id>/
    <16 characters of the hash>/   the installed copy, read-only
    data/                          the plugin's own writable place
```

The copy is checked against its hash after it is written. Adding the same files
again changes nothing. Adding a different version of an installed plugin is an
upgrade: it asks again, keeps the `data/` directory and removes the old copy
after the new one is in place. Adding the same version when its installed copy no
longer matches its hash repairs the copy.

## Turn a plugin on or off

A new plugin is off. To turn it on, open the Settings page and tick
`plugins.<id>.enabled`, or run:

```text
agentflow config set plugins.<id>.enabled=true
```

Turning a plugin on loosens what runs on your machine, so the Save review lists
it and asks for your tick, like any other loosening change. To turn it off, set
the same field to `false`.

Turning off a plugin that has a [permission rule](#permission-rules) loosens too,
because the operations it denied are allowed again. The Save review asks for your
tick, and the save answers to `console.autonomy`: with `console.autonomy: suggest`,
agentflow only suggests the change and does not save it. If the plugin registry
cannot be read, turning off any plugin counts, because it may have a rule.

A repository can turn plugins off for itself in `.agentflow/config.yml`:

```yaml
plugins:
  disabled: [ci-notify]
```

This turns off the plugin's handlers in that repository. It never removes a
permission rule that you installed. Saving it from Agentflow for a plugin with a
permission rule installed on your machine still counts as loosening, and asks for
your tick.

A plugin's own settings appear on the Settings page in a group named after the
plugin. They are saved in your personal settings, under
`plugins.<id>.settings`.

## See what is installed

```text
agentflow plugin list [--json]
```

One line per plugin: ID, version, tier, whether you turned it on, whether this
repository turns it off, and the last state the console reported. A line ends
with `FILES CHANGED` when the installed copy no longer matches its hash. Run
`plugin add` again to repair it.

`--json` adds the hash, the extensions, the permissions and each plugin's newest
undelivered events (`dead_letters`, at most 20, without their payloads). AI clients can read
the same list with the `agentflow_plugin_list` tool. No tool adds, removes or
turns on a plugin: that is your decision, at your terminal.

| State | Meaning |
| --- | --- |
| `installed` | Installed and turned off. |
| `starting` | Turned on and starting. |
| `running` | Working. |
| `degraded` | Running, but a program plugin missed pings or was slow to answer. |
| `backoff` | A program plugin stopped. agentflow starts it again later. |
| `failed` | Will not start until its settings, its enabled flag or its files change. |
| `stopped` | Turned off, or the console stopped. |

The code next to a state says why: `hash_mismatch` and `manifest_invalid` mean the
installed files changed, `crash_loop` means five restarts in ten minutes, and
`launch`, `protocol`, `timeout`, `exited` and `crashed` describe a program that
could not start or stopped.

## Remove a plugin

```text
agentflow plugin remove ID [--json]
```

[Run it yourself](#add-and-remove-plugins-yourself): it always asks.

After one confirmation, this deletes the plugin's entry, its installed copy, its
data directory and the `plugins.<id>` block in your personal settings. The console
stops the plugin when it sees the change. The rest of your settings file, with its
comments, stays as it was. Removal cannot be undone: add the plugin again to get
it back, without its data.

## Try the two samples

Both are in `examples/plugins/` in the agentflow repository.

**`ci-notify`** is a YAML plugin. When the checks of a branch you work on start
failing, it runs the `notify` command.

1. Add it: `agentflow plugin add examples/plugins/ci-notify`. Read the summary,
   then answer `y`.
2. Turn it on: `agentflow config set plugins.ci-notify.enabled=true`.
3. Route the `ci.failed` signal in your notification settings. With no route,
   `notify` reports that it is disabled and sends nothing.

When a failure arrives and a route exists, the plugin's write waits for you. A
plugin's writes never run on their own, even if your approval level is `auto`.

4. Approve it: copy `agentflow plugin approve <operation ID>` from the Approvals
   panel or the plugin's card, run it in your terminal and answer `y`. The console
   then sends the notification once. See [Approve a plugin's
   write](#approve-a-plugins-write).

**`timeline-hints`** is a program. When an operation fails, it adds a one-line hint
to that operation on the timeline, such as "The candidate changed after review.
Prepare it again."

1. Build it: `cd examples/plugins/timeline-hints && go build -o timeline-hints .`
2. Read `main.go`. It is about 150 lines and uses only the standard library.
3. Add it: `agentflow plugin add examples/plugins/timeline-hints`.
4. Turn it on: `agentflow config set plugins.timeline-hints.enabled=true`.
5. Make an operation fail, for example run `agentflow commit` with a confirmation
   token from an earlier preview after you changed the files. The failed
   operation shows its hint on the timeline.

The Plugins page of the console shows both plugins and their state.

## What a plugin can and cannot do

A plugin has no permission unless its manifest lists it, and you see the list
before you install.

| A plugin may | Limits |
| --- | --- |
| Receive events | Only `git.*`, `config.*`, `gh.*`, `op.*`, `commit.*` and `hook.*` events, and `plugin.annotation.added`. Never approvals, client or UI events, or events that belong to no repository. |
| Publish an event | Only `plugin.annotation.added`: a note on an operation. At most 60 a minute. |
| Run a command | Only `notify` and `comment` (writes), and `ship.status`, `context`, `ac.checklist` and `review.threads` (reads). At most 10 a minute. |
| Deny an operation | Only a declarative plugin, with a permission rule. A rule can deny and never allow. |

A plugin cannot run `commit`, `push`, `pr`, `promote`, `delegate`, anything under
`approval`, `policy`, `config`, `plugin` or `setup`, or any command it did not list.
It cannot confirm its own write: under `auto`, a plugin's write still waits for you.

### Approve a plugin's write

The Approvals panel and the plugin's card on the Plugins page show the command to
run for a waiting write. Run it in the repository the plugin acted on:

```text
agentflow plugin approve op_0123456789abcdef0123456789abcdef
```

It shows the plugin, the command, what the command does (for `notify`, the signal)
and when the request expires, then asks `Proceed? [y/N]`. It never shows the
command's full input, and the approval record stores none. After `y`:

- The console runs the write once, by itself, as the plugin. The plugin does not
  have to ask again, and asking again does not run it twice.
- Its `op.*` events and result appear on the timeline with the client
  `plugin:<id>`.

`--decline` cancels the write instead, and it never runs. A write that is not
approved before it expires is dropped. Plugin approve only answers requests that
a plugin made: it refuses any other approval.

The console keeps a waiting write in memory. If the console restarts before you
approve, the write does not run; the plugin asks again the next time its event
arrives. A write of a plugin you turned off in the meantime does not run either.

Every command a plugin runs appears on the timeline like your own, with the client
`plugin:<id>`.

## Write a declarative plugin

A declarative plugin is one YAML file. Start from
[`examples/plugins/ci-notify/agentflow-plugin.yml`](../examples/plugins/ci-notify/agentflow-plugin.yml).

```yaml
schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
description: One line about what it does.
tier: declarative
permissions:
  subscribe: [gh.ci.updated]
  invoke: [notify]
extensions:
  handler:
    - name: ci-failed
      on: [gh.ci.updated]
      when:
        - {field: payload.state, equals: failure}
      actions:
        - invoke:
            command: notify
            input:
              signal: "{{settings.signal}}"
settings:
  - key: signal
    type: string
    default: ci.failed
    label: Notification signal
    description: The notification signal to send.
```

The manifest is checked strictly: an unknown key is an error. These rules apply:

- `id` is 2 to 40 characters: lowercase letters, digits and hyphens, starting with a
  letter. It cannot be `agentflow`, `builtin`, `daemon`, `cli` or `mcp`, or start
  with `agentflow-`.
- `version` is a semantic version without a leading `v`. `tier` is `declarative` or
  `process`.
- Every pattern in `on`, every `invoke` command and every `annotate` action must be
  covered by `permissions`. Every permission must be inside the lists above.
- A manifest may have up to 32 handlers, 32 rules and 32 settings, 16 conditions per
  handler and 4 actions per handler. The file is at most 256 KiB.

### Handlers

A handler runs for each event that matches `on`. If every condition in `when` holds,
its `actions` run in order. If an action fails, the handler stops for that event and
the event goes to the dead-letter list (`~/.agentflow/plugins/deadletter.jsonl`).
An action that is waiting for your approval does not count as a failure.

A condition tests one field with exactly one operator: `equals`, `not_equals`, `in`
(a list) or `exists` (true or false). A field is `type`, `repo`, `worktree`, `source`,
`correlation_id`, or `payload.<key>` with more keys after it for nested values. A
missing field reads as null.

An action is `invoke` or `annotate`:

- `invoke` runs a command with `input`. It cannot set `confirm_token`.
- `annotate` adds a note to an operation on the timeline, with `text`, an optional
  `https` `url` and a `level` of `info` or `warning`. Use it only in a handler whose
  `on` patterns are all `op.*` events.

### Templates

Strings in `input` and in `annotate` can insert values with `{{event.<field>}}` and
`{{settings.<key>}}`. Write the name with no spaces inside the braces. There are no
functions, conditions or loops. A string that is exactly one placeholder keeps the
value's type, and any other string gets the value as text, with a list joined by
commas. A missing value becomes an empty string.

### Permission rules

A rule denies the operations it matches, and tells the caller why.

```yaml
extensions:
  permission_rule:
    - name: no-push-on-fridays
      match:
        operation: [push]
        branch: [release/*]
      deny: Pushes to release branches are paused. Ask in the release channel.
```

Every non-empty list in `match` must match, and any entry in a list may match.
Entries are glob patterns. A rule never applies to `config.*`, `plugin.*`,
`approval.*` or `policy.*` operations, so you can always turn a plugin off. Only
declarative plugins have rules.

### Settings

Each setting becomes a field in the plugin's group on the Settings page. A setting
has a `key`, a `type` (`boolean`, `string`, `integer` or `string-list`), a `label`
and a `description`, and may set a `default`, an `enum` (strings only), `min` and
`max` (an integer needs both), `min_duration` and `max_duration`, `applies` (`live`
or `restart`), `advanced` and a `widget` that suits the type. Settings that need a
secret or a file path are refused: no secret or path reaches a plugin.

## Write a process plugin

A process plugin is a directory with the manifest and a program. Set
`tier: process` and name the program:

```yaml
tier: process
exec:
  command: timeline-hints
  args: []
```

`exec.command` is a path inside the plugin directory, with no `..`. It is never
looked up on `PATH`. The manifest lists the events the program may subscribe to in
`on`, and has no `when`, `actions` or permission rules: the program decides what to
do.

agentflow starts the program with a clean environment and talks to it on standard
input and output with JSON-RPC 2.0: one JSON object per line, at most 1 MiB.

| Direction | Method | What it does |
| --- | --- | --- |
| agentflow to program | `initialize` | First message. Answer `{"protocol_version": 1}`. |
| agentflow to program | `event.deliver` | One event at a time. Answer `{}` when you are done. |
| agentflow to program | `settings.changed` | A notification: new settings values. |
| agentflow to program | `ping` | Answer `{}`. A program that misses pings is restarted. |
| agentflow to program | `shutdown` | Answer `{}`, then exit when standard input closes. |
| program to agentflow | `subscribe` | Choose the event patterns you want, within your permissions. |
| program to agentflow | `command.invoke` | Run a permitted command. |
| program to agentflow | `event.publish` | Publish `plugin.annotation.added`. |
| program to agentflow | `log` | A notification: one line for `data/plugin.log`. |

Send no request before you have answered `initialize`. The program finds
`AGENTFLOW_PLUGIN_ID`, `AGENTFLOW_PLUGIN_DIR`, `AGENTFLOW_PLUGIN_DATA` and
`AGENTFLOW_PLUGIN_PROTOCOL` in its environment. Its `HOME` and temporary
directory are inside its data directory, so keep what it stores there. The
installed copy is read-only. `examples/plugins/timeline-hints/main.go` is a
complete small program.

## Limits

| What | Limit |
| --- | --- |
| `initialize` | 5 seconds |
| One event delivery | 10 seconds, one at a time |
| Ping | every 30 seconds; 2 missed marks the plugin `degraded`, 4 restart it |
| Shutdown | 2 seconds, then the program is killed |
| Restarts | the 5th within 10 minutes marks the plugin `failed` |
| Installed plugins | 32 |
| Log file | `data/plugin.log`, 1 MiB, one older file kept |

agentflow sets no memory or CPU limit on a program, and on Windows it stops only
the program itself, not the processes it started.
