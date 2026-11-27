"use strict";

// The activity and approvals panels (docs/design/ui/live-stream.md, "Live
// panels"). Both show one repository-wide state from the event stream:
// ActivityState and ApprovalsState in internal/webui/projection_states.go.
// Values are read defensively: unknown fields are ignored and missing ones
// read as empty, so the shapes can grow.
(function (scope) {
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const strings = value => list(value).filter(entry => typeof entry === "string" && entry);
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const number = value => Number.isSafeInteger(value) && value > 0 ? value : 0;
  const time = value => { const at = Date.parse(text(value)); return Number.isFinite(at) ? at : 0; };
  const pick = (value, known, fallback = "") => known.includes(value) ? value : fallback;

  function activityState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const github = record(raw.github) || {};
    return {
      pulls: list(raw.pulls).map(record).filter(pull => pull && number(pull.number)).map(pull => ({
        number: pull.number, title: text(pull.title), url: text(pull.url), state: text(pull.state) || "open", draft: pull.draft === true,
        head: text(pull.head_ref), base: text(pull.base_ref), author: text(pull.author),
        review: text(pull.review_decision), merge: text(pull.merge_state), worktree: text(pull.worktree),
        ci: pick(pull.ci, ["pending", "success", "failure"]), failed: strings(pull.failed), dependsOn: number(pull.depends_on)
      })),
      issues: list(raw.issues).map(record).filter(issue => issue && number(issue.number)).map(issue => ({
        number: issue.number, title: text(issue.title), url: text(issue.url), state: text(issue.state) || "open", reason: text(issue.state_reason),
        labels: strings(issue.labels), assignees: strings(issue.assignees), worktree: text(issue.worktree)
      })),
      recent: list(raw.recent).map(record).filter(entry => entry && text(entry.event_id) && (record(entry.commit) || record(entry.hook))).map(entry => {
        const commit = record(entry.commit), hook = record(entry.hook);
        return {
          id: entry.event_id, at: time(entry.time), worktree: text(entry.worktree),
          commit: commit && {oid: text(commit.oid), branch: text(commit.branch), subject: text(commit.subject), type: text(commit.type), scope: text(commit.scope), breaking: commit.breaking === true},
          hook: !commit && hook ? {name: text(hook.hook), client: text(hook.client), outcome: pick(hook.outcome, ["blocked", "failed"], "failed"), reason: text(hook.reason), ms: number(hook.duration_ms)} : null
        };
      }),
      github: {state: pick(github.state, ["ok", "disabled", "unavailable", "rate_limited"], "unknown"), retryAt: time(github.retry_at)},
      handoff: text(raw.handoff)
    };
  }

  function approvalsState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const item = entry => ({
      id: entry.operation_id, action: text(entry.action), klass: text(entry.class), client: text(entry.client), source: text(entry.policy_source),
      issued: time(entry.issued_at), expires: time(entry.expires_at), state: text(entry.state), expired: entry.expired === true,
      // "plugin:<id>" when a plugin's write asked (phase 5). Older states have no such field.
      requestedBy: text(entry.requested_by)
    });
    const items = value => list(value).map(record).filter(entry => entry && text(entry.operation_id)).map(item);
    return {open: items(raw.open), recent: items(raw.recent)};
  }

  // One readiness label per pull request, the most blocking fact first.
  function pullReadiness(pull) {
    if (pull.draft) return {label: "Draft", tone: ""};
    if (pull.merge === "dirty") return {label: "Conflicts", tone: "bad"};
    if (pull.ci === "failure") return {label: "Checks failing", tone: "bad"};
    if (pull.review === "changes_requested") return {label: "Changes requested", tone: "warn"};
    if (pull.ci === "pending") return {label: "Checks running", tone: "warn"};
    if (pull.review === "review_required") return {label: "Review required", tone: "warn"};
    if (pull.merge === "behind") return {label: "Behind base", tone: "warn"};
    if (pull.merge === "blocked") return {label: "Blocked", tone: "warn"};
    if (pull.merge === "clean" || pull.merge === "has_hooks") return {label: "Ready to merge", tone: "good"};
    return {label: "Open", tone: ""};
  }

  const ciLabels = {success: "Checks passing", pending: "Checks running", failure: "Checks failing", "": "No checks"};
  const reviewLabels = {approved: "Approved", changes_requested: "Changes requested", review_required: "Review required"};
  const hookReasons = {convention: "commit convention", repo_boundary: "repository boundary", never_list: "never-list", error: "error"};
  const healthLabels = {ok: "GitHub up to date", disabled: "GitHub not connected", unavailable: "GitHub unavailable", rate_limited: "GitHub rate limited", unknown: "GitHub state unknown"};
  const approvalStates = {pending: ["Waiting for approval", "warn"], approved: ["Approved, not used yet", "good"], consumed: ["Used", "good"], cancelled: ["Cancelled", ""], expired: ["Expired", "bad"]};
  const classNames = {local: "local write", remote: "remote write"};
  const issueReasons = {completed: "completed", not_planned: "not planned"};

  const words = value => value.replaceAll("_", " ");
  const plural = (count, one, many = one + "s") => count + " " + (count === 1 ? one : many);

  // "4 min 05 s" above a minute, "32 s" below, "" once it has passed.
  function remaining(ms) {
    const left = Math.ceil(ms / 1000);
    if (left <= 0) return "";
    const hours = Math.floor(left / 3600), minutes = Math.floor(left % 3600 / 60), seconds = left % 60;
    if (hours) return hours + " h " + String(minutes).padStart(2, "0") + " min";
    if (minutes) return minutes + " min " + String(seconds).padStart(2, "0") + " s";
    return seconds + " s";
  }

  function clock(at, now = Date.now()) {
    if (!at) return "";
    const date = new Date(at);
    const sameDay = date.toDateString() === new Date(now).toDateString();
    return date.toLocaleString([], sameDay ? {hour: "2-digit", minute: "2-digit"} : {month: "short", day: "numeric", hour: "2-digit", minute: "2-digit"});
  }

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }

  function outbound(target) {
    try {
      const url = new URL(target);
      return url.protocol === "https:" || url.protocol === "http:" ? url.href : "";
    } catch { return ""; }
  }

  // options: {root, worktree, announce(message), actionName(action), now()}.
  // root holds the panel markup of index.html.
  function createPanels(options) {
    const find = id => options.root.querySelector("#" + id);
    const now = () => options.now ? options.now() : Date.now();
    const actionName = action => options.actionName?.(action) || words(action).replaceAll(".", " · ") || "Unnamed action";
    const timers = new WeakMap();
    const warned = new Set(), lapsed = new Set(); // approvals announced at one minute and at expiry
    let activity = null, approvals = null, ticker = 0;

    // Highlight a value that changed live (the 2 s rule of the design note).
    function mark(node) {
      clearTimeout(timers.get(node));
      node.classList.remove("live-changed");
      void node.offsetWidth; // restart the highlight when a value changes twice in a row
      node.classList.add("live-changed");
      timers.set(node, setTimeout(() => node.classList.remove("live-changed"), 2000));
    }

    // Set one named value of a row in place. It returns true when the text
    // changed; with flash the change is highlighted. An empty value hides
    // the element. state selects the mark shape or tag tone.
    function fact(row, name, value, {state, flash = false} = {}) {
      const node = row.querySelector('[data-fact="' + name + '"]');
      const label = node.querySelector(".fact-text") || node;
      const changed = label.textContent !== value;
      if (changed) label.textContent = value;
      node.hidden = !value;
      if (state !== undefined && node.dataset.state !== state) node.dataset.state = state;
      if (changed && flash && value) mark(node);
      return changed;
    }

    function slot(name, {marked = false, tag = "span", klass = "fact"} = {}) {
      const node = element(tag, "", klass);
      node.dataset.fact = name;
      if (marked) {
        const shape = element("span", "", "state-mark");
        shape.setAttribute("aria-hidden", "true");
        node.append(shape, element("span", "", "fact-text"));
      }
      return node;
    }

    // Bring a keyed list in line with items without rebuilding rows that
    // stay: focus and running highlights survive an update.
    function reconcile(target, items, keyOf, build, update, flash, empty) {
      const focused = document.activeElement;
      const rows = new Map();
      for (const row of [...target.children]) {
        if (row.dataset.key) rows.set(row.dataset.key, row);
        else row.remove();
      }
      let previous = null;
      for (const item of items) {
        const key = String(keyOf(item));
        let row = rows.get(key);
        const fresh = !row;
        if (fresh) {
          row = build(item);
          row.dataset.key = key;
        }
        rows.delete(key);
        update(row, item, flash && !fresh);
        const place = previous ? previous.nextSibling : target.firstChild;
        if (row !== place) target.insertBefore(row, place);
        if (fresh && flash) mark(row);
        previous = row;
      }
      for (const row of rows.values()) {
        // A row that goes away must not take keyboard focus with it.
        if (row.contains(focused)) target.closest("section")?.querySelector("h2")?.focus({preventScroll: true});
        row.remove();
      }
      if (!items.length) target.append(element("li", empty, "muted live-empty"));
      else if (focused && focused !== document.activeElement && target.contains(focused)) focused.focus({preventScroll: true});
    }

    function titled(prefix) {
      const head = element("div", "", "row-head");
      const title = element("a", "", "row-title");
      title.target = "_blank";
      title.rel = "noopener noreferrer";
      title.append(element("span", prefix, "row-number"), " ", slot("title", {klass: ""}));
      head.append(title, slot("status", {klass: "tag"}));
      return head;
    }

    function setLink(row, target) {
      const link = row.querySelector(".row-title");
      const href = outbound(target);
      if (href) link.href = href;
      else link.removeAttribute("href");
    }

    function place(worktree) {
      if (!worktree) return "";
      return worktree === options.worktree ? "This worktree" : "Worktree " + worktree;
    }

    function buildPull(pull) {
      const row = element("li", "", "live-row");
      const facts = element("p", "", "row-facts");
      facts.append(slot("ci", {marked: true}), slot("review"), slot("branch"), slot("depends"), slot("worktree"), slot("author"));
      row.append(titled("#" + pull.number), facts, slot("failed", {tag: "p", klass: "row-note"}));
      return row;
    }

    function updatePull(row, pull, flash) {
      const ready = pullReadiness(pull);
      setLink(row, pull.url);
      fact(row, "title", pull.title || "Untitled pull request", {flash});
      fact(row, "status", ready.label, {state: ready.tone, flash});
      fact(row, "ci", ciLabels[pull.ci], {state: pull.ci || "none", flash});
      fact(row, "review", reviewLabels[pull.review] || (pull.review ? words(pull.review) : ""), {flash});
      fact(row, "branch", pull.head && pull.base ? pull.head + " into " + pull.base : pull.head, {flash});
      const parent = pull.dependsOn && activity?.pulls.find(other => other.number === pull.dependsOn);
      fact(row, "depends", pull.dependsOn ? "Depends on #" + pull.dependsOn + (parent ? " (" + pullReadiness(parent).label.toLowerCase() + ")" : "") : "", {flash});
      fact(row, "worktree", place(pull.worktree), {flash});
      fact(row, "author", pull.author ? "by " + pull.author : "");
      fact(row, "failed", pull.failed.length ? "Failed: " + pull.failed.join(", ") : "", {flash});
    }

    function issueStatus(issue) {
      if (issue.state !== "closed") return {label: "Open", tone: ""};
      return {label: "Closed" + (issueReasons[issue.reason] ? " · " + issueReasons[issue.reason] : ""), tone: issue.reason === "not_planned" ? "" : "good"};
    }

    function buildIssue(issue) {
      const row = element("li", "", "live-row");
      const facts = element("p", "", "row-facts");
      facts.append(slot("worktree"), slot("labels"), slot("assignees"));
      row.append(titled("#" + issue.number), facts);
      return row;
    }

    function updateIssue(row, issue, flash) {
      const status = issueStatus(issue);
      setLink(row, issue.url);
      fact(row, "title", issue.title || "Untitled issue", {flash});
      fact(row, "status", status.label, {state: status.tone, flash});
      fact(row, "worktree", place(issue.worktree), {flash});
      fact(row, "labels", issue.labels.join(", "), {flash});
      fact(row, "assignees", issue.assignees.length ? "Assigned to " + issue.assignees.join(", ") : "", {flash});
    }

    function recentText(entry) {
      if (entry.commit) return entry.commit.subject || "Commit " + entry.commit.oid.slice(0, 7);
      const hook = entry.hook;
      return (hook.name || "A") + " hook " + hook.outcome + (hook.reason ? ": " + (hookReasons[hook.reason] || words(hook.reason)) : "");
    }

    function buildRecent() {
      const row = element("li", "", "live-row");
      const head = element("div", "", "row-head");
      head.append(slot("what", {marked: true, klass: "fact row-title"}), slot("when", {tag: "time", klass: "row-time"}));
      const facts = element("p", "", "row-facts");
      facts.append(slot("oid", {tag: "code"}), slot("branch"), slot("kind"), slot("client"), slot("worktree"));
      row.append(head, facts);
      return row;
    }

    function updateRecent(row, entry) {
      const commit = entry.commit, hook = entry.hook;
      fact(row, "what", recentText(entry), {state: commit ? "commit" : hook.outcome});
      fact(row, "when", clock(entry.at, now()));
      if (entry.at) row.querySelector("time").dateTime = new Date(entry.at).toISOString();
      fact(row, "oid", commit ? commit.oid.slice(0, 7) : "");
      fact(row, "branch", commit?.branch ? "on " + commit.branch : "");
      fact(row, "kind", commit ? [commit.type + (commit.scope ? "(" + commit.scope + ")" : ""), commit.breaking ? "breaking change" : ""].filter(Boolean).join(" · ") : hook.ms ? "after " + hook.ms + " ms" : "");
      fact(row, "client", hook?.client ? "from " + hook.client : "");
      fact(row, "worktree", place(entry.worktree));
    }

    function health(github) {
      const node = find("github-health");
      let label = healthLabels[github.state];
      if (github.state === "rate_limited" && github.retryAt) {
        const left = remaining(github.retryAt - now());
        label += left ? " · retries at " + clock(github.retryAt, now()) + " (in " + left + ")" : " · retrying now";
      }
      node.dataset.state = github.state;
      find("github-label").textContent = label;
      node.hidden = false;
      const notes = {
        disabled: "This repository has no GitHub remote, so pull requests and issues are not followed.",
        unavailable: "Agentflow cannot read GitHub right now. Pull requests and issues show the last values it read.",
        rate_limited: "GitHub asked Agentflow to wait. Pull requests and issues show the last values it read."
      };
      const note = find("github-note");
      note.textContent = notes[github.state] || "";
      note.hidden = !note.textContent;
    }

    function count(id, total) {
      find(id).textContent = total ? String(total) : "";
    }

    // What changed between two activity states, as short sentences.
    function activityChanges(before, after) {
      const out = [];
      const old = new Map(before.pulls.map(pull => [pull.number, pull]));
      for (const pull of after.pulls) {
        const was = old.get(pull.number);
        old.delete(pull.number);
        const ready = pullReadiness(pull).label;
        if (!was) out.push("Pull request #" + pull.number + " opened: " + (pull.title || "untitled"));
        else if (pullReadiness(was).label !== ready) out.push("Pull request #" + pull.number + ": " + ready.toLowerCase());
        else if (was.ci !== pull.ci) out.push("Pull request #" + pull.number + ": " + ciLabels[pull.ci].toLowerCase());
        else if (was.review !== pull.review && reviewLabels[pull.review]) out.push("Pull request #" + pull.number + ": " + reviewLabels[pull.review].toLowerCase());
      }
      for (const number of old.keys()) out.push("Pull request #" + number + " is no longer open");
      const issues = new Map(before.issues.map(issue => [issue.number, issue]));
      for (const issue of after.issues) {
        const was = issues.get(issue.number);
        if (was && was.state !== issue.state) out.push("Issue #" + issue.number + " " + (issue.state === "closed" ? "closed" : "reopened"));
      }
      const seen = new Set(before.recent.map(entry => entry.id));
      for (const entry of after.recent) {
        if (seen.has(entry.id)) continue;
        out.push(entry.commit ? "New commit" + (entry.commit.branch ? " on " + entry.commit.branch : "") + ": " + recentText(entry) : recentText(entry));
      }
      if (before.github.state !== after.github.state) {
        if (after.github.state === "ok") out.push("GitHub updates restored");
        else if (after.github.state === "rate_limited") out.push("GitHub is rate limited" + (after.github.retryAt ? ". Retries at " + clock(after.github.retryAt, now()) : ""));
        else out.push(healthLabels[after.github.state]);
      }
      return out;
    }

    function say(changes) {
      if (!changes.length) return;
      const shown = changes.slice(0, 3);
      if (changes.length > shown.length) shown.push("and " + plural(changes.length - shown.length, "more change"));
      options.announce(shown.join(". ") + ".");
    }

    // Show the activity state. With flash, values that differ from the ones
    // on screen are highlighted and announced. null keeps the last values.
    function showActivity(raw, flash = false) {
      const next = activityState(raw);
      if (!next) return activity;
      const changes = flash && activity ? activityChanges(activity, next) : [];
      activity = next;
      find("activity-body").hidden = false;
      health(next.github);
      const quiet = next.github.state === "disabled" ? "GitHub is not connected." : "";
      reconcile(find("pull-list"), next.pulls, pull => pull.number, buildPull, updatePull, flash, quiet || "No open pull requests.");
      reconcile(find("issue-list"), next.issues, issue => issue.number, buildIssue, updateIssue, flash, quiet || "No issues are bound to this repository’s worktrees.");
      reconcile(find("recent-list"), next.recent, entry => entry.id, buildRecent, updateRecent, flash, "No commits or stopped hooks recorded yet.");
      count("pull-count", next.pulls.length);
      count("issue-count", next.issues.length);
      say(changes);
      schedule();
      return activity;
    }

    function expiry(item) {
      const left = item.expires ? item.expires - now() : 0;
      if (item.expired || (item.expires && left <= 0)) return {label: "Expired", state: "expired", left: 0};
      if (!item.expires) return {label: "", state: "", left: Infinity};
      return {label: "Expires in " + remaining(left), state: left <= 60000 ? "soon" : "", left};
    }

    function buildApproval() {
      const row = element("li", "", "live-row");
      const head = element("div", "", "row-head");
      head.append(slot("action", {tag: "strong", klass: "row-title"}), slot("status", {klass: "tag"}));
      const facts = element("p", "", "row-facts");
      facts.append(slot("expiry", {marked: true}), slot("requested"), slot("class"), slot("client"), slot("source"), slot("id", {tag: "code"}));
      // A plugin's request is approved from the person's own terminal: the
      // command to copy, never a button that approves.
      const plugin = element("div", "", "approval-plugin");
      plugin.dataset.part = "plugin";
      plugin.hidden = true;
      row.append(head, facts, plugin);
      return row;
    }

    function updateApproval(row, item, flash) {
      const due = expiry(item);
      const open = item.state === "pending" || item.state === "approved";
      const [label, tone] = open && due.state === "expired" ? approvalStates.expired : approvalStates[item.state] || [words(item.state) || "Unknown", ""];
      fact(row, "action", actionName(item.action), {flash});
      fact(row, "status", label, {state: tone, flash});
      // The countdown changes every second; it is never highlighted.
      fact(row, "expiry", open && due.state !== "expired" ? due.label : "", {state: due.state || "waiting"});
      fact(row, "requested", item.issued ? "Requested " + clock(item.issued, now()) : "");
      fact(row, "class", classNames[item.klass] || item.klass);
      const asker = scope.AgentflowPlugins?.requester(item.requestedBy) || "";
      fact(row, "client", asker ? "Asked by plugin " + asker : item.client ? "Asked by " + item.client : "");
      const approve = asker && item.state === "pending" && due.state !== "expired" ? scope.AgentflowPlugins.approveCommand(item.id) : "";
      const part = row.querySelector("[data-part=plugin]");
      part.hidden = !approve || !options.command;
      if (!part.hidden && part.dataset.shown !== approve) {
        part.dataset.shown = approve;
        part.replaceChildren(element("p", "A plugin asked for this write. To approve it, run this in your own terminal."), options.command(approve, "Approve command"));
      }
      // The rule and the ID help answer an open request; an outcome needs neither.
      fact(row, "source", open && item.source ? "Rule " + item.source : "");
      fact(row, "id", open ? item.id : "");
    }

    function approvalChanges(before, after) {
      const out = [];
      const open = new Map(before.open.map(item => [item.id, item]));
      for (const item of after.open) {
        const was = open.get(item.id);
        const name = actionName(item.action);
        if (!was) {
          const due = expiry(item);
          out.push("Approval requested: " + name + (due.label ? ". " + due.label : ""));
        } else if (was.state !== item.state && item.state === "approved") out.push(name + " approved");
      }
      const outcomes = {consumed: "used", cancelled: "cancelled", expired: "expired"};
      const known = new Set(before.recent.map(item => item.id));
      for (const item of after.recent) {
        if (known.has(item.id) || !outcomes[item.state] || lapsed.has(item.id)) continue;
        out.push("Approval for " + actionName(item.action) + " " + outcomes[item.state]);
      }
      return out;
    }

    function waiting(state) {
      return state.open.filter(item => item.state === "pending" && expiry(item).state !== "expired").length;
    }

    function showApprovals(raw, flash = false) {
      const next = approvalsState(raw);
      if (!next) return approvals;
      const changes = flash && approvals ? approvalChanges(approvals, next) : [];
      approvals = next;
      find("approvals-body").hidden = false;
      reconcile(find("approval-open"), next.open, item => item.id, buildApproval, updateApproval, flash, "Nothing is waiting for approval.");
      reconcile(find("approval-recent"), next.recent, item => item.id, buildApproval, updateApproval, flash, "No approvals ended in the last 24 hours.");
      const pending = waiting(next);
      find("approval-count").textContent = pending ? pending + " waiting" : "";
      say(changes);
      schedule();
      return approvals;
    }

    // Once a second while something counts down: approval expiry and the
    // GitHub retry time. It announces only the one-minute mark and expiry.
    function tick() {
      if (activity?.github.state === "rate_limited") health(activity.github);
      if (!approvals) return;
      const changes = [];
      for (const item of approvals.open) {
        const row = [...find("approval-open").children].find(node => node.dataset.key === item.id);
        if (!row) continue;
        const due = expiry(item);
        updateApproval(row, item, false);
        if (due.state === "expired" && !lapsed.has(item.id)) {
          lapsed.add(item.id);
          changes.push("Approval for " + actionName(item.action) + " expired");
        } else if (due.state === "soon" && !warned.has(item.id)) {
          warned.add(item.id);
          // An approval that arrives with under a minute left was announced with its time.
          if (due.left > 50000) changes.push("Approval for " + actionName(item.action) + " expires in 1 minute");
        }
      }
      const pending = waiting(approvals);
      find("approval-count").textContent = pending ? pending + " waiting" : "";
      say(changes);
    }

    function counting() {
      const retry = activity?.github.state === "rate_limited" && activity.github.retryAt > now();
      return retry || Boolean(approvals?.open.some(item => expiry(item).state !== "expired"));
    }

    function schedule() {
      if (counting() && !ticker) {
        ticker = setInterval(() => {
          tick();
          if (!counting()) { clearInterval(ticker); ticker = 0; }
        }, 1000);
      }
    }

    return {
      showActivity, showApprovals, tick,
      get activity() { return activity; },
      get approvals() { return approvals; }
    };
  }

  scope.AgentflowPanels = {activityState, approvalsState, pullReadiness, remaining, createPanels};
})(globalThis);
