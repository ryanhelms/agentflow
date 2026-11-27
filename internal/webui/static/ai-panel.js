"use strict";

// The AI sessions page (docs/design/ui/ai-panel.md). It shows one
// repository-wide state from the event stream: AIState in
// internal/webui/projection_ai_state.go, this repository's ACP sessions.
// Every value arrives over the stream. The page sends two requests and
// neither answers with data: POST api/ai/respond when the reader answers
// what an agent asks, and POST api/ai/cancel. It starts no session.
//
// Everything a session says is untrusted: the agent's messages, thoughts,
// titles, option names, paths, command lines, diffs and terminal output.
// It is always set as text, never as markup and never as rendered
// Markdown, with control and text-reordering characters replaced. No
// address in it becomes a link. A permission card shows what Agentflow
// resolved in Agentflow's own words, and the agent's wording apart from
// it, marked as the agent's.
//
// Values are read defensively: unknown fields are ignored and missing ones
// read as empty, so the shape can grow.
(function (scope) {
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const count = value => Number.isSafeInteger(value) && value > 0 ? value : 0;
  const time = value => { const at = Date.parse(text(value)); return Number.isFinite(at) && at > 0 ? at : 0; };
  const pick = (value, known, fallback = "") => known.includes(value) ? value : fallback;
  const words = value => value.replaceAll("_", " ");
  const plural = (n, one, many = one + "s") => n + " " + (n === 1 ? one : many);
  const number = value => value.toLocaleString("en-US");

  // Characters that can hide, reorder or disguise text: control and
  // format characters (the bidirectional controls, zero-width characters
  // and tags among them), line separators, private-use and unpaired
  // characters, variation selectors, the fillers that draw nothing, and
  // every space that is not the plain space. The session store removes
  // most of them; the page does not rely on that.
  const hazards = "\\p{Cc}\\p{Cf}\\p{Cs}\\p{Co}\\p{Zl}\\p{Zp}\\u00a0\\u1680\\u2000-\\u200a\\u202f\\u205f\\u3000\\u034f\\u115f\\u1160\\u17b4\\u17b5\\u180b-\\u180f\\u3164\\ufe00-\\ufe0f\\uffa0\\u{e0100}-\\u{e01ef}";
  const unsafe = new RegExp("[" + hazards + "]", "u");
  const unsafeAll = new RegExp("[" + hazards + "]+", "gu");
  // The same set without the line break and the tab, for text of several lines.
  const unsafeInBlock = new RegExp("(?![\\n\\t])[" + hazards + "]", "gu");
  const unsafeBlock = new RegExp("(?![\\n\\t])[" + hazards + "]", "u");
  // One line of display text, bounded.
  const shown = (value, max) => {
    const line = text(value).replace(unsafeAll, " ").replace(/\s+/g, " ").trim();
    return line.length > max ? line.slice(0, max - 1) + "…" : line;
  };
  // Text of several lines, bounded. A character that could hide or reorder
  // text shows as the replacement character, so the reader sees it was there.
  const block = (value, max) => {
    const lines = text(value).replace(/\r\n?/g, "\n").replace(unsafeInBlock, "\ufffd");
    return lines.length > max ? lines.slice(0, max) + "\n…" : lines;
  };
  // A path or a command line the reader decides on is shown complete, on
  // one line. It is never cut silently, because the hidden part could be
  // the part that matters: whole() says whether the page can show a value
  // as it is, and a request with a value it cannot is not offered to allow.
  const whole = (value, max) => typeof value === "string" && value.length <= max && !unsafe.test(value);
  const wholeBlock = (value, max) => typeof value === "string" && value.length <= max && !unsafeBlock.test(value.replace(/\r\n?/g, "\n"));
  const exact = (value, max) => {
    const line = text(value).replace(unsafeAll, "\ufffd");
    return line.length > max ? line.slice(0, max) + "…" : line;
  };
  // Letters outside plain ASCII in a path or a command can look like
  // other letters. They are allowed, and the card points each one out.
  const foreign = value => [...new Set([...value].filter(char => char.codePointAt(0) > 0x7e && char !== "\ufffd" && char !== "…"))].slice(0, 12)
    .map(char => char + " (U+" + char.codePointAt(0).toString(16).toUpperCase().padStart(4, "0") + ")");
  // The agent's words on a permission card: one line, with every emoji
  // written out as "[emoji]", so none can pass for a mark of the console.
  const quoted = (value, max) => shown(shown(value, max).replace(/\p{Extended_Pictographic}/gu, " [emoji] "), max + 200);
  // The program of a POSIX-quoted command line: its first word.
  const program = line => /^(?:'[^']*'|[^\s'])+/.exec(line)?.[0] || "";

  // The session ID (ADR, "Phase 7 session store"). Only IDs of these forms
  // are sent in a request.
  const sessionForm = /^ais_[0-9a-f]{32}$/;
  const askForm = /^ask_[0-9a-f]{16}$/;
  const fingerprintForm = /^[0-9a-f]{64}$/;
  const plainCode = /^[a-z0-9][a-z0-9_.:-]{0,63}$/;
  const currencyForm = /^[A-Z]{3}$/;

  const sessionStates = ["starting", "initializing", "creating", "running", "waiting", "cancelling", "ended"];
  const itemKinds = ["prompt", "message", "thought", "tool", "plan", "usage", "mode", "ask", "answer", "terminal", "file", "unasked", "limit", "info"];
  const askKinds = ["permission", "fs_read", "fs_write", "terminal"];
  const toolKinds = ["read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"];
  const toolStates = ["pending", "in_progress", "completed", "failed"];
  const planStates = ["pending", "in_progress", "completed"];
  const optionKinds = ["allow_once", "allow_always", "reject_once", "reject_always"];

  const armAfter = 1200;      // how long a button waits after it appeared or moved
  const maxDiffRows = 4000;   // the longest change a card shows in full
  const confirmWithin = 10000; // how long "Answer sent" waits for the stream to show it

  // Bounds on what the page draws. The projection has its own (ADR,
  // "Phase 7 AI projection"); these hold when it does not.
  const maxSessions = 20, maxItems = 400, maxTools = 100, maxTerminals = 64, maxAsks = 8, maxPaths = 64, maxFiles = 200;

  function usageState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const amount = typeof raw.cost_amount === "number" && Number.isFinite(raw.cost_amount) && raw.cost_amount > 0 ? raw.cost_amount : 0;
    return {
      used: count(raw.used), size: count(raw.size), cost: amount,
      currency: currencyForm.test(text(raw.cost_currency)) ? raw.cost_currency : "",
      reported: raw.cost_reported === true || amount > 0
    };
  }

  // aisession.Counters, by its JSON names.
  function countersState(raw) {
    raw = record(raw) || {};
    return {
      tools: count(raw.tool_calls), asks: count(raw.asks), allowed: count(raw.allowed),
      denied: count(raw.denied), unasked: count(raw.unasked), terminals: count(raw.terminals)
    };
  }

  const maxPreview = 70000; // the longest side a card shows in full; the ask file holds up to 256 KiB, which only a terminal can allow

  function askState(raw) {
    raw = record(raw);
    const id = text(raw?.id);
    if (!raw || !askForm.test(id)) return null;
    const paths = list(raw.paths), names = list(raw.env_names);
    const options = [];
    for (const option of list(raw.options).map(record)) {
      if (!option || options.length >= 8) continue;
      options.push({name: quoted(option.name, 200), kind: pick(option.kind, optionKinds, "unknown")});
    }
    const kind = pick(raw.kind, askKinds, "unknown");
    const change = record(raw.diff);
    const ask = {
      id, kind, toolKind: pick(raw.tool_kind, toolKinds, raw.tool_kind ? "unknown" : ""),
      title: quoted(raw.title, 600),
      paths: paths.slice(0, maxPaths).map(path => exact(path, 4096)).filter(Boolean), line: exact(raw.line, 16384), cwd: exact(raw.cwd, 4096),
      envNames: names.slice(0, 64).map(name => exact(name, 256)).filter(Boolean),
      options,
      // The change an fs_write would make: the file as it is and as it would be.
      diff: change ? {path: exact(change.path, 4096), before: text(change.old_text), after: text(change.new_text), cut: change.cut === true} : null,
      reason: plainCode.test(text(raw.reason)) ? raw.reason : "",
      expires: time(raw.expires_at),
      fingerprint: fingerprintForm.test(text(raw.fingerprint)) ? raw.fingerprint : ""
    };
    // What the reader decides on must be on the card in full and as it is.
    // blocked says why a request cannot be allowed here: "" when it can.
    const values = kind !== "unknown" && paths.length <= maxPaths && paths.every(path => path !== "" && whole(path, 4096))
      && (raw.line === undefined || whole(raw.line, 16384)) && (raw.cwd === undefined || whole(raw.cwd, 4096))
      && names.length <= 64 && names.every(name => name !== "" && whole(name, 256))
      && (kind !== "terminal" || ask.line !== "");
    ask.blocked = kind === "unknown" ? "kind" : !values ? "values" : !ask.fingerprint ? "fingerprint" : "";
    if (!ask.blocked && kind === "fs_write") {
      // A write is allowed only with the whole change in view.
      if (ask.reason === "preview_too_large" || change?.cut === true) ask.blocked = "large";
      else if (!change) ask.blocked = "nodiff";
      else if (paths.length !== 1 || change.path !== paths[0]) ask.blocked = "values";
      else if ((change.old_text !== undefined && typeof change.old_text !== "string") || typeof change.new_text !== "string") ask.blocked = "nodiff";
      else if (ask.diff.before.length > maxPreview || ask.diff.after.length > maxPreview) ask.blocked = "large";
      else if (!wholeBlock(ask.diff.before, maxPreview) || !wholeBlock(ask.diff.after, maxPreview)) ask.blocked = "text";
      else if (lineDiff(ask.diff.before, ask.diff.after, 3, maxDiffRows).cut) ask.blocked = "large";
    }
    if (!ask.blocked && raw.can_allow !== true) ask.blocked = "option";
    ask.canAllow = ask.blocked === "";
    return ask;
  }

  function sessionState(item) {
    const id = text(item.id);
    if (!sessionForm.test(id)) return null;
    const tools = [], toolIDs = new Set();
    for (const call of list(item.tool_calls).map(record)) {
      const callID = shown(call?.id, 128);
      if (!call || !callID || toolIDs.has(callID) || tools.length >= maxTools) continue;
      toolIDs.add(callID);
      const diffs = [];
      for (const diff of list(call.diffs).map(record)) {
        if (!diff || diffs.length >= 16) continue;
        diffs.push({path: shown(diff.path, 1024), before: text(diff.old_text), after: text(diff.new_text), cut: diff.cut === true});
      }
      tools.push({
        id: callID, title: shown(call.title, 300), kind: pick(call.kind, toolKinds, "other"), status: pick(call.status, toolStates, "pending"),
        locations: list(call.locations).filter(path => typeof path === "string").slice(0, maxPaths).map(path => shown(path, 1024)).filter(Boolean),
        diffs, terminal: shown(call.terminal, 128), asked: call.asked === true, unasked: call.unasked === true
      });
    }
    const terminals = [];
    for (const terminal of list(item.terminals).map(record)) {
      const terminalID = shown(terminal?.id, 128);
      if (!terminal || !terminalID || terminals.length >= maxTerminals) continue;
      terminals.push({
        id: terminalID, line: exact(terminal.line, 16384) || shown(terminal.line, 2000), cwd: shown(terminal.cwd, 1024), output: text(terminal.output),
        exit: Number.isSafeInteger(terminal.exit) ? terminal.exit : null, signal: shown(terminal.signal, 32)
      });
    }
    const items = [];
    let last = -1;
    for (const entry of list(item.items).map(record)) {
      // Items come oldest first; one out of order is left out.
      if (!entry || !Number.isSafeInteger(entry.seq) || entry.seq <= last) continue;
      last = entry.seq;
      const kind = pick(entry.kind, itemKinds, "unknown");
      const toolCall = shown(entry.tool_call, 128);
      items.push({seq: entry.seq, at: time(entry.time), kind, text: kind === "message" || kind === "thought" || kind === "prompt" ? text(entry.text) : shown(entry.text, 1000), toolCall});
    }
    const asks = [], askIDs = new Set();
    for (const raw of list(item.asks)) {
      const ask = askState(raw);
      if (!ask || askIDs.has(ask.id) || asks.length >= maxAsks) continue;
      askIDs.add(ask.id);
      asks.push(ask);
    }
    const plan = [];
    for (const entry of list(item.plan).map(record)) {
      if (!entry || plan.length >= 100 || !shown(entry.content, 600)) continue;
      plan.push({content: shown(entry.content, 600), priority: pick(entry.priority, ["high", "medium", "low"]), status: pick(entry.status, planStates, "pending")});
    }
    const state = pick(item.state, sessionStates, "unknown");
    return {
      id, harness: shown(item.harness, 64), name: shown(item.harness_name, 64) || shown(item.harness, 64) || "Harness",
      worktree: shown(item.worktree, 1024), worktreeID: shown(item.worktree_id, 64), branch: shown(item.branch, 256),
      state, code: plainCode.test(text(item.code)) ? item.code : "",
      startedBy: pick(item.started_by, ["person", "agent"]), client: shown(item.client, 64), ceiling: pick(item.ceiling, ["confirm"]),
      home: pick(item.home, ["real", "isolated"]),
      created: time(item.created_at), ended: time(item.ended_at),
      prompt: text(item.prompt), usage: usageState(item.usage), plan,
      items: items.slice(-maxItems), truncated: item.truncated === true || items.length > maxItems,
      tools, terminals,
      // A session that ended waits for nobody.
      asks: state === "ended" ? [] : asks,
      changed: list(item.changed_files).filter(path => typeof path === "string").slice(0, maxFiles).map(path => shown(path, 1024)).filter(Boolean),
      counters: countersState(item.counters)
    };
  }

  function aiState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const sessions = [], seen = new Set();
    for (const item of list(raw.sessions).map(record)) {
      const session = item && sessionState(item);
      if (!session || seen.has(session.id) || sessions.length >= maxSessions) continue;
      seen.add(session.id);
      sessions.push(session);
    }
    return {sessions, running: count(raw.running), maxRunning: count(raw.max_running)};
  }

  // One tag per session. The label carries the meaning; the tone and the
  // mark's shape repeat it.
  const endings = {
    end_turn: ["Finished", "ok", "The agent finished its turn."],
    max_tokens: ["Stopped", "warn", "The model reached its token limit before the agent finished."],
    max_turn_requests: ["Stopped", "warn", "The agent reached the harness's limit of model requests in one turn."],
    refusal: ["Refused", "warn", "The model refused to continue."],
    cancelled: ["Cancelled", "neutral", "The session was cancelled."],
    limit_duration: ["Stopped", "warn", "The session reached its time limit, so Agentflow cancelled it."],
    limit_idle: ["Stopped", "warn", "Nothing came from the agent for too long, so Agentflow cancelled it."],
    limit_output: ["Stopped", "warn", "The session produced more output than Agentflow keeps, so Agentflow cancelled it."],
    unasked: ["Stopped", "bad", "The harness changed something outside the worktree, in .git or in a control file without asking, so Agentflow cancelled it."],
    launch: ["Failed", "bad", "The harness did not start."],
    protocol: ["Failed", "bad", "The harness sent something that is not ACP, or closed the connection."],
    protocol_version: ["Failed", "bad", "The harness speaks an ACP version Agentflow does not support."],
    timeout: ["Failed", "bad", "The harness did not answer in time while the session started."],
    auth_required: ["Failed", "bad", "The harness needs a sign-in. Sign in to it, then start the session again."],
    exited: ["Failed", "bad", "The harness exited before the turn ended."],
    host_lost: ["Failed", "bad", "The process that hosted this session stopped."]
  };
  const starting = {starting: "Starting the harness.", initializing: "Checking that the harness speaks ACP.", creating: "Opening the session in the harness."};
  function stateView(session) {
    const code = session.code ? " Reason code " + session.code + "." : "";
    if (session.state === "ended") {
      const [label, tone, detail] = endings[session.code] || ["Ended", "neutral", "The session ended." + code];
      return {label, tone, detail, live: false};
    }
    if (session.state === "running") return {label: "Running", tone: "info", detail: "The agent is working.", live: true};
    if (session.state === "waiting") return {label: "Waiting for you", tone: "warn", detail: "The agent asked for permission and waits for an answer.", live: true};
    if (session.state === "cancelling") return {label: "Cancelling", tone: "warn", detail: (endings[session.code]?.[2] || "The session is being cancelled.") + " It stops within a few seconds.", live: true};
    if (starting[session.state]) return {label: "Starting", tone: "info", detail: starting[session.state], live: true};
    return {label: "Unknown state", tone: "neutral", detail: "This console does not know this session state. Update Agentflow." + code, live: false};
  }
  // Cancel is offered while the turn can still be cancelled.
  const cancellable = session => ["starting", "initializing", "creating", "running", "waiting"].includes(session.state);

  // What is asked, in Agentflow's words. For fs_read, fs_write and
  // terminal Agentflow resolved the request itself. A "permission" is the
  // harness asking about one of its own tools: Agentflow knows only what
  // the agent says about it, so the card never words it as a finding.
  const ownTool = "The agent asks to use one of its own tools";
  const toolSays = {
    read: "read files", search: "search files", think: "think", edit: "change files", delete: "delete files", move: "move files",
    execute: "run a command", fetch: "fetch from the network", switch_mode: "change its mode", other: "something it did not name",
    unknown: "an action this console does not know"
  };
  function askHeading(ask) {
    if (ask.kind === "fs_write") return "Write a file";
    if (ask.kind === "fs_read") return "Read a file";
    if (ask.kind === "terminal") return "Run a command";
    if (ask.kind === "permission") return ownTool;
    return "A request this console does not know";
  }
  // Who does it once it is allowed (ADR, "what agentflow controls").
  function askEffect(ask) {
    if (ask.kind === "fs_write") return "If you allow it, Agentflow writes this file itself, in the session's worktree.";
    if (ask.kind === "fs_read") return "If you allow it, Agentflow reads this file itself and gives its content to the agent.";
    if (ask.kind === "terminal") return "If you allow it, Agentflow starts this command itself, in the session's worktree. What the command then does is not limited.";
    if (ask.kind === "permission") return "The harness does this itself, with its own tools. Agentflow passes your answer on and cannot check what happens then.";
    return "This console cannot show this request, so it can only be rejected. Update Agentflow.";
  }
  const reasons = {
    control_file: "It changes a file that controls an agent or this repository's automation. That always asks you.",
    secret_file: "The file may hold secrets, and what the agent reads goes to the harness's model provider. That always asks you.",
    terminal: "Commands always ask you, unless one of your rules allows the program.",
    repository_program: "The program is a file inside the worktree, so no rule can allow it.",
    shell: "The program is a shell or a wrapper that runs other commands, so no rule can allow it.",
    no_locations: "The agent did not say which files this touches.",
    kind: "This kind of request always asks you.",
    rule: "A permission rule says to ask you.",
    gate: "Your autonomy settings say to confirm this.",
    ceiling: "An agent started this session, so it cannot do this without you.",
    preview_too_large: "The change is too large to show here."
  };
  const reasonText = reason => reasons[reason] || (reason ? "Reason code " + reason + "." : "Agentflow did not say why.");
  const optionNames = {allow_once: "allow once", allow_always: "allow always", reject_once: "reject once", reject_always: "reject always", unknown: "a kind this console does not know"};
  // Why a request has no Allow once (ask.blocked).
  const blockedText = {
    kind: "This console does not know this kind of request, so it cannot be allowed here. You can reject it.",
    values: "This console cannot show the whole request as it is, so it cannot be allowed here. You can reject it.",
    fingerprint: "The request arrived without its check value, so it cannot be allowed here. You can reject it.",
    nodiff: "Agentflow did not send the change this would write, so it cannot be allowed here. You can reject it.",
    large: "The change is too large to show in full, so it cannot be allowed here. You can reject it.",
    text: "The change has characters this page cannot show as they are (marked \ufffd), so it cannot be allowed here. You can reject it.",
    option: "This request cannot be allowed: the agent offered no “allow once” choice. You can reject it, or it is rejected when the time runs out."
  };

  const toolNames = {read: "Read", edit: "Edit", delete: "Delete", move: "Move", search: "Search", execute: "Run", think: "Think", fetch: "Fetch", switch_mode: "Switch mode", other: "Tool"};
  const toolStatus = {pending: ["Waiting", "neutral"], in_progress: ["Running", "info"], completed: ["Done", "ok"], failed: ["Failed", "bad"]};
  const planStatus = {pending: "Not started", in_progress: "In progress", completed: "Done"};
  const itemLabels = {prompt: "Prompt", mode: "Mode", ask: "Asked", answer: "Answered", terminal: "Command", file: "File", unasked: "Ran without asking", limit: "Limit", info: "Note", plan: "Plan", unknown: "Update"};

  function sameDay(at, now) {
    return new Date(at).toDateString() === new Date(now).toDateString();
  }
  function clock(at, now) {
    if (!at) return "";
    const format = {hour: "2-digit", minute: "2-digit"};
    if (!sameDay(at, now)) { format.month = "short"; format.day = "numeric"; }
    return new Date(at).toLocaleString([], format);
  }
  // "1 min 05 s", as the approvals countdown reads.
  function remaining(ms) {
    const total = Math.max(0, Math.ceil(ms / 1000));
    const minutes = Math.floor(total / 60), seconds = total % 60;
    return minutes ? minutes + " min " + String(seconds).padStart(2, "0") + " s" : seconds + " s";
  }

  // The lines of a change: unchanged lines around it, "-" lines taken out
  // and "+" lines put in. Long unchanged stretches are left out and
  // counted. Texts too large to compare line by line show as all of the
  // old lines, then all of the new ones.
  function lineDiff(before, after, context = 3, limit = 600) {
    // A final line break ends the last line; it is not a line of its own.
    const split = value => value === "" ? [] : value.replace(/\n$/, "").split("\n");
    const a = split(before), b = split(after);
    let head = 0;
    while (head < a.length && head < b.length && a[head] === b[head]) head++;
    let tail = 0;
    while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail++;
    const left = a.slice(head, a.length - tail), right = b.slice(head, b.length - tail);
    const rows = [];
    const skip = n => { if (n > 0) rows.push({op: "…", text: plural(n, "unchanged line") + " not shown"}); };
    skip(head - context);
    for (const line of a.slice(Math.max(0, head - context), head)) rows.push({op: " ", text: line});
    if (left.length * right.length <= 250000) {
      // Longest common subsequence of the lines that differ.
      const width = right.length + 1, table = new Uint16Array((left.length + 1) * width);
      for (let i = left.length - 1; i >= 0; i--) {
        for (let j = right.length - 1; j >= 0; j--) {
          table[i * width + j] = left[i] === right[j] ? table[(i + 1) * width + j + 1] + 1 : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
        }
      }
      let i = 0, j = 0;
      while (i < left.length || j < right.length) {
        if (i < left.length && j < right.length && left[i] === right[j]) { rows.push({op: " ", text: left[i]}); i++; j++; }
        else if (i < left.length && (j >= right.length || table[(i + 1) * width + j] >= table[i * width + j + 1])) rows.push({op: "-", text: left[i++]});
        else rows.push({op: "+", text: right[j++]});
      }
    } else {
      for (const line of left) rows.push({op: "-", text: line});
      for (const line of right) rows.push({op: "+", text: line});
    }
    for (const line of a.slice(a.length - tail, a.length - tail + context)) rows.push({op: " ", text: line});
    skip(tail - context);
    if (rows.length <= limit) return rows;
    const cut = [...rows.slice(0, limit), {op: "…", text: plural(rows.length - limit, "more line") + " not shown"}];
    cut.cut = true;
    return cut;
  }

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }

  // A tag with a state mark: {label, tone}.
  function stateTag(label, tone, klass = "") {
    const tag = element("span", "", ("tag ai-state " + klass).trim());
    tag.dataset.tone = tone;
    const mark = element("span", "", "state-mark");
    mark.setAttribute("aria-hidden", "true");
    tag.append(mark, element("span", label, "ai-state-text"));
    return tag;
  }
  function setTag(tag, label, tone) {
    const label_ = tag.querySelector(".ai-state-text");
    const changed = label_.textContent !== label;
    if (changed) label_.textContent = label;
    if (tag.dataset.tone !== tone) tag.dataset.tone = tone;
    return changed;
  }

  // options: {root, announce(message), request(path, body), visible(),
  // now(), copy(text), arm (ms)}. root holds the page markup of index.html.
  function createAI(options) {
    const find = id => options.root.querySelector("#" + id);
    const now = () => options.now ? options.now() : Date.now();
    const open = () => !options.visible || options.visible();
    const armFor = Number.isFinite(options.arm) ? options.arm : armAfter;
    const timers = new WeakMap();
    const rows = new Map();   // session ID -> its row in the list
    const cards = new Map();  // ask key -> its card, in the order the asks arrived
    const blocks = new Map(); // stream block key -> its node
    const known = new Set();  // asks already announced
    let current = null, live = {state: "connecting"}, selected = "", shownSession = "", ticker = 0, armTimer = 0, watching = 0;
    let follow = true, unread = 0, confirming = false, cancel = {session: "", state: "", message: ""};

    function mark(node) {
      clearTimeout(timers.get(node));
      node.classList.remove("live-changed");
      void node.offsetWidth; // restart the highlight when a value changes twice in a row
      node.classList.add("live-changed");
      timers.set(node, setTimeout(() => node.classList.remove("live-changed"), 2000));
    }
    function put(node, value) {
      if (node.textContent !== value) node.textContent = value;
      return node;
    }
    // Replace a part only when what it shows changed, so focus and open
    // details in a part that did not change survive an update.
    function fill(target, signature, build) {
      if (target.dataset.shown === signature) return false;
      target.dataset.shown = signature;
      target.replaceChildren(...build());
      return true;
    }
    const session = () => current?.sessions.find(item => item.id === selected) || null;

    async function copy(value, button, what) {
      try {
        await (options.copy ? options.copy(value) : navigator.clipboard.writeText(value));
        options.announce(what + " copied.");
        const label = button.querySelector(".ai-copy-label");
        clearTimeout(timers.get(button));
        label.textContent = "Copied";
        timers.set(button, setTimeout(() => { label.textContent = "Copy"; }, 2000));
      } catch {
        options.announce("The browser did not allow copying. Select the command to copy it.");
      }
    }
    // A terminal command the reader copies: the text and one Copy button.
    // It is shown, never run.
    function command(value, what) {
      const box = element("div", "", "ai-command");
      const code = element("code", value);
      const pre = element("pre");
      pre.tabIndex = 0;
      pre.setAttribute("role", "group");
      pre.setAttribute("aria-label", what);
      pre.append(code);
      const button = element("button", "", "button secondary small");
      button.type = "button";
      button.append(element("span", "Copy", "ai-copy-label"));
      button.setAttribute("aria-label", "Copy the command: " + value);
      button.addEventListener("click", () => copy(code.textContent, button, what));
      box.append(pre, button);
      return box;
    }
    function term(label, ...values) {
      const group = element("div");
      const value = element("dd");
      value.append(...values);
      group.append(element("dt", label), value);
      return group;
    }
    // Text of several lines in a box that scrolls. It takes focus so the
    // keyboard can scroll it.
    function scroller(value, what, klass = "") {
      const pre = element("pre", value, ("ai-text " + klass).trim());
      pre.tabIndex = 0;
      pre.setAttribute("role", "group");
      pre.setAttribute("aria-label", what);
      return pre;
    }
    // Details whose content is built when they are first opened, so a long
    // diff or output costs nothing while it is closed.
    function lazy(key, summary, build) {
      const details = element("details", "", "ai-more");
      details.dataset.key = key;
      details.append(element("summary", summary));
      // The toggle event comes later; a block that reopens its details
      // fills them at once, so the stream's height is settled.
      details.fillNow = () => { if (details.children.length === 1) details.append(...build()); };
      details.addEventListener("toggle", () => { if (details.open) details.fillNow(); });
      return details;
    }

    // ---- The session list ----

    function startedText(item) {
      const who = item.startedBy === "agent" ? "Started by an agent" : item.startedBy === "person" ? "Started by you" : "Started";
      return who + (item.created ? " at " + clock(item.created, now()) : "");
    }
    function buildRow(item) {
      const row = element("li");
      const button = element("button", "", "ai-row");
      button.type = "button";
      button.dataset.session = item.id;
      const head = element("span", "", "ai-row-head");
      head.append(element("strong", "", "ai-row-name"), stateTag("", "neutral"));
      const asks = element("span", "", "ai-row-asks");
      button.append(head, asks, element("span", "", "ai-row-start"), element("code", "", "ai-row-branch"), element("span", "", "ai-row-path"));
      button.addEventListener("click", () => select(item.id, true));
      row.append(button);
      return row;
    }
    function renderList(flash, fresh) {
      const target = find("ai-session-list");
      const kept = new Set(current.sessions.map(item => item.id));
      for (const [id, row] of rows) {
        if (kept.has(id)) continue;
        if (row.contains(document.activeElement)) find("ai-title").focus({preventScroll: true});
        row.remove();
        rows.delete(id);
      }
      let before = null;
      const focused = document.activeElement;
      for (const item of current.sessions) {
        let row = rows.get(item.id);
        if (!row) { row = buildRow(item); rows.set(item.id, row); }
        const button = row.firstChild;
        const view = stateView(item);
        put(button.querySelector(".ai-row-name"), item.name);
        const tag = button.querySelector(".ai-state");
        if (setTag(tag, view.label, view.tone) && flash && !fresh.has(item.id)) mark(tag);
        const asks = button.querySelector(".ai-row-asks");
        put(asks, item.asks.length ? plural(item.asks.length, "request waits", "requests wait") + " for your answer" : "");
        asks.hidden = !item.asks.length;
        put(button.querySelector(".ai-row-start"), startedText(item));
        const branch = button.querySelector(".ai-row-branch");
        put(branch, item.branch);
        branch.hidden = !item.branch;
        put(button.querySelector(".ai-row-path"), item.worktree);
        if (item.id === selected) button.setAttribute("aria-current", "true");
        else button.removeAttribute("aria-current");
        const next = before ? before.nextSibling : target.firstChild;
        if (row !== next) target.insertBefore(row, next);
        if (flash && fresh.has(item.id)) mark(row);
        before = row;
      }
      if (focused && focused !== document.activeElement && target.contains(focused)) focused.focus({preventScroll: true});
    }

    function select(id, byReader) {
      if (selected === id) return;
      selected = id;
      confirming = false;
      renderList(false, new Set());
      renderSession(false);
      tick();
      if (byReader) {
        find("ai-session-title").focus({preventScroll: true});
        find("ai-session").scrollIntoView({block: "nearest"});
      }
    }

    // ---- Permission cards ----

    const askKey = (item, ask) => item.id + "/" + ask.id + "/" + ask.fingerprint;
    const frame = callback => (options.view || scope).requestAnimationFrame(callback);

    // A button answers only when it has stood still for a moment. Whatever
    // moves it (a card above that goes away, a new session in the list, a
    // note that appears) could put Allow once under a click aimed at
    // something else. place() reads where a card's buttons are on the page.
    function place(card) {
      const rect = card.querySelector(".ai-ask-actions").getBoundingClientRect();
      const view = options.view || scope;
      return Math.round(rect.left + view.scrollX) + ":" + Math.round(rect.top + view.scrollY) + ":" + Math.round(rect.width);
    }
    // Note the cards whose buttons moved since the last look. Reading a
    // position needs the page on screen.
    function settle() {
      if (!open()) return false;
      const at = now();
      let moved = false;
      for (const card of cards.values()) {
        const where = place(card);
        if (card.place === where) continue;
        card.place = where;
        card.armed = at + armFor;
        moved = true;
      }
      if (moved) {
        clearTimeout(armTimer);
        armTimer = setTimeout(renderCards, armFor + 20);
        renderCards();
      }
      return moved;
    }
    // Look once a frame while a card is on screen, and not otherwise.
    function watch() {
      watching = 0;
      if (!counting()) return;
      settle();
      watching = frame(watch);
    }

    // The agent's words: one paragraph, muted, two lines until the reader
    // asks for all of it. The label above it is the stronger text.
    function quote(value) {
      const out = [element("blockquote", value, "ai-quote")];
      if (value.length > 140) {
        const more = element("button", "Show all", "ai-quote-more");
        more.type = "button";
        more.setAttribute("aria-expanded", "false");
        out[0].dataset.clamped = "true";
        more.addEventListener("click", () => {
          const all = more.getAttribute("aria-expanded") !== "true";
          more.setAttribute("aria-expanded", String(all));
          more.textContent = all ? "Show less" : "Show all";
          out[0].dataset.clamped = String(!all);
        });
        out.push(more);
      }
      return out;
    }
    function codes(values) {
      const items = element("ul", "", "ai-paths");
      for (const value of values) items.appendChild(element("li")).append(element("code", value, "ai-exact"));
      return items;
    }
    function choices(ask) {
      const out = [element("p", "The agent's names for its choices. Agentflow answers only with “allow once” or “reject”, whatever a name says.", "ai-said-note")];
      const rows = element("ul", "", "ai-choices");
      for (const option of ask.options) {
        const row = element("li");
        row.append(element("q", option.name || "no name"), " (" + optionNames[option.kind] + ")");
        rows.append(row);
      }
      out.push(rows);
      return out;
    }
    // The change a write would make, in a box that scrolls.
    function change(ask) {
      const diff = ask.diff;
      const rows = lineDiff(block(diff.before, maxPreview), block(diff.after, maxPreview), 3, maxDiffRows);
      const added = rows.filter(row => row.op === "+").length, removed = rows.filter(row => row.op === "-").length;
      const pre = scroller("", "The change to " + diff.path, "ai-diff ai-ask-diff");
      for (const row of rows) {
        const line = element("span", "", "ai-diff-row");
        line.dataset.op = row.op === "+" ? "add" : row.op === "-" ? "remove" : row.op === "…" ? "skip" : "same";
        line.append(element("span", row.op === " " ? "\u00a0" : row.op, "ai-diff-op"), element("span", row.text === "" ? "\u00a0" : row.text));
        pre.append(line);
      }
      const count = element("p", !diff.before && !diff.after ? "The file is empty, before and after." : !added && !removed ? "No line changes." : plural(added, "line") + " put in, " + plural(removed, "line") + " taken out.", "ai-ask-count");
      return [count, pre];
    }

    function buildCard(item, ask) {
      const card = element("li", "", "ai-ask");
      card.dataset.kind = ask.kind;
      const own = ask.kind === "permission";
      const heading = element("h4", askHeading(ask), "ai-ask-title");
      heading.id = "ai-ask-" + ask.id + "-title";
      heading.tabIndex = -1;
      card.setAttribute("aria-labelledby", heading.id);
      card.append(heading);

      if (own) {
        // The harness asks about a tool of its own. Everything known about
        // it is what the agent says, so all of it goes in the agent's box.
        card.append(element("p", askEffect(ask), "ai-ask-warn"));
        const said = element("div", "", "ai-said");
        said.append(element("p", "What the agent says it will do. Agentflow cannot check it.", "ai-said-label"));
        const claims = element("dl", "", "ai-ask-facts ai-claims");
        claims.append(term("It says it will", toolSays[ask.toolKind] || toolSays.other));
        if (ask.paths.length) claims.append(term(ask.paths.length === 1 ? "On this file" : "On these files", codes(ask.paths)));
        said.append(claims);
        if (ask.title) said.append(...quote(ask.title));
        if (ask.options.length) said.append(...choices(ask));
        const about = element("dl", "", "ai-ask-facts ai-ask-about");
        about.append(term("Session worktree", element("code", item.worktree || "not reported", "ai-exact")), term("Why it asks", reasonText(ask.reason)));
        card.append(said, about);
      } else {
        // The agent's wording first, marked as the agent's; then what
        // Agentflow resolved, next to the buttons.
        if (ask.title) {
          const said = element("div", "", "ai-said");
          said.append(element("p", "Written by the agent. Agentflow did not check this text.", "ai-said-label"), ...quote(ask.title));
          card.append(said);
        }
        const facts = element("dl", "", "ai-ask-facts");
        if (ask.line) {
          // A long command hides its program in the crowd: name it first.
          const long = ask.line.length > 80;
          if (long && program(ask.line)) facts.append(term("Program", element("code", program(ask.line), "ai-exact")));
          facts.append(term(long ? "Command, " + number(ask.line.length) + " characters" : "Command", element("code", ask.line, "ai-exact")));
        }
        if (ask.paths.length) facts.append(term(ask.paths.length === 1 ? "File" : "Files", codes(ask.paths)));
        // A change that arrived without its new text is not drawn: it would read as "nothing changes".
        if (ask.kind === "fs_write" && ask.diff && ask.blocked !== "nodiff") facts.append(term("Change", ...change(ask)));
        if (ask.kind === "terminal" || ask.cwd) facts.append(term("Runs in", ask.cwd ? element("code", ask.cwd, "ai-exact") : "the worktree's top folder"));
        if (ask.envNames.length) facts.append(term("Sets variables", codes(ask.envNames), element("p", "The agent set values for these. Agentflow does not show or keep the values.", "muted")));
        facts.append(term("Worktree", element("code", item.worktree || "not reported", "ai-exact")));
        facts.append(term("Why it asks", reasonText(ask.reason)));
        const resolved = element("div", "", "ai-ask-resolved");
        resolved.append(element("p", "What Agentflow checked", "ai-ask-label"), facts);
        const odd = foreign([...ask.paths, ask.cwd, ask.line].join(""));
        if (odd.length) resolved.append(element("p", "Look closely: the " + (ask.line ? "command" : "path") + " has characters that are not plain English letters, digits or signs, and some look like other letters: " + odd.join(", ") + ".", "ai-ask-odd"));
        resolved.append(element("p", askEffect(ask), "ai-ask-effect"));
        card.append(resolved);
      }
      if (ask.blocked) card.append(element("p", blockedText[ask.blocked], "ai-ask-fixed"));

      const left = element("p", "", "ai-ask-time");
      left.dataset.part = "time";
      const offline = element("p", "Live updates are offline, so this request may already be answered or out of time.", "ai-ask-offline");
      offline.dataset.part = "offline";
      offline.hidden = true;
      const status = element("p", "", "ai-ask-status");
      status.dataset.part = "status";
      status.id = "ai-ask-" + ask.id + "-status";
      const actions = element("div", "", "ai-ask-actions");
      // Two buttons of equal weight at opposite ends. Their names are
      // short; the card's heading and status line describe them.
      for (const [decision, name] of ask.canAllow ? [["reject", "Reject"], ["allow", "Allow once"]] : [["reject", "Reject"]]) {
        const button = element("button", name, "button secondary");
        button.type = "button";
        button.dataset.decision = decision;
        button.setAttribute("aria-describedby", heading.id + " " + status.id);
        button.addEventListener("click", () => answer(card, decision));
        actions.append(button);
      }
      card.append(left, offline, actions, status);
      card.ask = ask;
      card.session = item.id;
      card.pending = "";
      card.failure = "";
      card.sent = 0;
      card.armed = now() + armFor;
      card.place = "";
      card.said = "";
      return card;
    }

    async function answer(card, decision) {
      const ask = card.ask;
      if (!options.request || !cards.has(card.key)) return;
      if (card.pending) { options.announce("Your answer is on its way."); return; }
      if (ask.expires && now() >= ask.expires) { options.announce("The time to answer ran out."); return; }
      // Look again now: the button may have moved since the last frame.
      settle();
      if (now() < card.armed) { options.announce("The buttons just appeared or moved. Read the request, then answer."); return; }
      if (decision === "allow" && !ask.canAllow) return;
      card.pending = decision;
      card.failure = "";
      card.sent = now();
      renderCards();
      tick();
      try {
        await options.request("api/ai/respond", {session: card.session, ask: ask.id, decision, fingerprint: ask.fingerprint});
        options.announce(decision === "allow" ? "Allow once sent." : "Reject sent.");
      } catch (error) {
        card.pending = "";
        card.failure = "Your answer was not sent. " + (shown(error?.message, 200) || "The console did not say why.");
        options.announce(card.failure);
      }
      renderCards();
      tick();
    }

    function renderCards() {
      const at = now();
      for (const card of cards.values()) {
        const ask = card.ask;
        const left = ask.expires ? ask.expires - at : Infinity;
        const expired = left <= 0;
        // An answer the stream has not confirmed in time can be given again.
        if (card.pending && at - card.sent > confirmWithin) { card.pending = ""; card.failure = "The session has not confirmed your answer yet. Your first answer may still count. You can answer again."; }
        const time = card.querySelector('[data-part="time"]');
        put(time, expired ? "The time to answer ran out. Agentflow rejects the request."
          : ask.expires ? "Answer within " + remaining(left) + ". After that, Agentflow rejects it." : "");
        time.hidden = !time.textContent;
        time.dataset.state = expired ? "over" : left <= 60000 ? "soon" : "";
        // The clock is said twice only: at the last minute and at the end.
        const moment = expired ? "over" : left <= 60000 ? "soon" : "";
        if (card.seen && moment && card.said !== moment) {
          options.announce(moment === "over" ? "The time to answer ran out: " + askHeading(ask).toLowerCase() + "." : "One minute left to answer: " + askHeading(ask).toLowerCase() + ".");
        }
        card.said = moment;
        card.seen = true;
        const waiting = !expired && !card.pending && at < card.armed;
        const said = card.pending === "allow" ? "Allow once sent. Waiting for the session to confirm."
          : card.pending === "reject" ? "Reject sent. Waiting for the session to confirm."
          : card.failure;
        const status = card.querySelector('[data-part="status"]');
        // The line keeps its room when empty, so an answer on one card
        // does not move the cards below it.
        put(status, said);
        status.dataset.state = card.failure && !card.pending ? "bad" : "";
        card.querySelector('[data-part="offline"]').hidden = live.state !== "offline";
        const off = expired || Boolean(card.pending) || waiting;
        for (const button of card.querySelectorAll(".ai-ask-actions button")) {
          // A button that waits stays in the tab order; pressing it says why.
          if (off) button.setAttribute("aria-disabled", "true");
          else button.removeAttribute("aria-disabled");
        }
      }
    }

    function reconcileCards(item, flash) {
      const target = find("ai-ask-list");
      const wanted = new Map(item ? item.asks.map(ask => [askKey(item, ask), ask]) : []);
      for (const [key, card] of cards) {
        if (wanted.has(key)) continue;
        // A card that goes away must not take keyboard focus with it.
        if (card.contains(document.activeElement)) find("ai-session-title").focus({preventScroll: true});
        card.remove();
        cards.delete(key);
      }
      for (const [key, ask] of wanted) {
        if (cards.has(key)) { cards.get(key).ask.expires = ask.expires; continue; }
        const card = buildCard(item, ask);
        card.key = key;
        cards.set(key, card);
        // New cards go below the ones already there, so those do not move.
        target.append(card);
        if (flash) mark(card);
      }
      const any = cards.size > 0;
      find("ai-asks").hidden = !any;
      put(find("ai-asks-title"), any ? plural(cards.size, "request waits", "requests wait") + " for your answer" : "");
      // Cards that moved up or down wait again before they answer.
      if (!settle()) renderCards();
      clearTimeout(armTimer);
      if (any) armTimer = setTimeout(renderCards, armFor + 20);
    }

    // ---- The stream of items ----

    function streamBlocks(item) {
      const tools = new Map(item.tools.map(call => [call.id, call]));
      const terminals = new Map(item.terminals.map(terminal => [terminal.id, terminal]));
      const out = [], drawn = new Set();
      for (const entry of item.items) {
        // The prompt, the plan and usage have their own places on the page.
        if (entry.kind === "usage" || entry.kind === "prompt") continue;
        const last = out[out.length - 1];
        if (entry.kind === "message" || entry.kind === "thought") {
          // The agent sends text in pieces; pieces in a row are one text.
          if (last && last.kind === entry.kind) { last.text += entry.text; last.sign = entry.kind + entry.seq + ":" + last.text.length; continue; }
          out.push({key: entry.kind[0] + entry.seq, kind: entry.kind, text: entry.text, at: entry.at, sign: entry.kind + entry.seq + ":" + entry.text.length});
          continue;
        }
        if (entry.kind === "tool" && entry.toolCall) {
          // One card per tool call, at its first item; later items update it.
          if (drawn.has(entry.toolCall)) continue;
          drawn.add(entry.toolCall);
          const call = tools.get(entry.toolCall) || null;
          const terminal = call?.terminal ? terminals.get(call.terminal) || null : null;
          const sign = call ? [call.status, call.kind, call.title, call.locations.join("\n"), call.diffs.map(diff => diff.path + ":" + diff.before.length + ":" + diff.after.length + ":" + diff.cut).join("\n"),
            call.asked, call.unasked, terminal ? terminal.line + ":" + terminal.output.length + ":" + terminal.exit + ":" + terminal.signal : ""].join("\u0000") : "gone:" + entry.text;
          out.push({key: "t" + entry.toolCall, kind: "tool", call, terminal, text: entry.text, at: entry.at, sign});
          continue;
        }
        if (entry.kind === "plan" && last?.kind === "plan") { last.at = entry.at; last.sign = "plan" + entry.seq; continue; }
        out.push({key: "i" + entry.seq, kind: entry.kind, text: entry.text, at: entry.at, sign: entry.kind + entry.seq + ":" + entry.text});
      }
      return out;
    }

    function diffBody(diff) {
      const rows = lineDiff(block(diff.before, 70000), block(diff.after, 70000));
      const pre = scroller("", "Changes to " + diff.path, "ai-diff");
      for (const row of rows) {
        const line = element("span", "", "ai-diff-row");
        line.dataset.op = row.op === "+" ? "add" : row.op === "-" ? "remove" : row.op === "…" ? "skip" : "same";
        line.append(element("span", row.op === " " ? "\u00a0" : row.op, "ai-diff-op"), element("span", row.text === "" ? "\u00a0" : row.text));
        pre.append(line);
      }
      const out = [pre];
      if (diff.cut) out.push(element("p", "This change is longer than Agentflow keeps. The rest is not shown.", "muted"));
      return out;
    }
    function terminalParts(terminal, key) {
      const result = terminal.signal ? "Stopped by signal " + terminal.signal : terminal.exit === null ? "Still running" : terminal.exit === 0 ? "Exit code 0" : "Failed with exit code " + terminal.exit;
      const line = element("p", "", "ai-term-line");
      line.append(element("code", terminal.line, "ai-exact"), " ", element("span", result + (terminal.cwd ? " · in " + terminal.cwd : ""), "muted"));
      const out = [line];
      if (terminal.output) out.push(lazy(key, "Output", () => [scroller(block(terminal.output, 70000), "Output of " + shown(terminal.line, 120), "ai-output")]));
      return out;
    }

    function fillBlock(node, entry) {
      // Keep what the reader opened across an update of this block.
      const opened = new Set([...node.querySelectorAll("details[open]")].map(details => details.dataset.key));
      const held = node.contains(document.activeElement) ? document.activeElement.closest("details")?.dataset.key : undefined;
      const parts = [];
      node.dataset.kind = entry.kind;
      delete node.dataset.tone;
      if (entry.kind === "message") {
        parts.push(element("p", block(entry.text, 300000), "ai-message"));
      } else if (entry.kind === "thought") {
        const details = element("details", "", "ai-more");
        details.dataset.key = "thought";
        details.append(element("summary", "Thought"), element("p", block(entry.text, 300000), "ai-message ai-thought"));
        parts.push(details);
      } else if (entry.kind === "tool" && entry.call) {
        const call = entry.call;
        const [label, tone] = toolStatus[call.status];
        node.dataset.tone = call.unasked ? "warn" : tone;
        const head = element("div", "", "ai-tool-head");
        head.append(element("span", toolNames[call.kind], "ai-kind"), stateTag(label, tone));
        if (call.unasked) head.append(stateTag("Ran without asking", "warn", "ai-unasked"));
        else if (call.asked) head.append(element("span", "Asked first", "tag"));
        parts.push(head);
        if (call.title) {
          const title = element("p", "", "ai-agent-line");
          title.append(element("span", "The agent's description: ", "ai-said-inline"), call.title);
          parts.push(title);
        }
        if (call.unasked) parts.push(element("p", "The harness did this with its own tools, and Agentflow's checks would not have allowed it without asking you. Agentflow could not stop it. Review the changes in the worktree.", "ai-warn"));
        if (call.locations.length) {
          const places = element("ul", "", "ai-paths");
          for (const path of call.locations) places.appendChild(element("li")).append(element("code", path));
          parts.push(places);
        }
        if (entry.terminal) parts.push(...terminalParts(entry.terminal, "output"));
        call.diffs.forEach((diff, index) => parts.push(lazy("diff" + index, "Changes to " + (diff.path || "a file"), () => diffBody(diff))));
      } else {
        const tone = entry.kind === "unasked" ? "warn" : entry.kind === "limit" ? "bad" : "";
        if (tone) node.dataset.tone = tone;
        const line = element("p", "", "ai-event");
        const label = entry.kind === "tool" ? "Tool call" : itemLabels[entry.kind] || itemLabels.unknown;
        line.append(element("span", label, "ai-kind"), " ", entry.text || (entry.kind === "plan" ? "The plan changed." : entry.kind === "tool" ? "Its details are no longer kept." : ""));
        parts.push(line);
      }
      node.replaceChildren(...parts);
      for (const details of node.querySelectorAll("details")) {
        if (opened.has(details.dataset.key)) { details.fillNow?.(); details.open = true; }
        if (held !== undefined && details.dataset.key === held) details.querySelector("summary").focus({preventScroll: true});
      }
    }

    const atEnd = box => box.scrollHeight - box.scrollTop - box.clientHeight < 24;
    const toEnd = box => { box.scrollTop = box.scrollHeight; };
    function renderStream(item, fresh) {
      const box = find("ai-stream"), target = find("ai-items");
      // The stream follows the newest output while the reader is at its
      // end. A reader who scrolled up, or opened a diff to read it, is
      // left where they are and offered "Show the latest".
      if (fresh) { blocks.clear(); target.replaceChildren(); follow = true; unread = 0; }
      else if (box.clientHeight) follow = atEnd(box);
      const next = streamBlocks(item);
      const kept = new Set(next.map(entry => entry.key));
      for (const [key, node] of blocks) {
        if (kept.has(key)) continue;
        if (node.contains(document.activeElement)) box.focus({preventScroll: true});
        node.remove();
        blocks.delete(key);
      }
      let before = null, added = 0;
      for (const entry of next) {
        let node = blocks.get(entry.key);
        if (!node) {
          node = element("li", "", "ai-item");
          blocks.set(entry.key, node);
          added++;
        }
        if (node.dataset.sign !== entry.sign) { node.dataset.sign = entry.sign; fillBlock(node, entry); }
        const place = before ? before.nextSibling : target.firstChild;
        if (node !== place) target.insertBefore(node, place);
        before = node;
      }
      find("ai-items-none").hidden = next.length > 0;
      put(find("ai-items-none"), stateView(item).live ? "Nothing from the agent yet." : "This session has no output to show.");
      find("ai-truncated").hidden = !item.truncated;
      if (item.truncated) fill(find("ai-truncated"), item.id, () => [element("p", "Older output is not shown here. This prints the whole session in a terminal."), command("agentflow ai status " + item.id + " --follow", "Command")]);
      if (follow) { toEnd(box); unread = 0; }
      else if (!fresh) unread += added;
      const jump = find("ai-latest");
      jump.hidden = follow || !unread;
      put(jump, "Show the latest (" + plural(unread, "new update") + ")");
    }

    // ---- The selected session ----

    function renderCancel(item) {
      const box = find("ai-cancel");
      const can = cancellable(item);
      const mine = cancel.session === item.id ? cancel : {state: "", message: ""};
      if (!can) confirming = false;
      box.hidden = !can && mine.state !== "failed";
      find("ai-cancel-open").hidden = !can || confirming || mine.state === "sending" || mine.state === "sent";
      find("ai-cancel-confirm").hidden = !can || !confirming;
      const status = find("ai-cancel-status");
      put(status, can || mine.state === "failed" ? mine.message : "");
      status.hidden = !status.textContent;
      status.dataset.state = mine.state === "failed" ? "bad" : "";
    }
    async function sendCancel() {
      const item = session();
      if (!item || !options.request || !cancellable(item)) return;
      confirming = false;
      cancel = {session: item.id, state: "sending", message: "Asking the session to stop…"};
      renderCancel(item);
      find("ai-session-title").focus({preventScroll: true});
      try {
        await options.request("api/ai/cancel", {session: item.id});
        cancel = {session: item.id, state: "sent", message: "Cancel sent. The session stops within a few seconds."};
        options.announce("Cancel sent.");
      } catch (error) {
        cancel = {session: item.id, state: "failed", message: "The cancel was not sent. " + (shown(error?.message, 200) || "The console did not say why.")};
        options.announce(cancel.message);
      }
      const after = session();
      if (after) renderCancel(after);
    }

    function renderSession(flash) {
      const item = session();
      const panel = find("ai-session");
      panel.hidden = !item;
      if (!item) { shownSession = ""; reconcileCards(null, false); return; }
      const fresh = shownSession !== item.id;
      shownSession = item.id;
      const view = stateView(item);
      put(find("ai-session-name"), item.name);
      if (setTag(find("ai-session-state"), view.label, view.tone) && flash && !fresh) mark(find("ai-session-state"));
      put(find("ai-session-detail"), view.detail);

      const who = item.startedBy === "agent" ? "An agent" + (item.client ? " (" + item.client + ")" : "") : item.startedBy === "person" ? "You" : "Not reported";
      fill(find("ai-session-facts"), [item.id, who, item.ceiling, item.worktree, item.branch].join("\u0000"), () => {
        const facts = [term("Started by", who)];
        if (item.ceiling === "confirm") facts.push(term("Limits", "An agent started this session. Everything except reading inside the worktree asks you first."));
        facts.push(term("Worktree", element("code", item.worktree || "not reported")));
        if (item.branch) facts.push(term("Branch", element("code", item.branch)));
        return facts;
      });
      fill(find("ai-session-details"), [item.id, item.name, item.harness, item.home, item.created, item.ended].join("\u0000"), () => {
        const facts = [term("Session", element("code", item.id)), term("Harness", item.name + (item.harness && item.harness !== item.name ? " (" + item.harness + ")" : ""))];
        if (item.home) facts.push(term("Home folder", item.home === "real" ? "Your own home folder, so the harness uses your sign-in." : "A separate, empty home folder."));
        if (item.created) facts.push(term("Started", clock(item.created, now())));
        if (item.ended) facts.push(term("Ended", clock(item.ended, now())));
        return facts;
      });
      if (fresh) find("ai-session-more").open = false;
      renderCancel(item);
      reconcileCards(item, flash && !fresh);

      find("ai-prompt").hidden = !item.prompt;
      fill(find("ai-prompt-body"), item.id + ":" + item.prompt.length, () => [scroller(block(item.prompt, 8192), "Prompt", "ai-prompt-text")]);

      find("ai-plan").hidden = !item.plan.length;
      const done = item.plan.filter(entry => entry.status === "completed").length;
      put(find("ai-plan-count"), item.plan.length ? done + " of " + item.plan.length + " done" : "");
      fill(find("ai-plan-list"), JSON.stringify(item.plan), () => item.plan.map(entry => {
        const row = element("li", "", "ai-plan-row");
        row.dataset.state = entry.status;
        const state = element("span", "", "ai-plan-state");
        const shape = element("span", "", "state-mark");
        shape.setAttribute("aria-hidden", "true");
        state.append(shape, planStatus[entry.status]);
        row.append(state, element("span", entry.content, "ai-plan-text"));
        if (entry.priority === "high") row.append(element("span", "High priority", "tag"));
        return row;
      }));

      renderStream(item, fresh);

      // Commands Agentflow started for the agent that no tool call shows.
      const linked = new Set(item.tools.map(call => call.terminal).filter(Boolean));
      const loose = item.terminals.filter(terminal => !linked.has(terminal.id));
      find("ai-commands").hidden = !loose.length;
      fill(find("ai-command-list"), loose.map(terminal => [terminal.id, terminal.line, terminal.output.length, terminal.exit, terminal.signal].join(":")).join("\u0000"), () => loose.map(terminal => {
        const row = element("li");
        row.append(...terminalParts(terminal, "t" + terminal.id));
        return row;
      }));

      const ended = item.state === "ended";
      find("ai-changed").hidden = !ended;
      put(find("ai-changed-count"), ended && item.changed.length ? plural(item.changed.length, "file") : "");
      put(find("ai-changed-none"), "No file in the worktree differs from its last commit.");
      find("ai-changed-none").hidden = item.changed.length > 0;
      fill(find("ai-changed-list"), item.changed.join("\u0000"), () => item.changed.map(path => { const row = element("li"); row.append(element("code", path)); return row; }));
      find("ai-changed-hint").hidden = !item.changed.length;

      const usage = item.usage, counters = item.counters;
      fill(find("ai-totals"), JSON.stringify([usage, counters]), () => {
        const facts = [];
        if (usage && (usage.used || usage.size)) facts.push(term("Context", usage.size ? number(usage.used) + " of " + number(usage.size) + " tokens" : number(usage.used) + " tokens"));
        if (usage?.reported) facts.push(term("Cost", (usage.currency ? usage.cost.toFixed(2) + " " + usage.currency : String(usage.cost)) + ", reported by the harness. Agentflow does not check it."));
        facts.push(term("Tool calls", String(counters.tools)));
        facts.push(term("Requests", plural(counters.asks, "asked you", "asked you") + " · " + counters.allowed + " allowed · " + counters.denied + " rejected"));
        if (counters.unasked) facts.push(term("Without asking", plural(counters.unasked, "tool call") + " ran without asking you."));
        return facts;
      });
    }

    // ---- The page ----

    function waiting(state) {
      return state.sessions.reduce((sum, item) => sum + item.asks.length, 0);
    }
    function renderNav() {
      const badge = find("ai-nav-count"), label = find("ai-nav-label");
      if (!badge || !label) return;
      const asks = current ? waiting(current) : 0;
      put(badge, asks ? String(asks) : "");
      badge.hidden = !asks;
      put(label, asks ? (asks === 1 ? ", 1 request waits for your answer" : ", " + asks + " requests wait for your answer") : "");
    }

    function render(flash, fresh) {
      const state = current;
      find("ai-body").hidden = false;
      // Keep the reader's session; choose one only when there is none.
      if (!state.sessions.some(item => item.id === selected)) {
        if (selected && find("ai-session").contains(document.activeElement)) find("ai-title").focus({preventScroll: true});
        selected = (state.sessions.find(item => item.asks.length) || state.sessions.find(item => stateView(item).live) || state.sessions[0] || {id: ""}).id;
        confirming = false;
      }
      const any = state.sessions.length > 0;
      const asks = waiting(state), running = state.sessions.filter(item => stateView(item).live).length;
      const parts = [plural(state.sessions.length, "session")];
      if (running) parts.push(running + " running");
      if (asks) parts.push(plural(asks, "request waits", "requests wait") + " for your answer");
      const cap = state.maxRunning ? " You are running " + state.running + " of at most " + state.maxRunning + " sessions on this computer." : "";
      put(find("ai-summary"), any ? parts.join(" · ") + "." + cap : "");
      find("ai-summary").hidden = !any;
      find("ai-layout").hidden = !any;
      find("ai-start-lead").hidden = any;
      renderList(flash, fresh);
      renderSession(flash);
      renderNav();
      tick();
      status(live);
    }

    // One timer runs once a second while a card counts down and the page
    // is open, and stops otherwise. A tick is never highlighted or announced.
    function counting() {
      return cards.size > 0 && open();
    }
    function tick() {
      if (counting()) {
        if (!ticker) ticker = setInterval(() => {
          renderCards();
          if (!counting()) tick();
        }, 1000);
      } else if (ticker) {
        clearInterval(ticker);
        ticker = 0;
      }
      if (counting() && !watching) watching = frame(watch);
      if (!current) return;
      renderCards();
      // Output that arrived while the page was closed: show its end.
      if (follow && open()) toEnd(find("ai-stream"));
    }

    // What to say about a live change: a new request, a session that
    // started and a change of state.
    function changes(before, after) {
      const old = new Map((before?.sessions || []).map(item => [item.id, item]));
      const out = [];
      for (const item of after.sessions) {
        const was = old.get(item.id);
        for (const ask of item.asks) {
          const key = askKey(item, ask);
          if (known.has(key)) continue;
          known.add(key);
          // A request is said on every page: it waits for the reader.
          if (before) out.push({text: item.name + " asks for your permission: " + askHeading(ask).toLowerCase() + (open() ? "" : ". Answer it on the AI sessions page"), always: true});
        }
        if (!before) continue;
        if (!was) out.push({text: "Session " + item.name + " started"});
        else if (stateView(was).label !== stateView(item).label && item.state !== "waiting") out.push({text: "Session " + item.name + ": " + stateView(item).label.toLowerCase()});
      }
      return out;
    }
    function say(sentences) {
      const audible = sentences.filter(item => item.always || open()).map(item => item.text);
      if (!audible.length) return;
      const first = audible.slice(0, 3);
      if (audible.length > first.length) first.push("and " + plural(audible.length - first.length, "more change"));
      options.announce(first.join(". ") + ".");
    }

    // Show the ai state. With flash, values that differ from the ones on
    // screen are highlighted and changes are announced. null keeps the
    // last values.
    function show(raw, flash = false) {
      const next = aiState(raw);
      if (!next) return current;
      const said = changes(flash ? current : null, next);
      const old = new Set((current?.sessions || []).map(item => item.id));
      const fresh = new Set(flash && current ? next.sessions.filter(item => !old.has(item.id)).map(item => item.id) : []);
      current = next;
      render(flash, fresh);
      say(said);
      return current;
    }

    // The stream's state, for the note above the sessions: {state, offline
    // (a reason's own sentence, or ""), ready, present}.
    function status(next) {
      live = next;
      const have = Boolean(current);
      let note = "";
      if (live.state === "offline") note = live.offline || "Live updates are offline. " + (have ? "The page shows the last session states it received, and a session may have moved on." : "Sessions appear when they return.") + " The console keeps retrying.";
      else if (live.state === "live" && live.ready && !live.present) note = "The live stream has no AI session state for this repository yet. It needs a console daemon that follows sessions: update Agentflow, then stop the console and open it again with agentflow ui.";
      put(find("ai-note"), note);
      find("ai-note").hidden = !note;
      find("ai-wait").hidden = have || Boolean(note);
      if (have) renderCards();
    }

    find("ai-cancel-open").addEventListener("click", () => {
      const item = session();
      if (!item) return;
      confirming = true;
      cancel = {session: "", state: "", message: ""};
      renderCancel(item);
      // Focus lands on the choice that changes nothing.
      find("ai-cancel-no").focus({preventScroll: true});
    });
    find("ai-cancel-no").addEventListener("click", () => {
      const item = session();
      confirming = false;
      if (item) renderCancel(item);
      find("ai-cancel-open").focus({preventScroll: true});
    });
    find("ai-cancel-yes").addEventListener("click", sendCancel);
    find("ai-stream").addEventListener("scroll", () => {
      const box = find("ai-stream");
      follow = atEnd(box);
      if (follow) { unread = 0; find("ai-latest").hidden = true; }
    }, {passive: true});
    find("ai-latest").addEventListener("click", () => {
      const box = find("ai-stream");
      toEnd(box);
      follow = true;
      unread = 0;
      find("ai-latest").hidden = true;
      box.focus({preventScroll: true});
    });
    for (const node of options.root.querySelectorAll("#page-ai pre[data-command]")) node.replaceWith(command(node.textContent, "Command"));

    return {
      show, status, tick, select,
      get state() { return current; },
      get selected() { return selected; }
    };
  }

  scope.AgentflowAI = {aiState, stateView, askHeading, askEffect, reasonText, remaining, lineDiff, createAI};
})(globalThis);
