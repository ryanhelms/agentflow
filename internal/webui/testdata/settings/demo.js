"use strict";

// A scripted backend for looking at the Settings page (TestSettingsDemo in
// settings_browser_test.go). It runs in the page: it answers the preview,
// save and undo requests itself and writes the stream frames a real console
// would send. It is a stand-in for lane 4.2's endpoints and projection, for
// design review only.
(async () => {
  const byID = id => document.getElementById(id);
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const repo = "3f2a9c1b7d4e5f60";
  let seq = 0;
  const emit = text => nativeFetch("/fixture/stream/emit", {method: "POST", body: text});
  const nativeFetch = window.fetch.bind(window);
  const frame = (event, data) => "event: " + event + "\nid: v1:" + repo + "=" + (++seq) + "\ndata: " + JSON.stringify(data) + "\n\n";
  const overview = {projection: "overview", repo, worktree: "first", state: {branch: "issue-71-settings", head: "1111111", oid: "1111111aaaaaaa1111111aaaaaaa1111111aaaa", detached: false, changed: 0, staged: 0, unstaged: 0, untracked: 0, operation: "", config: {valid: true, problems: []}}};

  let fields = new Map();
  const learn = raw => { for (const field of raw?.fields || []) fields.set(field.id + "|" + (field.provider || ""), field); };

  // Layer values: what the two files hold. Everything else is a default.
  const layer = new Map(Object.entries({
    "repo:task_management.provider|": "github-issues",
    "repo:task_management.settings.repository|github-issues": "acme/widgets",
    "repo:task_management.settings.labels|github-issues": ["agentflow", "triage"],
    "repo:task_management.settings.search.limit|github-issues": 25,
    "repo:task_management.settings.transitions|github-issues": {"pr.opened": "open", "pr.merged": "closed"},
    "repo:knowledge_base.provider|": "repository",
    "repo:knowledge_base.settings.directory|repository": "docs/knowledge",
    "repo:knowledge_base.settings.branch|repository": "main",
    "repo:notifications.provider|": "slack",
    "repo:notifications.settings.destination|slack": "#delivery",
    "repo:notifications.settings.credential_ref|slack": "SLACK_BOT_TOKEN",
    "personal:autonomy.local|": "confirm",
    "personal:autonomy.actions|": {commit: "auto", push: "confirm"},
    "personal:approval_window|": "10m",
    "repo:policy.max_autonomy.local|": "confirm",
    "repo:policy.max_autonomy.remote|": "confirm",
    "repo:policy.protected_branches|": ["main", "develop"],
    "repo:policy.test_cmd|": "make test",
    "repo:commit.types|": ["feat", "fix", "docs", "chore"],
    "repo:commit.subject_max|": 72,
    "repo:commit.require_key|": false,
    "personal:update.auto|": true,
    "repo:update.channel|": "stable"
  }));
  const defaults = {"personal:autonomy.remote|": "confirm", "repo:policy.max_approval_window|": "5m", "repo:policy.allow_auto_pr_publication|": false, "repo:policy.coordinator_merge|": false,
    "repo:commit.ai_trailer|": false, "personal:init.auto|": "ask", "repo:update.check_every|": "24h"};
  let lastSave = null, journal = null, revision = 1;
  const levels = ["suggest", "confirm", "auto"];

  function selected(role) { return layer.get("repo:" + role + ".provider|") ?? fields.get("repo:" + role + ".provider|")?.schema.default; }
  function values() {
    const out = [];
    for (const [key, field] of fields) {
      const at = field.path.indexOf(".settings");
      const active = !field.provider || (at > 0 && selected(field.path.slice(0, at)) === field.provider);
      const set = layer.has(key);
      if (!set && !active) continue;
      const fallback = defaults[key] ?? field.schema.default;
      let effective = set ? layer.get(key) : fallback;
      const item = {id: field.id, layer: set ? {json: layer.get(key), set: true} : {set: false}, source: set ? field.layer : "default", active};
      if (field.provider) item.provider = field.provider;
      // The repository ceiling caps a personal level.
      if (field.id === "personal:autonomy.local") {
        const ceiling = layer.get("repo:policy.max_autonomy.local|");
        if (ceiling && levels.indexOf(effective) > levels.indexOf(ceiling)) { effective = ceiling; item.capped_by = "repo:policy.max_autonomy.local"; }
      }
      item.effective = effective === undefined || effective === null ? {set: false} : {json: effective, set: true};
      out.push(item);
    }
    return out.sort((a, b) => a.id.localeCompare(b.id));
  }
  const state = () => ({revisions: {personal: "sha256:p" + revision, repo: "sha256:r" + revision}, catalog_revision: "sha256:fixture-1", values: values(), problems: [], last_save: lastSave, branch: "issue-71-settings", protected: false});
  const entry = () => ({projection: "settings", repo, worktree: "first", state: state()});
  const snapshot = () => emit(frame("snapshot", {cursor: "v1:" + repo + "=" + (seq + 1), states: [overview, entry()]}));
  const delta = type => emit(frame("delta", {event: {id: "e" + seq, type, repo, seq}, states: [entry()]}));

  const keyOf = id => [...fields.keys()].find(key => key.startsWith(id + "|") && (!fields.get(key).provider || fields.get(key).provider === selected(fields.get(key).path.split(".settings")[0]))) || id + "|";
  const loosens = (field, before, after) => {
    switch (field?.meta.loosens) {
      case "level": return typeof after === "string" ? levels.indexOf(after) > levels.indexOf(before ?? "confirm") : JSON.stringify(after).includes("auto") && !JSON.stringify(before ?? {}).includes("auto");
      case "enable": return after === true && before !== true;
      case "longer": return parseInt(after, 10) > parseInt(before ?? "5", 10);
      case "shrink": return Array.isArray(after) && (before || []).some(item => !after.includes(item));
    }
    return false;
  };

  function preview(set) {
    const changes = [], diagnostics = [], confirm = [], diff = [];
    let repoEdits = 0;
    (set.edits || []).forEach((edit, index) => {
      const key = keyOf(edit.id);
      const field = fields.get(key);
      const before = layer.has(key) ? layer.get(key) : null;
      const after = edit.op === "set" ? edit.value : null;
      if ("expect" in edit && JSON.stringify(edit.expect) !== JSON.stringify(before)) diagnostics.push({severity: "error", id: edit.id, edit: index, code: "conflict", message: "This setting changed outside this page."});
      const loose = loosens(field, before ?? defaults[key] ?? field?.schema.default, after);
      const change = {id: edit.id, edit: index, applies: field?.meta.applies || "live", loosens: loose, before: before === null ? {set: false} : {json: before, set: true}, after: after === null ? {set: false} : {json: after, set: true}};
      if (field?.provider) change.provider = field.provider;
      if (loose) {
        const message = field.meta.loosens === "level" ? "Allows " + field.meta.label.toLowerCase() + " with less review." : field.meta.loosens === "shrink" ? "Removes a protected branch." : "Loosens: " + field.meta.label + ".";
        confirm.push({id: edit.id, reason: "loosens", message});
        diagnostics.push({severity: "info", id: edit.id, edit: index, code: "loosens", message});
      }
      if (edit.id === "personal:autonomy.local" && after === "auto" && layer.get("repo:policy.max_autonomy.local|") !== "auto") {
        change.effective = {id: edit.id, before: {json: "confirm", set: true}, after: {json: "confirm", set: true}, source_before: "personal", source_after: "personal"};
        diagnostics.push({severity: "warning", id: edit.id, edit: index, code: "capped", message: "The repository ceiling for local actions is confirm, so auto has no effect here."});
      }
      if (field?.meta.widget === "credential-ref" && after && !/TOKEN|KEY|SECRET/.test(after)) diagnostics.push({severity: "warning", id: edit.id, edit: index, code: "credential", message: "The variable " + after + " is not set in this console's environment."});
      if (edit.id === "repo:commit.subject_max" && after > 100) diagnostics.push({severity: "error", id: edit.id, edit: index, code: "range", message: "A subject line longer than 100 characters does not fit a pull request title."});
      if (field?.meta.applies === "restart") diagnostics.push({severity: "info", id: edit.id, edit: index, code: "applies", message: "Running AI sessions keep the old value until they restart."});
      if (edit.id.startsWith("repo:")) {
        repoEdits++;
        const path = edit.id.slice(5);
        if (before !== null) diff.push("-" + path + ": " + JSON.stringify(before));
        if (after !== null) diff.push("+" + path + ": " + JSON.stringify(after));
      }
      changes.push(change);
    });
    const mode = set.repo?.mode || "draft";
    const files = [];
    if (repoEdits) files.push({layer: "repo", path: ".agentflow/config.yml", before_revision: "sha256:r" + revision, after_revision: "sha256:r" + (revision + 1), diff: "--- a/.agentflow/config.yml\n+++ b/.agentflow/config.yml\n@@ -1,12 +1,12 @@\n agentflow: 1\n" + diff.join("\n") + "\n"});
    if (changes.length > repoEdits) files.push({layer: "personal", path: "~/.agentflow/config.yml", before_revision: "sha256:p" + revision, after_revision: "sha256:p" + (revision + 1)});
    const errors = diagnostics.filter(item => item.severity === "error");
    return {
      status: !errors.length ? "ok" : errors.every(item => item.code === "conflict") ? "conflict" : "invalid", base: {personal: "sha256:p" + revision, repo: "sha256:r" + revision},
      changes, effects: [], files, diagnostics, confirm,
      repo: repoEdits ? {mode, title: set.repo?.title || "chore: update agentflow settings", destination: mode === "worktree" ? "" : "acme/widgets@main", branch: mode === "worktree" ? "issue-71-settings" : "", ci: mode === "worktree" ? "" : "This change does not start CI."} : undefined,
      restart: changes.some(change => change.applies === "restart")
    };
  }

  function apply(set, undoOf) {
    const edits = [];
    for (const edit of set.edits) {
      const key = keyOf(edit.id);
      edits.push({key, id: edit.id, before: layer.has(key) ? layer.get(key) : null, after: edit.op === "set" ? edit.value : null});
      if (edit.op === "set") layer.set(key, edit.value); else layer.delete(key);
    }
    revision++;
    journal = edits;
    const shared = set.edits.some(edit => edit.id.startsWith("repo:"));
    const mode = set.repo?.mode || "draft";
    const publication = shared ? (mode === "worktree" ? {mode, branch: "issue-71-settings"} : {mode, url: "https://github.com/acme/widgets/pull/128", number: 128, branch: "agentflow/settings-128", state: "open"}) : undefined;
    lastSave = {operation_id: "op_demo" + revision, time: new Date().toISOString(), client: "daemon", ids: edits.map(edit => edit.id), publication, undo_of: undoOf || undefined, undoable: true};
    setTimeout(() => delta("op.applied"), 150);
    return {status: "saved", operation_id: lastSave.operation_id, changes: preview({edits: []}).changes.concat(edits.map(edit => ({id: edit.id}))), revisions: state().revisions, publication, restart: false, undo: true};
  }

  const tokens = new Map();
  const json = body => new Response(JSON.stringify(body), {status: 200, headers: {"Content-Type": "application/json"}});
  window.fetch = async (input, init) => {
    const path = typeof input === "string" ? input : input.url;
    const route = /api\/settings\/(preview|save|undo)$/.exec(path)?.[1];
    if (!route) return nativeFetch(input, init);
    const body = JSON.parse(init.body);
    await wait(120);
    if (route === "preview") return json(preview(body));
    const set = route === "save" ? body.change_set : {edits: (journal || []).map(edit => edit.before === null ? {id: edit.id, op: "unset"} : {id: edit.id, op: "set", value: edit.before}), repo: body.undo?.repo};
    const seen = preview(set);
    if (seen.status !== "ok") return json(seen);
    if (!body.approval) {
      const token = "token-" + Math.random().toString(16).slice(2);
      tokens.set(token, JSON.stringify(set));
      return json({status: "needs_confirmation", approval: token, reason: "Settings saves require confirmation.", policy: {action: "console.settings", level: "confirm", source: "the built-in rule"}, summary: seen});
    }
    if (tokens.get(body.approval) !== JSON.stringify(set)) return new Response(JSON.stringify({error: "The request changed after it was confirmed (operation_changed)."}), {status: 409, headers: {"Content-Type": "application/json"}});
    tokens.delete(body.approval);
    return json(apply(set, route === "undo" ? "op_demo" + revision : ""));
  };

  // Open a session, then the Settings page, and fill it from the stream.
  for (;;) {
    if (!byID("console-shell").hidden) break;
    if (!byID("connection-options").hidden) {
      const ticket = await (await nativeFetch("/fixture/ticket", {method: "POST"})).text();
      const wanted = new URLSearchParams(location.search).get("screen") || "settings";
      location.hash = new URLSearchParams({launch: ticket, screen: wanted}).toString();
      await wait(400);
    }
    await wait(50);
  }
  if (!fields.size) {
    const session = sessionStorage.getItem("agentflow.session:/w/first/");
    learn(await (await nativeFetch("/w/first/api/settings/catalog", {headers: {Authorization: "Bearer " + session}})).json());
  }
  await snapshot();

  // Hooks for the reviewer: an outside edit, and a protected branch.
  window.demo = {
    outside(id, value) { if (value === undefined) layer.delete(keyOf(id)); else layer.set(keyOf(id), value); revision++; return delta("config.layer.changed"); },
    snapshot
  };
})();
