"use strict";

// The Settings page (docs/design/ui/settings.md; ADR "Phase 4 Settings
// page"). It draws one control per catalog field, collects edits in one
// draft, checks the draft with the preview endpoint, and saves it through a
// review. Current values arrive only over the event stream: the "settings"
// state in internal/webui/projection_settings_state.go. The page sends a
// request only for the reader's own actions: catalog, preview, save, undo.
// Nothing polls.
//
// The shapes are the contract types in internal/settings/types.go and
// internal/config/field_meta.go. Every value is read defensively and shown
// as text, never as markup or an instruction.
(function (scope) {
  const widgets = scope.AgentflowSettingsWidgets;
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const pick = (value, known, fallback = "") => known.includes(value) ? value : fallback;
  const plural = (count, one, many = one + "s") => count + " " + (count === 1 ? one : many);
  const time = value => { const at = Date.parse(text(value)); return Number.isFinite(at) ? at : 0; };

  // Compare two JSON values. Object keys are sorted, so a map that was only
  // reordered does not count as changed.
  function canon(value) {
    if (Array.isArray(value)) return "[" + value.map(canon).join(",") + "]";
    if (record(value)) return "{" + Object.keys(value).sort().map(key => JSON.stringify(key) + ":" + canon(value[key])).join(",") + "}";
    return JSON.stringify(value === undefined ? null : value);
  }
  const same = (a, b) => canon(a) === canon(b);

  const layers = ["personal", "repo"];
  const modes = ["draft", "worktree", "direct"];
  const severities = ["error", "warning", "info"];
  const fieldKey = (id, provider = "") => id + "|" + provider;

  // The segments of a field path: "." separates, "\." is a dot in a key.
  function segments(path) {
    const out = [];
    let current = "";
    for (let at = 0; at < path.length; at++) {
      if (path[at] === "\\" && at + 1 < path.length) current += path[++at];
      else if (path[at] === ".") { out.push(current); current = ""; }
      else current += path[at];
    }
    out.push(current);
    return out;
  }
  // The keys a concrete path fills a pattern's "*" segments with, or null.
  function matchPath(pattern, path) {
    const want = segments(pattern), have = segments(path);
    if (want.length !== have.length) return null;
    const keys = [];
    for (let at = 0; at < want.length; at++) {
      if (want[at] === "*") keys.push(have[at]);
      else if (want[at] !== have[at]) return null;
    }
    return keys;
  }

  // A link is shown only for an absolute https address.
  function outbound(target) {
    try {
      const url = new URL(target);
      return url.protocol === "https:" && !url.username && !url.password ? url.href : "";
    } catch { return ""; }
  }

  function readField(raw) {
    raw = record(raw);
    const id = text(raw?.id);
    const cut = id.indexOf(":");
    if (!raw || cut < 1 || !layers.includes(id.slice(0, cut))) return null;
    const meta = record(raw.meta) || {};
    const path = text(raw.path) || id.slice(cut + 1);
    return {
      key: fieldKey(id, text(raw.provider)), id, layer: id.slice(0, cut), path, provider: text(raw.provider),
      pattern: segments(path).includes("*"), instance: "",
      schema: record(raw.schema) || {}, columns: record(raw.columns) || {},
      meta: {...meta, label: text(meta.label) || path, description: text(meta.description), group: text(meta.group) || "other",
        order: Number.isFinite(meta.order) ? meta.order : 0, widget: text(meta.widget), applies: text(meta.applies),
        sensitive: meta.sensitive === true, loosens: text(meta.loosens), advanced: meta.advanced === true}
    };
  }

  // settings.View without values: the answer of GET api/settings/catalog.
  function readCatalog(raw) {
    raw = record(raw);
    if (!raw) return null;
    const byKey = new Map();
    for (const field of list(raw.fields).map(readField)) if (field && !byKey.has(field.key)) byKey.set(field.key, field);
    const groups = [];
    const seen = new Set();
    for (const group of list(raw.groups).map(record)) {
      if (!group || !text(group.id) || seen.has(group.id)) continue;
      seen.add(group.id);
      groups.push({id: group.id, label: text(group.label) || group.id, order: Number.isFinite(group.order) ? group.order : 0});
    }
    groups.sort((a, b) => a.order - b.order);
    // A group the fields name but the catalog does not list still gets a section.
    for (const field of byKey.values()) {
      if (seen.has(field.meta.group)) continue;
      seen.add(field.meta.group);
      groups.push({id: field.meta.group, label: field.meta.group, order: Infinity});
    }
    return {revision: text(raw.catalog_revision), groups, fields: [...byKey.values()], byKey};
  }

  // settings.Value, or a bare JSON value where null means "not set" (the
  // journal's form).
  function readValue(raw) {
    const value = record(raw);
    if (value && typeof value.set === "boolean") return {set: value.set, json: value.set && value.redacted !== true ? value.json : undefined, redacted: value.redacted === true, digest: text(value.digest)};
    if (raw === null || raw === undefined) return {set: false, json: undefined, redacted: false, digest: ""};
    return {set: true, json: raw, redacted: false, digest: ""};
  }

  function readDiagnostic(raw) {
    raw = record(raw);
    if (!raw || !text(raw.message)) return null;
    return {severity: pick(raw.severity, severities, "error"), id: text(raw.id), edit: Number.isSafeInteger(raw.edit) ? raw.edit : -1, code: text(raw.code), message: raw.message};
  }

  function readPublication(raw) {
    raw = record(raw);
    if (!raw) return null;
    return {mode: pick(raw.mode, modes), url: outbound(text(raw.url)), number: Number.isSafeInteger(raw.number) && raw.number > 0 ? raw.number : 0, branch: text(raw.branch), state: text(raw.state)};
  }

  // SettingsState.
  function readState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const values = new Map();
    for (const item of list(raw.values).map(record)) {
      if (!item || !text(item.id)) continue;
      values.set(fieldKey(item.id, text(item.provider)), {
        id: item.id, provider: text(item.provider), layer: readValue(record(item.layer) || {set: false}), effective: readValue(record(item.effective) || {set: false}),
        source: pick(item.source, ["default", "personal", "repo", "definition"], "default"), active: item.active !== false,
        cappedBy: text(item.capped_by), tooLarge: item.too_large === true
      });
    }
    const save = record(raw.last_save);
    const revisions = record(raw.revisions) || {};
    return {
      revisions: {personal: text(revisions.personal), repo: text(revisions.repo)}, catalogRevision: text(raw.catalog_revision), values,
      problems: list(raw.problems).map(readDiagnostic).filter(Boolean),
      lastSave: save && text(save.operation_id) ? {id: save.operation_id, time: time(save.time), client: text(save.client), ids: list(save.ids).filter(id => typeof id === "string"),
        publication: readPublication(save.publication), undoOf: text(save.undo_of), undoable: save.undoable === true} : null,
      branch: text(raw.branch), protected: raw.protected === true
    };
  }

  function readEffect(raw) {
    raw = record(raw);
    if (!raw || !text(raw.id)) return null;
    return {id: raw.id, before: readValue(raw.before), after: readValue(raw.after), sourceBefore: text(raw.source_before), sourceAfter: text(raw.source_after)};
  }

  // settings.Preview. The same reader takes the summary of a confirmation.
  function readPreview(raw) {
    raw = record(raw);
    if (!raw) return null;
    const plan = record(raw.repo);
    const base = record(raw.base) || {};
    return {
      status: text(raw.status), base: {personal: text(base.personal), repo: text(base.repo)},
      changes: list(raw.changes).map(record).filter(change => change && text(change.id)).map(change => ({
        id: change.id, provider: text(change.provider), edit: Number.isSafeInteger(change.edit) ? change.edit : -1, before: readValue(change.before), after: readValue(change.after),
        applies: text(change.applies), loosens: change.loosens === true, effect: readEffect(change.effective)})),
      effects: list(raw.effects).map(readEffect).filter(Boolean),
      files: list(raw.files).map(record).filter(Boolean).map(file => ({layer: pick(file.layer, layers), path: text(file.path), diff: file.layer === "repo" ? text(file.diff) : ""})),
      diagnostics: list(raw.diagnostics).map(readDiagnostic).filter(Boolean),
      confirm: list(raw.confirm).map(record).filter(item => item && text(item.id)).map(item => ({id: item.id, reason: text(item.reason), message: text(item.message) || "This change loosens the policy."})),
      repo: plan ? {mode: pick(plan.mode, modes, "draft"), title: text(plan.title), branch: text(plan.branch), destination: text(plan.destination), ci: text(plan.ci), warning: text(plan.warning)} : null,
      restart: raw.restart === true
    };
  }

  // A value in words, short enough for one line.
  function show(value) {
    let out;
    if (value === undefined || value === null) out = "not set";
    // The console formats a duration as Go does ("10m0s"); show it as the picker does ("10m").
    else if (typeof value === "string") out = value === "" ? "empty text" : /^(?:\d+h)?(?:\d+m)?\d+s$/.test(value) && /[hm]/.test(value) ? widgets.durationShort(value) : value;
    else if (typeof value === "boolean") out = value ? "on" : "off";
    else if (Array.isArray(value)) out = !value.length ? "empty list" : value.every(item => typeof item !== "object") ? value.join(", ") : plural(value.length, "item");
    else if (record(value)) {
      const keys = Object.keys(value);
      out = !keys.length ? "no entries" : keys.map(key => key + ": " + (typeof value[key] === "object" && value[key] !== null ? JSON.stringify(value[key]) : show(value[key]))).join(", ");
    } else out = String(value);
    return out.length > 160 ? out.slice(0, 157) + "…" : out;
  }
  const showValue = value => !value.set ? "not set" : value.redacted ? "hidden value" : show(value.json);

  // Does field match the search text? Every word must be in its label,
  // description or ID.
  function matches(field, query) {
    const hay = (field.meta.label + " " + field.meta.description + " " + field.id + " " + field.provider).toLowerCase();
    return query.toLowerCase().split(/\s+/).filter(Boolean).every(word => hay.includes(word));
  }

  // settings.ChangeSet from the draft's edits. A provider selection goes
  // before every other edit, so its settings are checked under it.
  function changeSet(edits, revisions, repo, isPicker = () => false) {
    const ordered = [...edits.filter(edit => isPicker(edit)), ...edits.filter(edit => !isPicker(edit))];
    const out = {
      edits: ordered.map(edit => {
        const item = {id: edit.id, op: edit.op};
        if (edit.op === "set") item.value = edit.value;
        if (!edit.blind) item.expect = edit.expect === undefined ? null : edit.expect;
        return item;
      }),
      base: {personal: revisions?.personal || "", repo: revisions?.repo || ""}
    };
    if (ordered.some(edit => edit.id.startsWith("repo:"))) {
      out.repo = {mode: pick(repo?.mode, modes, "draft")};
      if (out.repo.mode !== "worktree" && repo?.title) out.repo.title = repo.title;
      if (out.repo.mode !== "worktree" && repo?.task) out.repo.task_ref = repo.task;
    }
    return {body: out, keys: ordered.map(edit => edit.key)};
  }

  const sourceNames = {default: "the default", personal: "your personal settings", repo: "the repository file", definition: "the provider definition"};
  const layerNames = {personal: "Personal", repo: "Repository"};
  const layerFiles = {personal: "your personal settings", repo: "the repository file"};
  const appliesText = {live: "Applies at once", "next-run": "Applies to the next command", restart: "Applies after AI sessions restart", "new-worktrees": "Applies to new worktrees"};
  const modeText = {
    draft: {short: "a draft pull request", tag: "Draft pull request", save: "Saves as a draft pull request"},
    worktree: {short: "this worktree", tag: "This worktree", save: "Saves to this worktree"},
    direct: {short: "the default branch", tag: "Default branch", save: "Saves to the default branch"}
  };
  const severityNames = {error: "Error", warning: "Warning", info: "Note"};

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }
  function button(label, klass, run) {
    const node = element("button", label, klass);
    node.type = "button";
    node.addEventListener("click", run);
    return node;
  }
  function mark() {
    const shape = element("span", "", "state-mark");
    shape.setAttribute("aria-hidden", "true");
    return shape;
  }

  // options: {root, request(path, body), announce(message), storageKey,
  // route(group) -> "#…", completions() -> {branches, files}, groupLabel(id),
  // confirm(message), now()}. root holds the page markup of index.html.
  function createSettings(options) {
    const find = id => options.root.querySelector("#" + id);
    const now = () => options.now ? options.now() : Date.now();
    const ask = message => options.confirm ? options.confirm(message) : scope.confirm(message);
    const flashTimers = new WeakMap();
    const rows = new Map();     // field key -> row
    const draft = new Map();    // field key -> edit, in the order the edits were made
    const local = new Map();    // field key -> why its control holds no value yet
    const ticks = new Set();    // loosening edits ticked in the review
    const filters = {query: "", modified: false, advanced: false, group: ""};
    let repo = {mode: "draft", title: "", task: ""};
    let catalog = null, catalogState = "idle", catalogError = "", catalogSeq = 0, askedRevision = "";
    let current = null, live = {state: "connecting"}, opened = false, stored = true;
    let fields = [];            // the catalog's fields plus one per value of a "*" pattern
    let preview = null, previewError = "", previewSeq = 0, previewTimer = 0, checking = false, sentKeys = [], checkSignature = "";
    let phase = "idle";         // idle, asking, confirm (a token is held), saving
    let pending = null;         // {approval, body, reason} once the console asked for confirmation
    let dialogMode = "";        // "", "save", "undo"
    let reviewError = "", undoView = null, undoBusy = false, undoError = "";
    let lastResult = null, justSaved = new Set(), rowSerial = 0;

    // ---- the draft and its storage -------------------------------------

    function persist() {
      try {
        const edits = [...draft.values()].filter(edit => !edit.blind).map(({key, id, provider, op, value, expect}) => ({key, id, provider, op, value, expect}));
        if (!edits.length && repo.mode === "draft" && !repo.title && !repo.task) localStorage.removeItem(options.storageKey);
        else localStorage.setItem(options.storageKey, JSON.stringify({version: 1, edits, repo}));
        stored = true;
      } catch { stored = false; }
    }

    function restore() {
      try {
        const saved = record(JSON.parse(localStorage.getItem(options.storageKey)));
        if (!saved || saved.version !== 1) return;
        for (const edit of list(saved.edits).map(record)) {
          if (!edit || !text(edit.id) || !["set", "unset"].includes(edit.op) || draft.size >= 200) continue;
          const key = fieldKey(edit.id, text(edit.provider));
          draft.set(key, {key, id: edit.id, provider: text(edit.provider), op: edit.op, value: edit.op === "set" ? edit.value : undefined, expect: edit.expect === undefined ? null : edit.expect});
        }
        const target = record(saved.repo) || {};
        repo = {mode: pick(target.mode, modes, "draft"), title: text(target.title).slice(0, 64), task: text(target.task).slice(0, 200)};
      } catch { /* A damaged draft is dropped: it cannot authorize a write. */ }
    }

    const valueOf = key => current?.values.get(key) || null;
    // The field's own layer value, or null when the layer does not set it.
    const layerJSON = key => { const value = valueOf(key); return value && value.layer.set && value.layer.json !== undefined ? value.layer.json : null; };
    const fieldOf = key => fields.find(field => field.key === key) || catalog?.byKey.get(key) || null;
    const labelOf = key => fieldOf(key)?.meta.label || key.split("|")[0];
    const isPicker = field => field?.meta.widget === "provider-picker";
    const groupLabel = id => catalog?.groups.find(group => group.id === id)?.label || id;

    function previewChange(key) {
      const at = sentKeys.indexOf(key);
      return preview?.changes.find(change => at >= 0 && change.edit === at) || preview?.changes.find(change => fieldKey(change.id, change.provider) === key) || null;
    }

    // What applies when the layer holds nothing: the other layer or the default.
    function inherited(field) {
      const edit = draft.get(field.key);
      const effect = edit?.op === "unset" ? previewChange(field.key)?.effect : null;
      if (effect?.after.set) return effect.after.json;
      const value = valueOf(field.key);
      if (value && !value.layer.set && value.effective.set) return value.effective.json;
      return field.schema.default === null ? undefined : field.schema.default;
    }
    const inherits = field => { const edit = draft.get(field.key); return edit ? edit.op === "unset" : !valueOf(field.key)?.layer.set; };
    // The value the control shows: the draft's, else the layer's, else the inherited one.
    function shown(field) {
      const edit = draft.get(field.key);
      if (edit?.op === "set") return edit.value;
      if (!edit && valueOf(field.key)?.layer.set) return valueOf(field.key).layer.json;
      return inherited(field);
    }

    // A provider's settings belong to the provider its role selects, in the draft first.
    function roleOf(field) {
      const at = field.path.indexOf(".settings");
      return field.provider && at > 0 ? field.path.slice(0, at) : "";
    }
    function inUse(field) {
      if (!field.provider) return true;
      const picker = catalog.byKey.get(fieldKey("repo:" + roleOf(field) + ".provider"));
      const selected = picker ? shown(picker) : undefined;
      return selected === undefined ? valueOf(field.key)?.active === true : selected === field.provider;
    }
    const modified = field => draft.has(field.key) || valueOf(field.key)?.layer.set === true;

    // The fields to draw: every catalog field, and for a "*" pattern one
    // field per value the state or the draft holds.
    function resolveFields() {
      if (!catalog) { fields = []; return; }
      const out = [];
      const patterns = [];
      for (const field of catalog.fields) (field.pattern ? patterns : out).push(field);
      const concrete = new Map();
      for (const value of current?.values.values() || []) concrete.set(fieldKey(value.id, value.provider), value);
      for (const edit of draft.values()) if (!concrete.has(edit.key)) concrete.set(edit.key, edit);
      for (const [key, item] of concrete) {
        if (catalog.byKey.has(key)) continue;
        for (const pattern of patterns) {
          if (pattern.layer + ":" !== item.id.slice(0, pattern.layer.length + 1) || pattern.provider !== item.provider) continue;
          const keys = matchPath(pattern.path, item.id.slice(pattern.layer.length + 1));
          if (!keys) continue;
          out.push({...pattern, key, id: item.id, path: item.id.slice(pattern.layer.length + 1), pattern: false, instance: keys.join(" / "),
            meta: {...pattern.meta, label: pattern.meta.label + ": " + keys.join(" / ")}});
          break;
        }
      }
      fields = out.sort((a, b) => a.meta.order - b.meta.order || a.meta.label.localeCompare(b.meta.label));
    }

    function dropToken() {
      if (pending && dialogMode === "save") reviewError = "The changes are different now. Check them and save again.";
      if (dialogMode !== "undo") { pending = null; phase = phase === "confirm" ? "idle" : phase; }
    }

    // Called after every change to the draft or its repository target.
    function changed({check = true} = {}) {
      persist();
      dropToken();
      resolveFields();
      if (check) schedulePreview();
      render();
    }

    function edit(field, next) {
      const key = field.key;
      const row = rows.get(key);
      if (next.incomplete) {
        local.set(key, next.incomplete);
        if (row) row.incomplete = true;
        render();
        return;
      }
      local.delete(key);
      if (row) row.incomplete = false;
      const before = draft.get(key);
      const first = !draft.size;
      const blind = field.meta.sensitive;
      const expect = before ? before.expect : layerJSON(key);
      if (next.inherit) {
        if (expect === null && !blind) draft.delete(key);
        else draft.set(key, {key, id: field.id, provider: field.provider, op: "unset", expect, blind, theirs: before?.theirs});
      } else if (!blind && expect !== null && same(next.value, expect)) draft.delete(key);
      else draft.set(key, {key, id: field.id, provider: field.provider, op: "set", value: next.value, expect, blind, theirs: before?.theirs});
      if (first && draft.size) options.announce("1 unsaved change. Review and save is in the bar at the bottom of the page.");
      changed();
    }

    // Compare each edit with the layer value it was made against. A field
    // that changed outside the page conflicts; an edit the files already
    // hold is done.
    function reconcile() {
      const fresh = [];
      for (const [key, item] of draft) {
        const value = valueOf(key);
        if (item.blind || value?.tooLarge) continue;
        const theirs = layerJSON(key);
        if (same(theirs, item.expect)) { delete item.theirs; continue; }
        if (same(theirs, item.op === "set" ? item.value : null)) { draft.delete(key); continue; }
        if (!item.theirs || !same(item.theirs.json, theirs)) fresh.push(key);
        item.theirs = {json: theirs};
      }
      return fresh;
    }
    const conflicts = () => [...draft.values()].filter(item => item.theirs);

    // ---- live checks ----------------------------------------------------

    function schedulePreview(wait = 250) {
      clearTimeout(previewTimer);
      previewSeq++;
      if (!draft.size || !current || !catalog) {
        preview = null; previewError = ""; checking = false; sentKeys = [];
        return;
      }
      checking = true;
      previewTimer = setTimeout(runPreview, wait);
    }

    async function runPreview() {
      const seq = previewSeq;
      const {body, keys} = changeSet([...draft.values()], current.revisions, repo, item => isPicker(fieldOf(item.key)));
      try {
        const raw = await options.request("api/settings/preview", body);
        if (seq !== previewSeq) return;
        const next = readPreview(raw);
        if (!next) throw new Error("The console returned no preview.");
        preview = next; previewError = ""; sentKeys = keys;
      } catch (error) {
        if (seq !== previewSeq) return;
        preview = null; previewError = error.message; sentKeys = keys;
      }
      checking = false;
      for (const id of [...ticks]) if (!preview?.confirm.some(item => item.id === id)) ticks.delete(id);
      render();
      const {errors, warnings} = counts();
      const signature = errors + "/" + warnings + "/" + previewError;
      if (signature !== checkSignature) {
        checkSignature = signature;
        options.announce(previewError ? "The changes could not be checked." : errors || warnings ? "Checked: " + [errors && plural(errors, "error"), warnings && plural(warnings, "warning")].filter(Boolean).join(", ") + "." : "Checked: no problems.");
      }
    }

    // The diagnostics of one field: its edit's, and the stream's own for the files on disk.
    function diagnosticsOf(key) {
      const out = [];
      if (local.has(key)) out.push({severity: "error", code: "input", message: local.get(key)});
      const at = sentKeys.indexOf(key);
      const id = key.split("|")[0];
      for (const item of preview?.diagnostics || []) {
        if (item.code === "conflict" && draft.get(key)?.theirs) continue;
        if ((item.edit >= 0 && item.edit === at) || (item.edit < 0 && item.id && (item.id === id || id.startsWith(item.id + ".")) && draft.has(key))) out.push(item);
      }
      if (!draft.has(key)) for (const item of current?.problems || []) if (item.id === id) out.push(item);
      return out.sort((a, b) => severities.indexOf(a.severity) - severities.indexOf(b.severity));
    }
    const general = () => (preview?.diagnostics || []).filter(item => item.edit < 0 && !item.id || (item.edit >= sentKeys.length));

    function counts() {
      let errors = local.size, warnings = 0;
      for (const item of preview?.diagnostics || []) {
        if (item.code === "conflict" && item.edit >= 0 && draft.get(sentKeys[item.edit])?.theirs) continue;
        if (item.severity === "error") errors++;
        else if (item.severity === "warning") warnings++;
      }
      return {errors, warnings, conflicts: conflicts().length};
    }

    // ---- drawing --------------------------------------------------------

    // Highlight a value that changed live (the 2 s rule of live-stream.md).
    function flash(node) {
      clearTimeout(flashTimers.get(node));
      node.classList.remove("live-changed");
      void node.offsetWidth;
      node.classList.add("live-changed");
      flashTimers.set(node, setTimeout(() => node.classList.remove("live-changed"), 2000));
    }

    // Bring a keyed list in line with items without rebuilding what stays.
    function keyed(target, items, keyOf, build, update) {
      const focused = document.activeElement;
      const old = new Map();
      for (const node of [...target.children]) {
        if (node.dataset.key !== undefined) old.set(node.dataset.key, node);
        else node.remove();
      }
      let previous = null;
      for (const item of items) {
        const key = String(keyOf(item));
        let node = old.get(key);
        if (!node) { node = build(item); node.dataset.key = key; }
        old.delete(key);
        update?.(node, item);
        const place = previous ? previous.nextSibling : target.firstChild;
        if (node !== place) target.insertBefore(node, place);
        previous = node;
      }
      for (const node of old.values()) {
        if (node.contains(focused)) find("settings-title").focus({preventScroll: true});
        node.remove();
      }
      if (focused && focused !== document.activeElement && target.contains(focused)) focused.focus({preventScroll: true});
    }

    function fact(node, value) {
      if (node.textContent !== value) node.textContent = value;
      node.hidden = !value;
    }

    function variables() {
      const names = [];
      for (const field of catalog?.fields || []) {
        if (field.meta.widget !== "credential-ref") continue;
        const value = valueOf(field.key);
        if (typeof value?.effective.json === "string") names.push(value.effective.json);
      }
      return names;
    }

    function suggest(kind) {
      if (kind === "variables") return variables();
      return list(options.completions?.()[kind]);
    }

    const where = layer => layer === "personal" ? "Saves to your personal file" : modeText[repo.mode].save;

    function inheritLabel(field) {
      const value = inherited(field);
      return value === undefined ? "Inherit" : "Inherit (" + show(value) + ")";
    }

    function buildRow(field) {
      const n = ++rowSerial;
      const ids = {control: "st-c-" + n, label: "st-l-" + n, about: "st-d-" + n, source: "st-s-" + n, checks: "st-g-" + n, conflict: "st-x-" + n};
      const node = element("div", "", "st-field");
      const about = element("div", "", "st-about");
      const label = element("label", "", "st-label");
      label.id = ids.label;
      label.htmlFor = ids.control;
      const description = element("p", "", "st-description");
      description.id = ids.about;
      const facts = element("p", "", "st-facts");
      const id = element("code", "", "st-id");
      const layer = element("span", "", "tag");
      const applies = element("span", "", "st-applies");
      facts.append(layer, applies, id);
      about.append(label, description, facts);

      const side = element("div", "", "st-edit");
      const holder = element("div", "", "st-control");
      const control = widgets.create(field, {
        id: ids.control, label: field.meta.label, labelledBy: ids.label, suggest,
        inherit: () => inheritLabel(fieldOf(field.key) || field),
        change: next => edit(fieldOf(field.key) || field, next)
      });
      control.main.setAttribute("aria-describedby", [ids.about, ids.source, ids.checks].join(" "));
      holder.append(control.node);
      // A table needs the full width: it goes below the description.
      if (["kv-table", "list"].includes(control.kind)) node.classList.add("st-wide");
      const source = element("p", "", "st-source");
      source.id = ids.source;
      const cap = element("p", "", "st-cap");
      const state = element("div", "", "st-row-state");
      const edited = element("span", "", "tag st-edited");
      const revert = button("Revert", "st-link", () => { draft.delete(field.key); local.delete(field.key); row.incomplete = false; changed(); control.focus(); });
      const inherit = button("Inherit", "st-link", () => { edit(fieldOf(field.key) || field, {inherit: true}); control.focus(); });
      state.append(edited, revert, inherit);
      const conflict = element("div", "", "st-conflict");
      conflict.id = ids.conflict;
      conflict.setAttribute("role", "group");
      const theirs = element("p");
      const keep = button("Keep my edit", "button secondary small", () => {
        const item = draft.get(field.key);
        if (!item?.theirs) return;
        item.expect = item.theirs.json;
        delete item.theirs;
        options.announce("Kept your edit of " + labelOf(field.key) + ".");
        changed();
        control.focus();
      });
      const take = button("Use the new value", "button secondary small", () => {
        draft.delete(field.key);
        options.announce("Dropped your edit of " + labelOf(field.key) + ".");
        changed();
        control.focus();
      });
      const choices = element("div", "", "st-conflict-actions");
      choices.append(keep, take);
      conflict.append(theirs, choices);
      const checks = element("ul", "", "st-diagnostics");
      checks.id = ids.checks;
      side.append(holder, source, cap, state, conflict, checks);
      node.append(about, side);
      const row = {node, control, incomplete: false, parts: {label, description, id, layer, applies, holder, source, cap, edited, revert, inherit, conflict, theirs, keep, take, checks}};
      rows.set(field.key, row);
      return node;
    }

    function drawDiagnostics(target, items) {
      const signature = JSON.stringify(items.map(item => [item.severity, item.message]));
      if (target.dataset.items !== signature) {
        target.dataset.items = signature;
        target.replaceChildren(...items.map(item => {
          const line = element("li");
          line.dataset.severity = item.severity;
          line.append(mark(), element("span", severityNames[item.severity] + ": ", "sr-only"), element("span", item.message));
          return line;
        }));
      }
      target.hidden = !items.length;
    }

    function sourceText(field) {
      const value = valueOf(field.key);
      if (value?.tooLarge) return "This value is too large to show here. Edit it on the Configuration page.";
      if (field.provider && !inUse(field)) return "Not in use: " + groupLabel(field.meta.group) + " selects another provider.";
      if (!value) return field.schema.default === undefined || field.schema.default === null ? "Not set." : "Not set. The default is " + show(field.schema.default) + ".";
      if (value.layer.set) {
        const differs = value.effective.set && !value.layer.redacted && !same(value.effective.json, value.layer.json);
        return "Set in " + layerFiles[field.layer] + "." + (differs && !value.cappedBy ? " The effective value is " + showValue(value.effective) + ", from " + sourceNames[value.source] + "." : "");
      }
      return value.effective.set ? "Not set here. Uses " + sourceNames[value.source] + ": " + showValue(value.effective) + "." : "Not set.";
    }

    function capText(field) {
      const value = valueOf(field.key);
      if (!value?.cappedBy) return "";
      return "The repository ceiling “" + labelOf(fieldKey(value.cappedBy)) + "” limits this to " + showValue(value.effective) + ".";
    }

    function updateRow(field, live_) {
      const row = rows.get(field.key);
      const {parts, control} = row;
      const item = draft.get(field.key);
      const value = valueOf(field.key);
      const busy = phase === "asking" || phase === "saving" || undoBusy;
      fact(parts.label, field.meta.label);
      fact(parts.description, field.meta.description);
      fact(parts.id, field.id);
      fact(parts.layer, layerNames[field.layer] + (field.provider ? " · " + field.provider : ""));
      fact(parts.applies, appliesText[field.meta.applies] || "");
      parts.id.title = "Setting ID, as agentflow config set uses it";

      // The control: skip it while the reader's input is not a value yet.
      if (!row.incomplete) {
        const inheriting = inherits(field);
        const wanted = shown(field);
        control.ghost?.(inheriting ? wanted : undefined);
        const display = inheriting && (control.ghost || control.refresh) ? undefined : wanted;
        if (!same(control.get(), display)) {
          control.set(display);
          if (live_ && !item) flash(parts.holder);
        }
        control.refresh?.();
      }
      control.disable(busy || value?.tooLarge === true || !current);
      const checks = diagnosticsOf(field.key);
      const bad = checks.some(check => check.severity === "error");
      if (bad) control.main.setAttribute("aria-invalid", "true");
      else control.main.removeAttribute("aria-invalid");

      const change = item ? previewChange(field.key) : null;
      let source = sourceText(field);
      if (change?.effect && item) source += " After saving, the effective value is " + showValue(change.effect.after) + ".";
      fact(parts.source, source);
      fact(parts.cap, capText(field));

      fact(parts.edited, item ? (item.op === "unset" ? "Edited: inherit" : "Edited") + " · " + where(field.layer) : "");
      parts.revert.hidden = !item && !local.has(field.key);
      parts.revert.setAttribute("aria-label", "Revert " + field.meta.label + " to the saved value");
      const canInherit = item ? item.op === "set" : value?.layer.set === true;
      parts.inherit.hidden = !canInherit || busy;
      parts.inherit.setAttribute("aria-label", "Inherit " + field.meta.label + ": remove it from " + layerFiles[field.layer]);

      const clash = item?.theirs;
      parts.conflict.hidden = !clash;
      if (clash) {
        parts.conflict.setAttribute("aria-label", field.meta.label + " changed outside this page");
        fact(parts.theirs, "Changed outside this page: " + (clash.json === null ? "it was removed from " + layerFiles[field.layer] : "it is now " + show(clash.json)) + ". Your edit " + (item.op === "unset" ? "removes it" : "sets " + show(item.value)) + ".");
        parts.keep.setAttribute("aria-label", "Keep my edit of " + field.meta.label);
        parts.take.setAttribute("aria-label", "Use the new value of " + field.meta.label);
      }
      drawDiagnostics(parts.checks, checks);
      row.node.dataset.state = clash ? "conflict" : bad ? "error" : item ? "edited" : "";
    }

    const inGroup = (field, group) => !group || field.meta.group === group || field.meta.group.startsWith(group + ".");
    function passes(field, group = filters.group) {
      if (!inUse(field) && !modified(field)) return false;
      if (field.meta.advanced && !filters.advanced && !filters.query && !modified(field)) return false;
      if (filters.modified && !modified(field)) return false;
      return matches(field, filters.query) && inGroup(field, group);
    }

    function drawNav(counted) {
      const items = [{id: "", label: "All settings"}, ...catalog.groups];
      keyed(find("settings-groups"), items, group => "g:" + group.id, () => {
        const link = element("a");
        link.append(element("span", "", "st-nav-label"), element("span", "", "st-nav-count"));
        return link;
      }, (link, group) => {
        const total = counted.get(group.id) || 0;
        const edits = [...draft.values()].filter(item => inGroup(fieldOf(item.key) || {meta: {group: ""}}, group.id)).length;
        link.href = options.route(group.id);
        link.classList.toggle("st-nav-child", group.id.includes("."));
        fact(link.firstChild, group.label);
        fact(link.lastChild, edits ? edits + " edited" : String(total));
        link.lastChild.dataset.state = edits ? "edited" : "";
        link.lastChild.setAttribute("aria-label", plural(total, "setting") + (edits ? ", " + edits + " edited" : ""));
        if (group.id === filters.group) link.setAttribute("aria-current", "true");
        else link.removeAttribute("aria-current");
      });
    }

    function drawGroups(live_) {
      const visible = fields.filter(field => passes(field));
      const everywhere = fields.filter(field => passes(field, ""));
      const counted = new Map([["", everywhere.length]]);
      for (const group of catalog.groups) counted.set(group.id, everywhere.filter(field => inGroup(field, group.id)).length);
      drawNav(counted);
      const sections = catalog.groups.map(group => ({group, fields: visible.filter(field => field.meta.group === group.id)})).filter(section => section.fields.length);
      keyed(find("settings-fields"), sections, section => section.group.id, section => {
        const node = element("section", "", "panel st-group");
        const heading = element("div", "", "section-heading");
        const title = element("h2");
        title.id = "st-group-" + section.group.id.replace(/[^a-z0-9_-]/gi, "-");
        node.setAttribute("aria-labelledby", title.id);
        heading.append(title, element("span", "", "tag"));
        node.append(heading, element("div", "", "st-fields"));
        return node;
      }, (node, section) => {
        fact(node.querySelector("h2"), section.group.label);
        const edits = section.fields.filter(field => draft.has(field.key)).length;
        fact(node.querySelector(".section-heading .tag"), edits ? edits + " edited" : "");
        keyed(node.lastChild, section.fields, field => field.key, buildRow, (_, field) => updateRow(field, live_));
      });
      for (const key of [...rows.keys()]) if (!rows.get(key).node.isConnected) rows.delete(key);

      const total = fields.filter(field => inUse(field) || modified(field)).length;
      const elsewhere = everywhere.length - visible.length;
      const filtered = filters.query || filters.modified;
      let summary = visible.length === total ? plural(total, "setting") + "." : "Showing " + visible.length + " of " + plural(total, "setting") + ".";
      if (filters.group && filtered && elsewhere) summary += " " + plural(elsewhere, "more match", "more matches") + " in other groups.";
      fact(find("settings-summary"), summary);
      find("settings-all").hidden = !(filters.group && filtered && elsewhere);
      find("settings-clear").hidden = !filtered;
      const empty = find("settings-empty");
      empty.textContent = filters.modified && !filters.query ? "No setting is edited or set in a file" + (filters.group ? " in this group." : ".") : "No settings match.";
      empty.hidden = Boolean(visible.length);
      return {shown: visible.length, total};
    }

    function publicationLink(publication) {
      if (!publication) return null;
      const label = publication.number ? (publication.mode === "draft" ? "Draft pull request #" : "Pull request #") + publication.number
        : publication.mode === "worktree" ? "Written to this worktree" : publication.branch ? "Branch " + publication.branch : "";
      if (!label) return null;
      const node = element(publication.url ? "a" : "span", label);
      if (publication.url) {
        node.href = publication.url;
        node.target = "_blank";
        node.rel = "noopener noreferrer";
      }
      return node;
    }

    function clock(at) {
      if (!at) return "";
      const format = {hour: "2-digit", minute: "2-digit"};
      if (new Date(at).toDateString() !== new Date(now()).toDateString()) { format.month = "short"; format.day = "numeric"; }
      return new Date(at).toLocaleString([], format);
    }
    const clientName = client => client === "cli" ? "the command line" : ["daemon", "repository-console", "console"].includes(client) ? "this console" : client || "an unknown client";

    function drawBar() {
      const busy = phase === "asking" || phase === "saving" || undoBusy;
      const size = draft.size;
      const save = current?.lastSave || null;
      const result = lastResult && (!save || save.id !== lastResult.operation) ? lastResult : null;
      const bar = find("settings-bar");
      bar.hidden = !size && !save && !result;
      find("settings-bar-draft").hidden = !size;
      const {errors, warnings, conflicts: clashes} = counts();
      if (size) {
        fact(find("settings-bar-count"), plural(size, "unsaved change"));
        const parts = [clashes && plural(clashes, "conflict"), errors && plural(errors, "error"), warnings && plural(warnings, "warning")].filter(Boolean);
        const checks = find("settings-bar-checks");
        fact(checks, checking ? "Checking…" : previewError ? "Could not check the changes: " + previewError : parts.length ? parts.join(" · ") : "No problems found");
        checks.dataset.tone = previewError || errors || clashes ? "bad" : warnings ? "warn" : checking ? "" : "good";
        const personal = [...draft.values()].filter(item => item.id.startsWith("personal:")).length;
        const shared = size - personal;
        fact(find("settings-bar-where"), [personal && personal + " to your personal file", shared && shared + " to " + modeText[repo.mode].short].filter(Boolean).join(" · "));
        find("settings-discard").disabled = busy;
        find("settings-review-open").disabled = busy;
      }
      bar.dataset.tone = !size ? "" : errors || clashes || previewError ? "bad" : warnings ? "warn" : "";
      fact(find("settings-storage"), stored ? "" : "The browser could not store this draft. Keep this tab open until you save.");

      // The last save: from this page's own result until the stream reports it.
      const last = find("settings-bar-last");
      last.hidden = Boolean(size) && !undoError ? !save && !result : !save && !result;
      const line = find("settings-last-text");
      const link = find("settings-last-link");
      const undo = find("settings-undo");
      const why = find("settings-undo-why");
      link.replaceChildren();
      if (result) {
        fact(line, (result.status === "partial" ? "Partly saved: " : "Saved ") + plural(result.count, "setting") + "." + (result.error ? " " + result.error : "") + (result.restart ? " Restart running AI sessions to use the new provider selection." : ""));
        const target = publicationLink(result.publication);
        if (target) link.append(target);
        undo.hidden = true;
        fact(why, "");
      } else if (save) {
        fact(line, (save.undoOf ? "Last undo: " : "Last save: ") + plural(save.ids.length, "setting") + (save.time ? ", " + clock(save.time) : "") + ", from " + clientName(save.client) + ".");
        const target = publicationLink(save.publication);
        if (target) link.append(target);
        undo.hidden = false;
        undo.textContent = save.undoOf ? "Redo" : "Undo";
        undo.disabled = !save.undoable || busy;
        undo.setAttribute("aria-label", (save.undoOf ? "Redo the save that was undone: " : "Undo the last save: ") + plural(save.ids.length, "setting"));
        fact(why, undoError || (save.undoable ? "" : save.publication?.number ? "The repository change waits in pull request #" + save.publication.number + ". Close it to discard the change." : "Every setting of this save has changed since."));
        why.dataset.tone = undoError ? "bad" : "";
      }
      link.hidden = !link.childElementCount;
    }

    function drawProblems() {
      const loose = (current?.problems || []).filter(item => !item.id || !fields.some(field => field.id === item.id));
      const box = find("settings-problems");
      box.hidden = !loose.length;
      drawDiagnostics(find("settings-problem-list"), loose);
    }

    function render(live_ = false) {
      if (!opened) return;
      const ready = Boolean(catalog && current);
      find("settings-body").hidden = !ready;
      const failed = catalogState === "error";
      fact(find("settings-error-text"), failed ? "The settings catalog did not load. " + catalogError : "");
      find("settings-error").hidden = !failed;
      status(live);
      if (!ready) { drawBar(); return; }
      find("settings-search").value !== filters.query && (find("settings-search").value = filters.query);
      find("settings-modified").checked = filters.modified;
      find("settings-advanced").checked = filters.advanced;
      drawProblems();
      const shownNow = drawGroups(live_);
      drawBar();
      renderReview();
      return shownNow;
    }

    // ---- the review -----------------------------------------------------

    function changeItem(change, fallbackEdit) {
      const key = fieldKey(change.id, change.provider);
      const field = fieldOf(key) || fieldOf(fieldKey(change.id));
      const node = element("li", "", "st-change");
      const head = element("div", "", "st-change-head");
      head.append(element("strong", field?.meta.label || change.id), element("code", change.id, "st-id"));
      const layer = change.id.startsWith("personal:") ? "personal" : "repo";
      head.append(element("span", layer === "personal" ? "Personal file" : dialogMode === "undo" ? "Repository" : modeText[repo.mode].tag, "tag"));
      const values = element("p", "", "st-change-values");
      values.append(element("span", fallbackEdit ? "current value" : showValue(change.before), "removed"), element("span", " → ", "st-arrow"), element("span", showValue(change.after), "added"));
      values.querySelector(".st-arrow").setAttribute("aria-label", "changes to");
      node.append(head, values);
      const notes = [];
      if (change.effect) notes.push(same(change.effect.before.json, change.effect.after.json) && change.effect.before.set === change.effect.after.set
        ? "The effective value stays " + showValue(change.effect.after) + "." : "Effective value: " + showValue(change.effect.before) + " → " + showValue(change.effect.after) + (sourceNames[change.effect.sourceAfter] ? ", from " + sourceNames[change.effect.sourceAfter] : "") + ".");
      if (appliesText[change.applies]) notes.push(appliesText[change.applies] + ".");
      if (notes.length) node.append(element("p", notes.join(" "), "st-change-note"));
      return node;
    }

    function tick(item) {
      const label = element("label", "", "st-tick");
      const box = element("input");
      box.type = "checkbox";
      box.checked = ticks.has(item.id);
      box.dataset.confirm = item.id;
      box.addEventListener("change", () => {
        if (box.checked) ticks.add(item.id); else ticks.delete(item.id);
        drawReviewActions();
      });
      label.append(box, element("span", "I confirm: " + item.message));
      return label;
    }

    function drawDiff(target, diff) {
      target.replaceChildren(...diff.split("\n").map(line => {
        const kind = line.startsWith("+++") || line.startsWith("---") ? "file" : line.startsWith("@@") ? "hunk" : line.startsWith("+") ? "add" : line.startsWith("-") ? "remove" : "";
        const node = element("span", line + "\n");
        if (kind) node.dataset.line = kind;
        return node;
      }));
    }

    const reviewView = () => dialogMode === "undo" ? undoView : preview;
    function canConfirm() {
      const view = reviewView();
      if (!view || view.confirm.some(item => !ticks.has(item.id))) return false;
      if (dialogMode === "undo") return phase === "confirm";
      return view.status === "ok" && !checking && !local.size && !conflicts().length && (phase === "idle" || phase === "confirm");
    }

    function drawReviewActions() {
      const undo = dialogMode === "undo";
      const redo = undo && current?.lastSave?.undoOf;
      const save = find("settings-save");
      save.textContent = phase === "asking" ? "Asking…" : phase === "saving" ? "Saving…"
        : undo ? (redo ? "Confirm and redo" : "Confirm and undo") : phase === "confirm" ? "Confirm and save" : "Save " + plural(draft.size, "change");
      save.disabled = !canConfirm();
      save.hidden = undo && phase === "idle";
      find("settings-undo-skip").hidden = !(undo && phase === "idle" && undoView?.status === "conflict");
      const view = reviewView();
      const unticked = view ? view.confirm.filter(item => !ticks.has(item.id)).length : 0;
      fact(find("settings-save-why"), unticked ? "Tick " + plural(unticked, "confirmation") + " above to continue." : "");
      const note = find("settings-review-confirm");
      fact(note, phase === "confirm" && pending ? "Agentflow asks you to confirm. " + pending.reason : "");
    }

    function renderReview() {
      const dialog = find("settings-review");
      if (!dialog.open) return;
      const undo = dialogMode === "undo";
      const view = reviewView();
      const redo = undo && current?.lastSave?.undoOf;
      fact(find("settings-review-title"), undo ? (redo ? "Redo the last save" : "Undo the last save") : "Review and save");

      // What blocks the save, in one sentence.
      let note = "", bad = false;
      if (reviewError) { note = reviewError; bad = true; }
      else if (undo && !view) note = "";
      else if (!undo && checking) note = "Checking the changes…";
      else if (!undo && previewError) { note = "The changes could not be checked: " + previewError; bad = true; }
      else if (!undo && conflicts().length) { note = plural(conflicts().length, "setting") + " changed outside this page. Go back and choose which value to keep."; bad = true; }
      else if (view?.status === "invalid" || (!undo && local.size)) { note = "Fix the errors before you save."; bad = true; }
      else if (view?.status === "conflict") { note = undo ? "Some settings changed since the save, so they cannot be put back." : "Some settings changed outside this page. Go back and check them."; bad = true; }
      else if (view?.status === "unchanged") note = "Nothing would change: the files already hold these values.";
      else if (view?.restart) note = "After saving, restart running AI sessions: a provider selection changes their tools.";
      const status_ = find("settings-review-status");
      fact(status_, note);
      status_.classList.toggle("error", bad);

      // The changes, each with its loosening tick.
      const changes = find("settings-review-changes");
      const items = view?.changes.length ? view.changes.map(change => {
        const node = changeItem(change, false);
        const loosening = view.confirm.filter(item => item.id === change.id);
        if (change.loosens || loosening.length) {
          node.dataset.loosens = "true";
          node.querySelector(".st-change-head").append(element("span", "Loosens policy", "tag st-loosens"));
        }
        for (const item of loosening) node.append(tick(item));
        return node;
      }) : undo ? (current?.lastSave?.ids || []).map(id => changeItem({id, provider: "", before: readValue(undefined), after: readValue(undefined), applies: ""}, true))
        : [...draft.values()].map(item => changeItem({id: item.id, provider: item.provider, before: readValue(item.expect), after: readValue(item.op === "set" ? item.value : null), applies: ""}, false));
      const orphans = view ? view.confirm.filter(item => !view.changes.some(change => change.id === item.id)) : [];
      for (const item of orphans) {
        const node = element("li", "", "st-change");
        node.dataset.loosens = "true";
        node.append(element("strong", labelOf(fieldKey(item.id))), tick(item));
        items.push(node);
      }
      const signature = JSON.stringify([dialogMode, view?.changes, view?.confirm, view ? "" : [...draft.values()], repo.mode]);
      if (changes.dataset.items !== signature) {
        changes.dataset.items = signature;
        changes.replaceChildren(...items);
      }
      fact(find("settings-review-count"), plural(items.length, "change"));

      const effects = view?.effects || [];
      find("settings-review-effects-section").hidden = !effects.length;
      find("settings-review-effects").replaceChildren(...effects.map(effect => {
        const node = element("li", "", "st-change");
        node.append(element("strong", labelOf(fieldKey(effect.id))), element("p", "Effective value: " + showValue(effect.before) + " → " + showValue(effect.after) + (sourceNames[effect.sourceAfter] ? ", from " + sourceNames[effect.sourceAfter] : "") + ".", "st-change-note"));
        return node;
      }));

      // Where the repository half goes.
      const shared = undo ? Boolean(view?.repo) : [...draft.values()].some(item => item.id.startsWith("repo:"));
      const personal = undo ? Boolean(view?.changes.some(change => change.id.startsWith("personal:"))) : [...draft.values()].some(item => item.id.startsWith("personal:"));
      find("settings-review-repo").hidden = !shared;
      find("settings-review-modes").hidden = undo;
      find("settings-review-publication").hidden = undo || repo.mode === "worktree";
      for (const input of find("settings-review-modes").querySelectorAll("input[type=radio]")) {
        input.checked = input.value === repo.mode;
        input.disabled = phase === "asking" || phase === "saving" || (input.value === "worktree" && current?.protected === true);
      }
      fact(find("settings-mode-protected"), current?.protected ? "The branch " + (current.branch || "of this worktree") + " is protected, so the file cannot be written here." : "");
      const title = find("settings-review-commit");
      if (document.activeElement !== title && title.value !== repo.title) title.value = repo.title;
      title.placeholder = view?.repo?.title || "chore: update agentflow settings";
      const task = find("settings-review-task");
      if (document.activeElement !== task && task.value !== repo.task) task.value = repo.task;
      // The plan of an older preview does not describe the mode just chosen.
      const plan = view?.repo && (undo || view.repo.mode === repo.mode) ? view.repo : null;
      const mode = undo ? plan?.mode || "draft" : repo.mode;
      const goes = mode === "worktree" ? "Writes .agentflow/config.yml in this worktree" + (plan?.branch || current?.branch ? ", on " + (plan?.branch || current.branch) : "") + ". Commit it with your task."
        : (mode === "draft" ? "Opens a draft pull request" : "Applies to the default branch") + (plan?.destination ? " in " + plan.destination : "") + ". This worktree's file stays as it is.";
      fact(find("settings-review-destination"), [goes, plan?.ci, plan?.warning].filter(Boolean).join(" "));
      const diff = view?.files.find(file => file.layer === "repo")?.diff || "";
      find("settings-review-diff").hidden = !diff;
      if (find("settings-review-diff-text").dataset.diff !== diff) {
        find("settings-review-diff-text").dataset.diff = diff;
        drawDiff(find("settings-review-diff-text"), diff);
      }
      find("settings-review-personal").hidden = !personal;
      const loose = undo ? (view?.diagnostics || []) : [...general(), ...[...draft.keys()].flatMap(key => diagnosticsOf(key).filter(item => item.severity !== "info").map(item => ({...item, message: labelOf(key) + ": " + item.message})))];
      drawDiagnostics(find("settings-review-diagnostics"), loose);
      drawReviewActions();
    }

    function openReview(mode) {
      dialogMode = mode;
      reviewError = "";
      const dialog = find("settings-review");
      if (!dialog.open) dialog.showModal();
      renderReview();
      find("settings-review-title").focus();
    }

    function closeReview() {
      const dialog = find("settings-review");
      if (dialog.open) dialog.close();
    }

    // Read the console's own sentence about a confirmation.
    function explanation(raw) {
      const rule = record(raw.policy);
      const reason = text(raw.reason);
      if (!rule) return reason;
      return [text(rule.action) && text(rule.level) ? text(rule.action) + " requires " + rule.level + (text(rule.source) ? " under " + rule.source : "") + "." : "", reason].filter(Boolean).join(" ");
    }

    function finish(raw, undo) {
      const publication = readPublication(raw.publication);
      const partial = raw.status === "partial";
      const count = list(raw.changes).length || (undo ? current?.lastSave?.ids.length || 0 : draft.size);
      lastResult = {status: raw.status, operation: text(raw.operation_id), count, publication, error: text(raw.error), restart: raw.restart === true};
      if (!undo) {
        justSaved = new Set(draft.keys());
        // A partial save published the repository half and kept the personal one.
        for (const [key, item] of [...draft]) if (!partial || item.id.startsWith("repo:")) draft.delete(key);
        ticks.clear();
        // The next save starts from the default again: a draft pull request.
        repo = {mode: "draft", title: "", task: ""};
      }
      pending = null; phase = "idle"; undoBusy = false; undoError = ""; undoView = null; reviewError = "";
      persist();
      resolveFields();
      schedulePreview();
      closeReview();
      render();
      options.announce((partial ? "Partly saved. " + lastResult.error : (undo ? "Undone: " : "Saved ") + plural(count, "setting") + ".") + (publication?.number ? " Pull request #" + publication.number + " is open." : ""));
      (find("settings-undo").hidden || find("settings-undo").disabled ? find("settings-title") : find("settings-undo")).focus({preventScroll: true});
    }

    // The answer of api/settings/save and api/settings/undo: a request to
    // confirm with its token, the applied result, or a preview that says why
    // nothing was saved.
    function answer(raw, body, undo) {
      raw = record(raw) || {};
      const status_ = text(raw.status);
      if (status_ === "needs_confirmation") {
        const approval = text(raw.approval) || text(raw.confirm_token);
        if (!approval) throw new Error("The console asked for a confirmation without a token.");
        pending = {approval, body, reason: explanation(raw)};
        phase = "confirm";
        if (undo) undoView = readPreview(raw.summary) || {status: "ok", changes: [], effects: [], files: [], diagnostics: [], confirm: [], repo: null, restart: false};
        return;
      }
      if (status_ === "saved" || status_ === "partial") { finish(raw, undo); return; }
      pending = null; phase = "idle";
      if (["ok", "invalid", "conflict", "unchanged"].includes(status_)) {
        if (undo) undoView = readPreview(raw);
        else { preview = readPreview(raw); previewError = ""; }
        if (status_ === "ok") reviewError = "The console did not save the changes. Try again.";
        return;
      }
      reviewError = explanation(raw) || "The console did not save the changes" + (status_ ? " (" + status_.replaceAll("_", " ") + ")." : ".");
    }

    async function submit() {
      if (!canConfirm()) return;
      const undo = dialogMode === "undo";
      const path = undo ? "api/settings/undo" : "api/settings/save";
      const confirming = phase === "confirm" && pending;
      const body = confirming ? pending.body : changeSet([...draft.values()], current.revisions, repo, item => isPicker(fieldOf(item.key))).body;
      const payload = undo ? {undo: body} : {change_set: body};
      if (confirming) payload.approval = pending.approval;
      phase = confirming ? "saving" : "asking";
      reviewError = "";
      render();
      try {
        answer(await options.request(path, payload), body, undo);
      } catch (error) {
        pending = null; phase = "idle";
        reviewError = error.message + (confirming ? " Check the changes and save again." : "");
        if (!undo) schedulePreview(0);
      }
      render();
      if (phase === "confirm") find("settings-save").focus();
    }

    async function startUndo(skip = false) {
      const save = current?.lastSave;
      if (!save || undoBusy) return;
      const body = {};
      if (skip) body.skip_conflicts = true;
      if (save.publication?.mode) body.repo = {mode: save.publication.mode};
      undoBusy = true; undoError = ""; undoView = null; pending = null; phase = "idle"; dialogMode = "undo"; ticks.clear();
      render();
      try {
        answer(await options.request("api/settings/undo", {undo: body}), body, true);
        undoBusy = false;
        if (dialogMode === "undo" && (pending || undoView)) openReview("undo");
        if (pending) find("settings-save").focus();
      } catch (error) {
        undoBusy = false; dialogMode = "";
        undoError = error.message;
        options.announce("The save was not undone. " + error.message);
      }
      render();
    }

    // ---- the catalog and the stream --------------------------------------

    async function loadCatalog() {
      if (catalogState === "loading") return;
      const seq = ++catalogSeq;
      catalogState = "loading";
      render();
      try {
        const next = readCatalog(await options.request("api/settings/catalog"));
        if (seq !== catalogSeq) return;
        if (!next) throw new Error("The console returned no catalog.");
        catalog = next; catalogState = "ready"; catalogError = "";
        if (filters.group && !catalog.groups.some(group => group.id === filters.group)) filters.group = filters.group.includes(".") && catalog.groups.some(group => group.id === filters.group.split(".")[0]) ? filters.group.split(".")[0] : "";
        resolveFields();
        // An edit of a field the catalog no longer has cannot be drawn or saved.
        const gone = [...draft.keys()].filter(key => !fieldOf(key));
        for (const key of gone) draft.delete(key);
        if (gone.length) { persist(); options.announce(plural(gone.length, "unsaved change was", "unsaved changes were") + " dropped: the setting no longer exists."); }
        if (current) reconcile();
        schedulePreview(0);
      } catch (error) {
        if (seq !== catalogSeq) return;
        catalogState = "error"; catalogError = error.message;
      }
      render();
    }

    // Show the settings state. With flash, values that differ from the ones
    // on screen are highlighted and announced. null keeps the last values.
    function showState(raw, live_ = false) {
      const next = readState(raw);
      if (!next) return current;
      const previous = current;
      current = next;
      if (lastResult && next.lastSave?.id === lastResult.operation) lastResult = null;
      if (opened && catalog && next.catalogRevision && next.catalogRevision !== catalog.revision && next.catalogRevision !== askedRevision) {
        askedRevision = next.catalogRevision;
        loadCatalog();
      }
      const said = [];
      if (catalog) {
        const size = draft.size;
        const fresh = reconcile();
        resolveFields();
        for (const key of fresh) said.push(labelOf(key) + " changed outside this page. Choose which value to keep");
        if (live_ && previous) {
          let others = 0;
          for (const [key, value] of next.values) {
            if (draft.has(key) || justSaved.has(key) || !fieldOf(key)) continue;
            const before = previous.values.get(key);
            if (before && same(before.layer, value.layer) && same(before.effective, value.effective)) continue;
            if (said.length < 3) said.push(labelOf(key) + " changed to " + showValue(value.layer.set ? value.layer : value.effective));
            else others++;
          }
          if (others) said.push("and " + plural(others, "more setting") + " changed");
        }
        if (!same(previous?.revisions, next.revisions) || draft.size !== size) { persist(); dropToken(); schedulePreview(); }
      }
      justSaved = new Set();
      render(live_ && Boolean(previous));
      if (live_ && said.length) options.announce(said.join(". ") + ".");
      return current;
    }

    // The stream's state, for the note above the page: {state, offline (a
    // reason's own sentence, or ""), ready, present}.
    function status(next) {
      live = next;
      if (!opened) return;
      const has = Boolean(current);
      let note = "";
      if (live.state === "offline") note = live.offline || "Live updates are offline. " + (has ? "Settings show the last values received; a save still checks the files on disk." : "Settings appear when they return.") + " The console keeps retrying.";
      else if (live.state === "live" && live.ready && !live.present) note = "The live stream has no settings for this worktree yet.";
      fact(find("settings-note"), note);
      const waiting = !has || catalogState === "loading" || catalogState === "idle";
      fact(find("settings-wait"), note || catalogState === "error" || !waiting ? "" : !has ? "Waiting for live updates…" : "Loading the settings catalog…");
    }

    // Open the page at a group ("" shows every group).
    function open(group = "") {
      opened = true;
      filters.group = group;
      if (catalog && group && !catalog.groups.some(item => item.id === group)) filters.group = catalog.groups.some(item => item.id === group.split(".")[0]) ? group.split(".")[0] : "";
      if (catalogState === "idle") loadCatalog();
      else render();
    }

    function refilter() {
      const result = render();
      if (result) options.announce(result.shown === result.total ? plural(result.total, "setting") + "." : "Showing " + result.shown + " of " + plural(result.total, "setting") + ".");
    }

    // ---- events -----------------------------------------------------------

    find("settings-search").addEventListener("input", event => { filters.query = event.target.value.trim(); refilter(); });
    find("settings-modified").addEventListener("change", event => { filters.modified = event.target.checked; refilter(); });
    find("settings-advanced").addEventListener("change", event => { filters.advanced = event.target.checked; refilter(); });
    find("settings-tools").addEventListener("submit", event => event.preventDefault());
    find("settings-clear").addEventListener("click", () => {
      filters.query = ""; filters.modified = false;
      find("settings-search").focus();
      refilter();
    });
    find("settings-all").addEventListener("click", () => { scope.location.hash = options.route(""); });
    find("settings-retry").addEventListener("click", loadCatalog);
    // A control whose input was never a value goes back to the saved one.
    find("settings-fields").addEventListener("focusout", event => {
      const node = event.target.closest?.(".st-field");
      const row = node && rows.get(node.dataset.key);
      if (!row?.incomplete || node.contains(event.relatedTarget)) return;
      row.incomplete = false;
      local.delete(node.dataset.key);
      render();
    });
    find("settings-discard").addEventListener("click", () => {
      if (!draft.size || !ask("Discard " + plural(draft.size, "unsaved settings change") + "?")) return;
      const count = draft.size;
      draft.clear(); local.clear(); ticks.clear();
      for (const row of rows.values()) row.incomplete = false;
      repo = {mode: "draft", title: "", task: ""};
      changed();
      options.announce("Discarded " + plural(count, "change") + ".");
      find("settings-title").focus({preventScroll: true});
    });
    find("settings-review-open").addEventListener("click", () => {
      if (!draft.size) return;
      if (current?.protected && repo.mode === "worktree") repo.mode = "draft";
      schedulePreview(0);
      openReview("save");
    });
    find("settings-undo").addEventListener("click", () => startUndo(false));
    find("settings-undo-skip").addEventListener("click", () => startUndo(true));
    find("settings-save").addEventListener("click", submit);
    find("settings-review-close").addEventListener("click", closeReview);
    find("settings-review").addEventListener("close", () => {
      if (phase === "confirm") { pending = null; phase = "idle"; }
      if (dialogMode === "undo") undoView = null;
      dialogMode = ""; reviewError = "";
      render();
    });
    find("settings-review-form").addEventListener("submit", event => event.preventDefault());
    for (const input of find("settings-review-modes").querySelectorAll("input[type=radio]")) {
      input.addEventListener("change", () => {
        if (!input.checked) return;
        repo.mode = pick(input.value, modes, "draft");
        reviewError = "";
        changed();
      });
    }
    find("settings-review-commit").addEventListener("input", event => { repo.title = event.target.value.trim().slice(0, 64); reviewError = ""; changed(); });
    find("settings-review-task").addEventListener("input", event => { repo.task = event.target.value.trim().slice(0, 200); reviewError = ""; changed(); });

    restore();

    return {
      show: showState, status, open, reload: loadCatalog,
      get state() { return current; },
      get catalog() { return catalog; },
      get edits() { return [...draft.values()].map(item => ({...item})); },
      get preview() { return preview; },
      get busy() { return phase === "asking" || phase === "saving" || undoBusy; },
      get stored() { return stored; }
    };
  }

  scope.AgentflowSettings = {readCatalog, readState, readPreview, readValue, changeSet, matches, show, segments, matchPath, canon, createSettings};
})(globalThis);
