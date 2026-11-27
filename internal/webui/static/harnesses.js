"use strict";

// The Harnesses page (docs/design/ui/harnesses.md). It shows one
// repository-wide state from the event stream: HarnessesState in
// internal/webui/projection_harnesses_state.go, the daemon's last
// harness.Report. Every value arrives over the stream. The page sends two
// requests and neither answers with data: POST api/harnesses/probe when the
// reader asks for a probe, and POST api/harnesses/check when the window
// comes back into view. It edits nothing: automatic probes, the sign-in
// check and overrides are on the Settings page, group "ai".
//
// Values are read defensively: unknown fields are ignored and missing ones
// read as empty, so the shape can grow. Text from the state (names,
// descriptions, fix text, sign-in methods, paths) is always set as text,
// never as markup. A command is offered to copy only when it is one plain
// line. No registry icon is loaded: the page draws one icon per kind.
(function (scope) {
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const count = value => Number.isSafeInteger(value) && value > 0 ? value : 0;
  const time = value => { const at = Date.parse(text(value)); return Number.isFinite(at) && at > 0 ? at : 0; };
  const pick = (value, known, fallback = "") => known.includes(value) ? value : fallback;
  const words = value => value.replaceAll("_", " ");
  const plural = (n, one, many = one + "s") => n + " " + (n === 1 ? one : many);
  const capital = value => value ? value[0].toUpperCase() + value.slice(1) : "";
  // Control characters, line separators and the characters that reorder
  // text (bidirectional controls).
  const unsafe = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u200e\u200f\u202a-\u202e\u2066-\u2069]/;
  const unsafeAll = new RegExp(unsafe.source + "+", "g");
  // Display text from the state: one line, bounded.
  const shown = (value, max) => {
    const line = text(value).replace(unsafeAll, " ").replace(/\s+/g, " ").trim();
    return line.length > max ? line.slice(0, max - 1) + "…" : line;
  };
  // Text the reader copies into a shell: kept exactly, or not offered at
  // all. Cutting or cleaning a command would change what it does.
  const commandLine = value => {
    const line = text(value);
    return line && line.length <= 512 && line === line.trim() && !unsafe.test(line) ? line : "";
  };
  // A link is made only from an absolute https address without credentials.
  const httpsAddress = value => {
    const raw = text(value);
    if (!raw || raw.length > 2048 || unsafe.test(raw) || /\s/.test(raw)) return "";
    try {
      const url = new URL(raw);
      return url.protocol === "https:" && !url.username && !url.password && url.hostname ? url.href : "";
    } catch { return ""; }
  };

  // harness.State. not_installed is not sent to the page; one that arrives
  // anyway is left out.
  const states = ["native", "adapter_installed", "adapter_available", "needs_auth", "incompatible"];
  const kinds = ["native", "adapter"];
  const probeStates = ["none", "ok", "failed", "refused", "cached"];
  const authStates = ["signed_in", "signed_out", "unknown"];
  // The definition ID (ADR, "Phase 6 harness definitions"). Only an ID of
  // this form is sent in a probe request.
  const idForm = /^[a-z][a-z0-9-]{0,63}$/;
  const plainCode = /^[a-z0-9][a-z0-9_.-]{0,63}$/;
  const commitForm = /^[0-9a-f]{7,64}$/;
  const settingsGroup = "ai";
  const probeEvery = 60000;   // one probe a minute per console (ADR, "Phase 6 harnesses projection")
  const checkEvery = 10000;   // one focus check every 10 s per console
  const askedFor = 15000;     // how long "Probe requested" waits for the stream to show it

  const capabilities = [
    ["load_session", "resuming a session"], ["image", "images"], ["audio", "audio"], ["embedded_context", "embedded context"],
    ["mcp_http", "MCP servers over HTTP"], ["mcp_sse", "MCP servers over SSE"], ["logout", "signing out"]
  ];

  function harnessesState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const seen = new Set();
    const harnesses = [];
    for (const item of list(raw.harnesses).map(record)) {
      const id = shown(item?.id, 64);
      if (!item || !id || id !== item.id || seen.has(id) || item.state === "not_installed") continue;
      seen.add(id);
      const base = record(item.base);
      const probe = record(item.probe) || {};
      const auth = record(item.auth) || {};
      const acp = record(item.acp);
      const fix = record(item.fix);
      const signIn = [];
      for (const method of list(item.sign_in).map(record)) {
        if (!method || signIn.length >= 8) continue;
        const type = pick(method.type, ["agent", "terminal"], "agent");
        signIn.push({
          method: shown(method.method, 64), type, name: shown(method.name, 64) || shown(method.method, 64) || "Sign in",
          description: shown(method.description, 256),
          command: type === "terminal" && method.unsafe !== true ? commandLine(method.command) : "",
          unsafe: type === "terminal" && (method.unsafe === true || !commandLine(method.command))
        });
      }
      harnesses.push({
        id, probeable: idForm.test(id),
        name: shown(item.name, 64) || id, description: shown(item.description, 256),
        kind: pick(item.kind, kinds, "unknown"),
        state: pick(item.state, states, "unknown"), code: plainCode.test(text(item.code)) ? item.code : "",
        sources: list(item.sources).map(source => shown(source, 80)).filter(Boolean).slice(0, 8),
        quarantined: item.quarantined === true, quarantineReason: shown(item.quarantine_reason, 256),
        command: shown(item.command, 256), args: list(item.args).filter(arg => typeof arg === "string").slice(0, 16).map(arg => shown(arg, 256)),
        path: shown(item.path, 1024),
        base: base ? {name: shown(base.name, 64), command: shown(base.command, 256), path: shown(base.path, 1024)} : null,
        version: shown(item.version, 40), versionCode: text(item.version_code), registryVersion: shown(item.registry_version, 40),
        probe: {status: pick(probe.status, probeStates, "none"), code: plainCode.test(text(probe.code)) ? probe.code : "", at: time(probe.probed_at), duration: count(probe.duration_ms)},
        acp: acp ? {
          protocol: count(acp.protocol_version), agent: shown(acp.agent_title, 64) || shown(acp.agent_name, 64), agentVersion: shown(acp.agent_version, 40),
          can: capabilities.filter(([key]) => acp[key] === true).map(([, label]) => label),
          cannot: capabilities.filter(([key]) => acp[key] !== true).map(([, label]) => label)
        } : null,
        signIn,
        auth: {status: pick(auth.status, authStates, "unknown"), code: plainCode.test(text(auth.code)) ? auth.code : "", at: time(auth.checked_at)},
        fix: fix && (shown(fix.text, 600) || commandLine(fix.command) || httpsAddress(fix.url))
          ? {text: shown(fix.text, 600), command: commandLine(fix.command), url: httpsAddress(fix.url)} : null,
        notes: list(item.notes).map(note => shown(note, 300)).filter(Boolean).slice(0, 8)
      });
    }
    const registry = record(raw.registry) || {};
    const skipped = [];
    for (const item of list(raw.skipped_path).map(record)) {
      if (!item || !shown(item.dir, 1024) || skipped.length >= 50) continue;
      skipped.push({dir: shown(item.dir, 1024), reason: shown(item.reason, 120)});
    }
    const problems = [];
    for (const item of list(raw.problems).map(record)) {
      if (!item || problems.length >= 50) continue;
      const problem = {layer: shown(item.layer, 80), id: shown(item.id, 64), code: plainCode.test(text(item.code)) ? item.code : "", message: shown(item.message, 400)};
      if (problem.message || problem.code) problems.push(problem);
    }
    return {
      detector: raw.detector === true,
      registry: {commit: commitForm.test(text(registry.commit)) ? registry.commit : "", committed: time(registry.committed_at), agents: count(registry.agents)},
      autoProbe: raw.auto_probe === true,
      harnesses, skipped, problems,
      checked: time(raw.checked_at),
      probing: [...new Set(list(raw.probing).filter(id => typeof id === "string" && seen.has(id)))]
    };
  }

  // A harness is ready when its command was found and answered a probe
  // with ACP version 1, now or in the cached result. Every other found
  // harness needs attention.
  const answered = harness => harness.probe.code === "ok" && (harness.probe.status === "ok" || harness.probe.status === "cached");
  const ready = harness => (harness.state === "native" || harness.state === "adapter_installed") && answered(harness);
  // The command itself is on PATH (for adapter_available only the base CLI is).
  const found = harness => harness.state !== "adapter_available";

  // One tag and one mark per harness. The label carries the meaning; the
  // tone, the card's keyline and the mark's shape repeat it.
  function view(harness) {
    if (ready(harness)) return {label: "Ready", tone: "good", mark: "ready"};
    switch (harness.state) {
      case "needs_auth": return {label: "Sign-in needed", tone: "warn", mark: "attention"};
      case "incompatible": return {label: "Incompatible", tone: "bad", mark: "bad"};
      case "adapter_available": return {label: "Adapter not installed", tone: "warn", mark: "attention"};
      case "unknown": return {label: "Unknown state", tone: "", mark: "idle"};
    }
    if (harness.probe.status === "failed" || harness.probe.status === "cached") return {label: "Probe failed", tone: "warn", mark: "attention"};
    if (harness.probe.status === "refused") return {label: "Not probed", tone: "", mark: "idle"};
    return {label: "Not checked yet", tone: "", mark: "idle"};
  }

  // What the state means when the report sends no fix text of its own.
  // The fix text is agentflow's own template and comes first when present.
  function meaning(harness) {
    if (ready(harness)) return "";
    const base = harness.base?.name || "Its base CLI";
    switch (harness.state) {
      case "adapter_available": return base + " is installed, but its ACP adapter is not.";
      case "needs_auth": return harness.code === "auth_required" ? "It said that it needs you to sign in." : "You are signed out of it.";
      case "incompatible":
        if (harness.code === "protocol_version") return "It speaks another version of ACP. Agentflow needs version 1.";
        if (harness.code === "version_too_old") return "This version is too old for Agentflow.";
        return "It did not answer like an ACP agent.";
      case "unknown": return "Agentflow reported a state this console does not know.";
    }
    switch (harness.probe.code) {
      case "timeout": return "It did not answer the probe in time.";
      case "exited": return "It exited before it answered the probe.";
      case "launch": return "Agentflow could not start it.";
      case "needs_person": return "Automatic probes are off, so Agentflow has not started it.";
      case "shim": return "It is a Windows command script, which Agentflow does not start.";
      case "test_guard": return "It was not started, because this is a test run.";
    }
    return "Found on your PATH. Agentflow has not started it yet, so it does not know whether it speaks ACP.";
  }

  const stateNames = {
    native: "Native", adapter_installed: "Adapter installed", adapter_available: "Adapter available",
    needs_auth: "Needs sign-in", incompatible: "Incompatible", unknown: "Unknown"
  };
  const kindViews = {
    native: ["Native", "The product's own program speaks ACP."],
    adapter: ["Adapter", "A separate program that speaks ACP for a CLI."],
    unknown: ["Unknown kind", "Agentflow does not know this kind of harness."]
  };
  const probeCodes = {
    timeout: "No answer in time", exited: "Exited before it answered", launch: "Could not be started",
    protocol: "Did not answer like an ACP agent", protocol_version: "Answered with another ACP version",
    needs_person: "Not run: automatic probes are off", shim: "Not run: it is a Windows command script", test_guard: "Not run in a test"
  };
  function probeText(probe) {
    if (probe.status === "none") return "Not run yet";
    const took = probe.duration ? " in " + (probe.duration >= 10000 ? Math.round(probe.duration / 1000) + " s" : probe.duration + " ms") : "";
    const what = probe.code === "ok" ? "Answered" + took : probeCodes[probe.code] || (probe.code ? "Code " + probe.code : "No result recorded");
    return what + (probe.status === "cached" ? " (saved result)" : "");
  }

  // The sign-in check's verdict. "Not known" is never shown as signed out:
  // the label, the sentence and the mark's shape all differ.
  const authCodes = {
    no_check: "Agentflow has no verified way to check sign-in for this harness.",
    env_credential: "You may be signed in with an environment variable; Agentflow cannot check that.",
    timeout: "The sign-in check did not answer in time.",
    exit_code: "The sign-in check gave an answer Agentflow does not know.",
    launch: "Agentflow could not start the sign-in check.",
    off: "Sign-in checks are turned off.",
    needs_person: "Automatic probes are off, so Agentflow has not checked. Probe it yourself to check.",
    base_missing: "Its base CLI was not found, so Agentflow could not check.",
    unsafe_environment: "Agentflow did not check, because a configuration folder of this harness is inside a repository it works in.",
    test_guard: "The sign-in check does not run in a test."
  };
  function authView(auth) {
    if (auth.status === "signed_in") return {label: "Signed in", mark: "in", detail: ""};
    if (auth.status === "signed_out") return {label: "Signed out", mark: "out", detail: "Its own status command says you are not signed in."};
    return {label: "Sign-in not known", mark: "unknown", detail: authCodes[auth.code] || (auth.code ? "Reason code " + auth.code + "." : "Agentflow has not checked.")};
  }

  const sourceNames = {registry: "the ACP registry", agentflow: "Agentflow", personal: "your settings"};
  function sourceName(source) {
    if (sourceNames[source]) return sourceNames[source];
    return source.startsWith("plugin:") && source.length > 7 ? "plugin " + source.slice(7) : source;
  }
  const noteCodes = {
    registry_env_ignored: "The ACP registry lists environment variables for this harness. Agentflow never applies them.",
    quarantined: "The ACP registry has quarantined this agent."
  };
  const skipReasons = {
    relative: "an empty or relative entry", inside_repository: "inside a repository Agentflow is working in",
    node_modules: "a node_modules folder", unsafe_permissions: "writable by others, or not owned by you",
    shim: "a Windows command script", test_guard: "outside the test's temporary folder", missing: "does not exist"
  };
  const skipReason = reason => skipReasons[reason] || (plainCode.test(reason) ? words(reason) : reason || "no reason given");

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

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }

  // Fix text names commands between backticks. Each such span becomes a
  // code element whose content is set as text; nothing is read as markup.
  function prose(value) {
    const parts = value.split("`");
    if (parts.length % 2 === 0) return [value];
    return parts.map((part, index) => index % 2 ? element("code", part) : part).filter(part => part !== "");
  }

  // options: {root, announce(message), request(path, body), settingsRoute(group),
  // visible(), now(), copy(text), view}. root holds the page markup of
  // index.html; view is the window whose focus asks for a check.
  function createHarnesses(options) {
    const find = id => options.root.querySelector("#" + id);
    const now = () => options.now ? options.now() : Date.now();
    const open = () => !options.visible || options.visible();
    const settingsRoute = () => options.settingsRoute ? options.settingsRoute(settingsGroup) : "#settings?group=" + settingsGroup;
    const timers = new WeakMap();
    const cards = new Map();
    let current = null, live = {state: "connecting"}, ticker = 0, serial = 0;
    // asked: a probe request the stream has not shown yet. until: when the
    // console may probe again. failure: why the last request did not start.
    let asked = null, until = 0, failure = "", lastCheck = 0;

    // Highlight a value that changed live (the 2 s rule of live-stream.md).
    function mark(node) {
      clearTimeout(timers.get(node));
      node.classList.remove("live-changed");
      void node.offsetWidth; // restart the highlight when a value changes twice in a row
      node.classList.add("live-changed");
      timers.set(node, setTimeout(() => node.classList.remove("live-changed"), 2000));
    }

    function slot(key, {tag = "span", klass = "", marked = false} = {}) {
      const node = element(tag, "", klass);
      node.dataset.fact = key;
      if (marked) {
        const shape = element("span", "", "state-mark");
        shape.setAttribute("aria-hidden", "true");
        node.append(shape, element("span", "", "fact-text"));
      }
      return node;
    }

    // Set one named value of a card in place; an empty value hides it. It
    // returns true when the text changed.
    function fact(card, key, value, {state, flash = false} = {}) {
      const node = card.querySelector('[data-fact="' + key + '"]');
      const label = node.querySelector(".fact-text") || node;
      const changed = label.textContent !== value;
      if (changed) label.textContent = value;
      node.hidden = !value;
      if (state !== undefined && node.dataset.state !== state) node.dataset.state = state;
      if (changed && flash && value) mark(node);
      return changed;
    }

    // Replace a part only when what it shows changed, so a Copy button
    // that did not change keeps keyboard focus across updates.
    function fill(target, signature, build) {
      if (target.dataset.shown === signature) return false;
      target.dataset.shown = signature;
      target.replaceChildren(...build());
      return true;
    }

    async function copy(value, button, what) {
      try {
        await (options.copy ? options.copy(value) : navigator.clipboard.writeText(value));
        options.announce(what + " copied.");
        const label = button.querySelector(".hn-copy-label");
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
      const box = element("div", "", "hn-command");
      const code = element("code", value);
      const pre = element("pre");
      pre.tabIndex = 0;
      pre.setAttribute("aria-label", what);
      pre.append(code);
      const button = element("button", "", "button secondary small");
      button.type = "button";
      button.append(element("span", "Copy", "hn-copy-label"));
      button.setAttribute("aria-label", "Copy the command: " + value);
      button.addEventListener("click", () => copy(code.textContent, button, what));
      box.append(pre, button);
      return box;
    }

    function term(label, node) {
      const group = element("div");
      group.append(element("dt", label), node);
      return group;
    }

    function buildCard(harness) {
      const number = ++serial;
      const card = element("li", "", "hn-card panel");
      card.dataset.key = harness.id;
      const icon = element("span", "", "hn-icon");
      icon.setAttribute("aria-hidden", "true");
      const title = element("h3", "", "hn-name");
      title.id = "harness-" + number + "-name";
      title.tabIndex = -1;
      title.append(slot("name", {marked: true, klass: "fact"}));
      const head = element("div", "", "hn-head");
      const tags = element("span", "", "hn-tags");
      tags.append(slot("probing", {klass: "tag hn-probing", marked: true}), slot("status", {klass: "tag"}));
      head.append(icon, title, tags);
      card.setAttribute("aria-labelledby", title.id);

      const facts = element("p", "", "hn-facts");
      facts.append(element("code", harness.id), slot("kind"), slot("version"), slot("registry"));

      const found = element("dl", "", "hn-where");
      found.append(term("Runs", slot("runs", {tag: "dd"})), term("Found at", slot("path", {tag: "dd"})), term("Base CLI", slot("base", {tag: "dd"})));

      const fix = element("div", "", "hn-fix");
      fix.dataset.part = "fix";

      const signIn = element("section", "", "hn-signin");
      signIn.dataset.part = "sign-in";
      const signInTitle = element("h4", "Sign-in");
      signInTitle.id = "harness-" + number + "-sign-in";
      signIn.setAttribute("aria-labelledby", signInTitle.id);
      const verdict = element("p", "", "hn-verdict");
      verdict.append(slot("auth", {klass: "fact", marked: true}), slot("auth-detail"), slot("auth-at", {tag: "time"}));
      const methods = element("details", "", "hn-methods");
      methods.dataset.part = "methods";
      const methodsSummary = element("summary");
      methodsSummary.append(slot("methods-title"));
      const methodList = element("ul", "", "hn-method-list");
      methods.append(methodsSummary, methodList);
      signIn.append(signInTitle, verdict, methods);

      const about = element("details", "", "hn-about");
      const aboutSummary = element("summary", "Detection details");
      const aboutFacts = element("dl", "", "hn-about-facts");
      aboutFacts.append(
        term("State", slot("state", {tag: "dd"})), term("Probe", slot("probe", {tag: "dd"})),
        term("Defined by", slot("sources", {tag: "dd"})), term("ACP", slot("acp", {tag: "dd"})),
        term("Supports", slot("can", {tag: "dd"})), term("Does not support", slot("cannot", {tag: "dd"})));
      about.append(aboutSummary, aboutFacts);

      const notes = element("ul", "", "hn-notes");
      notes.dataset.part = "notes";

      const actions = element("div", "", "hn-actions");
      const probe = element("button", "", "button secondary small");
      probe.type = "button";
      probe.dataset.part = "probe";
      probe.append(element("span", "Probe now", "hn-probe-label"));
      probe.addEventListener("click", () => requestProbe([card.dataset.key]));
      actions.append(probe);

      card.append(head, facts, slot("description", {tag: "p", klass: "hn-description"}), found, fix, signIn, notes, about, actions);
      return card;
    }

    function fixParts(harness) {
      const fix = harness.fix;
      const out = [];
      const sentence = element("p", "", "hn-fix-text");
      sentence.append(...prose(fix?.text || meaning(harness)));
      out.push(sentence);
      if (fix?.command) out.push(command(fix.command, "Fix command"));
      if (fix?.url) {
        const line = element("p", "", "hn-fix-link");
        const link = element("a", fix.url);
        link.href = fix.url;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        line.append(link, element("span", " (opens in a new tab)", "muted"));
        out.push(line);
      }
      return out;
    }

    function methodRow(method, harness) {
      const item = element("li");
      const head = element("p", "", "hn-method-head");
      head.append(element("strong", method.name), " ", element("span", method.type === "terminal" ? "in your terminal" : "in the harness", "hn-kind"));
      item.append(head);
      if (method.description) item.append(element("p", method.description, "hn-method-description"));
      if (method.command) {
        item.append(element("p", "Run this in your own terminal, then probe again.", "muted"), command(method.command, "Sign-in command"));
      } else if (method.unsafe) {
        item.append(element("p", "Agentflow does not show this method's command, because it did not pass the rules for a command you copy. Start " + harness.name + " yourself and use its own sign-in.", "muted"));
      } else {
        item.append(element("p", "Start " + harness.name + " in your terminal and sign in with its own login, then probe again.", "muted"));
      }
      return item;
    }

    function codes(values) {
      const out = [];
      values.forEach((value, index) => out.push(index ? " " : "", element("code", value)));
      return out;
    }

    function updateCard(card, harness, flash) {
      const shows = view(harness);
      const at = now();
      const isReady = ready(harness);
      const probing = current.probing.includes(harness.id);
      card.dataset.mark = shows.mark;
      card.dataset.kind = harness.kind;
      fact(card, "name", harness.name, {state: shows.mark});
      fact(card, "status", shows.label, {state: shows.tone, flash});
      fact(card, "probing", probing ? "Probing" : "", {state: "probing"});
      const [kind, kindHint] = kindViews[harness.kind];
      fact(card, "kind", kind);
      card.querySelector('[data-fact="kind"]').title = kindHint;
      fact(card, "version", harness.version ? "Version " + harness.version : found(harness) ? "Version not reported" : "", {flash});
      fact(card, "registry", harness.registryVersion && harness.registryVersion !== harness.version ? "Registry lists " + harness.registryVersion : "");
      fact(card, "description", harness.description);

      const runs = card.querySelector('[data-fact="runs"]');
      fill(runs, JSON.stringify([harness.command, harness.args]), () => codes([harness.command, ...harness.args].filter(Boolean)));
      runs.parentNode.hidden = !harness.command;
      runs.previousSibling.textContent = found(harness) ? "Runs" : "Would run";
      fact(card, "path", harness.path || (found(harness) ? "" : "Not on your PATH"), {flash});
      card.querySelector('[data-fact="path"]').parentNode.hidden = !harness.path && found(harness);
      card.querySelector('[data-fact="path"]').classList.toggle("hn-path", Boolean(harness.path));
      const base = card.querySelector('[data-fact="base"]');
      fill(base, JSON.stringify(harness.base), () => harness.base
        ? [harness.base.name || harness.base.command, ...(harness.base.path ? [" at ", element("code", harness.base.path)] : harness.base.command && harness.base.name ? [" (", element("code", harness.base.command), ")"] : [])] : []);
      base.parentNode.hidden = !harness.base || !(harness.base.name || harness.base.command);
      base.hidden = false;

      // The fix: agentflow's text for this state, the command to copy and
      // the link. A ready harness has none.
      const fix = card.querySelector('[data-part="fix"]');
      fix.hidden = isReady && !harness.fix;
      fix.dataset.tone = shows.tone;
      const fixed = fill(fix, isReady && !harness.fix ? "" : JSON.stringify([harness.fix, harness.fix ? "" : meaning(harness)]), () => isReady && !harness.fix ? [] : fixParts(harness));
      if (fixed && flash && !fix.hidden) mark(fix);

      // Sign-in: the verdict, then how to sign in. Shown for a harness
      // whose command was found.
      const signIn = card.querySelector('[data-part="sign-in"]');
      signIn.hidden = !found(harness);
      const auth = authView(harness.auth);
      fact(card, "auth", auth.label, {state: auth.mark, flash});
      // The fix may already say the same sentence (env_credential).
      fact(card, "auth-detail", harness.fix?.text.toLowerCase() === auth.detail.toLowerCase() ? "" : auth.detail);
      fact(card, "auth-at", harness.auth.at ? "Checked " + clock(harness.auth.at, at) : "");
      if (harness.auth.at) card.querySelector('time[data-fact="auth-at"]').dateTime = new Date(harness.auth.at).toISOString();
      const methods = card.querySelector('[data-part="methods"]');
      methods.hidden = !harness.signIn.length;
      fact(card, "methods-title", harness.signIn.length === 1 ? "How to sign in" : "How to sign in (" + harness.signIn.length + " ways)");
      fill(card.querySelector(".hn-method-list"), JSON.stringify([harness.signIn, harness.name]), () => harness.signIn.map(method => methodRow(method, harness)));
      // Open when the harness needs a sign-in, closed otherwise. The
      // reader's own toggle stays until that changes.
      const needed = String(harness.state === "needs_auth" || harness.auth.status === "signed_out");
      if (methods.dataset.needed !== needed) {
        methods.dataset.needed = needed;
        methods.open = needed === "true";
      }

      const notes = card.querySelector('[data-part="notes"]');
      // The reason is registry text: shown after agentflow's own sentence, as text.
      const quarantine = harness.quarantineReason ? "The ACP registry has quarantined this agent: " + harness.quarantineReason : "";
      const noted = harness.notes.concat(harness.quarantined || quarantine ? ["quarantined"] : []);
      const noteList = [...new Set(noted)].map(note => note === "quarantined" && quarantine ? quarantine : note);
      notes.hidden = !noteList.length;
      fill(notes, JSON.stringify(noteList), () => noteList.map(note => element("li", noteCodes[note] || note)));

      const state = card.querySelector('[data-fact="state"]');
      fill(state, harness.state + "|" + harness.code, () => [stateNames[harness.state], ...(harness.code ? [" ", element("code", harness.code)] : [])]);
      state.hidden = false;
      const probe = card.querySelector('[data-fact="probe"]');
      const when = harness.probe.at ? clock(harness.probe.at, at) : "";
      fill(probe, JSON.stringify([harness.probe, when]), () => {
        const out = [probeText(harness.probe)];
        if (harness.probe.at) {
          const stamp = element("time", when);
          stamp.dateTime = new Date(harness.probe.at).toISOString();
          out.push(" · ", stamp);
        }
        return out;
      });
      probe.hidden = false;
      fact(card, "sources", harness.sources.map(sourceName).join(", then ") || "Not reported");
      fact(card, "acp", harness.acp ? "Protocol version " + (harness.acp.protocol || "not reported") + (harness.acp.agent ? " · agent " + harness.acp.agent + (harness.acp.agentVersion ? " " + harness.acp.agentVersion : "") : "") : "");
      fact(card, "can", harness.acp ? capital(harness.acp.can.join(", ")) || "None of the optional capabilities" : "");
      fact(card, "cannot", harness.acp ? capital(harness.acp.cannot.join(", ")) : "");
      for (const key of ["acp", "can", "cannot"]) card.querySelector('[data-fact="' + key + '"]').parentNode.hidden = card.querySelector('[data-fact="' + key + '"]').hidden;

      const button = card.querySelector('[data-part="probe"]');
      button.parentNode.hidden = !current.detector || !harness.probeable || !found(harness);
      button.setAttribute("aria-label", (probing ? "Probing: " : "Probe now: ") + harness.name);
      button.querySelector(".hn-probe-label").textContent = probing ? "Probing…" : "Probe now";
    }

    // Bring one group's list in line with its harnesses. A card that stays
    // is updated in place and moved, never rebuilt, so focus, open details
    // and highlights survive, also when a probe moves it to the other group.
    function reconcile(target, harnesses, flash, fresh) {
      let previous = null;
      for (const harness of harnesses) {
        const card = cards.get(harness.id);
        updateCard(card, harness, flash && !fresh.has(harness.id));
        const place = previous ? previous.nextSibling : target.firstChild;
        if (card !== place) {
          const moved = card.parentNode && card.parentNode !== target;
          target.insertBefore(card, place);
          if (moved && flash) mark(card);
        }
        if (fresh.has(harness.id) && flash) mark(card);
        previous = card;
      }
    }

    function summary(state) {
      const total = state.harnesses.length;
      if (!total) return "";
      const fine = state.harnesses.filter(ready).length;
      const parts = [plural(total, "harness", "harnesses") + " found", fine + " ready"];
      if (total - fine) parts.push(total - fine === 1 ? "1 needs attention" : (total - fine) + " need attention");
      return parts.join(" · ") + ".";
    }

    // The probe controls. A probe is rate limited per console, so the
    // buttons say when the next one may start instead of failing.
    function busy() {
      const at = now();
      if (asked && at - asked.at > askedFor) asked = null;
      return Boolean(current?.probing.length) || Boolean(asked) || at < until;
    }
    function probeStatus() {
      if (!current) return "";
      if (current.probing.length) {
        const names = current.probing.map(id => current.harnesses.find(harness => harness.id === id)?.name || id);
        return names.length === 1 ? "Probing " + names[0] + "…" : "Probing " + names.length + " harnesses…";
      }
      if (asked && now() - asked.at <= askedFor) return "Probe requested. The results arrive here.";
      const left = until - now();
      if (left > 0) return "You can probe again in " + remaining(left) + ".";
      return "";
    }
    function renderProbe() {
      if (!current) return;
      const line = find("harnesses-probe-status");
      const said = probeStatus();
      if (line.textContent !== said) line.textContent = said;
      const blocked = busy();
      const probes = [find("harnesses-probe"), ...[...cards.values()].map(card => card.querySelector('[data-part="probe"]'))];
      for (const button of probes) {
        if (blocked) button.setAttribute("aria-disabled", "true");
        else button.removeAttribute("aria-disabled");
      }
      const none = !current.detector || !current.harnesses.some(harness => harness.probeable && found(harness));
      find("harnesses-probe").parentNode.hidden = none;
      find("harnesses-probe-hint").hidden = none;
      const error = find("harnesses-probe-error");
      if (error.textContent !== failure) error.textContent = failure;
      error.hidden = !failure;
    }

    async function requestProbe(ids) {
      if (!current || !options.request) return;
      if (busy()) { options.announce(probeStatus() || "A probe is running."); return; }
      const body = ids ? {ids: ids.filter(id => idForm.test(id))} : {};
      if (ids && !body.ids.length) return;
      asked = {at: now()};
      failure = "";
      renderProbe();
      tick();
      try {
        await options.request("api/harnesses/probe", body);
        until = now() + probeEvery;
        options.announce(ids ? "Probe requested for " + (current.harnesses.find(harness => harness.id === ids[0])?.name || ids[0]) + "." : "Probe requested for every harness found.");
      } catch (error) {
        asked = null;
        failure = "The probe did not start. " + (shown(error?.message, 200) || "The console did not say why.");
        options.announce(failure);
      }
      renderProbe();
      tick();
    }

    // The window came back into view: ask the daemon to look again. The
    // answer carries no data; a change arrives over the stream.
    function check() {
      if (!options.request || !open() || document.visibilityState !== "visible") return;
      if (current && !current.detector) return;
      const at = now();
      if (at - lastCheck < checkEvery) return;
      lastCheck = at;
      Promise.resolve().then(() => options.request("api/harnesses/check", {})).catch(() => { /* The stream indicator already says when the console is unreachable. */ });
    }

    function render(flash) {
      const state = current;
      find("harnesses-body").hidden = false;
      find("harnesses-detector").hidden = state.detector;
      const line = find("harnesses-summary");
      const totals = summary(state);
      const checked = state.checked ? "Checked at " + clock(state.checked, now()) + "." : "";
      const sentence = [totals, checked].filter(Boolean).join(" ");
      if (line.textContent !== sentence) line.textContent = sentence;
      line.hidden = !sentence;

      // Cards: build the new ones, drop the ones that left, then place
      // each in its group.
      const focused = document.activeElement;
      const fresh = new Set();
      const kept = new Set(state.harnesses.map(harness => harness.id));
      for (const harness of state.harnesses) {
        if (cards.has(harness.id)) continue;
        cards.set(harness.id, buildCard(harness));
        fresh.add(harness.id);
      }
      for (const [id, card] of cards) {
        if (kept.has(id)) continue;
        // A card that goes away must not take keyboard focus with it.
        if (card.contains(focused)) find("harnesses-title").focus({preventScroll: true});
        card.remove();
        cards.delete(id);
      }
      const fine = state.harnesses.filter(ready), rest = state.harnesses.filter(harness => !ready(harness));
      reconcile(find("harnesses-ready-list"), fine, flash, fresh);
      reconcile(find("harnesses-attention-list"), rest, flash, fresh);
      // Moving a card between the groups drops focus in the browser; give it back.
      if (focused && focused !== document.activeElement && find("harnesses-body").contains(focused)) focused.focus({preventScroll: true});
      const any = Boolean(state.harnesses.length);
      find("harnesses-ready").hidden = !any;
      find("harnesses-attention").hidden = !any;
      find("harnesses-ready-list").hidden = !fine.length;
      find("harnesses-attention-list").hidden = !rest.length;
      find("harnesses-ready-none").hidden = Boolean(fine.length);
      find("harnesses-attention-none").hidden = Boolean(rest.length);
      find("harnesses-ready-count").textContent = fine.length ? String(fine.length) : "";
      find("harnesses-attention-count").textContent = rest.length ? String(rest.length) : "";
      find("harnesses-empty").hidden = any || !state.detector;

      // The header: the registry copy, automatic probes, skipped PATH
      // entries and problems.
      const registry = state.registry;
      const copyOf = [];
      if (registry.commit) copyOf.push("commit " + registry.commit.slice(0, 12));
      if (registry.committed) copyOf.push("from " + new Date(registry.committed).toLocaleDateString([], {dateStyle: "medium"}));
      if (registry.agents) copyOf.push(plural(registry.agents, "agent"));
      const registryText = copyOf.join(" · ") || "Not reported";
      if (find("harnesses-registry").textContent !== registryText) find("harnesses-registry").textContent = registryText;
      find("harnesses-registry").title = registry.commit;
      const auto = find("harnesses-auto");
      const autoText = state.autoProbe ? "On" : "Off";
      if (auto.querySelector(".fact-text").textContent !== autoText) {
        const first = auto.querySelector(".fact-text").textContent === "";
        auto.querySelector(".fact-text").textContent = autoText;
        if (flash && !first) mark(auto);
      }
      auto.dataset.state = state.autoProbe ? "on" : "off";
      find("harnesses-auto-detail").textContent = !state.detector ? "This daemon does not detect harnesses, so the setting has no effect here."
        : state.autoProbe ? "Agentflow starts a new or changed harness once, to check that it answers."
        : "Agentflow only looks at files. It starts a harness when you press Probe now.";
      for (const link of options.root.querySelectorAll("#page-harnesses a[data-settings]")) link.setAttribute("href", settingsRoute());

      const skipped = find("harnesses-skipped");
      skipped.hidden = !state.skipped.length;
      find("harnesses-skipped-title").textContent = plural(state.skipped.length, "PATH entry", "PATH entries") + " skipped";
      fill(find("harnesses-skipped-list"), JSON.stringify(state.skipped), () => state.skipped.map(item => {
        const row = element("li");
        row.append(element("code", item.dir), " ", element("span", skipReason(item.reason)));
        return row;
      }));
      const problems = find("harnesses-problems");
      problems.hidden = !state.problems.length;
      find("harnesses-problems-title").textContent = state.problems.length === 1 ? "1 harness definition has a problem" : state.problems.length + " harness definitions have problems";
      fill(find("harnesses-problem-list"), JSON.stringify(state.problems), () => state.problems.map(item => {
        const row = element("li");
        const from = [item.id, item.layer ? "from " + sourceName(item.layer) : ""].filter(Boolean).join(" ");
        if (from) row.append(element("strong", from), " ");
        row.append(item.message || "No message.");
        if (item.code) row.append(" ", element("code", item.code));
        return row;
      }));

      renderProbe();
      tick();
      status(live);
    }

    // One timer runs once a second while the probe line counts down and the
    // page is open, and stops otherwise. A tick is never highlighted or
    // announced.
    function counting() {
      return Boolean(current) && open() && (now() < until || Boolean(asked));
    }
    function tick() {
      if (counting()) {
        if (!ticker) ticker = setInterval(() => {
          renderProbe();
          if (!counting()) tick();
        }, 1000);
      } else if (ticker) {
        clearInterval(ticker);
        ticker = 0;
        renderProbe();
      }
    }

    // What to say about a live change: a harness found or gone, and a
    // change of state.
    function changes(before, after) {
      const old = new Map(before.harnesses.map(harness => [harness.id, harness]));
      const out = [];
      for (const harness of after.harnesses) {
        const was = old.get(harness.id);
        old.delete(harness.id);
        const label = view(harness).label.toLowerCase();
        if (!was) { out.push("Harness " + harness.name + " found: " + label); continue; }
        if (view(was).label !== view(harness).label) out.push("Harness " + harness.name + ": " + label);
        else if (was.auth.status !== harness.auth.status) out.push("Harness " + harness.name + ": " + authView(harness.auth).label.toLowerCase());
      }
      for (const gone of old.values()) out.push("Harness " + gone.name + " is no longer found");
      if (before.probing.length && !after.probing.length) out.push("Probe finished");
      if (before.detector && !after.detector) out.push("Harness detection stopped");
      return out;
    }

    function say(sentences) {
      if (!sentences.length || !open()) return;
      const first = sentences.slice(0, 3);
      if (sentences.length > first.length) first.push("and " + plural(sentences.length - first.length, "more change"));
      options.announce(first.join(". ") + ".");
    }

    // Show the harnesses state. With flash, values that differ from the
    // ones on screen are highlighted and changes are announced. null keeps
    // the last values.
    function show(raw, flash = false) {
      const next = harnessesState(raw);
      if (!next) return current;
      const said = flash && current ? changes(current, next) : [];
      // The stream now shows the probe that was asked for.
      if (asked && (next.probing.length || next.checked > asked.at)) asked = null;
      current = next;
      render(flash);
      say(said);
      return current;
    }

    // The stream's state, for the note above the cards: {state, offline (a
    // reason's own sentence, or ""), ready, present}.
    function status(next) {
      live = next;
      const have = Boolean(current);
      let note = "";
      if (live.state === "offline") note = live.offline || "Live updates are offline. " + (have ? "The page shows the last harness states it received." : "Harnesses appear when they return.") + " The console keeps retrying.";
      else if (live.state === "live" && live.ready && !live.present) note = "The live stream has no harness state for this repository yet. Harness detection needs a console daemon that runs it.";
      find("harnesses-note").textContent = note;
      find("harnesses-note").hidden = !note;
      find("harnesses-wait").hidden = have || Boolean(note);
    }

    find("harnesses-probe").addEventListener("click", () => requestProbe(null));
    for (const node of options.root.querySelectorAll("#page-harnesses pre[data-command]")) node.replaceWith(command(node.textContent, "Command"));
    for (const link of options.root.querySelectorAll("#page-harnesses a[data-settings]")) link.setAttribute("href", settingsRoute());
    document.addEventListener("visibilitychange", check);
    (options.view || scope).addEventListener("focus", check);

    return {
      show, status, tick, check,
      get harnesses() { return current; }
    };
  }

  scope.AgentflowHarnesses = {harnessesState, ready, view, meaning, probeText, authView, skipReason, remaining, createHarnesses};
})(globalThis);
