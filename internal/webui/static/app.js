"use strict";
const $ = (id) => document.getElementById(id);
const repositoryFile = ".devflow/config.yml";
const drafts = new Map();
const sessionKey = "devflow.session:" + location.pathname;
const commanderKey = "devflow.commander:" + location.pathname;
const reconnectCommander = "Open this worktree again from its Agent Commander session to see worker requests or send a reply.";
let commanderContext = "";
let commanderEpoch = 0;
try { commanderContext = sessionStorage.getItem(commanderKey) || ""; } catch { /* A fresh launch still works in memory. */ }
const savedDraftKey = "devflow.drafts:" + location.pathname;
const rememberKey = "devflow.browser"; // localStorage is isolated by clone origin.
let memoryGrant = "";
let resumeAttempt;
let leavingForWorktree = false;
let rememberedModal;
let modalContext;
let pendingProviderChoice;
let saveTimer;
let loadSequence = 0;
const screenNames = {overview: "repository overview", providers: "provider editor", configuration: "configuration editor", handoff: "handoff screen"};
const views = Object.keys(screenNames);
const roleNames = {
  code_hosting: "Code hosting", task_management: "Task management", knowledge_base: "Knowledge base",
  "devops.checks": "Checks", "devops.deployments": "Deployments", "devops.releases": "Releases",
  secops: "Security operations", notifications: "Notifications"
};
let session = "";
let authAttempt = 0;
let state = null;
let activity = null;
let roleID = "";
let filePath = "";
let review = null;
let settingBeingEdited = null;
let settingEditor = null;
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

function rememberGrant() {
  try { return localStorage.getItem(rememberKey) || ""; } catch { return memoryGrant; }
}

// Worker questions, reply approvals, and their credential belong to the
// launching Commander session. They never enter remembered browser grants or drafts.
function clearCommanderActivity() {
  commanderEpoch++;
  activity = null;
  if (state) renderActivity();
}

function setCommanderContext(value) {
  const previous = commanderContext;
  commanderContext = typeof value === "string" ? value : "";
  try {
    if (commanderContext) sessionStorage.setItem(commanderKey,commanderContext);
    else sessionStorage.removeItem(commanderKey);
  } catch { /* Use the context only in this page. */ }
  clearCommanderActivity();
  if (commanderContext !== previous && state && !$("console-shell").hidden) {
    tabs.stop();
    if (session) tabs.start();
  }
}

function acceptSession(data, freshLaunch = false) {
  if (typeof data.session !== "string" || !data.session) throw new Error("The local console could not start this session.");
  if (freshLaunch) tabs.stop();
  session = data.session;
  if (freshLaunch) setCommanderContext(data.commander);
  try { sessionStorage.setItem(sessionKey,session); } catch { /* Keep this worktree session in memory. */ }
  if (typeof data.remember === "string" && data.remember) {
    memoryGrant = data.remember;
    try { localStorage.setItem(rememberKey,memoryGrant); } catch { /* Use the grant in this tab. */ }
  }
}

async function resumeSession(previous) {
  if (session && session !== previous) return true;
  if (resumeAttempt) return resumeAttempt;
  const grant = rememberGrant();
  if (!grant) return false;
  resumeAttempt = (async () => {
    try {
      const result = await request("api/resume",{remember:grant});
      if (session === previous) acceptSession(result);
      return Boolean(session);
    } catch (error) {
      if (error.status === 401 && rememberGrant() === grant) {
        memoryGrant = "";
        try { localStorage.removeItem(rememberKey); } catch { /* Memory was cleared. */ }
      }
      return false;
    } finally { resumeAttempt = null; }
  })();
  return resumeAttempt;
}

function expireSession(previous) {
  if (session !== previous) return;
  session = "";
  setCommanderContext("");
  try { sessionStorage.removeItem(sessionKey); } catch { /* Memory was cleared. */ }
  showConnection("Your local console session has ended.");
}

async function request(path, body, retried = false) {
  const headers = { Accept: "application/json" };
  const credential = session;
  const bootstrap = path === "api/session" || path === "api/resume";
  const commanderRequest = path === "api/activity" || path === "api/reply";
  const epoch = commanderEpoch;
  if (path === "api/reply" && !commanderContext) throw new Error(reconnectCommander);
  if (commanderRequest && commanderContext) headers["X-Devflow-Commander"] = commanderContext;
  if (credential && !bootstrap) headers.Authorization = "Bearer " + credential;
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
  if (commanderRequest) {
    if (epoch !== commanderEpoch) throw new Error("The Commander session changed. Check activity again.");
    if (data.commander_reconnect === true) {
      setCommanderContext("");
      if (path === "api/reply") {
        $("activity-note").textContent = data.error || reconnectCommander;
        throw new Error(data.error || reconnectCommander);
      }
    }
  }
  if (!response.ok) {
    if (response.status === 401) {
      const error = new Error(path === "api/session" ? "This one-time link was already used or has expired." : "Your local console session has ended.");
      error.status = response.status;
      if (!bootstrap) {
        if (!retried && await resumeSession(credential)) {
          if (commanderRequest && epoch !== commanderEpoch) throw new Error("The Commander session changed. Check activity again.");
          return request(path,body,true);
        }
        expireSession(credential);
      }
      throw error;
    }
    throw new Error(data.error || "Request failed (" + response.status + ").");
  }
  return data;
}

// The launch credential lives only in the fragment and is removed before exchange.
// Only known destinations can become a route, a chat request or a terminal command.
function destination() {
  const hash = location.hash.slice(1);
  const split = hash.indexOf("?");
  const launchParams = new URLSearchParams(hash);
  const launching = launchParams.has("launch");
  const params = launching ? launchParams : new URLSearchParams(split < 0 ? "" : hash.slice(split + 1));
  const screen = launching ? params.get("screen") : split < 0 ? hash : hash.slice(0,split);
  const role = params.get("role");
  return {
    screen: views.includes(screen) ? screen : "overview",
    role: screen === "providers" && Object.hasOwn(roleNames,role) ? role : "",
    launch: launching ? params.get("launch") : null
  };
}

function route(screen, role) {
  return "#" + screen + (role ? "?role=" + encodeURIComponent(role) : "");
}

function updateConnectionOptions() {
  const {screen,role} = destination();
  const name = role ? labelRole(role).toLowerCase() + " provider editor" : screenNames[screen];
  $("chat-prompt").textContent = "Open the devflow " + name + " for this repository.";
  const resume = drafts.size > 0;
  $("reconnect-command").textContent = "devflow ui --screen " + screen + (role ? " --role " + role : "");
  $("terminal-instructions").textContent = resume
    ? "Run this command in the same worktree to resume its saved edits."
    : "Run this command in the worktree you want to manage.";
  $("reconnect-drafts").hidden = !resume;
  const downloads = $("draft-downloads");
  downloads.replaceChildren();
  for (const [path,draft] of drafts) {
    const button = element("button","Download " + path,"button secondary small wrap");
    button.type = "button";
    button.addEventListener("click",() => downloadText(path.split("/").pop(),draft.content));
    downloads.append(button);
  }
}

function showConnection(message) {
  const leavingConsole = !$("console-shell").hidden;
  if (!$("console-shell").hidden) {
    storeDrafts();
    if ($("setting-dialog").open) rememberedModal = modalSnapshot();
  }
  tabs.stop();
  $("console-shell").hidden = true;
  if ($("setting-dialog").open) $("setting-dialog").close();

  $("connection-title").textContent = "Reconnect to devflow";
  $("connection-message").textContent = message;
  $("connection-options").hidden = false;
  $("connection-retry").hidden = !session;
  $("connection").hidden = false;
  document.title = "Reconnect · devflow";
  updateConnectionOptions();
  if (leavingConsole) $("connection-title").focus();
}

async function copyText(node, status, success) {
  try {
    await navigator.clipboard.writeText(node.textContent);
    status.textContent = success;
  } catch {
    const range = document.createRange();
    range.selectNodeContents(node);
    const selection = getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    status.textContent = "Text selected. Use your browser’s Copy command.";
  }
}

$("copy-prompt").addEventListener("click",() => copyText($("chat-prompt"),$("connection-feedback"),"Chat request copied."));
$("copy-command").addEventListener("click",() => copyText($("reconnect-command"),$("connection-feedback"),"Command copied."));
$("connection-retry").addEventListener("click",loadState);

async function connect() {
  const attempt = ++authAttempt;
  const {screen,role,launch} = destination();
  if (!session) {
    try { session = sessionStorage.getItem(sessionKey) || ""; } catch { /* A launch also works without storage. */ }
  }
  let reason = "Open a local session for this worktree to continue.";
  if (launch !== null) {
    history.replaceState(null,"",location.pathname + location.search + route(screen,role));
    try {
      const result = await request("api/session",{launch});
      if (attempt !== authAttempt) return;
      acceptSession(result,true);
    } catch (e) {
      if (attempt !== authAttempt) return;
      if (e.status !== 401) { showConnection(e.message); return; }
      // A bookmarked or spent link may still have a valid browser grant.
      reason = e.message;
    }
  }
  if (!session) await resumeSession("");
  if (attempt !== authAttempt) return;
  if (session) await loadState();
  else showConnection(reason);
}

function showPage(focus = false) {
  const {screen,role} = destination();
  if ($("console-shell").hidden) { updateConnectionOptions(); return; }
  if (screen === "providers") {
    if (role && role !== roleID) { roleID = role; $("setting-filter").value = ""; }
    renderProviders();
    if (pendingProviderChoice?.role === roleID) {
      const select=$("provider-choice");
      if ([...select.options].some(o => o.value === pendingProviderChoice.choice)) {
        select.value = pendingProviderChoice.choice;
        windowChoice();
      }
      pendingProviderChoice = null;
    }
    if (rememberedModal?.role === roleID && !$("setting-dialog").open) {
      const previous=rememberedModal;
      openSetting(previous.setting,previous);
    }
  }
  for (const name of views) $("page-" + name).hidden = screen !== name;
  for (const link of document.querySelectorAll("[data-page]")) {
    if (link.dataset.page === screen) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  if (screen === "configuration" && state) renderEditor();
  if (focus) $("main").focus();
}

function go(screen, role = "") {
  const hash = route(screen,role);
  if (location.hash === hash) showPage(true);
  else location.hash = hash;
}
window.addEventListener("hashchange", () => {
  if (destination().launch !== null) connect();
  else showPage(true);
});
document.querySelector(".skip").addEventListener("click",event => { event.preventDefault(); $("main").focus(); });

document.addEventListener("input",event => {
  const editor = event.target.closest(".te-editor");
  if (editor) editor.dataset.modified = "true";
});

// Store only editable browser state. Each clone has its own origin and each
// worktree has its own key. Credentials and review approvals live elsewhere.
function modalSnapshot() {
  if (!$("setting-dialog").open || !settingEditor || !modalContext) return rememberedModal;
  return {...modalContext,raw:settingEditor.snapshot(),message:$("setting-message").textContent};
}

function storeDrafts() {
  if (!state || $("console-shell").hidden) return true;
  const raw = {root:state.root,files:Array.from(drafts),filePath,role:roleID,roleFilter:$("setting-filter").value,
    providerChoice:pendingProviderChoice?.choice || $("provider-choice").value,screen:destination().screen,modal:modalSnapshot(),
    document:window.snapshotDocumentEditor(),title:$("commit-title").value,task:$("task-ref").value,delivery:$("delivery").value};
  try {
    localStorage.setItem(savedDraftKey,JSON.stringify(raw));
    $("workspace-save-error").hidden = true;
    $("setting-storage-error").hidden = true;
    return true;
  } catch {
    $("workspace-save-error").hidden = false;
    return false;
  }
}

function scheduleSave() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(storeDrafts,120);
}
for (const type of ["input","change","click","keyup","select","focusout"]) document.addEventListener(type,scheduleSave);
window.addEventListener("hashchange",scheduleSave);
document.addEventListener("visibilitychange",() => { if (document.hidden) storeDrafts(); });
window.addEventListener("pagehide",() => { if (!leavingForWorktree) storeDrafts(); });

function restoreDrafts(next) {
  try {
    const saved = JSON.parse(localStorage.getItem(savedDraftKey));
    if (!saved || saved.root !== next.root || !Array.isArray(saved.files)) return;
    for (const row of saved.files) {
      if (Array.isArray(row) && typeof row[0] === "string" && typeof row[1]?.content === "string" && typeof row[1]?.base_digest === "string") drafts.set(row[0],row[1]);
    }
    if (typeof saved.filePath === "string") filePath = saved.filePath;
    if (Object.hasOwn(roleNames,saved.role)) roleID = saved.role;
    if (typeof saved.roleFilter === "string") $("setting-filter").value = saved.roleFilter;
    if (typeof saved.providerChoice === "string") pendingProviderChoice = {role:roleID,choice:saved.providerChoice};
    if (typeof saved.title === "string") $("commit-title").value = saved.title;
    if (typeof saved.task === "string") $("task-ref").value = saved.task;
    if (["apply","draft"].includes(saved.delivery)) $("delivery").value = saved.delivery;
    if (saved.modal && Object.hasOwn(roleNames,saved.modal.role) && saved.modal.setting?.path) rememberedModal = saved.modal;
    window.restoreDocumentEditor(saved.document);
    if (!location.hash && views.includes(saved.screen)) history.replaceState(null,"",route(saved.screen,saved.screen==="providers" ? roleID : ""));
  } catch { /* An invalid or unavailable local draft cannot authorize a write. */ }
}

function hideSettingForNavigation() {
  if ($("setting-dialog").open) {
    rememberedModal = modalSnapshot();
    $("setting-dialog").close();
  }
}

const tabs = consoleTabs({
  credential: () => session, commander: () => commanderContext, request,
  async unauthorized(previous) {
    if (await resumeSession(previous)) return true;
    expireSession(previous);
    return false;
  },
  navigate({screen,role="",target}) {
    if (!views.includes(screen) || (role && (screen !== "providers" || !Object.hasOwn(roleNames,role)))) return null;
    let next;
    if (target) {
      try { next = new URL(target); } catch { return null; }
      if (next.origin !== location.origin || next.username || next.password || next.search || !/^\/w\/[a-z0-9][a-z0-9-]{0,62}\/$/.test(next.pathname)) return null;
    }
    window.focus();
    clearCommanderActivity();
    const current = destination();
    if (!next && current.screen === screen && (!role || roleID === role)) return {handled:true};
    if (publishing) {
      notice("status","A publication is in progress. Request the editor again once it finishes.");
      return {blocked:"publishing"};
    }
    if (next && !storeDrafts()) {
      if ($("setting-dialog").open) notice("setting-storage-error",$("workspace-save-error").textContent,true);
      return {blocked:"storage"};
    }
    return {handled:true,perform() {
      if (next) {
        leavingForWorktree = true;
        location.assign(next.href);
      } else {
        hideSettingForNavigation();
        go(screen,role);
      }
    }};
  }
});
window.addEventListener("pagehide",() => tabs.stop());
window.addEventListener("pageshow",event => {
  leavingForWorktree = false;
  if (!$("console-shell").hidden) {
    if (event.persisted) {
      clearCommanderActivity();
      loadState();
    } else tabs.start();
  }
});

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
    button.addEventListener("click", () => chooseRole(item.role));
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
    if ((entry.run_id || entry.request_id) && !commanderContext) continue;
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
  const revision = ++loadSequence;
  try {
    const next = await request("api/state");
    if (!session || revision !== loadSequence) return;
    if (state && drafts.size && state.root !== next.root) {
      showConnection("This address now serves a different worktree. Reopen the console from the worktree where you started editing.");
      return;
    }
    if (!state) restoreDrafts(next);
    state = next;
    activity = null;
    connectionSequence++;
    $("check-connections").disabled = !next.config?.providers;
    $("connection-results").hidden = true;
    $("connection-list").replaceChildren();
    $("connection-status").textContent = "Check the providers selected in the configuration on disk.";
    invalidateReview();
    $("activity").disabled = false;
    const reconnecting = !$("connection-options").hidden && $("console-shell").hidden;
    renderOverview();
    $("connection").hidden = true;
    $("connection-options").hidden = true;
    $("connection-feedback").textContent = "";
    $("console-shell").hidden = false;
    showPage(reconnecting);
    tabs.start();
    notice("status");
  } catch (e) {
    if (!session || revision !== loadSequence) return;
    if ($("console-shell").hidden) showConnection(e.message);
    else notice("status",e.message,true);
  }
  finally { $("refresh").disabled = !session; }
}
$("refresh").addEventListener("click", loadState);

async function loadActivity() {
  $("activity").disabled = true;
  $("activity-note").textContent = "Checking repository activity…";
  try { activity = await request("api/activity"); renderActivity(); }
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
      const result = await request("api/reply",body);
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

$("copy-handoff").addEventListener("click",() => {
  $("copy-message").hidden = false;
  return copyText($("handoff-text"),$("copy-message"),"Copied the repository brief.");
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
    const checks = await request("api/readiness", {});
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
  go("providers",id);
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
    row.append(head,settingSummary(setting.value),element("p",origin,"source-line"));
    appendHistory(row,setting);
    target.append(row);
  }
  if (!target.children.length) target.append(element("p",filter ? "No matching settings." : "This role has no settings to display.","muted"));
}

function settingSummary(value) {
  if (Array.isArray(value)) {
    if (!value.length) return element("div","Empty list","value muted");
    const list = element("div","","collection-values");
    for (const item of value.slice(0,8)) list.append(element("code", display(item)));
    if (value.length > 8) list.append(element("span", "+" + (value.length - 8) + " more","muted"));
    return list;
  }
  if (value && typeof value === "object") {
    const count = Object.keys(value).length;
    return element("div", count + (count === 1 ? " entry" : " entries"), "value muted");
  }
  return element("div",display(value),"value");
}
$("setting-filter").addEventListener("input",renderSettings);

function settingPath(path) {
  return (path.match(/(?:\\.|[^.\\])+/g) || []).map(part => part.replace(/\\(.)/g, "$1"));
}

function propertySchema(setting) {
  let schema = state?.config?.schemas?.[roleID]?.[role()?.selected];
  for (const key of settingPath(setting.path)) {
    schema = schema?.properties && Object.hasOwn(schema.properties,key)
      ? schema.properties[key] : schema?.additional_properties;
  }
  return schema || {};
}

function editorSuggestions(path) {
  const key = path.filter(part => typeof part === "string").at(-1);
  const git = state?.completions || {};
  switch (key) {
    case "branch":
    case "protected_branches":
      return [state?.branch,...(git.branches || [])].filter(Boolean);
    case "source_remote":
    case "target_remote":
      return git.remotes || [];
    case "repository":
      return git.repositories || [];
    case "catalog":
      return files().filter(f => f.kind === "Task templates").map(f => f.path);
    case "flow_map":
      return files().filter(f => f.kind === "Flow map").map(f => f.path);
  }
  if (path.includes("routes") && typeof path.at(-1) === "number" && path.includes("notifications")) {
    const routeRole = roles().find(item => item.role === "notifications");
    const destinations = routeRole?.settings?.find(item => item.path === "destinations")?.value;
    return Object.keys(destinations || {});
  }
  return [];
}

function openSetting(setting, previous) {
  settingBeingEdited = setting;
  const schema = previous?.schema || propertySchema(setting);
  const base = file(repositoryFile)?.digest;
  const stale = previous && (previous.base !== base || previous.provider !== role()?.selected || !role()?.settings?.some(s => s.path === setting.path));
  modalContext = previous || {role:roleID,provider:role()?.selected,setting,schema,base};
  rememberedModal = undefined;
  const inline = setting.history?.find(item => item.step === "inline");
  // Map overrides merge by key. Start with what the team file actually owns,
  // so editing one entry cannot copy personal or inherited entries into Git.
  let value = inline ? inline.value : setting.value;
  const isMap = schema.type === "object" && Boolean(schema.additional_properties);
  if (isMap && !inline) value = {};
  $("setting-dialog").toggleAttribute("data-collection", schema.type === "array" || schema.type === "object");
  const parts = [...roleID.split("."), "settings", ...settingPath(setting.path)];
  settingEditor = createTypedEditor($("typed-control"), schema, value, {
    label: setting.path, path: parts, suggestions: editorSuggestions, allowed: setting.constraint?.allow, restore:previous?.raw
  });
  $("setting-name").textContent = labelRole(roleID) + " / " + setting.path;
  const help = isMap
    ? "Edit the keys owned by this repository. Removing a key restores any inherited value."
    : "Stage a repository override, then preview its effective value before publishing.";
  $("setting-help").textContent = help + (setting.source?.layer === "user" ? " A personal definition overrides this setting on your machine." : "");
  $("stage-setting").disabled = Boolean(stale);
  $("inherit-setting").disabled = !inline || Boolean(stale);
  notice("setting-message",stale ? "The checkout changed since this edit began. Copy your input, then close and reopen the field to edit the current value." : (previous?.message || ""),Boolean(stale || previous?.message));
  $("setting-dialog").showModal();
  $("setting-dialog-title").focus();
}

$("close-setting").addEventListener("click",() => $("setting-dialog").close());
$("setting-dialog").addEventListener("close",() => { settingBeingEdited = null; settingEditor = null; modalContext = null; scheduleSave(); });

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
  const result = await request("api/compose", {
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
  if (!setting || $("stage-setting").disabled) return;
  const revert = $("inherit-setting");
  const wasDisabled = revert.disabled;
  $("stage-setting").disabled = true;
  revert.disabled = true;
  try {
    const value = operation === "remove" ? null : settingEditor.read();
    if (value === undefined) throw new Error("Choose a value to stage.");
    await compose(operation,setting.path,value);
    $("setting-dialog").close();
  } catch (e) { notice("setting-message",e.message,true); }
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
  updateDocumentEditor();
}

function updateDocumentEditor() {
  if ($("page-configuration").hidden) return;
  const path = filePath;
  const draft = localDraft(path);
  renderDocumentEditor({
    file: file(), draft, disabled: publishing || draft.base_digest !== file()?.digest,
    request, suggestions: editorSuggestions,
    openProvider: chooseRole,
    stage(result) {
      const current = localDraft(path);
      if (publishing || path !== filePath || current.base_digest !== draft.base_digest || current.content !== draft.content) {
        throw new Error("The draft changed. Reopen the field to apply this edit.");
      }
      putDraft(path,result.content,draft.base_digest,result.providers);
      invalidateReview();
      renderEditor();
      notice("edit-message",result.warning || "Field staged. Preview the change before publishing.",Boolean(result.warning));
    }
  });
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
  updateDocumentEditor();
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
    const data = await request("api/preview",intent);
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
function downloadText(name, text, type = "text/plain;charset=utf-8") {
  const link = document.createElement("a");
  const url = URL.createObjectURL(new Blob([text],{type}));
  link.href = url;
  link.download = name;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url),1000);
}
$("download-patch").addEventListener("click",() => {
  const patch = review?.data.plan?.diff;
  if (patch) downloadText("devflow-config.patch",patch,"text/x-diff;charset=utf-8");
});
$("back-to-edit").addEventListener("click",() => { invalidateReview(); documentEditorFocus(); });

function setPublishing(busy) {
  publishing = busy;
  $("source").readOnly = busy || !file()?.editable;
  for (const key of ["file","commit-title","task-ref","delivery","publish","back-to-edit","refresh"]) $(key).disabled = busy;
  refreshEditorControls();
  updateDocumentEditor();
}

$("publish").addEventListener("click",async () => {
  if (!review || publishing) return;
  const current = review;
  setPublishing(true);
  notice("publish-message","Publishing the reviewed change…");
  try {
    const result = await request("api/publish",{ ...current.intent, approval: current.data.approval });
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

function hasPendingEdits() {
  return drafts.size || $("setting-dialog").open || $("document-control").querySelector("[data-modified]") || $("commit-title").value !== $("commit-title").defaultValue || $("task-ref").value || $("delivery").value === "draft";
}
window.addEventListener("beforeunload",(event) => {
  if (!leavingForWorktree && !storeDrafts() && hasPendingEdits()) { event.preventDefault(); event.returnValue = ""; }
});

$("refresh").disabled = true;
$("activity").disabled = true;
$("copy-handoff").disabled = true;
connect();
