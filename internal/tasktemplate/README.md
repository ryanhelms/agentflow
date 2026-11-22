# Task object templates

Configure a JSON catalog in `.devflow/config.yml` under the task management role.
The same settings apply to any task provider.

```yaml
task_management:
  templates:
    catalog: .devflow/task-templates.json
    default:
      name: professional
      revision: '1'
```

Omit `templates` to leave templating unset. When present, `catalog` is required and uses a
slash-separated path relative to the repository root. The file must be regular. Any symbolic
links in the path must use relative targets and stay inside the repository. Omit `default`
or set it to `null` to apply templates only when a caller selects one explicitly. A default
requires both `name` and `revision`. Quote numeric-looking revisions in YAML to preserve
their exact spelling. Catalog lookup occurs when a reference is selected.

The catalog is a JSON array. Each name and opaque revision string together identify one
immutable definition. Keep old revisions available for existing tasks. Unknown properties
and duplicate name/revision pairs are invalid. Catalog entries contain declarative fields,
Markdown section bodies, and checklist items. This example requires the standard professional
sections and supplies a default for Dependencies.

```json
[{
  "name": "professional",
  "revision": "1",
  "required": ["title"],
  "sections": [
    {"heading": "Objective"},
    {"heading": "Scope"},
    {"heading": "Dependencies", "body": "None identified."},
    {"heading": "Acceptance Criteria"},
    {"heading": "Verification"}
  ]
}]
```

**Create.** Call `(*app.App).ResolveTaskTemplate(ref)` on the opened app. Pass `tasktemplate.Ref{}` to use the configured default. A nil template and nil error allow the existing creation path. On success with a template, call `template.CheckSupport(writableFields)` using the configured adapter's canonical field names. Call `template.Resolve(inputs)` and pass the resulting `provider.TaskDraft` to the provider only on success. Persist `template.Ref` alongside the task identity. The provider's write revision and the template revision have independent lifecycles.

**Inputs.** Omitted fields inherit defaults. Explicit strings and lists replace defaults, including `""` and `[]`. Required field names are `scope`, `title`, `description`, `type`, `priority`, `labels`, and `links`. Unknown requirements and unsupported adapter requirements wrap `provider.ErrUnsupported`.

**Body format.** Supply section content in the description under an exact, unindented `## Heading` line. Missing sections with default bodies are appended in declaration order. Each required section needs nonblank content. Missing checklist entries are appended in declaration order as `- [ ] Item`. Existing checked entries (`- [x]` or `- [X]`) satisfy the requirement. The scanner skips entries inside unindented backtick or tilde fences. Existing text is retained byte for byte.

**Later operations.** For a task with a stored template ref, pass that ref to `ResolveTaskTemplate`, check support for the fields the adapter can read, and call `template.Validate(currentDraft)` before the operation. Use the existing operation path for tasks without a stored ref. Populate the snapshot from current task data, including scope and any required labels. Validation is read-only. The caller retains the full provider task, including its identity, state and write revision.
