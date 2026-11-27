"use strict";
const $ = (id) => document.getElementById(id);
const repositoryFile = ".agentflow/config.yml";
const drafts = new Map();
const sessionKey = "agentflow.session:" + location.pathname;
const commanderKey = "agentflow.commander:" + location.pathname;
const reconnectCommander = "Open this worktree again from its Agent Commander session to see worker requests or send a reply.";
let commanderContext = "";
let commanderEpoch = 0;
try { commanderContext = sessionStorage.getItem(commanderKey) || ""; } catch { /* A fresh launch still works in memory. */ }
const savedDraftKey = "agentflow.drafts:" + location.pathname;
const rememberKey = "agentflow.browser"; // localStorage is isolated by clone origin.
let memoryGrant = "";
let resumeAttempt;
let leavingForWorktree = false;
let saveTimer;
let loadSequence = 0;
// Providers and Autonomy are groups of the Settings page. Their screen names
// stay, so agentflow ui --screen providers and saved links keep working.
const screenNames = {overview: "repository overview", settings: "settings", providers: "provider settings", autonomy: "autonomy settings", plugins: "plugins page", configuration: "configuration editor", handoff: "handoff screen"};
const views = Object.keys(screenNames);
const pages = {overview: "overview", settings: "settings", providers: "settings", autonomy: "settings", plugins: "plugins", configuration: "configuration", handoff: "handoff"};
const settingsStore = "agentflow.settings:" + location.pathname;
const roleNames = {
  code_hosting: "Code hosting", task_management: "Task management", knowledge_base: "Knowledge base",
  "devops.checks": "Checks", "devops.deployments": "Deployments", "devops.releases": "Releases",
  secops: "Security operations", notifications: "Notifications"
};
let session = "";
let authAttempt = 0;
let state = null;
let activity = null;
let commanderNote = "";
let filePath = "";
let review = null;
let publishing = false;
let previewSequence = 0;
let connectionSequence = 0;
const commonActions = {
  commit: "Create a local commit", push: "Push a branch", pr_open_draft: "Open a draft pull request",
  pr_open: "Open a ready pull request", pr_ready_for_review: "Mark a pull request ready",
  ticket_create: "Create a task", review_reply: "Reply to a review"
};

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
function display(value) {
  if (value === null) return "null";
  if (value === undefined) return "Unset";
  if (typeof value === "string") return value === "" ? '""' : value;
  return JSON.stringify(value);
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

// Worker requests are the one part of the overview the event stream does not
// carry yet. They load with the checkout and again on Resync, and only for a
// console that an Agent Commander session opened.
async function loadCommanderActivity() {
  if (!commanderContext) return;
  try { activity = await request("api/activity"); }
  catch (e) { commanderNote = e.message; }
  if (state) renderActivity();
}

function setCommanderContext(value) {
  const previous = commanderContext;
  commanderContext = typeof value === "string" ? value : "";
  if (commanderContext) commanderNote = "";
  try {
    if (commanderContext) sessionStorage.setItem(commanderKey,commanderContext);
    else sessionStorage.removeItem(commanderKey);
  } catch { /* Use the context only in this page. */ }
  clearCommanderActivity();
  // The console scopes a tab by the capability it attached with.
  if (commanderContext !== previous && state && !$("console-shell").hidden && session) tabs.refresh();
}

function acceptSession(data, freshLaunch = false) {
  if (typeof data.session !== "string" || !data.session) throw new Error("The local console could not start this session.");
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
  if (commanderRequest && commanderContext) headers["X-Agentflow-Commander"] = commanderContext;
  if (credential && !bootstrap) headers.Authorization = "Bearer " + credential;
  const options = { headers, cache: "no-store", credentials: "omit", redirect: "error" };
  if (body !== undefined) {
    options.method = "POST";
    headers["Content-Type"] = "application/json";
    headers["X-Agentflow-Request"] = "console";
    options.body = JSON.stringify(body);
  }
  let response;
  try { response = await fetch(path, options); }
  catch { throw new Error("Cannot reach the local agentflow process. Check that agentflow ui is still running."); }
  const data = await response.json().catch(() => ({}));
  if (commanderRequest) {
    if (epoch !== commanderEpoch) throw new Error("The Commander session changed. Use Resync to load its worker requests.");
    if (data.commander_reconnect === true) {
      setCommanderContext("");
      commanderNote = data.error || data.warning || reconnectCommander;
      if (state) renderActivity();
      if (path === "api/reply") throw new Error(data.error || reconnectCommander);
    }
  }
  if (!response.ok) {
    if (response.status === 401) {
      const error = new Error(path === "api/session" ? "This one-time link was already used or has expired." : "Your local console session has ended.");
      error.status = response.status;
      if (!bootstrap) {
        if (!retried && await resumeSession(credential)) {
          if (commanderRequest && epoch !== commanderEpoch) throw new Error("The Commander session changed. Use Resync to load its worker requests.");
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
  const group = params.get("group") || "";
  return {
    screen: views.includes(screen) ? screen : "overview",
    role: screen === "providers" && Object.hasOwn(roleNames,role) ? role : "",
    group: screen === "settings" && /^[a-z0-9_.-]{1,80}$/.test(group) ? group : "",
    launch: launching ? params.get("launch") : null
  };
}

function route(screen, role) {
  return "#" + screen + (role ? "?role=" + encodeURIComponent(role) : "");
}

// The Settings group a destination opens, and the address of a group. The
// providers and autonomy groups keep their own screens.
function settingsGroup({screen,role,group}) {
  if (screen === "providers") return role ? "providers." + role : "providers";
  return screen === "autonomy" ? "autonomy" : group;
}
function groupRoute(group) {
  if (group === "providers" || group === "autonomy") return "#" + group;
  if (group.startsWith("providers.") && Object.hasOwn(roleNames,group.slice(10))) return route("providers",group.slice(10));
  return "#settings" + (group ? "?group=" + encodeURIComponent(group) : "");
}

function updateConnectionOptions() {
  const {screen,role} = destination();
  const name = role ? labelRole(role).toLowerCase() + " provider editor" : screenNames[screen];
  // The command line knows the providers and autonomy screens; any other
  // Settings group reopens at the providers group.
  const reopen = screen === "settings" ? "providers" : screen;
  $("chat-prompt").textContent = "Open the agentflow " + name + " for this repository.";
  const resume = drafts.size > 0;
  $("reconnect-command").textContent = "agentflow ui --screen " + reopen + (role ? " --role " + role : "");
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
  if (!$("console-shell").hidden) storeDrafts();
  tabs.stop();
  live.stop();
  $("console-shell").hidden = true;
  if ($("settings-review").open) $("settings-review").close();

  $("connection-title").textContent = "Reconnect to agentflow";
  $("connection-message").textContent = message;
  $("connection-options").hidden = false;
  $("connection-retry").hidden = !session;
  $("connection").hidden = false;
  document.title = "Reconnect · agentflow";
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
  const where = destination();
  const {screen} = where;
  if ($("console-shell").hidden) { updateConnectionOptions(); return; }
  for (const name of new Set(Object.values(pages))) $("page-" + name).hidden = pages[screen] !== name;
  for (const link of document.querySelectorAll("[data-page]")) {
    if (link.dataset.page === screen) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  if (pages[screen] === "settings" && state) settingsPage.open(settingsGroup(where));
  pluginsPage.tick();
  if (screen === "configuration" && state) renderEditor();
  if (focus) {
    $("main").focus({preventScroll: true});
    window.scrollTo(0, 0);
  }
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
function storeDrafts() {
  if (!state || $("console-shell").hidden) return true;
  const raw = {root:state.root,files:Array.from(drafts),filePath,screen:destination().screen,
    document:window.snapshotDocumentEditor(),title:$("commit-title").value,task:$("task-ref").value,delivery:$("delivery").value};
  try {
    localStorage.setItem(savedDraftKey,JSON.stringify(raw));
    $("workspace-save-error").hidden = true;
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
    if (typeof saved.title === "string") $("commit-title").value = saved.title;
    if (typeof saved.task === "string") $("task-ref").value = saved.task;
    if (["apply","draft"].includes(saved.delivery)) $("delivery").value = saved.delivery;
    window.restoreDocumentEditor(saved.document);
    if (!location.hash && views.includes(saved.screen)) history.replaceState(null,"",route(saved.screen,""));
  } catch { /* An invalid or unavailable local draft cannot authorize a write. */ }
}

async function renewSession(previous) {
  if (await resumeSession(previous)) return true;
  expireSession(previous);
  return false;
}

const tabs = consoleTabs({
  credential: () => session, commander: () => commanderContext, request,
  unauthorized: renewSession,
  navigate({screen,role="",target}) {
    if (!views.includes(screen) || (role && (screen !== "providers" || !Object.hasOwn(roleNames,role)))) return null;
    let next;
    if (target) {
      try { next = new URL(target); } catch { return null; }
      if (next.origin !== location.origin || next.username || next.password || next.search || !/^\/w\/[a-z0-9][a-z0-9-]{0,62}\/$/.test(next.pathname)) return null;
    }
    window.focus();
    // A navigation may come from another session: drop this tab's worker requests.
    if ((activity?.attention || []).some(commanderItem)) commanderNote = "Worker requests were cleared when this tab was asked to navigate. Use Resync to load them again.";
    clearCommanderActivity();
    const current = destination();
    if (!next && current.screen === screen && (!role || current.role === role)) return {handled:true};
    if (publishing || settingsPage.busy) {
      notice("status","A save is in progress. Request the editor again once it finishes.");
      return {blocked:"publishing"};
    }
    // A settings draft the browser could not store would be lost with the page.
    if (next && (!storeDrafts() || !settingsPage.stored)) {
      $("workspace-save-error").hidden = false;
      return {blocked:"storage"};
    }
    return {handled:true,perform() {
      if (next) {
        leavingForWorktree = true;
        location.assign(next.href);
      } else {
        if ($("settings-review").open && !settingsPage.busy) $("settings-review").close();
        go(screen,role);
      }
    }};
  }
});
window.addEventListener("pagehide",() => { tabs.stop(); live.stop(); });
window.addEventListener("pageshow",event => {
  leavingForWorktree = false;
  if (!$("console-shell").hidden) {
    if (event.persisted) {
      clearCommanderActivity();
      loadState();
    } else { tabs.start(); live.start(); }
  }
});

// Live overview (docs/design/ui/live-stream.md). The event stream keeps the
// checkout facts current; api/state supplies them until the first snapshot.
const worktreeID = /^\/w\/([a-z0-9][a-z0-9-]{0,62})\/$/.exec(location.pathname)?.[1] || "*";
const streamLabels = {idle: "Connecting", connecting: "Connecting", live: "Live", reconnecting: "Reconnecting", resyncing: "Resyncing", offline: "Offline"};
const offlineNotes = {
  unsupported: "This console does not serve live updates. Use Resync to reload the checkout.",
  forbidden: "The console refused the live stream for this worktree. Use Resync to try again.",
  busy: "Too many live streams are open for this console. Close another console tab, or use Resync.",
  unavailable: "Live updates are not available for this worktree yet. The console keeps retrying."
};
const operationNames = {rebase: "Rebase", merge: "Merge", "cherry-pick": "Cherry-pick", revert: "Revert", bisect: "Bisect"};
const shownFacts = new Map();
const flashTimers = new WeakMap();
let liveOverview = null;
let liveStatus = {state: "connecting", reason: ""};
let liveReady = false;
let loadedAt = null;
let configTimer;

// The overview follows its own worktree; activity and approvals are states of
// the whole repository; the tabs state carries navigation for this tab.
// A change of the personal layer is a "global" event. The stream sends the
// states it changes without a topic for it (ADR, "Phase 2 scoping").
const liveTopics = ["*/" + worktreeID + "/git.**","*/" + worktreeID + "/config.**",
  "*/*/gh.**","*/*/commit.created","*/*/hook.ran","*/*/git.head.changed","*/*/git.worktrees.changed","*/*/approval.**","*/*/op.**","*/*/ui.**",
  // A plugin's note on an operation is not an op.* event (ADR, "Phase 5 timeline annotations").
  "*/*/plugin.annotation.added"];
const live = AgentflowEvents.connect({
  endpoint: new URL("api/events",location.href).href,
  credential: () => session,
  topics: () => liveTopics,
  tab: () => tabs.id,
  unauthorized: renewSession,
  onStatus(status) {
    const previous = liveStatus.state;
    liveStatus = status;
    if (status.state === "offline" && previous !== "offline") announce("Live updates are offline. The overview shows the last loaded values.");
    if (status.state === "live" && previous === "offline") announce("Live updates restored.");
    renderStream();
  },
  onSnapshot({states}) {
    liveReady = true;
    showLive(states.find(ownOverview)?.state ?? null);
    showRepository("activity",states.find(repositoryState("activity"))?.state ?? null);
    showRepository("approvals",states.find(repositoryState("approvals"))?.state ?? null);
    showRepository("timeline",states.find(repositoryState("timeline"))?.state ?? null);
    showRepository("plugins",states.find(repositoryState("plugins"))?.state ?? null);
    showSettings(states.find(ownSettings)?.state ?? null,false);
    tabs.receive(states.find(ownTabs)?.state ?? null);
  },
  onDelta({states,event}) {
    const own = states.find(ownOverview);
    if (own) showLive(own.state);
    for (const name of ["activity","approvals","timeline","plugins"]) {
      const next = states.find(repositoryState(name));
      if (next) showRepository(name,next.state);
    }
    const settings = states.find(ownSettings);
    if (settings) showSettings(settings.state,true);
    const mine = states.find(ownTabs);
    if (mine) tabs.receive(mine.state);
    if (event?.type === "config.layer.changed" && (!event.worktree || event.worktree === worktreeID)) {
      clearTimeout(configTimer);
      configTimer = setTimeout(configChangedOnDisk,300);
    }
  }
});

function ownOverview(entry) {
  return entry.projection === "overview" && (worktreeID === "*" || entry.worktree === worktreeID);
}
function ownSettings(entry) {
  return entry.projection === "settings" && (worktreeID === "*" || entry.worktree === worktreeID);
}
function ownTabs(entry) {
  return entry.projection === "tabs" && entry.worktree === worktreeID;
}
// A stream serves one repository, so a state without a worktree is this one's.
function repositoryState(name) {
  return entry => entry.projection === name && !entry.worktree;
}

// Activity and approvals (docs/design/ui/live-stream.md, "Live panels").
// liveRepository holds the raw state last received; the panels keep the last
// values on screen when the stream has none.
const liveRepository = {activity: null, approvals: null, timeline: null, plugins: null};
// The Plugins page (docs/design/ui/plugins.md). Its values are the
// repository's "plugins" state; it sends no request. The plugins state has
// no topic of its own: it rides in the repository's frames (ADR, phase 5
// decision 10).
const pluginsPage = AgentflowPlugins.createPlugins({
  root: document, announce,
  actionName: action => commonActions[action],
  visible: () => !$("console-shell").hidden && pages[destination().screen] === "plugins"
});
const panels = AgentflowPanels.createPanels({
  root: document, worktree: worktreeID, announce,
  command: pluginsPage.command,
  actionName: action => commonActions[action]
});
// The operation timeline (docs/design/ui/live-stream.md, "Timeline").
const timeline = AgentflowTimeline.createTimeline({
  root: document, worktree: worktreeID, announce,
  actionName: action => commonActions[action]
});

// The Settings page (docs/design/ui/settings.md). Its values are the
// worktree's "settings" state; its own requests are catalog, preview, save
// and undo.
let liveSettings = null;
const settingsPage = AgentflowSettings.createSettings({
  root: document, request, announce, storageKey: settingsStore, route: groupRoute,
  completions: () => ({
    branches: [state?.branch,...(state?.completions?.branches || [])].filter(Boolean),
    files: files().map(item => item.path)
  })
});

function showSettings(raw, flash) {
  const previous = liveSettings;
  liveSettings = raw;
  if (!state || $("console-shell").hidden) return;
  settingsPage.show(raw,flash && Boolean(previous && raw));
  renderStream();
}

function showRepository(name, raw) {
  const previous = liveRepository[name];
  liveRepository[name] = raw;
  if (!state || $("console-shell").hidden) return;
  renderRepository(name,Boolean(previous && raw));
}

function renderRepository(name, flash = false) {
  if (name === "activity") {
    panels.showActivity(liveRepository.activity,flash);
    renderHandoff(flash);
  } else if (name === "timeline") timeline.show(liveRepository.timeline,flash);
  else if (name === "plugins") pluginsPage.show(liveRepository.plugins,flash);
  else {
    panels.showApprovals(liveRepository.approvals,flash);
    // A plugin's card lists the approvals its writes wait for.
    pluginsPage.showApprovals(liveRepository.approvals,flash);
  }
  renderStream();
}

// The overview projection's state (OverviewState in projection_overview.go).
// head is the short OID, "" before the first commit; upstream is
// {name, ahead, behind} and absent when the branch has none. Unknown fields
// are ignored and missing ones read as empty, so the shape can grow.
function overviewState(raw) {
  if (raw === null || typeof raw !== "object") return null;
  const text = value => typeof value === "string" ? value : "";
  const count = value => Number.isSafeInteger(value) && value > 0 ? value : 0;
  const config = raw.config !== null && typeof raw.config === "object" ? raw.config : {};
  const problems = Array.isArray(config.problems) ? config.problems.filter(issue => typeof issue === "string" && issue) : [];
  if (config.valid === false && !problems.length) problems.push("The repository configuration is not valid.");
  const upstream = raw.upstream !== null && typeof raw.upstream === "object"
    ? {name: text(raw.upstream.name), ahead: count(raw.upstream.ahead), behind: count(raw.upstream.behind)} : null;
  const staged = count(raw.staged), unstaged = count(raw.unstaged), untracked = count(raw.untracked);
  return {
    branch: text(raw.branch), head: text(raw.head), oid: text(raw.oid), detached: raw.detached === true, upstream,
    changed: count(raw.changed) || staged + unstaged + untracked, staged, unstaged, untracked,
    operation: text(raw.operation), task: text(raw.task), problems
  };
}

// One polite sentence group per update. Messages from the same update are
// joined, so one screen's change does not replace another's.
let announcements = [];
function announce(message) {
  announcements.push(message);
  if (announcements.length > 1) return;
  // Clearing first makes a repeated message audible again.
  $("live-announcer").textContent = "";
  requestAnimationFrame(() => {
    $("live-announcer").textContent = announcements.join(" ");
    announcements = [];
  });
}

function showLive(raw) {
  const previous = liveOverview;
  liveOverview = overviewState(raw);
  if (!state || $("console-shell").hidden) return;
  const changed = renderOverview(Boolean(previous && liveOverview));
  if (changed.length) announce(changed.join(". ") + ".");
}

function configChangedOnDisk() {
  if (!state || !session || $("console-shell").hidden) return;
  if (publishing || hasPendingEdits()) {
    notice("status","The configuration changed on disk. Save or discard your edits, then use Resync to load it.");
    return;
  }
  loadState();
}

function clock(date) {
  return date.toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"});
}

function renderStream() {
  const name = Object.hasOwn(streamLabels,liveStatus.state) ? liveStatus.state : "connecting";
  $("stream-state").dataset.state = name;
  $("stream-label").textContent = streamLabels[name];
  let note = "";
  const since = loadedAt ? " The overview shows the values loaded at " + clock(loadedAt) + "." : "";
  if (name === "offline") note = offlineNotes[liveStatus.reason] || "Live updates are offline." + (liveOverview ? " The overview shows the last values it received." : since) + " The console keeps retrying.";
  else if (name === "live" && liveReady && !liveOverview) note = "The live stream has no data for this worktree yet." + since;
  $("live-note").textContent = note;
  $("live-note").hidden = !note;
  // The repository panels: say when their values may be stale or are missing.
  const retrying = " The console keeps retrying.";
  for (const [key,label,empty] of [["activity","Activity","The live stream has no activity for this repository yet."],["approvals","Approvals","The live stream has no approvals for this repository yet."]]) {
    const shown = Boolean(panels[key]);
    let text = "";
    if (name === "offline") text = offlineNotes[liveStatus.reason] || "Live updates are offline. " + (shown ? label + " shows the last values it received." : label + " appears when they return.") + retrying;
    else if (name === "live" && liveReady && !liveRepository[key]) text = empty;
    const prefix = key === "activity" ? "activity-live" : "approvals";
    $(prefix + "-note").textContent = text;
    $(prefix + "-note").hidden = !text;
    $(key + "-wait").hidden = shown || Boolean(text);
  }
  timeline.status({state: name, offline: name === "offline" ? offlineNotes[liveStatus.reason] || "" : "", ready: liveReady, present: Boolean(liveRepository.timeline)});
  pluginsPage.status({state: name, offline: name === "offline" ? offlineNotes[liveStatus.reason] || "" : "", ready: liveReady, present: Boolean(liveRepository.plugins)});
  settingsPage.status({state: name, offline: name === "offline" ? offlineNotes[liveStatus.reason] || "" : "", ready: liveReady, present: Boolean(liveSettings)});
  const stale = name === "offline" && panels.activity ? "Live updates are offline. The pull requests in this brief are the last ones received." : "";
  $("handoff-note").textContent = stale;
  $("handoff-note").hidden = !stale;
}

// Show one overview value. With flash set, a value that differs from the one
// on screen is highlighted; the caller announces it.
function showFact(node, key, text, flash) {
  const previous = shownFacts.get(key);
  shownFacts.set(key,text);
  if (node.textContent !== text) node.textContent = text;
  if (!flash || previous === undefined || previous === text) return false;
  clearTimeout(flashTimers.get(node));
  node.classList.remove("live-changed");
  void node.offsetWidth; // restart the highlight when a value changes twice in a row
  node.classList.add("live-changed");
  flashTimers.set(node,setTimeout(() => node.classList.remove("live-changed"),2000));
  return true;
}

function plural(count, one, many = one + "s") {
  return count + " " + (count === 1 ? one : many);
}

// The checkout facts on screen: live when the stream has them, else as loaded.
function checkoutFacts() {
  const now = liveOverview;
  if (!now) {
    const count = state.dirty_count || 0;
    return {branch: state.branch || "", head: state.head || "", dirty: Boolean(state.dirty), operation: "",
      branchText: state.branch || "Detached HEAD", changes: count ? plural(count,"changed path") : state.dirty ? "Uncommitted changes" : "None"};
  }
  const parts = [[now.staged,"staged"],[now.unstaged,"unstaged"],[now.untracked,"untracked"]].filter(([count]) => count).map(([count,label]) => count + " " + label);
  const up = now.upstream;
  const sync = up ? [up.ahead && up.ahead + " ahead", up.behind && up.behind + " behind"].filter(Boolean).join(", ") : "";
  const branch = now.detached ? "" : now.branch;
  return {branch, head: now.head, dirty: now.changed > 0, operation: now.operation,
    branchText: (branch || "Detached HEAD") + (sync ? " · " + sync + (up.name ? " " + up.name : "") : ""),
    changes: parts.join(" · ") || (now.changed ? plural(now.changed,"changed path") : "None")};
}

// Configuration problems and uncommitted work follow the stream; every other
// item (pins, pull requests) stays as the last load reported it.
function checkoutAttention() {
  const loaded = state?.attention || [];
  if (!liveOverview) return loaded;
  const followed = ["Configuration needs attention","Uncommitted work"];
  const fresh = liveOverview.problems.map(detail => ({title: followed[0], detail}));
  const facts = checkoutFacts();
  if (facts.dirty) fresh.push({title: followed[1], detail: facts.changes.replaceAll(" · ",", ") + " in this worktree. Publication uses only the reviewed configuration file."});
  return [...fresh,...loaded.filter(entry => !followed.includes(entry.title))];
}

function renderOverview(flash = false) {
  const changed = [];
  const facts = checkoutFacts();
  const name = state.repository || state.root?.split(/[\\/]/).pop() || "Local checkout";
  $("top-repo").textContent = name;
  const branch = $("context-branch");
  showFact(branch,"context-branch",facts.branch || facts.head.slice(0,12),flash);
  branch.hidden = !branch.textContent;
  branch.title = facts.branch || "Detached HEAD";
  $("checkout-name").textContent = name;
  $("root-path").textContent = state.root || "Worktree path unavailable";
  $("rail-version").textContent = state.version || "";
  document.title = name + " · agentflow";
  if (showFact($("working-state"),"working-state",facts.dirty ? "Uncommitted changes" : "Clean worktree",flash)) changed.push(facts.dirty ? "The worktree has uncommitted changes" : "The worktree is clean");
  $("working-state").classList.toggle("changed", facts.dirty);
  const operation = $("git-operation");
  const running = facts.operation ? (operationNames[facts.operation] || facts.operation) + " in progress" : "";
  if (showFact(operation,"git-operation",running,flash)) changed.push(running || "The git operation finished");
  operation.hidden = !running;
  const metadata = $("repo-metadata");
  for (const [key, label, value, follows] of [["branch","Branch",facts.branchText,true], ["revision","Revision",facts.head.slice(0,12) || "No commits yet",true], ["changes","Changes",facts.changes,true], ["cli","Running CLI",state.version], ["pin","Workflow pin",state.pin]]) {
    let field = metadata.querySelector('[data-field="' + key + '"]');
    if (!field) {
      field = element("dd");
      field.dataset.field = key;
      const group = element("div");
      group.append(element("dt",label),field);
      metadata.append(group);
    }
    if (showFact(field,key,value || "Unavailable",flash && follows)) changed.push(label + " changed to " + (value || "unavailable"));
  }
  const issues = liveOverview ? liveOverview.problems.length : null;
  if (flash && issues !== null && shownFacts.get("problems") !== undefined && shownFacts.get("problems") !== issues) {
    changed.push(issues ? "The configuration has " + plural(issues,"problem") : "The configuration problems are resolved");
  }
  if (issues !== null) shownFacts.set("problems",issues);
  renderStream();
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
  return changed;
}

// Pull requests come from the stream; api/activity adds only what belongs to
// the Commander session.
function commanderItem(entry) {
  return Boolean(entry.run_id || entry.request_id) || entry.title === "Agent Commander unavailable";
}

// The brief: the checkout, open worker requests and the live pull requests.
function renderHandoff(flash = false) {
  const requests = commanderContext ? (activity?.attention || []).filter(commanderItem).map(entry => [entry.title,"—",entry.detail,entry.url].filter(Boolean).join(" ")) : [];
  const brief = [state?.handoff,requests.join("\n"),panels.activity?.handoff].map(part => (part || "").trim()).filter(Boolean).join("\n");
  const changed = showFact($("handoff-text"),"handoff",brief || "No handoff is available for this checkout.",flash);
  $("copy-handoff").disabled = !brief;
  if (changed) {
    $("handoff-updated").textContent = "Updated live at " + clock(new Date()) + ".";
    $("handoff-updated").hidden = false;
    if (!$("page-handoff").hidden) announce("The repository brief changed.");
  }
}

function renderActivity() {
  const entries = [...checkoutAttention(), ...(activity?.attention || []).filter(commanderItem)];
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
  const note = commanderNote || (target.childElementCount ? "" : "Nothing in this checkout needs attention.");
  $("activity-note").textContent = note;
  $("activity-note").hidden = !note;
  renderHandoff();
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
    loadedAt = new Date();
    activity = null;
    connectionSequence++;
    $("check-connections").disabled = !next.config?.providers;
    $("connection-results").hidden = true;
    $("connection-list").replaceChildren();
    $("connection-status").textContent = "Check the providers selected in the configuration on disk.";
    invalidateReview();
    const reconnecting = !$("connection-options").hidden && $("console-shell").hidden;
    renderOverview();
    renderRepository("activity");
    renderRepository("approvals");
    renderRepository("timeline");
    renderRepository("plugins");
    settingsPage.show(liveSettings);
    $("connection").hidden = true;
    $("connection-options").hidden = true;
    $("connection-feedback").textContent = "";
    $("console-shell").hidden = false;
    showPage(reconnecting);
    tabs.start();
    live.start();
    notice("status");
    loadCommanderActivity();
  } catch (e) {
    if (!session || revision !== loadSequence) return;
    if ($("console-shell").hidden) showConnection(e.message);
    else notice("status",e.message,true);
  }
  finally { $("refresh").disabled = !session; }
}
// Resync reloads the checkout and asks the stream for a fresh snapshot.
$("refresh").addEventListener("click", () => {
  liveOverview = null;
  liveRepository.activity = null;
  liveRepository.approvals = null;
  liveRepository.timeline = null;
  liveRepository.plugins = null;
  liveSettings = null;
  liveReady = false;
  live.resync();
  loadState();
});

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
        message.textContent = approvalExplanation(result);
      } else if (result.state === "answered") {
        message.textContent = "Reply delivered.";
        await loadCommanderActivity();
      } else { throw new Error(result.reason || "The reply has no confirmed delivery status. Use Resync to check."); }
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
  const reason = active?.reason || (!active ? "Run agentflow init in this checkout to create repository configuration, then refresh." : "");
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
  const messages = [data.note,data.ci,data.warning,approvalExplanation(data),data.plan?.warning];
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
  if (patch) downloadText("agentflow-config.patch",patch,"text/x-diff;charset=utf-8");
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
  return drafts.size || $("document-control").querySelector("[data-modified]") || $("commit-title").value !== $("commit-title").defaultValue || $("task-ref").value || $("delivery").value === "draft";
}
window.addEventListener("beforeunload",(event) => {
  if (!leavingForWorktree && ((!settingsPage.stored && settingsPage.edits.length) || (!storeDrafts() && hasPendingEdits()))) { event.preventDefault(); event.returnValue = ""; }
});

$("refresh").disabled = true;
$("copy-handoff").disabled = true;
connect();

function approvalExplanation(data) {
  if (!data.policy) return data.reason || "";
  const rule = data.policy;
  let message = rule.action + " requires " + rule.level + " under " + rule.source + ".";
  if (data.request?.expires_at) message += " Review by " + new Date(data.request.expires_at).toLocaleTimeString() + ".";
  return message + (data.reason ? " " + data.reason : "");
}
