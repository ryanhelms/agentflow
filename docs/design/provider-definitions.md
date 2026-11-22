# Configuration-driven providers

Status: phase 1 (contracts, layers and constraints for compiled providers) is
implemented. Later phases are a design for review. User-facing documentation is
in [provider definitions](../providers.md#provider-definitions).

## Goal

Adding or changing a provider should be configuration, not a devflow release.
A provider is described by a YAML **definition**. devflow loads definitions from
several layers, validates them, notices changes, and exposes them through the CLI
and MCP server with the same behaviour on both. Compiled Go providers remain for
the cases that need code, and they can be overridden by configuration unless a
higher layer locks the property.

## Decisions so far

| Topic | Decision |
| --- | --- |
| Driver model | One operation model. Each role operation binds to a driver: `compiled`, `mcp`, `cli`, `http` or `plugin`. An opt-in `agent` fallback is allowed for MCP only. |
| Sources | The release layer (compiled providers plus nscale YAML definitions, both embedded in the binary), project `.devflow/providers/`, and user `~/.devflow/providers/`. |
| Precedence | release → project → user. A later layer wins unless the release layer constrains the property, section or operation. |
| Lock kinds | `lock: true`, `allow: [...]`, `lock: append` and `pattern: ...`. |
| Contracts | Each definition declares a contract version per role. devflow supports the current and previous major version of each contract. |
| Write safety | Every write operation declares idempotency: a lookup and a match, or `idempotent: true`. |
| Daemon | One per user, started on demand, exits when idle. |
| Runners | A `runner` profile loads release and project layers, treats project approvals as implicit, and binds credentials from env. |
| Compiled providers | Kept. Their settings and operation bindings can be overridden by definitions unless locked. |
| Project trust | A project definition that adds or changes commands, endpoints, servers or executables needs user approval, stored per content digest. Changes that only touch settings or mappings need none. |
| Mapping language | CEL, for input templates and output mappings. |
| Credentials | Named credential refs. Only the user layer binds a ref to a keychain entry, env var or command. |
| Tool exposure | The role tools (`devflow_*`) stay generic and route to the selected provider. Extra operations a definition declares become `devflow_<provider>_<op>` (MCP) and `devflow <provider> <op>` (CLI). |
| Reload | `devflow mcp` hot-reloads. An optional shared daemon serves the CLI when running, with a fresh load as fallback. Every change is validated first, and the last good version is kept on failure. |

## Layers and merge

```
release  compiled Go + embedded YAML   versioned with devflow, can set constraints
project  .devflow/providers/*.yml      approval needed for transport changes
user     ~/.devflow/providers/*.yml    binds credentials and local transports
```

The release layer combines what were separate built-in and org catalogs. It holds
the compiled providers, nscale's YAML definitions and every constraint.
Changing it takes a devflow release.

Merging works one property at a time. Maps merge, and scalars and lists replace.
Any property, settings section or operation in the release layer can carry a
constraint:

| Constraint | Meaning | Example |
| --- | --- | --- |
| `lock: true` | No later layer may change it. | `site` |
| `allow: [...]` | Later values must come from this set (each list item, for a list). | Jira `projects` |
| `lock: append` | Later layers may add list entries but not remove release entries. | required labels, reviewers |
| `pattern: <regex>` | Later string values must match. | `base_url` restricted to `*.nscale.com` |

Constraints live in a `constraints` block next to `settings`, keyed by the
settings path (`search.limit`). A separate block keeps a constraint from being
mistaken for a map-valued setting such as `hosts` or `transitions`.

If a project or user layer breaks a constraint, the load fails with a
validation error naming the file, the constraint and the release file that set
it. In phase 1, an invalid definition stops provider operations. Repository
commit, branch protection and update policies load independently. Phase 4 isolates failures to
the affected provider: the reload snapshot keeps that provider's last good
version and reports the error.

The effective selection records where each value came from.
`devflow setup providers --explain` shows the layer that set each property
and any constraint on it. `.devflow/config.yml` role settings apply after the
project definition files and before the user layer.

## Definition shape

A definition file is keyed by provider, then role. Most providers (`github`
for example) use different settings types in different roles, so every block
is scoped to one role. Named transports and credentials are declared once per
file, and any role block in that file can use them.

Phase 1 accepts `contract`, `settings` and `constraints` for compiled
providers. Everything else below is the phase 2+ design.

```yaml
# .devflow/providers/linear.yml
version: 1
credentials:
  linear: {description: Linear API key}
transports:
  linear-api:
    http: {base_url: https://api.linear.app, auth: {bearer: linear}}
  linear-mcp:
    mcp: {command: [linear-mcp], env: {LINEAR_API_KEY: {credential: linear}}}
providers:
  linear:
    task_management:
      contract: tasks/v1
      settings_schema:                # config-only providers declare their settings
        team: {type: string, required: true}
      settings:
        team: PLAT
      operations:
        task.get:
          driver: http
          transport: linear-api
          request:
            method: POST
            path: /graphql
            body: '{"query": "...", "variables": {"id": ref.id}}'      # CEL
          output: |                                                    # CEL -> provider.Task
            {"ref": ref, "summary": body.data.issue.title,
             "status": body.data.issue.state.name}
        task.transition:
          driver: mcp
          transport: linear-mcp
          tool: update_issue
          input: '{"id": ref.id, "stateId": settings.states[target]}'
          effects: remote             # drives the policy/approval gate
          idempotent: true            # setting the same state twice is harmless
        task.create:
          driver: http
          transport: linear-api
          request: {method: POST, path: /graphql, body: '...'}
          output: '...'
          effects: remote
          idempotency:                # required for every write
            lookup: task.find
            query: '{"text": "devflow-op:" + operation_id}'
            match: 'result.size() == 1'
      extras:                         # become devflow_linear_<op> and devflow linear <op>
        cycle_current:
          driver: http
          transport: linear-api
          description: Current cycle for the team
          input_schema: {}
          effects: read
```
Role operations (`task.get`, `task.find`, `task.create`, `task.transition`,
`task.comment`, `task.assign`, `docs.search`, `docs.publish`, `notify.send`,
`checks.list`, ...) are the existing Go interfaces in `internal/provider`.
Their input and output types become a versioned contract schema. CEL output
mappings are type-checked against that schema when a definition loads, so a
mapping that cannot produce a `Task` fails validation before anything runs.

### Contract versions

Each definition declares one contract version per role (`tasks/v1`). Adding a
field is a minor change: existing definitions keep loading and the new field is
empty. A breaking change creates a new major version (`tasks/v2`). devflow
supports the current and previous major version of each contract, and converts
results from the older version internally. Dropping support for a major version
is a devflow breaking change and needs a devflow major release.

### Write safety

Every operation with `effects: local` or `effects: remote` must declare one of:

- `idempotent: true`: repeating the call has no further effect.
- `idempotency: {lookup, query, match}`: before a retry, devflow runs the
  lookup operation with the CEL query, and returns the existing effect if
  the CEL match succeeds. The operation's input must carry
  `operation_id` somewhere the lookup can find it.

A write with neither fails validation. An optional `revision` block (a CEL
expression for the current revision, plus the request field that sends it)
handles concurrent edits the same way Confluence `expected_revision` does today.
Ambiguous results (a timeout after sending) are reported as `unknown` and
resolved with the lookup on the next call.

## Drivers

| Driver | Runs | Notes |
| --- | --- | --- |
| `compiled` | A registered Go function | Today's adapters, which register their operations by name. |
| `http` | A request through devflow's HTTP client | devflow injects auth from the credential ref. Retries and idempotency use `operation_id`. |
| `mcp` | A tool call on a stdio or hosted MCP server | Builds on `managedmcp`, which currently only probes companions and will now call their tools too. |
| `cli` | An argv exec with no shell | Captures stdout as JSON, or as text that CEL parses. Exit code maps to error. |
| `plugin` | An executable speaking the devflow provider protocol (JSON-RPC over stdio) | For logic that a mapping can't express. Versioned and checksum-pinned like companions. |
| `agent` | Returns the provider's raw MCP tools to the calling agent with instructions | MCP only, explicit opt-in per operation, and marked non-deterministic in results and audit. The CLI reports the operation as unavailable. |

Every driver passes through `internal/app` and `internal/policy`. Approvals,
confirm tokens, the never-list, audit and secret scanning apply the same way
whichever driver runs.

## Trust

- **Release:** trusted, and shipped and signed with devflow.
- **User:** trusted because the user wrote it.
- **Project:** devflow hashes each project provider's transport-affecting fields
  (commands, URLs, servers, plugin paths, credential uses). An unapproved hash
  blocks those operations, and `devflow setup plan` reports `needs approval`.
  `devflow setup approve <provider>` (MCP: `devflow_setup_apply` with a
  confirmation) stores the approval in the user state, keyed by repo and hash.
  A change to settings or a mapping alone keeps the approval.
- **Constraints** let the release layer stop the project or user layer from
  redirecting a provider, for example `lock: true` on `site` or a `pattern` on
  `transports.api.base_url`.

### Runner profile

`DEVFLOW_PROFILE=runner`, set by devflow's reusable workflows, loads only the
release and project layers. Project approvals are implicit, because the
definitions are already reviewed in the PR or on the default branch.
Credentials bind from `DEVFLOW_CRED_<NAME>` environment variables. There is no
daemon and no hot reload. One guard applies: under `pull_request_target`, or for
a PR from a fork, project definitions that change transports are ignored and the
default branch's versions are used instead, so untrusted PR code never runs with
secrets.

## Credentials

Definitions declare named credentials. The user layer binds them:

```yaml
# ~/.devflow/credentials.yml
linear: {keychain: devflow/linear}
jira:   {command: [op, read, "op://Eng/Jira/token"]}
github: {env: GH_TOKEN}
```

The release layer can restrict the allowed source types for a credential, for
example keychain only. Credential values never appear in definitions, logs, MCP results or
CEL environments. Transports receive them only at the point of use.

## Loading and reload

1. **Discover** the four layers. Project files come from the repo root and user
   files from the devflow home.
2. **Parse and merge**, enforcing locks.
3. **Validate**: settings schema, CEL compile and type check against the contract,
   driver and transport references, and approval state.
4. **Publish** an immutable snapshot that the app layer reads.

`devflow mcp` watches the layer directories (fsnotify, debounced). When a change
validates, the server swaps the snapshot, re-registers extra tools and sends
`notifications/tools/list_changed`. When validation fails, the server keeps the
previous snapshot and reports the error through `devflow_setup_plan` and a log
notification.

`devflow daemon` (optional) runs once per user. It keeps one snapshot per repo
root, on a user-only socket under the devflow home. The first CLI or MCP call
that finds no daemon may start one when `daemon: auto` is set in user config.
It exits after an idle timeout (default 30 minutes), and needs no
launchd/systemd setup. The CLI uses it only when it runs the same binary
version; otherwise the CLI loads fresh, using a cache keyed by file digests.
Results are identical either way.

## Exposure

- Role tools: today's `devflow_*` tools are unchanged. They route by the selected
  provider for the role.
- Extras: `devflow_<provider>_<op>` and `devflow <provider> <op> [--json]`,
  both generated from the definition's `input_schema`, so CLI/MCP parity holds by
  construction. Their read-only, local or remote annotations come from `effects`.
- `devflow setup providers` lists definitions from every layer, along with their
  source, lock state, approval state and readiness.

## Phases

1. **Contracts and layers** (done). A versioned contract registry
   (`internal/contract`: `tasks/v1`, ... with each role's operation names and
   effects), the layered loader, the merge and all four constraint kinds,
   used for today's compiled providers with settings only and no new drivers.
   `setup providers --explain` shows effective settings and their sources.
2. **Operation engine.** Add input and output type schemas to the contracts,
   so CEL mappings can be type-checked. Add CEL and the `http`, `cli` and `mcp` drivers, with
   the idempotency and revision rules, all routed through `internal/app` and
   policy. Rebuild one compiled provider (proposal: `github-issues`) as a
   definition and compare its results with the compiled adapter's on the same
   fixtures.
3. **Trust, credentials and runners.** Add project approvals per digest, the
   credential binding, constraints on transports, and the `runner` profile with
   its fork guard.
4. **Exposure and reload.** Generate extra tools and commands, add MCP hot
   reload with `tools/list_changed`, and add the per-user on-demand daemon.
5. **Plugins and agent fallback.** Define the plugin protocol and the
   explicitly marked agent driver.
6. **Release definitions.** Move suitable compiled providers (Jira, Confluence,
   Slack, ...) into embedded YAML definitions in the release layer, and keep
   compiled code only where it is needed (filesystem, git repository, secret
   scanning).

## Open questions

None. The decisions are in the table at the top. Remaining choices are
implementation detail for each phase.
