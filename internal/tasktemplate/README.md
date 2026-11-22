# Task object templates

Decode a catalog from a JSON array. Each name and opaque revision string together identify one immutable definition. Keep old revisions available for existing tasks. For example:

```json
[{
  "name": "bug",
  "revision": "1",
  "defaults": {"type": "bug", "labels": ["triage"]},
  "required": ["title", "priority"],
  "sections": [{"heading": "Impact"}, {"heading": "Verification", "body": "Pending"}],
  "checklist": ["Reproduce the issue"]
}]
```

**Create.** Call `catalog.Select(ref)`, then `template.CheckSupport(writableFields)` using the configured adapter's canonical field names. Call `template.Resolve(inputs)` and pass the resulting `provider.TaskDraft` to the provider only on success. Persist `template.Ref` alongside the task identity. The provider's write revision and the template revision have independent lifecycles.

**Inputs.** Omitted fields inherit defaults. Explicit strings and lists replace defaults, including `""` and `[]`. Required field names are `scope`, `title`, `description`, `type`, `priority`, `labels`, and `links`. Unknown requirements and unsupported adapter requirements wrap `provider.ErrUnsupported`.

**Body format.** Supply section content in the description under an exact, unindented `## Heading` line. Missing sections with default bodies are appended in declaration order. Each required section needs nonblank content. Missing checklist entries are appended in declaration order as `- [ ] Item`. Existing checked entries (`- [x]` or `- [X]`) satisfy the requirement. The scanner skips entries inside unindented backtick or tilde fences. Existing text is retained byte for byte.

**Later operations.** Select the stored ref, check support for the fields the adapter can read, and call `template.Validate(currentDraft)` before the operation. Populate the snapshot from current task data, including scope and any required labels. Validation is read-only. The caller retains the full provider task, including its identity, state and write revision.
