"use strict";

// The draft view on the AI sessions page (docs/design/ui/ai-draft.md). It
// shows two parts of the repository's "ai" state from the event stream:
// AIState.Features and AIState.Drafts in
// internal/webui/projection_ai_state.go. Every value arrives over the
// stream. The page sends three requests: POST api/ai/draft asks Agentflow
// what a draft would send, POST api/ai/draft/confirm sends it, and POST
// api/ai/cancel stops a draft. Only the first answers with data: the
// manifest, which lists what would be sent and never holds the data.
//
// The page applies nothing. A finished draft shows each candidate's command
// to copy into a terminal, where the command's own checks run.
//
// Everything in a draft is untrusted: the model's text, and the paths, task
// titles and review text a manifest or a target names. It is always set as
// text, never as markup and never as rendered Markdown; no address in it is
// a link; and it sits in a box of its own, so it cannot pass for a heading,
// a button or a note of the page.
//
// The helpers that clean text come from the AI sessions page (options.kit),
// so both views treat hidden characters the same way.
(function (scope) {
  const featureIDs = ["commit", "pr", "ticket", "ac", "reply"];
  const featureNames = {commit: "Commit message", pr: "PR title and body", ticket: "Ticket", ac: "Acceptance-criteria check", reply: "Review replies"};
  const featureWhat = {
    commit: "A commit message for the staged changes.",
    pr: "A title and a body for this branch's pull request.",
    ticket: "A ticket from your description and the task template.",
    ac: "A check of this branch's changes against the task's acceptance criteria.",
    reply: "Replies to the open review threads on this branch's pull request."
  };
  const draftStates = ["starting", "running", "cancelling", "ready", "invalid", "failed", "cancelled"];
  const partNames = {diff: "Diff", diffstat: "Summary of the changes", files: "Changed files", commits: "Commits", rules: "Rules", task: "Task", criteria: "Acceptance criteria", template: "Template", thread: "Review thread", text: "Your description", hint: "Your hint"};
  const sourceNames = {git: "git", caller: "you", github: "GitHub"};
  const exclusions = {secret_file: "looks like a secret file", binary: "binary file", generated: "generated file", too_large: "too large"};
  const targetNames = {criterion: "acceptance criterion", thread: "review thread", section: "template section"};
  const operations = {"commit.prepare": "Commit message", "pr.prepare": "Pull request title and body", "task.create": "Ticket", "ac.update": "Acceptance criterion to tick", "review.reply": "Review reply"};

  const confirmForm = /^cf_[0-9a-f]{32}$/;
  const hashForm = /^[0-9a-f]{64}$/;
  const threadForm = /^[A-Za-z0-9_=:.-]{1,128}$/;
  const plainWord = /^[A-Za-z0-9_@%+=:,.\/-]+$/;

  // ai.MaxText and ai.MaxHint, in bytes.
  const maxText = 16 << 10, maxHint = 2 << 10;
  // Bounds on what the page draws; the projection has its own.
  const maxDrafts = 20, maxParts = 32, maxExcluded = 500, maxTargets = 64, maxCandidates = 64, maxFields = 16, maxIssues = 64, maxWords = 64, maxCommand = 70000, maxValue = 70000;
  const confirmFor = 5 * 60000 - 5000; // the console keeps a confirmation for five minutes
  const armAfter = 1200;               // how long Send waits after it appeared or moved
  const startWithin = 10000;           // how long "Sent" waits for the stream to show the draft
  const forgetAfter = 120000;          // when the page stops waiting for a draft it sent

  // A command line for a POSIX shell: a word of plain characters as it is,
  // any other in single quotes, where the shell reads every character as
  // itself. A single quote ends the quotes, is written escaped, and opens
  // them again.
  const shellWord = word => plainWord.test(word) ? word : "'" + word.replaceAll("'", "'\\''") + "'";
  const commandLine = words => words.map(shellWord).join(" ");

  const size = n => n < 1024 ? n + (n === 1 ? " byte" : " bytes") : (n / 1024).toFixed(1) + " KiB";
  function duration(ms) {
    const total = Math.round(ms / 1000), minutes = Math.floor(total / 60), seconds = total % 60;
    return [minutes ? minutes + " min" : "", seconds || !minutes ? seconds + " s" : ""].filter(Boolean).join(" ");
  }

  const failures = {
    unparsable: "The model did not answer with the one JSON object Agentflow asked for, so there is nothing to use. Its answer is below, as text.",
    answer_too_large: "The model wrote more than Agentflow accepts for a draft, so Agentflow stopped it.",
    no_feature: "This build of Agentflow does not have this draft feature.",
    draft_tool: "The harness used one of its own tools. A draft may only write text, so Agentflow stopped it. Review the worktree: Agentflow cannot undo what the tool did.",
    draft_profile: "The harness asked for a file, a command or a tool. A draft gets none.",
    refusal: "The model refused to write this draft.",
    max_tokens: "The model reached its token limit before it finished.",
    max_turn_requests: "The harness reached its limit of model requests before it finished.",
    limit_duration: "The draft reached its time limit, so Agentflow stopped it.",
    limit_idle: "Nothing came from the harness for too long, so Agentflow stopped it.",
    limit_output: "The harness produced more output than Agentflow keeps, so Agentflow stopped it.",
    unasked: "The harness changed something without asking, so Agentflow stopped it. Review the worktree.",
    launch: "The harness did not start.",
    protocol: "The harness did not speak ACP as Agentflow expects.",
    protocol_version: "The harness speaks a version of ACP that Agentflow does not.",
    timeout: "The harness did not answer in time.",
    auth_required: "The harness needs a sign-in. Sign in to it in a terminal, then request the draft again.",
    exited: "The harness stopped before it finished.",
    host_lost: "The process that ran this draft stopped."
  };
  // One tag per draft. The label carries the meaning; the tone and the
  // mark's shape repeat it.
  function stateView(draft) {
    const code = draft.code ? " Reason code " + draft.code + "." : "";
    switch (draft.state) {
    case "starting": return {label: "Starting", tone: "info", live: true, detail: "Agentflow is starting the harness in an empty folder."};
    case "running": return {label: "Writing", tone: "info", live: true, detail: "The harness is writing the draft. Nothing is applied when it finishes."};
    case "cancelling": return {label: "Cancelling", tone: "warn", live: true, detail: "The draft is stopping. It ends within a few seconds."};
    case "ready": return {label: "Ready to read", tone: "ok", live: false, detail: "The draft is ready. Read it before you use it. Nothing has been applied."};
    case "invalid": return {label: "Not usable", tone: "warn", live: false, detail: "The model answered, but nothing in its answer passed Agentflow's checks. Nothing has been applied."};
    case "failed": return {label: "Failed", tone: "bad", live: false, detail: failures[draft.code] || "The draft failed." + code};
    case "cancelled": return {label: "Cancelled", tone: "neutral", live: false, detail: "The draft was cancelled. Nothing was applied."};
    default: return {label: "Unknown state", tone: "neutral", live: false, detail: "This console does not know this draft state. Update Agentflow." + code};
    }
  }
  const cancellable = draft => draft.state === "starting" || draft.state === "running";

  // The readers. They are defensive: unknown fields are ignored and missing
  // ones read as empty, so the shape can grow. k is the kit of ai-panel.js.
  function reader(k) {
    const {text, list, record, count, time, pick, shown, exact, wholeBlock, plainCode, sessionForm} = k;
    const amount = value => Number.isSafeInteger(value) && value >= 0;

    function issue(raw) {
      raw = record(raw);
      if (!raw) return null;
      const code = plainCode.test(text(raw.code)) ? raw.code : "", message = shown(raw.message, 600);
      return code || message ? {code, field: shown(raw.field, 64), message: message || "Reason code " + code + "."} : null;
    }
    const issues = raw => list(raw).map(issue).filter(Boolean).slice(0, maxIssues);

    function target(raw) {
      raw = record(raw);
      if (!raw) return null;
      return {index: count(raw.index), kind: pick(raw.kind, Object.keys(targetNames), "item"), label: exact(text(raw.label) || text(raw.id), 600), done: raw.done === true};
    }

    // ai.Manifest: what a draft would send. complete says whether the page
    // can show every part that would leave the computer; only a complete
    // manifest can be sent from here.
    function manifest(raw) {
      raw = record(raw);
      if (!raw) return null;
      let complete = Array.isArray(raw.parts) && raw.parts.length > 0 && raw.parts.length <= maxParts;
      const partList = [];
      for (const item of list(raw.parts).slice(0, maxParts).map(record)) {
        if (!item || !amount(item.bytes)) { complete = false; continue; }
        const kind = pick(item.kind, Object.keys(partNames), "other");
        const label = shown(item.label, 200) || partNames[kind] || "";
        if (!label) complete = false;
        const left = item.excluded === undefined || item.excluded === null ? [] : item.excluded;
        if (!Array.isArray(left)) complete = false;
        const excluded = [];
        for (const entry of list(left).slice(0, maxExcluded).map(record)) {
          if (!entry || !text(entry.path)) continue;
          excluded.push({path: exact(entry.path, 4096), reason: exclusions[entry.reason] || (plainCode.test(text(entry.reason)) ? "reason code " + entry.reason : "left out")});
        }
        partList.push({
          kind, label: label || "Part", source: sourceNames[item.source] || shown(item.source, 64) || "not reported",
          bytes: item.bytes, original: count(item.original_bytes), cut: item.cut === true, redacted: count(item.redacted),
          excluded, more: Math.max(0, list(left).length - excluded.length)
        });
      }
      const targets = list(raw.targets).map(target).filter(Boolean).slice(0, maxTargets);
      const harness = shown(raw.harness, 64), feature = pick(raw.feature, featureIDs);
      if (!harness || !feature || !count(raw.bytes) || (raw.targets !== undefined && raw.targets !== null && !Array.isArray(raw.targets)) || list(raw.targets).length > maxTargets) complete = false;
      return {
        feature, harness, name: shown(raw.harness_name, 64) || harness || "the harness", version: shown(raw.version, 64),
        path: exact(raw.path, 4096), worktree: exact(raw.worktree, 4096), branch: exact(raw.branch, 256),
        parts: partList, targets, hint: text(raw.hint).slice(0, 8192), text: text(raw.text).slice(0, 16384),
        bytes: count(raw.bytes), hash: hashForm.test(text(raw.prompt_sha256)) ? raw.prompt_sha256 : "",
        limit: count(raw.max_duration) / 1e6, // a Go duration, in nanoseconds
        never: list(raw.never_sent).filter(item => typeof item === "string").slice(0, 32).map(item => shown(item, 200)).filter(Boolean),
        complete
      };
    }

    // ai.Candidate. command is the line to copy: "" when the candidate has
    // issues, or when the page cannot show the whole command as it is.
    function candidate(raw) {
      raw = record(raw);
      if (!raw) return null;
      const fields = [];
      for (const field of list(raw.fields).map(record)) {
        if (!field || fields.length >= maxFields) continue;
        const name = shown(field.name, 64);
        fields.push({name, label: shown(field.label, 64) || name || "Text", value: text(field.value)});
      }
      const found = issues(raw.issues);
      const words = raw.cli;
      const whole = Array.isArray(words) && words.length > 0 && words.length <= maxWords && words[0] === "agentflow"
        && words.every(word => typeof word === "string" && wholeBlock(word, maxCommand));
      const line = whole ? commandLine(words.map(word => word.replace(/\r\n?/g, "\n"))) : "";
      return {
        operation: plainCode.test(text(raw.operation)) ? raw.operation : "", target: target(raw.target), fields,
        issues: found, warnings: issues(raw.warnings),
        command: !found.length && line.length <= maxCommand ? line : "",
        // Why no command is offered: the candidate's issues say it, or the page could not show it.
        hidden: !found.length && (!line || line.length > maxCommand)
      };
    }

    // ai.Draft: the result of a finished draft.
    function result(raw) {
      raw = record(raw);
      if (!raw) return null;
      return {
        candidates: list(raw.candidates).map(candidate).filter(Boolean).slice(0, maxCandidates),
        issues: issues(raw.issues), answer: text(raw.answer).slice(0, 32768)
      };
    }

    function draft(raw) {
      raw = record(raw);
      const id = text(raw?.session);
      if (!raw || !sessionForm.test(id)) return null;
      const feature = pick(raw.feature, featureIDs);
      const harness = shown(raw.harness, 64);
      return {
        id, feature, label: featureNames[feature] || "Draft", harness, name: shown(raw.harness_name, 64) || harness || "Harness",
        worktree: shown(raw.worktree, 1024), branch: shown(raw.branch, 256),
        state: pick(raw.state, draftStates, "unknown"), code: plainCode.test(text(raw.code)) ? raw.code : "",
        startedBy: pick(raw.started_by, ["person", "agent"]), created: time(raw.created_at), ended: time(raw.ended_at),
        manifest: manifest(raw.manifest), result: result(raw.draft), denied: count(raw.denied), warnings: issues(raw.warnings)
      };
    }

    function feature(raw) {
      raw = record(raw);
      const id = pick(raw?.id, featureIDs);
      if (!id) return null;
      const harness = shown(raw.harness, 64);
      return {
        id, label: shown(raw.label, 64) || featureNames[id], harness, name: shown(raw.harness_name, 64) || harness,
        available: raw.available === true, code: plainCode.test(text(raw.code)) ? raw.code : "", fix: shown(raw.fix, 400)
      };
    }

    // The two parts of AIState this view shows. offers says whether the
    // daemon sends features at all: an older one does not.
    function state(raw) {
      raw = record(raw);
      if (!raw) return null;
      const features = [], drafts = [], seen = new Set();
      for (const item of list(raw.features)) {
        const next = feature(item);
        if (next && !seen.has(next.id)) { seen.add(next.id); features.push(next); }
      }
      for (const item of list(raw.drafts)) {
        const next = draft(item);
        if (next && !seen.has(next.id) && drafts.length < maxDrafts) { seen.add(next.id); drafts.push(next); }
      }
      return {features, drafts, offers: Array.isArray(raw.features)};
    }
    return {state, manifest, draft, candidate, issue};
  }

  // options: those of AgentflowAI.createAI ({root, announce, request,
  // settingsRoute, copy, arm}) and kit, the helpers of ai-panel.js. root
  // holds the markup of index.html.
  function createDraft(options) {
    const k = options.kit;
    const {text, record, plural, number, shown, block, wholeBlock, bytes, parts, markName, codeName, clock, remaining, element, stateTag, setTag, put, term, plainCode, now, open} = k;
    const read = reader(k);
    const find = id => options.root.querySelector("#" + id);
    const armFor = Number.isFinite(options.arm) ? options.arm : armAfter;
    const timers = new WeakMap(), signatures = new WeakMap();
    const mine = new Set(); // drafts this page sent
    let current = null, live = {state: "connecting"}, ticker = 0, armTimer = 0, sequence = 0;
    // step: "" (nothing open), compose, preparing, confirm, sending.
    let step = "", feature = "", note = {text: "", bad: false};
    let confirm = null; // {id, manifest, until, armedAt, place, said}
    let pending = null; // a draft this page sent that the stream has not shown: {feature, at, known}
    let selected = "", cancel = {session: "", state: "", message: ""};

    // Replace a part only when what it shows changed. It says whether focus
    // was inside, so the caller can put it somewhere that still exists.
    function refill(target, signature, build) {
      if (signatures.get(target) === signature) return false;
      signatures.set(target, signature);
      const held = target.contains(document.activeElement);
      target.replaceChildren(...build());
      return held;
    }
    // Untrusted text, as text. Each "<U+XXXX>" Agentflow wrote for a hidden
    // character is drawn as a mark; its text stays, so a copy keeps it.
    function marked(node, value) {
      for (const part of parts(value)) {
        if (part.code === undefined) { node.append(part.text); continue; }
        const mark = element("span", part.text, "ai-mark");
        mark.title = "Hidden character " + codeName(part.code) + ": " + markName(part.code);
        node.append(mark);
      }
      return node;
    }
    const exactly = value => marked(element("code", "", "ai-exact"), value);
    // Text of several lines in a box that scrolls. It takes focus so the
    // keyboard can scroll it.
    function textBox(value, what, klass = "") {
      const pre = marked(element("pre", "", ("ai-text " + klass).trim()), block(value, maxValue));
      pre.tabIndex = 0;
      pre.setAttribute("role", "group");
      pre.setAttribute("aria-label", what);
      return pre;
    }
    const label = value => element("p", value, "ai-ask-label");
    function copyButton(name, value, what) {
      const button = element("button", "", "button secondary small ai-draft-copy");
      button.type = "button";
      const shownName = element("span", name);
      button.append(shownName);
      button.setAttribute("aria-label", name + ": " + what);
      button.addEventListener("click", async () => {
        try {
          await (options.copy ? options.copy(value) : navigator.clipboard.writeText(value));
          options.announce(what + " copied.");
          clearTimeout(timers.get(button));
          shownName.textContent = "Copied";
          timers.set(button, setTimeout(() => { shownName.textContent = name; }, 2000));
        } catch {
          options.announce("The browser did not allow copying. Select the text to copy it.");
        }
      });
      return button;
    }
    const draft = () => current?.drafts.find(item => item.id === selected) || null;
    const offered = id => current?.features.find(item => item.id === id && item.available) || null;
    const settings = () => options.settingsRoute ? options.settingsRoute("ai") : "#settings?group=ai";
    const offline = () => live.state === "offline";

    // ---- What a draft sends ----

    // The manifest, in Agentflow's words. asking is true before the reader
    // decides, false for a draft that was sent.
    function manifestView(m, asking) {
      if (!m) return [element("p", asking ? "Agentflow did not say what this draft would send." : "Agentflow did not keep the list of what this draft sent.", "ai-ask-fixed")];
      const out = [];
      const facts = element("dl", "", "ai-ask-facts");
      facts.append(term("Harness", m.name + (m.harness && m.harness !== m.name ? " (" + m.harness + ")" : "") + (m.version ? ", version " + m.version : "")));
      if (m.path) facts.append(term("Program", exactly(m.path)));
      facts.append(term("After that", "The harness passes it to its model provider. The provider and the harness may keep it, under their own terms."));
      if (m.worktree) facts.append(term("Worktree", exactly(m.worktree)));
      if (m.branch) facts.append(term("Branch", exactly(m.branch)));
      if (m.limit) facts.append(term("Time limit", "Agentflow stops the draft after " + duration(m.limit) + "."));
      if (asking) facts.append(term("If it changes", "If any part changes before you choose Send, Agentflow sends nothing and you request the draft again."));
      out.push(label(asking ? "Where it goes" : "Where it went"), facts);

      // What was held back comes before the list, so it is read first.
      const excluded = m.parts.reduce((sum, part) => sum + part.excluded.length + part.more, 0);
      const redacted = m.parts.reduce((sum, part) => sum + part.redacted, 0);
      const cut = m.parts.filter(part => part.cut).length;
      const held = [];
      if (excluded) held.push(plural(excluded, "file is", "files are") + " left out: the path is sent, the content is not.");
      if (redacted) held.push(plural(redacted, "value that looks like a token is", "values that look like tokens are") + " replaced with [redacted].");
      if (cut) held.push(plural(cut, "part is", "parts are") + " cut to fit.");
      out.push(held.length ? element("p", held.join(" "), "ai-ask-warn ai-draft-held") : element("p", "Nothing is left out, cut or replaced.", "muted ai-draft-held"));

      const table = element("table", "", "ai-draft-parts");
      table.append(element("caption", (asking ? "What would be sent: " : "What was sent: ") + plural(m.parts.length, "part") + ", " + size(m.bytes) + " with Agentflow's instructions"));
      const head = table.createTHead().insertRow();
      for (const name of ["Part", "From", "Size"]) {
        const cell = element("th", name);
        cell.scope = "col";
        head.append(cell);
      }
      const body = table.createTBody();
      for (const part of m.parts) {
        const row = body.insertRow();
        const what = element("th", "", "ai-draft-part");
        what.scope = "row";
        what.append(element("span", part.label, "ai-draft-part-name"));
        if (part.cut) what.append(element("p", "Cut to fit: " + size(part.bytes) + " of " + size(part.original) + " is sent.", "ai-draft-part-note"));
        if (part.redacted) what.append(element("p", plural(part.redacted, "value") + " that " + (part.redacted === 1 ? "looks" : "look") + " like a token " + (part.redacted === 1 ? "is" : "are") + " replaced with [redacted].", "ai-draft-part-note"));
        if (part.excluded.length) {
          const count = part.excluded.length + part.more;
          what.append(element("p", "Left out, content not sent (" + plural(count, "file") + "):", "ai-draft-part-note"));
          const paths = element("ul", "", "ai-draft-excluded");
          if (part.excluded.length > 5) {
            paths.tabIndex = 0;
            paths.setAttribute("aria-label", "Files left out of " + part.label);
          }
          for (const entry of part.excluded) {
            const item = element("li");
            item.append(exactly(entry.path), element("span", entry.reason, "ai-draft-reason"));
            paths.append(item);
          }
          if (part.more) paths.append(element("li", "and " + plural(part.more, "more file"), "muted"));
          what.append(paths);
        }
        row.append(what, element("td", part.source), element("td", size(part.bytes), "ai-draft-size"));
      }
      out.push(table);

      if (m.targets.length) {
        const box = element("div", "", "ai-said ai-draft-targets");
        box.append(element("p", (asking ? "The model is asked about " : "The model was asked about ") + plural(m.targets.length, "item") + ". Their names are text from the task, the template or reviewers; Agentflow did not check it.", "ai-said-label"));
        const items = element("ol", "", "ai-draft-target-list");
        for (const item of m.targets) {
          const row = element("li");
          row.value = item.index || 1;
          row.append(element("span", (targetNames[item.kind] || "item") + (item.done ? ", already ticked" : ""), "ai-draft-reason"), exactly(item.label));
          items.append(row);
        }
        box.append(items);
        out.push(box);
      }
      if (m.hint) out.push(label("Your hint, sent as you wrote it"), textBox(m.hint, "Your hint", "ai-draft-own"));
      if (m.text) out.push(label("Your description, sent as you wrote it (the first 4 KiB are shown)"), textBox(m.text, "Your description", "ai-draft-own"));
      if (m.never.length) {
        const never = element("details", "", "ai-more ai-draft-never");
        never.append(element("summary", "Never sent, by any draft"));
        const items = element("ul", "", "ai-draft-never-list");
        for (const item of m.never) items.append(element("li", item));
        never.append(items);
        out.push(never);
      }
      if (m.hash) {
        const print = element("details", "", "ai-more");
        print.append(element("summary", "Prompt fingerprint"), element("p", "SHA-256 of the prompt, " + number(m.bytes) + " bytes. Agentflow sends only the prompt with this fingerprint.", "muted ai-hint"), exactly(m.hash));
        out.push(print);
      }
      return out;
    }

    // ---- The features ----

    function reason(item) {
      if (item.code === "feature_off") return "Off. No harness is chosen for this feature, so nothing can be sent for it.";
      if (item.code === "feature_off_account") return "Off. The account this console runs as names another harness for this feature.";
      if (item.code === "not_ready") return (item.name ? "The harness " + item.name : "Its harness") + " is not ready.";
      if (item.code === "repo_disabled") return "This repository turned AI sessions off.";
      return "Not available." + (item.code ? " Reason code " + item.code + "." : "");
    }
    function buildFeature(item) {
      const row = element("li", "", "ai-draft-feature");
      row.dataset.feature = item.id;
      const head = element("div", "", "ai-draft-feature-head");
      head.append(element("h3", item.label, "ai-draft-feature-name"), stateTag(item.available ? "Ready" : item.code === "feature_off" ? "Off" : "Not ready", item.available ? "ok" : item.code === "feature_off" ? "neutral" : "warn"));
      row.append(head, element("p", featureWhat[item.id], "ai-draft-feature-what"));
      if (item.available) {
        row.append(element("p", "Its harness is " + item.name + ".", "muted ai-draft-feature-why"));
        const button = element("button", "Draft", "button secondary small ai-draft-open");
        button.type = "button";
        button.dataset.feature = item.id;
        button.setAttribute("aria-label", "Draft: " + item.label);
        button.setAttribute("aria-controls", "ai-draft-compose");
        button.addEventListener("click", () => openCompose(item.id));
        row.append(button);
      } else {
        row.append(element("p", reason(item), "ai-draft-feature-why"));
        if (item.fix) row.append(element("p", item.fix, "muted ai-draft-feature-why"));
        const link = element("a", "Open the AI settings", "ai-draft-settings");
        link.setAttribute("href", settings());
        link.setAttribute("aria-label", "Open the AI settings for " + item.label);
        row.append(link);
      }
      return row;
    }
    function renderFeatures() {
      const none = find("ai-draft-none");
      put(none, current.features.length ? "" : current.offers ? "This build of Agentflow has no draft feature yet." : "This console's daemon does not offer drafts. Update Agentflow, then stop the console and open it again with agentflow ui.");
      none.hidden = current.features.length > 0;
      const target = find("ai-draft-features");
      const focused = target.contains(document.activeElement) ? document.activeElement.dataset?.feature : "";
      if (refill(target, JSON.stringify(current.features), () => current.features.map(buildFeature))) {
        (target.querySelector('.ai-draft-open[data-feature="' + focused + '"]') || find("ai-draft-title")).focus({preventScroll: true});
      }
      const busy = step !== "";
      for (const button of target.querySelectorAll(".ai-draft-open")) button.setAttribute("aria-expanded", String(busy && button.dataset.feature === feature));
    }

    // ---- Asking for a draft ----

    function counter(input, max, what) {
      const line = element("p", "", "ai-draft-count");
      line.id = input.id + "-count";
      input.setAttribute("aria-describedby", line.id);
      const update = () => {
        const used = bytes(input.value.trim());
        put(line, used > max ? what + " is " + number(used - max) + " bytes too long. The limit is " + number(max) + " bytes." : number(used) + " of " + number(max) + " bytes");
        line.dataset.state = used > max ? "bad" : "";
      };
      input.addEventListener("input", update);
      update();
      return line;
    }
    function field(name, id, tag, rows) {
      const wrap = element("label", name);
      const input = element(tag);
      input.id = id;
      if (rows) input.rows = rows;
      input.autocomplete = "off";
      input.spellcheck = tag === "textarea";
      wrap.append(input);
      return [wrap, input];
    }
    function buildInputs() {
      const out = [];
      if (feature === "ticket") {
        const [wrap, input] = field("Description, in your own words (required)", "ai-draft-text", "textarea", 6);
        out.push(wrap, counter(input, maxText, "The description"), element("p", "Agentflow uses your default task template, when your task provider has one.", "muted ai-hint"));
      }
      if (feature === "reply") {
        const [wrap, input] = field("One review thread only (optional)", "ai-draft-thread", "input");
        input.type = "text";
        out.push(wrap, element("p", "Paste a thread ID from agentflow review. Leave it empty to draft replies to every open thread, at most 10.", "muted ai-hint"));
      }
      const [wrap, input] = field("Hint for the model (optional)", "ai-draft-hint", "textarea", 2);
      out.push(wrap, counter(input, maxHint, "The hint"));
      find("ai-draft-inputs").replaceChildren(...out);
    }
    function openCompose(id) {
      if (step === "sending" || !offered(id)) return;
      sequence++;
      feature = id;
      step = "compose";
      confirm = null;
      note = {text: "", bad: false};
      buildInputs();
      render();
      find("ai-draft-compose-title").focus();
    }
    function closeCompose() {
      sequence++;
      const was = feature;
      step = "";
      confirm = null;
      note = {text: "", bad: false};
      render();
      (find("ai-draft-features").querySelector('.ai-draft-open[data-feature="' + was + '"]') || find("ai-draft-title")).focus({preventScroll: true});
    }
    // What the console said about a request it refused: its own code,
    // message and fix (aiDraftRefusal), or the error's text.
    function refusal(error) {
      const body = record(error?.body);
      const message = shown(body?.message, 400) || shown(error?.message, 300) || "The console did not say why.";
      const fix = shown(body?.fix, 400);
      return {text: "Nothing was sent. " + message + (fix ? " " + fix : ""), bad: true, code: plainCode.test(text(body?.code)) ? body.code : ""};
    }
    const value = id => (find(id)?.value || "").trim();
    async function prepare() {
      if (step !== "compose" || !options.request) return;
      const fail = (message, id) => {
        note = {text: message, bad: true};
        render();
        find(id)?.focus();
      };
      const input = {};
      if (feature === "ticket") {
        input.text = value("ai-draft-text");
        if (!input.text) return fail("Write a description first.", "ai-draft-text");
        if (bytes(input.text) > maxText) return fail("The description is too long.", "ai-draft-text");
      }
      if (feature === "reply" && value("ai-draft-thread")) {
        input.thread = value("ai-draft-thread");
        if (!threadForm.test(input.thread)) return fail("This does not look like a thread ID.", "ai-draft-thread");
      }
      if (value("ai-draft-hint")) {
        input.hint = value("ai-draft-hint");
        if (bytes(input.hint) > maxHint) return fail("The hint is too long.", "ai-draft-hint");
      }
      const turn = ++sequence, asked = feature;
      const known = new Set((current?.drafts || []).map(item => item.id));
      step = "preparing";
      note = {text: "Agentflow is gathering what this draft would send. Nothing is sent yet.", bad: false};
      render();
      let answer = null, failed = null;
      try { answer = await options.request("api/ai/draft", {feature: asked, input}); }
      catch (error) { failed = refusal(error); }
      if (turn !== sequence) return; // the reader closed the request or began another
      if (failed) {
        step = "compose";
        note = failed;
        render();
        return;
      }
      const id = text(record(answer)?.confirm);
      if (!id) {
        // 202: the reader's autonomy settings start drafts without a question.
        pending = {feature: asked, at: now(), known};
        step = "";
        note = {text: "The draft started without a question, because your autonomy settings allow that. What it sent is listed on the draft.", bad: false};
        render();
        find("ai-draft-title").focus({preventScroll: true});
        return;
      }
      confirm = {id: confirmForm.test(id) ? id : "", manifest: read.manifest(answer.manifest), until: now() + confirmFor, armedAt: now() + armFor, place: "", said: ""};
      step = "confirm";
      note = {text: "", bad: false};
      render();
      find("ai-draft-confirm-title").focus();
      options.announce("Nothing has been sent yet. Read what this draft would send, then choose Send or Discard.");
    }

    // ---- The reader's decision ----

    // Where a button is on the page. Send answers only where it has stood still.
    function where(button) {
      const box = button.getBoundingClientRect();
      return [Math.round(box.top + scrollY), Math.round(box.left + scrollX), Math.round(box.width)].join(":");
    }
    function renderConfirm() {
      const box = find("ai-draft-confirm");
      const shownNow = (step === "confirm" || step === "sending") && Boolean(confirm);
      box.hidden = !shownNow;
      if (!shownNow) return;
      const m = confirm.manifest, t = now();
      const name = featureNames[feature] || "Draft";
      put(find("ai-draft-confirm-lead"), "Nothing has been sent yet. This is everything a " + name.toLowerCase() + " draft would send from this computer. Send starts the draft. Discard sends nothing.");
      refill(find("ai-draft-confirm-body"), confirm.id + ":" + (m?.hash || ""), () => manifestView(m, true));
      const left = confirm.until - t, over = left <= 0;
      const whole = Boolean(confirm.id && m && m.complete);
      const blocked = find("ai-draft-confirm-blocked");
      put(blocked, whole ? "" : "This console cannot show the whole list of what would be sent, so it cannot send this draft. Run agentflow ai draft " + feature + " in a terminal instead.");
      blocked.hidden = whole;
      const away = find("ai-draft-confirm-offline");
      away.hidden = !offline();
      const clock_ = find("ai-draft-confirm-time");
      put(clock_, over ? "The time to send ran out. Nothing was sent. Request the draft again." : whole ? "Send within " + remaining(left) + ". After that, nothing is sent." : "");
      clock_.dataset.state = over ? "over" : left <= 60000 ? "soon" : "";
      const send = find("ai-draft-send"), discard = find("ai-draft-discard");
      put(send.querySelector(".ai-draft-send-label"), "Send to " + (m?.name || "the harness"));
      send.hidden = !whole || over;
      const busy = step === "sending";
      send.disabled = busy;
      discard.disabled = busy;
      if (!send.hidden && !confirm.place) confirm.place = where(send);
      const waits = !busy && t < confirm.armedAt;
      if (waits) send.setAttribute("aria-disabled", "true");
      else send.removeAttribute("aria-disabled");
      clearTimeout(armTimer);
      if (waits) armTimer = setTimeout(renderConfirm, confirm.armedAt - t + 20);
      const line = find("ai-draft-confirm-status");
      put(line, confirm.said);
    }
    function discard() {
      if (step !== "confirm") return;
      sequence++;
      confirm = null;
      step = "compose";
      note = {text: "Discarded. Nothing was sent.", bad: false};
      render();
      find("ai-draft-compose-title").focus();
    }
    async function send() {
      if (step !== "confirm" || !confirm || !confirm.id || !confirm.manifest?.complete || !options.request) return;
      const button = find("ai-draft-send"), t = now();
      if (confirm.until - t <= 0) return renderConfirm();
      const place = where(button);
      if (place !== confirm.place || t < confirm.armedAt) {
        // The button appeared or moved a moment ago: this click may not be meant for it.
        const moved = place !== confirm.place;
        confirm.place = place;
        if (moved) confirm.armedAt = t + armFor;
        confirm.said = "The button just " + (moved ? "moved" : "appeared") + ". Read the list, then choose again.";
        return renderConfirm();
      }
      const turn = ++sequence, asked = feature, name = confirm.manifest.name;
      const known = new Set((current?.drafts || []).map(item => item.id));
      step = "sending";
      confirm.said = "Sending…";
      renderConfirm();
      let failed = null;
      try { await options.request("api/ai/draft/confirm", {confirm: confirm.id}); }
      catch (error) { failed = refusal(error); }
      if (turn !== sequence) return;
      confirm = null;
      if (failed) {
        // The form is still filled in, so the reader can ask for the list again.
        step = "compose";
        note = failed;
        render();
        find("ai-draft-compose-title").focus();
        return;
      }
      pending = {feature: asked, at: now(), known};
      step = "";
      note = {text: "Sent to " + name + ". The draft appears under Drafts when it starts.", bad: false};
      render();
      find("ai-draft-title").focus({preventScroll: true});
    }

    // ---- The drafts ----

    function startedText(item) {
      const who = item.startedBy === "agent" ? "Requested by an agent" : item.startedBy === "person" ? "Requested by you" : "Requested";
      return who + (item.created ? " at " + clock(item.created, now()) : "");
    }
    function buildRow(item) {
      const row = element("li");
      const button = element("button", "", "ai-row");
      button.type = "button";
      button.dataset.draft = item.id;
      const view = stateView(item);
      const head = element("span", "", "ai-row-head");
      head.append(element("strong", item.label, "ai-row-name"), stateTag(view.label, view.tone));
      button.append(head, element("span", startedText(item), "ai-row-start"));
      if (item.branch) button.append(element("code", item.branch, "ai-row-branch"));
      if (item.worktree) button.append(element("span", item.worktree, "ai-row-path"));
      if (item.id === selected) button.setAttribute("aria-current", "true");
      button.addEventListener("click", () => select(item.id, true));
      row.append(button);
      return row;
    }
    function renderList() {
      const target = find("ai-draft-list");
      const focused = target.contains(document.activeElement) ? document.activeElement.dataset?.draft : "";
      const signature = JSON.stringify(current.drafts.map(item => [item.id, item.state, item.code, item.startedBy, item.created, item.branch, item.worktree, item.id === selected]));
      if (refill(target, signature, () => current.drafts.map(buildRow))) {
        (target.querySelector('.ai-row[data-draft="' + focused + '"]') || find("ai-draft-title")).focus({preventScroll: true});
      }
    }
    function select(id, byReader) {
      if (!current?.drafts.some(item => item.id === id)) return;
      selected = id;
      render();
      if (byReader) find("ai-draft-item-title").focus({preventScroll: true});
    }

    async function sendCancel(item) {
      if (!options.request || !cancellable(item)) return;
      cancel = {session: item.id, state: "sending", message: "Asking the draft to stop…"};
      renderItem();
      try {
        await options.request("api/ai/cancel", {session: item.id});
        cancel = {session: item.id, state: "sent", message: "Cancel sent. The draft stops within a few seconds."};
      } catch (error) {
        cancel = {session: item.id, state: "failed", message: "The cancel was not sent. " + (shown(error?.message, 200) || "The console did not say why.")};
      }
      renderItem();
    }

    function notes(items, klass) {
      const box = element("ul", "", "ai-draft-notes " + klass);
      for (const item of items) box.append(element("li", item.message));
      return box;
    }
    // One candidate: the model's words in their own box, then Agentflow's
    // findings and the command, next to each other and last.
    function buildCandidate(item, n, of, session) {
      const card = element("li", "", "ai-draft-candidate");
      const name = operations[item.operation] || "Candidate";
      const what = of > 1 ? "candidate " + n : "the candidate";
      card.append(element("h5", of > 1 ? n + ". " + name : name, "ai-draft-candidate-title"));
      if (item.target) {
        const about = element("p", "", "ai-draft-about");
        about.append(element("span", "For this " + (targetNames[item.target.kind] || "item") + " (text from the task, the template or a reviewer):", "ai-draft-about-label"), exactly(item.target.label));
        card.append(about);
      }
      const box = element("div", "", "ai-said ai-draft-said");
      box.append(element("p", "Written by the model. Agentflow checked its form, not whether it is right or true.", "ai-said-label"));
      for (const entry of item.fields) {
        const head = element("div", "", "ai-draft-field-head");
        head.append(element("span", entry.label, "ai-draft-field-name"));
        if (!entry.value) {
          box.append(head, element("p", "Agentflow took this text out.", "muted ai-draft-field-gone"));
          continue;
        }
        const whole = wholeBlock(entry.value, maxValue);
        head.append(element("span", size(bytes(entry.value)), "ai-count"));
        if (whole) head.append(copyButton("Copy text", entry.value.replace(/\r\n?/g, "\n"), entry.label + " of " + what));
        box.append(head, textBox(entry.value, entry.label + " of " + what + ", written by the model", "ai-draft-value"));
        if (!whole) box.append(element("p", "This text is too long, or has characters this page cannot show as they are (marked �), so it is not offered to copy.", "ai-said-note"));
      }
      if (!item.fields.length) box.append(element("p", "The model wrote no text for this candidate.", "muted ai-draft-field-gone"));
      card.append(box);
      if (item.warnings.length) card.append(label("Look at this before you use it"), notes(item.warnings, "ai-draft-warnings"));
      if (item.issues.length) {
        card.append(label("Agentflow does not offer this candidate"), notes(item.issues, "ai-draft-issues"));
      } else if (item.command) {
        const head = element("div", "", "ai-draft-field-head ai-draft-use");
        head.append(label("To use it, copy this command and run it in a terminal"), copyButton("Copy command", item.command, "Command for " + what));
        const line = element("div", "", "ai-command ai-draft-command");
        const pre = element("pre");
        pre.tabIndex = 0;
        pre.setAttribute("role", "group");
        pre.setAttribute("aria-label", "Command for " + what);
        pre.append(marked(element("code"), item.command));
        line.append(pre);
        card.append(head, line, element("p", "The command asks you before it changes anything, as it always does. This page runs nothing.", "muted ai-hint"));
      } else {
        const why = element("p", "This console cannot show the whole command as it is, so it does not offer it. Read the draft in a terminal: ", "ai-ask-fixed");
        why.append(element("code", "agentflow ai status " + session));
        card.append(why);
      }
      return card;
    }
    function buildItem(item, view) {
      const out = [];
      if (view.live) {
        if (offline()) out.push(element("p", "Live updates are offline, so this draft may have moved on.", "ai-ask-offline"));
        const box = element("div", "", "ai-cancel ai-draft-cancel");
        const mine_ = cancel.session === item.id ? cancel : {state: "", message: ""};
        if (cancellable(item) && mine_.state !== "sending" && mine_.state !== "sent") {
          const button = element("button", "Cancel draft", "button secondary small ai-danger");
          button.type = "button";
          button.dataset.action = "cancel";
          button.addEventListener("click", () => sendCancel(item));
          box.append(button);
        }
        const line = element("p", mine_.message, "ai-cancel-status");
        line.setAttribute("role", "status");
        line.dataset.state = mine_.state === "failed" ? "bad" : "";
        box.append(line);
        out.push(box);
      }
      if (item.warnings.length) out.push(notes(item.warnings, "ai-draft-warnings"));
      if (item.denied) out.push(element("p", "The harness asked " + plural(item.denied, "time") + " for a file, a command or a tool. A draft gets none, so Agentflow refused.", "ai-warn ai-draft-denied"));
      const result = item.result;
      if (result?.candidates.length) {
        out.push(element("h4", plural(result.candidates.length, "candidate"), "ai-draft-part-title"));
        const cards = element("ol", "", "ai-draft-candidates");
        result.candidates.forEach((entry, n) => cards.append(buildCandidate(entry, n + 1, result.candidates.length, item.id)));
        out.push(cards);
      } else if (item.state === "ready" || item.state === "invalid") {
        out.push(element("p", "The draft has no candidate.", "muted"));
      }
      if (result?.issues.length) out.push(element("h4", "What Agentflow did not make a candidate of", "ai-draft-part-title"), notes(result.issues, "ai-draft-issues"));
      if (result?.answer) {
        const whole = wholeBlock(result.answer, maxValue);
        const head = element("div", "", "ai-draft-field-head");
        head.append(element("span", "The model's answer, as text", "ai-draft-field-name"));
        if (whole) head.append(copyButton("Copy text", result.answer.replace(/\r\n?/g, "\n"), "The model's answer"));
        const box = element("div", "", "ai-said ai-draft-said");
        box.append(element("p", "Written by the model. Agentflow could not use it.", "ai-said-label"), head, textBox(result.answer, "The model's answer", "ai-draft-value"));
        out.push(box);
      }
      if (!view.live && item.state !== "ready" && offered(item.feature)) {
        const again = element("button", "Request a new draft", "button secondary small ai-draft-again");
        again.type = "button";
        again.addEventListener("click", () => openCompose(item.feature));
        out.push(again);
      }
      const facts = element("dl", "", "ai-facts");
      facts.append(term("Requested by", item.startedBy === "agent" ? "An agent" : item.startedBy === "person" ? "You" : "Not reported"), term("Harness", item.name + (item.harness && item.harness !== item.name ? " (" + item.harness + ")" : "")));
      if (item.worktree) facts.append(term("Worktree", element("code", item.worktree)));
      if (item.branch) facts.append(term("Branch", element("code", item.branch)));
      if (item.created) facts.append(term("Started", clock(item.created, now())));
      if (item.ended) facts.append(term("Ended", clock(item.ended, now())));
      facts.append(term("Draft", element("code", item.id)));
      out.push(facts);
      const sent = element("details", "", "ai-more ai-draft-sent");
      sent.append(element("summary", "What this draft sent"), ...manifestView(item.manifest, false));
      out.push(sent);
      return out;
    }
    function renderItem() {
      const item = draft(), panel = find("ai-draft-item");
      panel.hidden = !item;
      if (!item) return;
      const view = stateView(item);
      put(find("ai-draft-item-name"), item.label);
      setTag(find("ai-draft-item-state"), view.label, view.tone);
      put(find("ai-draft-item-detail"), view.detail);
      const signature = JSON.stringify([item, cancel.session === item.id ? cancel : null, view.live && offline(), Boolean(offered(item.feature))]);
      if (refill(find("ai-draft-item-body"), signature, () => buildItem(item, view))) find("ai-draft-item-title").focus({preventScroll: true});
    }

    // ---- The view ----

    function renderNote() {
      const line = find("ai-draft-status");
      let said = note.text;
      if (pending && !note.bad && now() - pending.at > startWithin) said = "The draft has not shown up yet. The console accepted it, so it may still start.";
      put(line, said);
      line.dataset.state = note.bad ? "bad" : "";
    }
    function render() {
      if (!current) return;
      find("ai-draft").hidden = false;
      if ((step === "compose" || step === "preparing") && !offered(feature)) {
        step = "";
        note = {text: "This feature is no longer available, so the request was closed.", bad: true};
      }
      if (!current.drafts.some(item => item.id === selected)) {
        if (selected && find("ai-draft-item").contains(document.activeElement)) find("ai-draft-title").focus({preventScroll: true});
        selected = (current.drafts.find(item => stateView(item).live) || current.drafts[0] || {id: ""}).id;
      }
      renderFeatures();
      const composing = step === "compose" || step === "preparing";
      find("ai-draft-compose").hidden = !composing;
      if (composing) {
        put(find("ai-draft-compose-name"), featureNames[feature] || "Draft");
        find("ai-draft-prepare").disabled = step === "preparing";
      }
      renderConfirm();
      renderNote();
      const any = current.drafts.length > 0;
      find("ai-draft-results").hidden = !any;
      find("ai-draft-empty").hidden = any;
      renderList();
      renderItem();
      tick();
    }

    // One timer runs once a second while the reader decides or the page
    // waits for a draft it sent, and the page is open. It stops otherwise.
    const counting = () => open() && Boolean((step === "confirm" && confirm) || pending);
    function tick() {
      if (pending && now() - pending.at > forgetAfter) {
        pending = null;
        if (!note.bad) note = {text: "", bad: false};
        if (current) renderNote();
      }
      if (counting()) {
        if (!ticker) ticker = setInterval(() => {
          if (step === "confirm") renderConfirm();
          renderNote();
          if (!counting()) tick();
        }, 1000);
      } else if (ticker) {
        clearInterval(ticker);
        ticker = 0;
      }
    }

    // Show the ai state. With flash, changes are announced. null keeps the
    // last values.
    function show(raw, flash = false) {
      const next = read.state(raw);
      if (!next) return current;
      const before = flash ? current : null;
      // A draft this page sent arrives as one the page has not seen, for the feature it asked.
      const born = pending && next.drafts.find(item => !pending.known.has(item.id) && item.feature === pending.feature);
      if (born) {
        mine.add(born.id);
        selected = born.id;
        pending = null;
        if (!note.bad) note = {text: "", bad: false};
      }
      const old = new Map((before?.drafts || []).map(item => [item.id, item]));
      const said = [];
      if (before) {
        for (const item of next.drafts) {
          const was = old.get(item.id), view = stateView(item);
          if (!was) said.push({text: item.label + " draft started", always: false});
          else if (was.state !== item.state) said.push({text: item.label + " draft: " + view.label.toLowerCase() + (item.state === "ready" && item.result ? ", " + plural(item.result.candidates.length, "candidate") : ""), always: mine.has(item.id) && !view.live});
        }
      }
      current = next;
      render();
      if (born && (document.activeElement === find("ai-draft-title") || document.activeElement === document.body)) find("ai-draft-item-title").focus({preventScroll: true});
      const audible = said.filter(item => item.always || open());
      if (audible.length) {
        const first = audible.slice(0, 3).map(item => item.text);
        if (audible.length > first.length) first.push("and " + plural(audible.length - first.length, "more change"));
        options.announce(first.join(". ") + "." + (open() ? "" : " Read it on the AI sessions page."));
      }
      return current;
    }
    // The stream's state: {state, offline, ready, present}.
    function status(next) {
      live = next;
      if (!current) return;
      renderConfirm();
      renderItem();
    }

    find("ai-draft-prepare").addEventListener("click", prepare);
    find("ai-draft-close").addEventListener("click", closeCompose);
    find("ai-draft-discard").addEventListener("click", discard);
    find("ai-draft-send").addEventListener("click", send);

    return {
      show, status, tick, select, read,
      get state() { return current; },
      get selected() { return selected; },
      get step() { return step; }
    };
  }

  scope.AgentflowAIDraft = {createDraft, reader, stateView, commandLine, shellWord};
})(globalThis);
