"use strict";

// The timeline panel (docs/design/ui/live-stream.md, "Timeline"). It shows
// one repository-wide state from the event stream: TimelineState in
// internal/webui/projection_timeline_state.go. Values are read defensively:
// unknown fields are ignored and missing ones read as empty, so the shape
// can grow. Every value is text for display, never markup or an instruction.
(function (scope) {
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const number = value => Number.isSafeInteger(value) && value > 0 ? value : 0;
  const time = value => { const at = Date.parse(text(value)); return Number.isFinite(at) ? at : 0; };
  const pick = (value, known, fallback = "") => known.includes(value) ? value : fallback;
  const words = value => value.replaceAll("_", " ");
  const plural = (count, one, many = one + "s") => count + " " + (count === 1 ? one : many);

  const states = ["requested", "suggested", "awaiting_approval", "prepared", "applied", "failed", "approved", "cancelled", "expired"];
  const stepStates = [...states, "consumed"];
  const linkKinds = ["commit", "branch", "pr", "task", "doc", "release"];
  const pageSize = 10;
  // A request with no result after this long most likely lost its process.
  const stalledAfter = 10 * 60 * 1000;

  // A link is shown only for an absolute https address.
  function outbound(target) {
    try {
      const url = new URL(target);
      return url.protocol === "https:" && !url.username && !url.password ? url.href : "";
    } catch { return ""; }
  }

  function timelineState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const seen = new Set();
    const operations = [];
    for (const op of list(raw.operations).map(record)) {
      if (!op || !text(op.id) || seen.has(op.id)) continue;
      seen.add(op.id);
      const approval = record(op.approval);
      const stepIDs = new Set(), linkIDs = new Set();
      operations.push({
        id: op.id, operation: text(op.operation), action: text(op.action), klass: text(op.class), client: text(op.client),
        worktree: text(op.worktree), subject: text(op.subject), task: text(op.task), branch: text(op.branch),
        state: pick(op.state, states, "unknown"), code: text(op.code),
        approval: approval ? {state: text(approval.state), expires: time(approval.expires_at)} : null,
        links: list(op.links).map(record).filter(link => {
          if (!link || !linkKinds.includes(link.kind) || !text(link.id) || linkIDs.has(link.kind + ":" + link.id)) return false;
          linkIDs.add(link.kind + ":" + link.id);
          return true;
        }).map(link => ({kind: link.kind, id: link.id, url: outbound(text(link.url))})),
        started: time(op.started), updated: time(op.updated) || time(op.started),
        steps: list(op.steps).map(record).filter(step => {
          if (!step || !text(step.event_id) || stepIDs.has(step.event_id)) return false;
          stepIDs.add(step.event_id);
          return true;
        }).map(step => ({id: step.event_id, type: text(step.type), at: time(step.time), state: pick(step.state, stepStates, "unknown"), code: text(step.code), seq: number(step.seq)})),
        truncated: op.truncated === true
      });
    }
    return {operations, truncated: raw.truncated === true};
  }

  // Who ran the operation: the command line, an agent through MCP, or the
  // console itself. kind is what the origin filter selects.
  function origin(client) {
    if (client === "cli") return {kind: "cli", label: "Command line", from: "From the command line"};
    if (client === "daemon") return {kind: "console", label: "Console", from: "From the console"};
    if (!client) return {kind: "unknown", label: "Unknown origin", from: ""};
    return {kind: "agent", label: client, from: "From " + client + " (agent)"};
  }

  // One tag and one mark per operation. The label carries the meaning; the
  // tone and the mark shape repeat it.
  const stateViews = {
    requested: {label: "In progress", tone: "", group: "active"},
    stalled: {label: "No result recorded", tone: "", group: "active"},
    awaiting_approval: {label: "Waiting for approval", tone: "warn", group: "waiting"},
    approved: {label: "Approved, not applied yet", tone: "good", group: "active"},
    prepared: {label: "Ready to apply", tone: "", group: "active"},
    applied: {label: "Applied", tone: "good", group: "applied"},
    failed: {label: "Failed", tone: "bad", group: "failed"},
    suggested: {label: "Suggested only", tone: "", group: "suggested"},
    cancelled: {label: "Cancelled", tone: "", group: "ended"},
    expired: {label: "Approval expired", tone: "bad", group: "ended"},
    unknown: {label: "Unknown state", tone: "", group: ""}
  };

  // Failure and suggestion codes are fixed identifiers (ADR, "Codes, never
  // error text"). Each has a short form for a step and a sentence for the row.
  const codes = {
    approval_expired: ["approval expired", "The approval expired before the operation ran. Ask for a fresh preview."],
    operation_changed: ["request changed", "The request changed after it was approved, or the policy became stricter. Ask for a fresh preview."],
    approval_consumed: ["approval already used", "The approval was already used. Check the earlier result before you retry."],
    approval_cancelled: ["approval cancelled", "The approval was cancelled."],
    approval_pending: ["not approved yet", "The operation was resumed before a person approved it."],
    invalid_token: ["confirmation not valid", "The confirmation token was not valid."],
    cancelled: ["cancelled", "The operation was cancelled before it finished."],
    timeout: ["timed out", "The operation timed out."],
    native_read_required: ["provider read needed", "The agent has to read from its own provider connection first, then run the operation again."],
    invalid: ["input not valid", "The input was not valid, so nothing ran."],
    blocked: ["blocked by a check", "A check blocked the operation, so nothing ran."],
    unexpected_result: ["unexpected result", "The operation returned a result Agentflow did not expect."],
    unknown_status: ["unexpected result", "The operation returned a status Agentflow does not know."],
    error: ["error", "The operation returned an error. The terminal or agent session that ran it shows the details."],
    suggest_only: ["policy suggests only", "The policy only suggests this operation."]
  };
  const plainCode = /^[a-z0-9][a-z0-9_.-]{0,63}$/;

  function codeShort(code) {
    if (!code) return "";
    return codes[code]?.[0] || (plainCode.test(code) ? words(code) : "unknown reason");
  }

  function codeText(state, code) {
    if (state === "suggested") return "The policy only suggests this operation, so nothing ran. Run it yourself if you want it.";
    if (state !== "failed") return "";
    if (!code) return "The operation failed. No reason was recorded.";
    return codes[code]?.[1] || "The operation failed. Reason code: " + (plainCode.test(code) ? code : "not readable") + ".";
  }

  // The label of one step. first is false for a request that resumes the
  // operation after an approval.
  function stepLabel(step, first = true) {
    switch (step.type) {
      case "op.requested": return first ? "Requested" : "Requested again";
      case "op.awaiting_approval": return "Waiting for approval";
      case "approval.requested": return "Approval requested";
    }
    switch (step.state) {
      case "requested": return first ? "Requested" : "Requested again";
      case "awaiting_approval": return "Waiting for approval";
      case "approved": return "Approved";
      case "consumed": return "Approval used";
      case "cancelled": return "Cancelled";
      case "expired": return "Approval expired";
      case "prepared": return "Prepared";
      case "applied": return "Applied";
      case "suggested": return "Suggested, not run";
      case "failed": return "Failed" + (step.code ? ": " + codeShort(step.code) : "");
    }
    return "Unknown step";
  }

  const linkNames = {commit: "Commit", branch: "Branch", pr: "Pull request", task: "Task", doc: "Document", release: "Release"};
  function linkLabel(link) {
    if (link.kind === "commit") return "Commit " + (/^[0-9a-f]{8,64}$/.test(link.id) ? link.id.slice(0, 7) : link.id);
    if (link.kind === "pr") return "Pull request " + (/^\d+$/.test(link.id) ? "#" : "") + link.id;
    return linkNames[link.kind] + " " + link.id;
  }

  const classNames = {local: "local write", remote: "remote write"};

  function sameDay(at, now) {
    return new Date(at).toDateString() === new Date(now).toDateString();
  }
  function clock(at, now, seconds = false) {
    if (!at) return "";
    const format = {hour: "2-digit", minute: "2-digit"};
    if (seconds) format.second = "2-digit";
    if (!sameDay(at, now)) { format.month = "short"; format.day = "numeric"; }
    return new Date(at).toLocaleString([], format);
  }

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }

  // Does op pass the filters? own is this console's worktree ID ("*" when
  // the console serves no single worktree).
  function matches(op, filters, own, now) {
    if (filters.worktree === "own") {
      // An operation of the whole repository belongs to every worktree's view.
      if (own !== "*" && op.worktree && op.worktree !== own) return false;
    } else if (filters.worktree !== "all" && op.worktree !== filters.worktree.slice(3)) return false;
    if (filters.state !== "all" && stateViews[shownState(op, now)].group !== filters.state) return false;
    if (filters.origin !== "all") {
      const from = origin(op.client);
      if (filters.origin.startsWith("client:") ? op.client !== filters.origin.slice(7) : from.kind !== filters.origin) return false;
    }
    return true;
  }

  function shownState(op, now) {
    if (op.state === "requested" && op.updated && now - op.updated > stalledAfter) return "stalled";
    return op.state;
  }

  // options: {root, worktree, announce(message), actionName(action), now(),
  // copy(text)}. root holds the panel markup of index.html.
  function createTimeline(options) {
    const find = id => options.root.querySelector("#" + id);
    const now = () => options.now ? options.now() : Date.now();
    const own = options.worktree || "*";
    const defaults = () => ({worktree: own === "*" ? "all" : "own", state: "all", origin: "all"});
    const timers = new WeakMap();
    let current = null, filters = defaults(), limit = pageSize, live = {state: "connecting"};

    function name(op) {
      const known = options.actionName?.(op.action) || options.actionName?.(op.operation);
      if (known) return known;
      const plain = words(op.operation || op.action).replaceAll(".", " ").trim();
      return plain ? plain[0].toUpperCase() + plain.slice(1) : "Unnamed operation";
    }

    // Highlight a value that changed live (the 2 s rule of the design note).
    function mark(node) {
      clearTimeout(timers.get(node));
      node.classList.remove("live-changed");
      void node.offsetWidth; // restart the highlight when a value changes twice in a row
      node.classList.add("live-changed");
      timers.set(node, setTimeout(() => node.classList.remove("live-changed"), 2000));
    }

    function slot(key, {marked = false, tag = "span", klass = "fact"} = {}) {
      const node = element(tag, "", klass);
      node.dataset.fact = key;
      if (marked) {
        const shape = element("span", "", "state-mark");
        shape.setAttribute("aria-hidden", "true");
        node.append(shape, element("span", "", "fact-text"));
      }
      return node;
    }

    // Set one named value of a row in place; an empty value hides it. It
    // returns true when the text changed.
    function fact(row, key, value, {state, flash = false} = {}) {
      const node = row.querySelector('[data-fact="' + key + '"]');
      const label = node.querySelector(".fact-text") || node;
      const changed = label.textContent !== value;
      if (changed) label.textContent = value;
      node.hidden = !value;
      if (state !== undefined && node.dataset.state !== state) node.dataset.state = state;
      if (changed && flash && value) mark(node);
      return changed;
    }

    // Bring a keyed list in line with items without rebuilding the rows
    // that stay, so focus and running highlights survive an update.
    function reconcile(target, items, keyOf, build, update, flash) {
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
        update?.(row, item, flash && !fresh);
        const place = previous ? previous.nextSibling : target.firstChild;
        if (row !== place) target.insertBefore(row, place);
        if (fresh && flash) mark(row);
        previous = row;
      }
      for (const row of rows.values()) {
        // A row that goes away must not take keyboard focus with it.
        if (row.contains(focused)) find("timeline-title").focus({preventScroll: true});
        row.remove();
      }
      if (focused && focused !== document.activeElement && target.contains(focused)) focused.focus({preventScroll: true});
    }

    function place(worktree) {
      if (!worktree) return "Whole repository";
      return worktree === own ? "This worktree" : "Worktree " + worktree;
    }

    function buildRow(op) {
      const row = element("li", "", "live-row timeline-row");
      const head = element("div", "", "row-head");
      head.append(slot("what", {marked: true, klass: "fact row-title"}), slot("status", {klass: "tag"}));
      const facts = element("p", "", "row-facts");
      facts.append(slot("origin"), slot("worktree"), slot("task"), slot("branch"), slot("when", {tag: "time"}), slot("class"));
      const approval = element("p", "", "timeline-approval");
      approval.dataset.part = "approval";
      const jump = element("button", "Show in Approvals", "timeline-jump");
      jump.type = "button";
      jump.addEventListener("click", () => showApproval(op.id));
      approval.append(slot("approval", {klass: ""}), " ", jump);
      const steps = element("ol", "", "timeline-steps");
      const links = element("ul", "", "timeline-links");
      const audit = element("p", "", "timeline-audit");
      const copy = element("button", "Copy ID", "timeline-jump");
      copy.type = "button";
      copy.addEventListener("click", () => copyID(op.id));
      audit.append(element("span", "Operation ID"), " ", element("code", op.id), " ", copy);
      row.append(head, slot("subject", {tag: "p", klass: "timeline-subject"}), facts, slot("reason", {tag: "p", klass: "row-note"}), approval, steps, links, audit);
      return row;
    }

    function buildStep(step) {
      const item = element("li", "", "timeline-step");
      const label = element("span", "", "fact");
      label.dataset.state = step.state;
      const shape = element("span", "", "state-mark");
      shape.setAttribute("aria-hidden", "true");
      label.append(shape, element("span", step.label, "fact-text"));
      const when = element("time", clock(step.at, now(), true));
      if (step.at) {
        when.dateTime = new Date(step.at).toISOString();
        when.title = new Date(step.at).toLocaleString();
      }
      item.append(label, " ", when);
      return item;
    }

    function buildLink(link) {
      const item = element("li");
      const node = element(link.url ? "a" : "span", linkLabel(link));
      if (link.url) {
        node.href = link.url;
        node.target = "_blank";
        node.rel = "noopener noreferrer";
      }
      item.append(node);
      return item;
    }

    function waitingText(op) {
      if (op.state === "approved") return "A person approved it. The session that asked can now apply it.";
      const expires = op.approval?.expires;
      return "A person has to approve it before it runs." + (expires ? " The approval expires at " + clock(expires, now()) + "." : "");
    }

    function updateRow(row, op, flash) {
      const state = shownState(op, now());
      const view = stateViews[state];
      const title = name(op);
      const from = origin(op.client);
      row.dataset.state = state;
      fact(row, "what", title, {state, flash});
      fact(row, "status", view.label, {state: view.tone, flash});
      fact(row, "subject", op.subject, {flash});
      fact(row, "origin", from.from || from.label);
      fact(row, "worktree", place(op.worktree));
      fact(row, "task", op.task && !op.links.some(link => link.kind === "task") ? "Task " + op.task : "");
      fact(row, "branch", op.branch && !op.links.some(link => link.kind === "branch") ? "on " + op.branch : "");
      const moved = op.updated - op.started >= 60000;
      fact(row, "when", "Started " + clock(op.started, now()) + (moved ? ", last step " + clock(op.updated, now()) : ""));
      if (op.started) row.querySelector("time[data-fact=when]").dateTime = new Date(op.started).toISOString();
      fact(row, "class", classNames[op.klass] || op.klass);
      const reason = codeText(op.state, op.code);
      fact(row, "reason", reason, {flash});
      row.querySelector("[data-fact=reason]").dataset.tone = op.state === "failed" ? "bad" : "";
      const waiting = op.state === "awaiting_approval" || op.state === "approved";
      row.querySelector("[data-part=approval]").hidden = !waiting;
      fact(row.querySelector("[data-part=approval]"), "approval", waiting ? waitingText(op) : "");
      for (const button of row.querySelectorAll(".timeline-jump")) button.setAttribute("aria-label", button.textContent + ": " + title);
      let requests = 0;
      const steps = op.steps.map(step => ({...step, label: stepLabel(step, step.state !== "requested" || requests++ === 0)}));
      const stepList = row.querySelector(".timeline-steps");
      stepList.setAttribute("aria-label", "Steps of " + title);
      reconcile(stepList, steps, step => step.id, buildStep, null, flash);
      if (op.truncated) stepList.prepend(element("li", "Earlier steps are not shown.", "timeline-step muted"));
      const linkList = row.querySelector(".timeline-links");
      linkList.setAttribute("aria-label", "Links of " + title);
      linkList.hidden = !op.links.length;
      reconcile(linkList, op.links, link => link.kind + ":" + link.id + ":" + link.url, buildLink, null, flash);
    }

    // Move to an operation's request in the Approvals panel. The request is
    // answered in the session that asked; the panel shows its details.
    function showApproval(id) {
      const heading = find("approvals-title");
      if (!heading) return;
      const open = find("approval-open");
      const row = open && [...open.children].find(node => node.dataset.key === id);
      (row || heading).scrollIntoView({block: "center"});
      heading.focus({preventScroll: true});
      if (row) mark(row);
      options.announce(row ? "Approvals. The request is highlighted." : "Approvals. This request is not in the open list.");
    }

    async function copyID(id) {
      try {
        await (options.copy ? options.copy(id) : navigator.clipboard.writeText(id));
        options.announce("Operation ID copied.");
      } catch {
        options.announce("The browser did not allow copying. Select the operation ID to copy it.");
      }
    }

    // Keep a select's options in line with choices [[value, label], ...]
    // without touching it when nothing changed (it may be open).
    function sync(select, choices, value) {
      if (!choices.some(([key]) => key === value)) choices.push([value, value.split(":").pop()]);
      const wanted = JSON.stringify(choices);
      if (select.dataset.choices !== wanted) {
        select.dataset.choices = wanted;
        select.replaceChildren(...choices.map(([key, label]) => { const option = element("option", label); option.value = key; return option; }));
      }
      if (select.value !== value) select.value = value;
    }

    function controls() {
      const worktrees = new Set(), clients = new Set();
      let console_ = false;
      for (const op of current.operations) {
        if (op.worktree && op.worktree !== own) worktrees.add(op.worktree);
        const from = origin(op.client);
        if (from.kind === "agent") clients.add(op.client);
        if (from.kind === "console") console_ = true;
      }
      const places = own === "*" ? [["all", "All worktrees"]] : [["own", "This worktree"], ["all", "All worktrees"]];
      for (const id of [...worktrees].sort()) places.push(["wt:" + id, "Worktree " + id]);
      sync(find("timeline-worktree"), places, filters.worktree);
      sync(find("timeline-state"), [["all", "All states"], ["waiting", "Waiting for approval"], ["active", "In progress"], ["applied", "Applied"], ["failed", "Failed"], ["suggested", "Suggested only"], ["ended", "Cancelled or expired"]], filters.state);
      const origins = [["all", "All origins"], ["cli", "Command line"], ["agent", "Agents (MCP)"]];
      if (console_) origins.push(["console", "Console"]);
      if (clients.size > 1) for (const client of [...clients].sort()) origins.push(["client:" + client, "Agent " + client]);
      sync(find("timeline-origin"), origins, filters.origin);
      const initial = defaults();
      find("timeline-reset").hidden = filters.worktree === initial.worktree && filters.state === "all" && filters.origin === "all";
    }

    function summary(shown, matching, total) {
      const scope = matching === total ? "" : " · " + plural(total, "operation") + " in the repository";
      if (!matching) return total ? "No operations match these filters" + scope + "." : "";
      return (shown < matching ? "Showing " + shown + " of " + plural(matching, "operation") : plural(matching, "operation")) + scope + ".";
    }

    function render(flash) {
      find("timeline-body").hidden = false;
      controls();
      const matching = current.operations.filter(op => matches(op, filters, own, now()));
      const shown = matching.slice(0, limit);
      const target = find("timeline-list");
      reconcile(target, shown, op => op.id, buildRow, updateRow, flash);
      const total = current.operations.length;
      const empty = find("timeline-empty");
      empty.textContent = total ? "No operations match these filters." : "No operations recorded in the last 7 days. Run agentflow commit, agentflow pr or an agent tool, and its steps appear here.";
      empty.hidden = Boolean(shown.length);
      target.hidden = !shown.length;
      const line = find("timeline-summary");
      line.textContent = shown.length ? summary(shown.length, matching.length, total) : "";
      line.hidden = !line.textContent;
      const more = find("timeline-more");
      const rest = matching.length - shown.length;
      if (!rest && more.contains(document.activeElement)) find("timeline-title").focus({preventScroll: true});
      more.hidden = !rest;
      more.textContent = "Show " + Math.min(rest, pageSize) + " more";
      find("timeline-count").textContent = total ? String(total) : "";
      find("timeline-truncated").hidden = !current.truncated;
      status(live);
      return {shown: shown.length, matching: matching.length, total};
    }

    // What to say about a live change: the results only. A request and its
    // approval are announced by the Approvals panel.
    function changes(before, after) {
      const old = new Map(before.operations.map(op => [op.id, op]));
      const out = [];
      for (const op of after.operations) {
        if (old.get(op.id)?.state === op.state || !matches(op, filters, own, now())) continue;
        const lead = origin(op.client).label + ": " + name(op);
        if (op.state === "applied") out.push(lead + " applied" + (op.subject ? ": " + op.subject : ""));
        else if (op.state === "failed") out.push(lead + " failed" + (op.code ? ": " + codeShort(op.code) : ""));
        else if (op.state === "suggested") out.push(lead + " was suggested, not run");
      }
      return out;
    }

    function say(sentences) {
      if (!sentences.length) return;
      const shown = sentences.slice(0, 3);
      if (sentences.length > shown.length) shown.push("and " + plural(sentences.length - shown.length, "more change"));
      options.announce(shown.join(". ") + ".");
    }

    // Show the timeline state. With flash, values that differ from the ones
    // on screen are highlighted and results are announced. null keeps the
    // last values.
    function show(raw, flash = false) {
      const next = timelineState(raw);
      if (!next) return current;
      const said = flash && current ? changes(current, next) : [];
      current = next;
      render(flash);
      say(said);
      return current;
    }

    // The stream's state, for the note above the list: {state, offline (a
    // reason's own sentence, or ""), ready, present}.
    function status(next) {
      live = next;
      const shown = Boolean(current);
      let note = "";
      if (live.state === "offline") note = live.offline || "Live updates are offline. " + (shown ? "The timeline shows the last operations it received." : "The timeline appears when they return.") + " The console keeps retrying.";
      else if (live.state === "live" && live.ready && !live.present) note = "The live stream has no timeline for this repository yet.";
      find("timeline-note").textContent = note;
      find("timeline-note").hidden = !note;
      find("timeline-wait").hidden = shown || Boolean(note);
    }

    function refilter() {
      limit = pageSize;
      if (!current) return;
      const {shown, matching, total} = render(false);
      options.announce(summary(shown, matching, total) || "No operations recorded.");
    }

    for (const key of ["worktree", "state", "origin"]) {
      find("timeline-" + key).addEventListener("change", event => {
        filters[key] = event.target.value;
        refilter();
      });
    }
    find("timeline-reset").addEventListener("click", () => {
      filters = defaults();
      find("timeline-worktree").focus();
      refilter();
    });
    find("timeline-more").addEventListener("click", () => {
      limit += pageSize;
      if (current) render(false);
    });
    find("timeline-filters").addEventListener("submit", event => event.preventDefault());

    return {
      show, status, showApproval,
      get timeline() { return current; },
      get filters() { return {...filters}; }
    };
  }

  scope.AgentflowTimeline = {timelineState, origin, codeShort, codeText, stepLabel, linkLabel, matches, createTimeline};
})(globalThis);
