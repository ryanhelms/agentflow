"use strict";
const $ = (id) => document.getElementById(id);
const repositoryFile = ".devflow/config.yml";
const drafts = new Map();
const sessionKey = "devflow.session";
const views = ["overview", "providers", "configuration", "handoff"];
const roleNames = {
  code_hosting: "Code hosting", task_management: "Task management", knowledge_base: "Knowledge base",
  "devops.checks": "Checks", "devops.deployments": "Deployments", "devops.releases": "Releases",
  secops: "Security operations", notifications: "Notifications"
};
let session = "";
let state = null;
let activity = null;
let roleID = "";
let filePath = "";
let review = null;
let settingBeingEdited = null;
let publishing = false;
let previewSequence = 0;
let connectionSequence = 0;

function element(tag, text = "", klass = "") {
  const node = document.createElement(tag);
  if (text !== "") node.textContent = text;
  if (klass) node.className = klass;
  return node;
}

function notice(id, text = "", error = false) {
  const node = $(id);
  node.textContent = text;
  node.hidden = !text;
  node.classList.toggle("error", error);
}

function labelRole(id) { return roleNames[id] || id; }
function problems() { return (state?.config?.problems || []).join("\n"); }
function files() { return state?.config?.files || []; }
function checkoutRoles() { return state?.config?.providers?.roles || []; }
function roles() {
  const draft = drafts.get(repositoryFile);
  if (!draft) return checkoutRoles();
  if (draft.base_digest !== file(repositoryFile)?.digest) return [];
  return draft.providers?.roles || [];
}
function file(path = filePath) { return files().find((item) => item.path === path); }
function role() { return roles().find((item) => item.role === roleID); }
function display(value) {
  if (value === null) return "null";
  if (value === undefined) return "Unset";
  if (typeof value === "string") return value === "" ? '""' : value;
  return JSON.stringify(value);
}
function valueType(value) {
  if (value === null) return "nullable";
  return Array.isArray(value) ? "list" : typeof value;
}
function sourceName(source) {
  return [source?.layer, source?.file].filter(Boolean).join(" · ") || "Source unavailable";
}
function outbound(label, target) {
  let url;
  try { url = new URL(target); } catch { return null; }
  if (url.protocol !== "https:" && url.protocol !== "http:") return null;
  const a = element("a", label);
  a.href = url.href;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  return a;
}

async function request(path, body) {
  const headers = { Accept: "application/json" };
  if (session) headers.Authorization = "Bearer " + session;
  const options = { headers, cache: "no-store", credentials: "omit", redirect: "error" };
  if (body !== undefined) {
    options.method = "POST";
    headers["Content-Type"] = "application/json";
    headers["X-Devflow-Request"] = "console";
    options.body = JSON.stringify(body);
  }
  let response;
  try { response = await fetch(path, options); }
  catch { throw new Error("Cannot reach the local devflow process. Check that devflow ui is still running."); }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401) throw new Error("This session has expired. Reopen the launch URL printed by devflow ui.");
    throw new Error(data.error || "Request failed (" + response.status + ").");
  }
  return data;
}

async function authenticate() {
  const launch = new URLSearchParams(location.hash.slice(1)).get("launch");
  if (launch) {
    history.replaceState(null, "", location.pathname + location.search);
    const result = await request("/api/session", { launch });
    if (!result.session) throw new Error("Launch could not be verified. Open the URL printed by devflow ui.");
    session = result.session;
    try { sessionStorage.setItem(sessionKey, session); } catch { /* Keep this tab usable without storage. */ }
  } else {
    try { session = sessionStorage.getItem(sessionKey) || ""; } catch { /* Show the launch instructions below. */ }
  }
  if (!session) throw new Error("Open the launch URL printed by devflow ui to access this checkout.");
}

function showPage(focus = false) {
  let page = location.hash.slice(1);
  if (!views.includes(page)) page = "overview";
  if (page === "providers") renderProviders();
  for (const name of views) $("page-" + name).hidden = page !== name;
  for (const link of document.querySelectorAll("[data-page]")) {
    if (link.dataset.page === page) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  if (focus) $("main").focus();
}

function go(page) {
  if (location.hash === "#" + page) showPage(true);
  else location.hash = page;
}
window.addEventListener("hashchange", () => showPage(true));

function renderOverview() {
  const name = state.repository || state.root?.split(/[\\/]/).pop() || "Local checkout";
  $("top-repo").textContent = name;
  $("checkout-name").textContent = name;
  $("root-path").textContent = state.root || "Worktree path unavailable";
  $("rail-version").textContent = state.version || "";
  document.title = name + " · devflow";
  const dirty = Boolean(state.dirty);
  $("working-state").textContent = dirty ? "Uncommitted changes" : "Clean worktree";
  $("working-state").classList.toggle("changed", dirty);
  const metadata = $("repo-metadata");
  metadata.replaceChildren();
  for (const [label, value] of [["Branch",state.branch || "Detached HEAD"], ["Revision",state.head?.slice(0,12)], ["CLI version",state.version], ["Caller pin",state.pin]]) {
    const group = element("div");
    group.append(element("dt",label),element("dd",value || "Unavailable"));
    metadata.append(group);
  }
  const routing = $("routing-list");
  routing.replaceChildren();
  for (const item of checkoutRoles()) {
    const row = element("li","","route");
    const button = element("button",labelRole(item.role));
    button.type = "button";
    button.addEventListener("click", () => { chooseRole(item.role); go("providers"); });
    row.append(button,element("code",item.selected || "Unconfigured"));
    routing.append(row);
  }
  if (!checkoutRoles().length) routing.append(element("li","No resolved provider roles are available.","muted"));
  renderActivity();
}

function renderActivity() {
  const entries = [...(state?.attention || []), ...(activity?.attention || [])];
  const target = $("attention-list");
  target.replaceChildren();
  for (const entry of entries) {
    const li = element("li");
    const header = element("div", "", "attention-item");
    header.append(element("strong", entry.title));
    const link = entry.url && outbound("Open", entry.url);
    if (link) header.append(link);
    li.append(header);
    if (entry.detail) li.append(element("p",entry.detail,"attention-detail"));
    if (entry.run_id && entry.request_id) li.append(replyForm(entry));
    target.append(li);
  }
  $("activity-note").textContent = activity
    ? (activity.warning || (!entries.length ? "No outstanding items reported." : "Latest activity for this checkout."))
    : "Check activity to include remote pull requests and blockers.";
  const brief = [state?.handoff,activity?.handoff].filter(Boolean).join("\n");
  $("handoff-text").textContent = brief || "No handoff is available for this checkout.";
  $("copy-handoff").disabled = !brief;
}

async function loadState() {
  $("refresh").disabled = true;
  try {
    const next = await request("/api/state");
    state = next;
    activity = null;
    connectionSequence++;
    $("check-connections").disabled = !next.config?.providers;
    $("connection-results").hidden = true;
    $("connection-list").replaceChildren();
    $("connection-status").textContent = "Check the providers selected in the configuration on disk.";
    invalidateReview();
    $("activity").disabled = false;
    renderOverview();
    renderProviders();
    renderEditor();
    notice("status");
  } catch (e) { notice("status", e.message, true); }
  finally { $("refresh").disabled = !session; }
}
$("refresh").addEventListener("click", loadState);

async function loadActivity() {
  $("activity").disabled = true;
  $("activity-note").textContent = "Checking repository activity…";
  try { activity = await request("/api/activity"); renderActivity(); }
  catch (e) { $("activity-note").textContent = e.message; }
  finally { $("activity").disabled = false; }
}
$("activity").addEventListener("click",loadActivity);

function replyForm(entry) {
  const form = element("form","","reply-form");
  const label = element("label","Reply to this worker");
  const input = element("textarea");
  input.rows = 2;
  input.required = true;
  label.append(input);
  const send = element("button","Review reply","button secondary small");
  send.type = "submit";
  const message = element("p","","muted");
  message.setAttribute("role","status");
  let approval = "";
  input.addEventListener("input",() => {
    approval = "";
    send.textContent = "Review reply";
    message.textContent = "";
  });
  form.append(label,send,message);
  form.addEventListener("submit",async (event) => {
    event.preventDefault();
    const answer = input.value;
    if (!answer.trim()) return;
    send.disabled = true;
    input.readOnly = true;
    try {
      const body = {run_id:entry.run_id,request_id:entry.request_id,answer};
      if (approval) body.approval = approval;
      const result = await request("/api/reply",body);
      if (result.approval) {
        approval = result.approval;
        send.textContent = "Confirm send";
        message.textContent = "Confirm to deliver this answer to the worker.";
      } else if (result.state === "answered") {
        message.textContent = "Reply delivered.";
        await loadActivity();
      } else { throw new Error("The reply has no confirmed delivery status. Check activity."); }
    } catch (e) {
      approval = "";
      send.textContent = "Review reply";
      message.textContent = e.message;
    } finally { send.disabled = false; input.readOnly = false; }
  });
  return form;
}

$("copy-handoff").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("handoff-text").textContent);
    notice("copy-message","Copied the repository brief.");
  } catch {
    const selection = getSelection();
    const range = document.createRange();
    range.selectNodeContents($("handoff-text"));
    selection.removeAllRanges();
    selection.addRange(range);
    notice("copy-message","The brief is selected. Use your browser’s Copy command.");
  }
});

$("check-connections").addEventListener("click", async () => {
  const attempt = ++connectionSequence;
  const button = $("check-connections");
  const status = $("connection-status");
  const results = $("connection-results");
  const list = $("connection-list");
  button.disabled = true;
  status.textContent = "Checking providers configured on disk…";
  results.hidden = true;
  list.replaceChildren();
  try {
    const checks = await request("/api/readiness", {});
    if (attempt !== connectionSequence) return;
    const requirements = checks.requirements || [];
    let ready = 0, verify = 0;
    for (const item of requirements) {
      if (item.ready) ready++;
      if (item.verify_in_client) verify++;
      const row = element("li");
      const head = element("div","","attention-item");
      const name = item.role === "companion" ? "Companion" : labelRole(item.role);
      head.append(element("strong",name + " · " + item.provider),
        element("span",item.verify_in_client ? "Verify in client" : item.ready ? "Ready" : "Needs attention","tag"));
      row.append(head,element("p",item.detail,"attention-detail"));
      list.append(row);
    }
    status.textContent = requirements.length
      ? ready + " checks ready · " + verify + " to verify in client · " + (requirements.length - ready - verify) + " needing attention"
      : "No connection checks apply to the providers configured on disk.";
    results.hidden = !requirements.length;
    results.open = !checks.ready && requirements.length > 0;
  } catch (e) {
    if (attempt === connectionSequence) status.textContent = e.message;
  } finally {
    if (attempt === connectionSequence) button.disabled = !state?.config?.providers;
  }
});

function selectOption(select, value, label) {
  const option = element("option",label);
  option.value = value;
  select.append(option);
}
function chooseRole(id) {
  roleID = id;
  $("setting-filter").value = "";
  renderProviders();
}
function renderProviders() {
  const staged = drafts.has(repositoryFile);
  const error = staged && !roles().length ? "Preview your edited repository configuration to inspect its provider settings." : problems();
  notice("provider-error",error,true);
  const available = roles();
  if (!role()) roleID = available[0]?.role || "";
  const items = $("roles");
  items.replaceChildren();
  for (const item of available) {
    const button = element("button",labelRole(item.role));
    button.type = "button";
    button.setAttribute("aria-pressed",String(item.role === roleID));
    button.append(element("span",item.selected || "Unconfigured"));
    button.addEventListener("click",() => chooseRole(item.role));
    items.append(button);
  }
  const active = role();
  $("role-empty").hidden = Boolean(active);
  $("role-detail").hidden = !active;
  $("role-title").textContent = active ? labelRole(active.role) : "Provider";
  const selected = active?.options?.find((option) => option.id === active.selected);
  const roleFacts = active ? [active.contract, active.configured ? "Repository selection" : "Role default"] : [];
  if (staged && active) roleFacts.unshift("Staged configuration");
  if (selected?.readiness && selected.readiness !== "none") roleFacts.push("Verification method: " + selected.readiness);
  $("role-contract").textContent = roleFacts.filter(Boolean).join(" · ");
  notice("compose-message");
  if (!active) return;
  const choices = $("provider-choice");
  choices.replaceChildren();
  if (!active.selected) selectOption(choices,"","Choose a provider");
  for (const option of active.options || []) selectOption(choices,option.id,option.id + (option.default ? " (default)" : ""));
  choices.value = active.selected || "";
  windowChoice();
  renderSettings();
}
function windowChoice() {
  const current = role();
  const switching = Boolean(current && $("provider-choice").value && $("provider-choice").value !== current.selected);
  $("stage-provider").disabled = !switching || !file(repositoryFile)?.editable;
  $("switch-note").hidden = !switching;
}
$("provider-choice").addEventListener("change",windowChoice);

function appendHistory(node, setting) {
  const history = setting.history || [];
  if (!history.length && !setting.constraint) return;
  const detail = element("details");
  detail.append(element("summary","Sources and constraints"));
  if (history.length) {
    const list = element("ol","","history");
    for (const item of history) list.append(element("li",sourceName(item.source) + " · " + display(item.value)));
    detail.append(list);
  }
  if (setting.constraint) detail.append(element("div","Release constraint · " + display(setting.constraint),"code wrap"));
  node.append(detail);
}
function renderSettings() {
  const filter = $("setting-filter").value.trim().toLowerCase();
  const target = $("settings-list");
  target.replaceChildren();
  for (const setting of role()?.settings || []) {
    const value = display(setting.value);
    const origin = sourceName(setting.source);
    if (!(setting.path + " " + value + " " + origin).toLowerCase().includes(filter)) continue;
    const row = element("div","","setting");
    const head = element("div","","setting-top");
    head.append(element("strong",setting.path),element("span",propertySchema(setting).type || setting.type || valueType(setting.value),"field-type"));
    const edit = element("button","Edit","button secondary small");
    edit.disabled = !file(repositoryFile)?.editable || setting.constraint?.lock === true;
    edit.setAttribute("aria-label","Edit " + setting.path);
    edit.addEventListener("click",() => openSetting(setting));
    head.append(edit);
    row.append(head,element("div",value,"value"),element("p",origin,"source-line"));
    appendHistory(row,setting);
    target.append(row);
  }
  if (!target.children.length) target.append(element("p",filter ? "No matching settings." : "This role has no settings to display.","muted"));
}
$("setting-filter").addEventListener("input",renderSettings);

function propertySchema(setting) {
  let schema = state?.config?.schemas?.[roleID]?.[role()?.selected];
  const parts = setting.path.match(/(?:\\.|[^.\\])+/g) || [];
  for (const part of parts) {
    const key = part.replace(/\\(.)/g,"$1");
    schema = schema?.properties?.[key] || schema?.additional_properties;
  }
  return schema || {};
}

function openSetting(setting) {
  settingBeingEdited = setting;
  $("setting-name").textContent = labelRole(roleID) + " / " + setting.path;
  const schema = propertySchema(setting);
  const type = schema.type || setting.type || valueType(setting.value);
  const nullable = schema.nullable === true;
  const choices = Array.isArray(schema.enum) ? schema.enum : [];
  let input;
  let format;
  if (choices.length || type === "boolean") {
    input = element("select");
    for (const choice of choices.length ? choices : [true,false]) selectOption(input,String(choice),String(choice));
    format = type === "boolean" ? "boolean" : "string";
  } else if (type === "string") {
    input = element("input"); input.type = "text"; format = "string";
  } else if (["integer","number"].includes(type)) {
    input = element("input"); input.type = "number"; input.step = type === "integer" ? "1" : "any";
    if (schema.nonnegative) input.min = "0";
    format = "number";
  } else {
    input = element("textarea"); input.className = "setting-input"; input.spellcheck = false; format = "json";
  }
  input.id = "setting-value";
  input.dataset.format = format;
  const raw = setting.value;
  if (raw !== null && raw !== undefined) input.value = format === "json" ? JSON.stringify(raw,null,2) : String(raw);
  else if (format === "json") input.value = "null";
  $("typed-control").replaceChildren(input);
  $("setting-null-label").hidden = !nullable;
  $("setting-null").checked = nullable && raw === null;
  input.disabled = $("setting-null").checked;
  $("setting-help").textContent = format === "json" ? "Use JSON for this value. Staging edits the repository’s inline settings." : "Staging writes an inline repository override. Personal settings may take precedence.";
  const {source,history} = setting;
  $("inherit-setting").disabled = !(source?.file === repositoryFile || history?.some((item) => item.source?.file === repositoryFile));
  notice("setting-message");
  $("setting-dialog").showModal();
  (input.disabled ? $("setting-null") : input).focus();
}
$("setting-null").addEventListener("change",() => { $("setting-value").disabled = $("setting-null").checked; });

function settingInputValue() {
  const input = $("setting-value");
  if (!$("setting-null-label").hidden && $("setting-null").checked) return null;
  if (input.dataset.format === "number") {
    if (!input.value || !input.validity.valid) throw new Error("Enter a valid number for this field.");
    return input.valueAsNumber;
  }
  if (input.dataset.format === "boolean") return input.value === "true";
  if (input.dataset.format === "string") return input.value;
  return JSON.parse(input.value);
}

$("close-setting").addEventListener("click",() => $("setting-dialog").close());
$("setting-dialog").addEventListener("close",() => { settingBeingEdited = null; });

function localDraft(path) {
  const saved = drafts.get(path);
  const item = file(path);
  return saved || { content: item?.content ?? "", base_digest: item?.digest ?? "" };
}
function putDraft(path, content, baseDigest, providers) {
  const item = file(path);
  if (item && item.digest === baseDigest && content === item.content) drafts.delete(path);
  else drafts.set(path, { content, base_digest: baseDigest, providers });
}
async function compose(operation, setting, value) {
  const item = file(repositoryFile);
  if (!item?.editable) throw new Error("The repository configuration is read-only.");
  const draft = localDraft(repositoryFile);
  if (draft.base_digest !== item.digest) throw new Error("The base file changed. Review the current checkout in Configuration.");
  const result = await request("/api/compose", {
    path: repositoryFile, content: draft.content, base_digest: draft.base_digest,
    role: roleID, operation, setting, value
  });
  if (localDraft(repositoryFile).content !== draft.content) throw new Error("The candidate changed. Stage the value again.");
  if (typeof result.content !== "string") throw new Error("The server returned no editable source.");
  putDraft(repositoryFile,result.content,draft.base_digest,result.providers);
  filePath = repositoryFile;
  invalidateReview();
  renderEditor();
  go("configuration");
  notice("edit-message",result.warning || "Change staged. You can edit more provider settings or preview this change.",Boolean(result.warning));
}
$("select-provider").addEventListener("submit",async (event) => {
  event.preventDefault();
  $("stage-provider").disabled = true;
  try { await compose("select","",$("provider-choice").value); }
  catch (e) { notice("compose-message",e.message,true); }
  finally { windowChoice(); }
});
async function stageSetting(operation) {
  const setting = settingBeingEdited;
  if (!setting) return;
  const revert = $("inherit-setting");
  const wasDisabled = revert.disabled;
  $("stage-setting").disabled = true;
  revert.disabled = true;
  try {
    const value = operation === "remove" ? null : settingInputValue();
    await compose(operation,setting.path,value);
    $("setting-dialog").close();
  } catch (e) { notice("setting-message",e instanceof SyntaxError ? "Enter a valid JSON value." : e.message,true); }
  finally { $("stage-setting").disabled = false; revert.disabled = wasDisabled; }
}
$("setting-form").addEventListener("submit",(event) => { event.preventDefault(); stageSetting("set"); });
$("inherit-setting").addEventListener("click",() => stageSetting("remove"));

function invalidateReview() {
  previewSequence++;
  review = null;
  $("review").hidden = true;
  $("published").hidden = true;
  notice("edit-message");
  notice("publish-message");
}

function renderEditor() {
  const list = files();
  if (!file()) filePath = list.find((item) => item.path === repositoryFile)?.path || list[0]?.path || "";
  const active = file();
  const select = $("file");
  select.replaceChildren();
  for (const item of list) selectOption(select,item.path,item.path + (drafts.has(item.path) ? " · edited" : ""));
  select.value = filePath;
  select.disabled = !active || publishing;
  const errors = problems();
  notice("config-error",errors,true);
  const draft = localDraft(filePath);
  $("source").value = draft.content;
  const hasCore = filePath === repositoryFile && state?.config?.core;
  $("core-details").hidden = !hasCore;
  $("core-values").textContent = hasCore ? JSON.stringify(state.config.core,null,2) : "";
  $("source").readOnly = !active?.editable || publishing;
  const reason = active?.reason || (!active ? "Run devflow init in this checkout to create repository configuration, then refresh." : "");
  $("file-reason").textContent = reason;
  $("file-reason").hidden = !reason;
  $("edit-controls").hidden = !active?.editable;
  refreshEditorControls();
}

function refreshEditorControls() {
  const active = file();
  const draft = localDraft(filePath);
  const changed = drafts.has(filePath);
  const stale = changed && active && active.digest !== draft.base_digest;
  $("draft-state").textContent = !active ? "Unavailable" : stale ? "Checkout changed" : changed ? "Edited" : active.editable ? "Current" : "Read-only";
  $("preview").disabled = !active?.editable || !changed || stale || publishing || !$("commit-title").value.trim();
  $("discard").disabled = !changed || publishing;
  if (stale) notice("edit-message","The checkout changed after you began editing. Copy your edits, then discard this candidate to load the new source.",true);
}

$("file").addEventListener("change",(event) => {
  filePath = event.target.value;
  invalidateReview();
  renderEditor();
});
$("source").addEventListener("input",() => {
  const draft = localDraft(filePath);
  putDraft(filePath,$("source").value,draft.base_digest);
  invalidateReview();
  refreshEditorControls();
});
for (const id of ["commit-title","task-ref","delivery"]) {
  $(id).addEventListener("input",() => { invalidateReview(); refreshEditorControls(); });
}
$("discard").addEventListener("click",() => {
  if (!window.confirm("Discard your staged edits to " + filePath + "?")) return;
  drafts.delete(filePath);
  invalidateReview();
  renderEditor();
});

function candidate() {
  const { content, base_digest } = localDraft(filePath);
  return { path: filePath, content, base_digest, title: $("commit-title").value.trim(), task_ref: $("task-ref").value.trim(), draft: $("delivery").value === "draft" };
}

function renderReview(data, intent) {
  const changes = data.changes || data.plan?.changes || [];
  const list = $("change-list");
  list.replaceChildren();
  for (const change of changes) {
    const row = element("div","","change");
    const key = [change.role && labelRole(change.role),change.path].filter(Boolean).join(" / ");
    row.append(element("strong",key),element("code","Current · " + display(change.before),"removed"),element("code","Proposed · " + display(change.after),"added"));
    list.append(row);
  }
  if (!changes.length) list.append(element("p","The resolver reports no change to the effective provider values for this workstation.","muted"));
  const before = data.before ?? file()?.content ?? "";
  const after = data.after ?? intent.content;
  $("before").textContent = before;
  $("after").textContent = after;
  const patch = data.plan?.diff || "";
  $("patch").hidden = !patch;
  $("patch").open = Boolean(patch);
  $("patch-text").textContent = patch;
  $("source-diff").open = !patch && !changes.length;
  $("review-summary").textContent = intent.path + " · " + intent.title + (intent.task_ref ? " · " + intent.task_ref : "");
  if (data.destination) $("review-summary").textContent += " · " + data.destination;
  const notes = $("review-messages");
  notes.replaceChildren();
  const messages = [data.note,data.ci,data.warning,data.reason,data.plan?.warning];
  for (const message of messages) if (typeof message === "string" && message) notes.append(element("p",message,"notice"));
  $("publish").textContent = intent.draft ? "Confirm and open draft PR" : "Confirm and apply to default branch";
  $("publish").disabled = data.publishable === false || !data.approval || publishing;
  if (!data.approval && !notes.children.length) notes.append(element("p","Publication is unavailable for this preview.","notice"));
  $("review").hidden = false;
  $("review-title").focus();
}

$("preview").addEventListener("click",async () => {
  const sequence = ++previewSequence;
  const intent = candidate();
  review = null;
  $("review").hidden = true;
  $("published").hidden = true;
  $("preview").disabled = true;
  notice("edit-message","Validating the candidate…");
  try {
    const data = await request("/api/preview",intent);
    if (sequence !== previewSequence) return;
    if (intent.path === repositoryFile && drafts.has(repositoryFile)) {
      drafts.get(repositoryFile).providers = data.plan?.providers;
    }
    review = { data, intent };
    notice("edit-message");
    renderReview(data,intent);
  } catch (e) {
    if (sequence === previewSequence) notice("edit-message",e.message,true);
  } finally { refreshEditorControls(); }
});
$("download-patch").addEventListener("click",() => {
  const patch = review?.data.plan?.diff;
  if (!patch) return;
  const link = document.createElement("a");
  const url = URL.createObjectURL(new Blob([patch],{type:"text/x-diff;charset=utf-8"}));
  link.href = url;
  link.download = "devflow-config.patch";
  link.click();
  setTimeout(() => URL.revokeObjectURL(url),1000);
});
$("back-to-edit").addEventListener("click",() => { invalidateReview(); $("source").focus(); });

function setPublishing(busy) {
  publishing = busy;
  $("source").readOnly = busy || !file()?.editable;
  for (const key of ["file","commit-title","task-ref","delivery","publish","back-to-edit","refresh"]) $(key).disabled = busy;
  refreshEditorControls();
}

$("publish").addEventListener("click",async () => {
  if (!review || publishing) return;
  const current = review;
  setPublishing(true);
  notice("publish-message","Publishing the reviewed change…");
  try {
    const result = await request("/api/publish",{ ...current.intent, approval: current.data.approval });
    const receipt = $("published");
    receipt.replaceChildren(element("strong",result.state || "Submitted"));
    if (result.branch) receipt.append(document.createTextNode(" · " + result.branch));
    const link = result.url && outbound("View pull request",result.url);
    if (link) receipt.append(document.createTextNode(" · "),link);
    if (result.ci) receipt.append(element("p",result.ci));
    if (result.warning) receipt.append(element("p",result.warning));
    if (localDraft(current.intent.path).content === current.intent.content) drafts.delete(current.intent.path);
    review = null;
    $("review").hidden = true;
    renderEditor();
    receipt.hidden = false;
  } catch (e) {
    notice("publish-message",e.message + " Preview the current candidate again before retrying.",true);
    review = null;
  } finally {
    setPublishing(false);
    $("publish").disabled = !review;
  }
});

window.addEventListener("beforeunload",(event) => {
  if (drafts.size) { event.preventDefault(); event.returnValue = ""; }
});

async function start() {
  showPage();
  $("refresh").disabled = true;
  $("activity").disabled = true;
  $("copy-handoff").disabled = true;
  try {
    await authenticate();
    await loadState();
    $("activity").disabled = !state;
  } catch (e) {
    notice("status",e.message,true);
    $("top-repo").textContent = "Session unavailable";
    $("working-state").textContent = "Unavailable";
  }
}
start();
