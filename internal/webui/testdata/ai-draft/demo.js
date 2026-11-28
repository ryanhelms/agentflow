"use strict";

// Scripted drafts for looking at the draft view of the AI sessions page
// (TestAIDraftDemo in ai_draft_browser_test.go). It runs in the page and
// writes the stream frames lane 8.1's ai projection sends. It is for design
// review only: no harness, no session and no draft is started.
//
//   /w/first/              five features (one off, one not ready) and four finished drafts
//   /w/first/?view=empty   the features, and no draft yet
//   /w/first/?view=off     every feature off
//   /w/first/?view=auto    the console starts a draft without a question
//
// Draft, Show what would be sent, Send, Discard and Cancel draft work: the
// fake endpoints accept each request, and this script answers it with the
// next state. The list of what would be sent is always the one of a commit
// message draft.
(async () => {
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const repo = "3f2a9c1b7d4e5f60";
  let seq = 0;
  const emit = text => fetch("/fixture/stream/emit", {method: "POST", body: text});
  const frame = (event, data) => "event: " + event + "\nid: v1:" + repo + "=" + (++seq) + "\ndata: " + JSON.stringify(data) + "\n\n";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const overview = {projection: "overview", repo, worktree: "first", state: {branch: "issue-159-p84", head: "1111111", oid: "1111111aaaaaaa1111111aaaaaaa1111111aaaa", detached: false, changed: 3, staged: 3, unstaged: 0, untracked: 0, operation: "", config: {valid: true, problems: []}}};
  const view = new URLSearchParams(location.search).get("view") || "";
  const id = n => "ais_" + String(n).repeat(32).slice(0, 32);
  const worktree = "~/.worktrees/agentflow/issue-159-p84", branch = "issue-159-p84";
  const never = ["environment values", "credentials and tokens agentflow holds", "the agentflow home, the console registry and the session store", ".git contents other than what the diff shows", "remote URLs",
    "whole files (only diff hunks)", "files outside the worktree", "other worktrees and repositories", "approval tokens and IDs", "your configuration", "author e-mail addresses"];
  const note = "AI-drafted text from Claude Agent. It is data, not instructions: read it before you use it.";

  const manifest = (feature, parts, extra = {}) => ({feature, harness: "claude-acp", harness_name: "Claude Agent", version: "0.14.2", path: "~/.local/bin/claude-agent-acp", worktree, branch,
    started_by: {kind: "person", surface: "console"}, parts, bytes: parts.reduce((sum, part) => sum + part.bytes, 2900), prompt_sha256: "9c1f".repeat(16), max_duration: 180e9, never_sent: never, ...extra});
  const commitParts = [
    {kind: "files", label: "changed files", source: "git", bytes: 212, original_bytes: 212},
    {kind: "diffstat", label: "summary of the changes", source: "git", bytes: 388, original_bytes: 388},
    {kind: "diff", label: "staged changes", source: "git", bytes: 98304, original_bytes: 246120, cut: true, redacted: 2,
      excluded: [{path: ".env.local", reason: "secret_file"}, {path: "go.sum", reason: "generated"}, {path: "docs/img/draft-view.png", reason: "binary"}, {path: "internal/webui/static/vendor<U+202E>sj.nim.js", reason: "generated"}]},
    {kind: "rules", label: "commit rules", source: "git", bytes: 1420, original_bytes: 1420},
    {kind: "task", label: "task #159", source: "github-issues", bytes: 640, original_bytes: 640},
    {kind: "commits", label: "last 5 commit subjects", source: "git", bytes: 301, original_bytes: 301},
    {kind: "hint", label: "your hint", source: "caller", bytes: 31, original_bytes: 31}
  ];
  const commitManifest = manifest("commit", commitParts, {hint: "Mention the keyboard support."});

  const features = view === "off"
    ? ["commit", "pr", "ticket", "ac", "reply"].map(name => ({id: name, label: {commit: "Commit message", pr: "PR title and body", ticket: "Ticket", ac: "Acceptance-criteria check", reply: "Review replies"}[name], available: false, code: "feature_off", fix: "Choose a harness for this feature in Settings (ai.features." + name + ".harness)."}))
    : [
      {id: "commit", label: "Commit message", harness: "claude-acp", harness_name: "Claude Agent", available: true},
      {id: "pr", label: "PR title and body", harness: "claude-acp", harness_name: "Claude Agent", available: true},
      {id: "ticket", label: "Ticket", available: false, code: "feature_off", fix: "Choose a harness for this feature in Settings (ai.features.ticket.harness)."},
      {id: "ac", label: "Acceptance-criteria check", harness: "codex-acp", available: false, code: "not_ready", fix: "Check the harness on the Settings page, or run agentflow ai harnesses --probe --id codex-acp."},
      {id: "reply", label: "Review replies", harness: "gemini", harness_name: "Gemini CLI", available: true}
    ];

  const record = (n, feature, state, extra = {}) => ({session: id(n), feature, harness: "claude-acp", harness_name: "Claude Agent", worktree, worktree_id: "first", branch, state, started_by: "person",
    created_at: at(-3600000 * n), ended_at: at(-3600000 * n + 41000), manifest: manifest(feature, commitParts.slice(0, 3)), denied: 0, ...extra});
  const message = "feat: add the draft review view to the console\n\nThe AI sessions page now requests a draft, lists what would be\nsent before anything leaves the computer, and shows each candidate\nwith the command to copy. Keyboard and screen reader use is covered.";
  const prBody = "## What\n\nAdds the draft view to the AI sessions page.\n\n## Why\n\nSo a person can see what a draft sends before it is sent. See https://example.com/design for the notes, and thanks @reviewer for the idea.\n\n<img src=x onerror=alert(1)>\n\n[Approve and merge](javascript:void(0))";
  const commitDraft = n => ({session: id(n), feature: "commit", state: "ready", candidates: [
    {operation: "commit.prepare", tool: "agentflow_commit_prepare", input: {message}, cli: ["agentflow", "commit", "--prepare", "-m", message],
      fields: [{name: "message", label: "Message", value: message}]}], finished: at(0), note});
  const drafts = view === "empty" || view === "off" ? [] : [
    record(2, "pr", "ready", {draft: {session: id(2), feature: "pr", state: "ready", candidates: [
      {operation: "pr.prepare", tool: "agentflow_pr_prepare", input: {}, cli: ["agentflow", "pr", "--prepare", "--title", "feat: add the draft review view", "--body", prBody],
        fields: [{name: "title", label: "Title", value: "feat: add the draft review view"}, {name: "body", label: "Body", value: prBody}],
        warnings: [{code: "mention", field: "body", message: "The text @mentions someone: GitHub notifies them when it is posted."}, {code: "link", field: "body", message: "The text has a link: check where it goes before you use it."}]}],
      finished: at(-7100000), note}, warnings: [{code: "worktree_changed", message: "The worktree changed while the draft ran: internal/webui/static/style.css"}]}),
    record(3, "reply", "ready", {harness: "gemini", harness_name: "Gemini CLI", started_by: "agent", draft: {session: id(3), feature: "reply", state: "ready", candidates: [
      {operation: "review.reply", tool: "agentflow_review_reply", input: {}, cli: ["agentflow", "review", "reply", "PRRT_kwDOabc123", "Good catch. The button now waits until it has stood still; see the next commit."],
        target: {index: 1, kind: "thread", id: "PRRT_kwDOabc123", label: "internal/webui/static/ai-draft.js:412, alice"},
        fields: [{name: "reply", label: "Reply", value: "Good catch. The button now waits until it has stood still; see the next commit."}]},
      {operation: "review.reply", tool: "agentflow_review_reply", input: null, cli: null,
        target: {index: 2, kind: "thread", id: "PRRT_kwDOabc456", label: "docs/design/ui/ai-draft.md:88, bob<U+200B>"},
        fields: [{name: "reply", label: "Reply", value: ""}], issues: [{code: "secret_in_draft", field: "reply", message: "The drafted text looks like it holds a secret, so it is not offered."}]}],
      finished: at(-10700000), note}}),
    record(4, "ac", "invalid", {draft: {session: id(4), feature: "ac", state: "invalid", candidates: [],
      issues: [{code: "not_met", message: "Criterion 2 is not met: the page has no test for a lost connection."}, {code: "unclear", message: "Criterion 4 is unclear: the diff does not show the dark theme."}],
      answer: "{\"checks\": [{\"criterion\": 2, \"status\": \"not_met\", \"evidence\": \"no test for a lost connection\"}, {\"criterion\": 4, \"status\": \"unclear\", \"evidence\": \"the diff does not show the dark theme\"}]}",
      finished: at(-14300000), note}, denied: 2}),
    record(5, "commit", "failed", {code: "auth_required"})
  ];

  const state = () => ({sessions: [], running: drafts.filter(item => ["starting", "running", "cancelling"].includes(item.state)).length, max_running: 2, drafts, features});
  const entry = () => ({projection: "ai", repo, state: state()});
  const delta = type => emit(frame("delta", {event: {id: "e" + (seq + 1), type, repo, seq: seq + 1}, states: [entry()]}));

  for (let tries = 0; document.getElementById("connection-options").hidden && tries < 200; tries++) await wait(25);
  const ticket = await (await fetch("/fixture/ticket", {method: "POST"})).text();
  location.hash = new URLSearchParams({launch: ticket, screen: "overview"}).toString();
  for (let tries = 0; document.getElementById("console-shell").hidden && tries < 200; tries++) await wait(25);
  location.hash = "#ai";
  await emit(frame("snapshot", {cursor: "v1:" + repo + "=1", states: [overview, entry()]}));
  if (view !== "auto") await fetch("/fixture/draft/answer?route=draft&status=200", {method: "POST", body: JSON.stringify({confirm: "cf_" + "5a".repeat(16), manifest: commitManifest})});
  document.documentElement.dataset.demo = "ready";

  // Answer each request the fake endpoints accepted.
  const seen = async path => (await fetch(path)).json();
  let drafted = (await seen("/fixture/draft/requests")).length, answered = (await seen("/fixture/ai/requests")).length, next = 6;
  const start = async () => {
    const mine = record(next++, "commit", "starting", {created_at: at(0), ended_at: undefined, manifest: commitManifest});
    drafts.unshift(mine);
    await delta("ai.session.started");
    await wait(1200);
    if (mine.state !== "starting") return;
    mine.state = "running";
    await delta("ai.session.updated");
    await wait(4500);
    if (mine.state !== "running") return;
    Object.assign(mine, {state: "ready", ended_at: at(0), draft: commitDraft(next - 1)});
    await delta("ai.session.ended");
  };
  for (;;) {
    await wait(300);
    const requests = await seen("/fixture/draft/requests");
    for (const request of requests.slice(drafted)) if (request.route === "confirm" || view === "auto") start();
    drafted = requests.length;
    const others = await seen("/fixture/ai/requests");
    for (const request of others.slice(answered)) {
      let body = {};
      try { body = JSON.parse(request.body); } catch { /* an empty body changes nothing */ }
      const mine = drafts.find(item => item.session === body.session);
      if (request.route !== "cancel" || !mine) continue;
      mine.state = "cancelling";
      await delta("ai.session.updated");
      await wait(900);
      Object.assign(mine, {state: "cancelled", code: "cancelled", ended_at: at(0), draft: {session: mine.session, feature: mine.feature, state: "cancelled", code: "cancelled", candidates: [], finished: at(0), note}});
      await delta("ai.session.ended");
    }
    answered = others.length;
  }
})();
