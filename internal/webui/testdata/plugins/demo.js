"use strict";

// Scripted plugin states for looking at the Plugins page (TestPluginsDemo in
// plugins_browser_test.go). It runs in the page and writes the stream frames
// lane 5.2's plugins projection will send. It is for design review only.
//
//   /w/first/            four plugins in different states, and a timeline with plugin notes
//   /w/first/?view=empty no plugin installed
//   /w/first/?view=host  the plugin host is not running
(async () => {
  const byID = id => document.getElementById(id);
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const repo = "3f2a9c1b7d4e5f60";
  let seq = 0;
  const emit = text => fetch("/fixture/stream/emit", {method: "POST", body: text});
  const frame = (event, data) => "event: " + event + "\nid: v1:" + repo + "=" + (++seq) + "\ndata: " + JSON.stringify(data) + "\n\n";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const overview = {projection: "overview", repo, worktree: "first", state: {branch: "issue-81-plugins", head: "1111111", oid: "1111111aaaaaaa1111111aaaaaaa1111111aaaa", detached: false, changed: 0, staged: 0, unstaged: 0, untracked: 0, operation: "", config: {valid: true, problems: []}}};
  const view = new URLSearchParams(location.search).get("view") || "";

  const hash = letter => "sha256:" + letter.repeat(64);
  const notifier = {id: "ci-notify", name: "CI failure notifier", description: "Sends a notification when a check fails on a pull request of this repository.", version: "0.1.0", tier: "declarative",
    hash: hash("a"), installed_at: at(-9 * 86400000), enabled: true, state: "running", since: at(-3 * 3600000),
    delivered: 124, dead_letters: 0, dropped: 0, invocations: 3, published: 0,
    extensions: [{point: "handler", name: "ci-failed", on: ["gh.ci.updated"], invokes: ["notify"]}],
    permissions: {subscribe: ["gh.ci.updated"], publish: [], invoke: ["notify"]}, settings_group: "plugins.ci-notify"};
  const annotator = retry => ({id: "timeline-notes", name: "Timeline annotator", description: "Adds a note to an operation on the timeline when it is applied or fails.", version: "0.3.2", tier: "process",
    hash: hash("b"), installed_at: at(-2 * 86400000), enabled: true, state: "backoff", code: "exited", since: at(-20000), restarts: 3, retry_at: at(retry),
    delivered: 58, dead_letters: 2, dropped: 0, invocations: 0, published: 41,
    extensions: [{point: "handler", name: "annotate", on: ["op.applied", "op.failed"], annotates: true}],
    permissions: {subscribe: ["op.applied", "op.failed"], publish: ["plugin.annotation.added"], invoke: []}, settings_group: "plugins.timeline-notes"});
  const freeze = {id: "friday-freeze", name: "Friday freeze", description: "Refuses pushes and pull requests on Fridays.", version: "1.0.0", tier: "declarative",
    hash: hash("c"), installed_at: at(-3600000), enabled: false, state: "installed",
    delivered: 0, dead_letters: 0, dropped: 0, invocations: 0, published: 0,
    extensions: [{point: "permission_rule", name: "no-friday-push", deny: "No pushes on Fridays. Ask the release owner if this cannot wait."}],
    permissions: {subscribe: [], publish: [], invoke: []}, settings_group: "plugins.friday-freeze"};
  const checker = {id: "release-check", name: "Release checker", description: "Comments on the task when a release is cut.", version: "0.2.0", tier: "process",
    hash: hash("d"), installed_at: at(-30 * 86400000), enabled: true, disabled_here: true, state: "failed", code: "hash_mismatch", since: at(-5400000),
    delivered: 12, dead_letters: 1, dropped: 4, invocations: 2, published: 0,
    extensions: [{point: "handler", name: "on-release", on: ["op.applied"], invokes: ["comment", "ship.status"]}],
    permissions: {subscribe: ["op.applied"], publish: [], invoke: ["comment", "ship.status"]}, settings_group: "plugins.release-check"};
  const letters = [
    {plugin: "timeline-notes", event_id: "01JD9Z3K8Q4W6X7Y8Z9A0B1C2D", event_type: "op.applied", event_repo: repo, code: "exited", time: at(-20000)},
    {plugin: "timeline-notes", event_id: "01JD9Z2F5H7J9K1M3N5P7Q9R1S", event_type: "op.failed", event_repo: repo, code: "timeout", time: at(-340000)},
    {plugin: "release-check", event_id: "01JD9W8T6V4X2Z0B8D6F4H2K0M", event_type: "op.applied", event_repo: repo, code: "rpc_error", time: at(-5400000)}
  ];
  const plugins = retry => view === "empty" ? {host: true, plugins: [], dead_letters: []}
    : {host: view !== "host", plugins: view === "host" ? [{...notifier, state: "", since: undefined}, {...freeze, state: ""}] : [notifier, checker, annotator(retry), freeze], dead_letters: view === "host" ? [] : letters};

  const step = (id, type, state, ms) => ({event_id: id, seq: 1, type, time: at(ms), state});
  const timeline = {operations: [
    {id: "op_0123456789abcdef0123456789abcdef", operation: "push", action: "push", class: "remote", client: "claude-code", worktree: "first", subject: "Push issue-81-plugins", branch: "issue-81-plugins", state: "applied",
      started: at(-240000), updated: at(-236000), steps: [step("s1", "op.requested", "requested", -240000), step("s2", "op.applied", "applied", -236000)],
      annotations: [
        {event_id: "n1", plugin: "timeline-notes", text: "Pushed 3 commits. CI run 412 started.", url: "https://ci.example.com/runs/412", level: "info", time: at(-235000)},
        {event_id: "n2", plugin: "timeline-notes", text: "The branch is 14 commits behind develop.", level: "warning", time: at(-234000)}]},
    {id: "op_fedcba9876543210fedcba9876543210", operation: "commit", action: "commit", class: "local", client: "cli", worktree: "first", subject: "feat: show installed plugins and their health live", state: "applied",
      started: at(-900000), updated: at(-899000), steps: [step("c1", "op.requested", "requested", -900000), step("c2", "op.applied", "applied", -899000)]}
  ]};

  // A stored session opens the console by itself; otherwise launch one.
  while (byID("connection-options").hidden && byID("console-shell").hidden) await wait(20);
  if (byID("console-shell").hidden) {
    const ticket = await (await fetch("/fixture/ticket", {method: "POST"})).text();
    location.hash = new URLSearchParams({launch: ticket, screen: "overview"}).toString();
    while (byID("console-shell").hidden) await wait(20);
  }
  location.hash = "#plugins";
  await emit(frame("snapshot", {cursor: "v1:" + repo + "=1", states: [overview, {projection: "plugins", repo, state: plugins(42000)}, {projection: "timeline", repo, state: timeline}, {projection: "approvals", repo, state: {open: [], recent: []}}]}));
  document.documentElement.dataset.demo = "ready";
  if (view) return;
  // The annotator restarts, runs, then crashes again: the page changes live.
  for (;;) {
    await wait(42000);
    await emit(frame("delta", {event: {id: "e" + seq, type: "plugin.lifecycle.changed", repo: "global", seq}, states: [{projection: "plugins", repo, state: {...plugins(0), plugins: [notifier, checker, {...annotator(0), state: "running", code: "", since: at(0), retry_at: undefined}, freeze]}}]}));
    await wait(20000);
    await emit(frame("delta", {event: {id: "e" + seq, type: "plugin.lifecycle.changed", repo: "global", seq}, states: [{projection: "plugins", repo, state: plugins(42000)}]}));
  }
})();
