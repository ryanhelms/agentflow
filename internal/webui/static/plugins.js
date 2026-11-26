"use strict";

// The Plugins page (docs/design/ui/plugins.md). It shows one repository-wide
// state from the event stream: PluginsState in
// internal/webui/projection_plugins_state.go. The page sends no request:
// every value arrives over the stream, enabling is a Settings change, and
// installing and removing are terminal commands the page only shows.
//
// Values are read defensively: unknown fields are ignored and missing ones
// read as empty, so the shape can grow. A plugin's own text (name,
// description, deny message) is untrusted: it is always set as text, never
// as markup, and never read as an instruction.
(function (scope) {
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const count = value => Number.isSafeInteger(value) && value > 0 ? value : 0;
  const time = value => { const at = Date.parse(text(value)); return Number.isFinite(at) && at > 0 ? at : 0; };
  const pick = (value, known, fallback = "") => known.includes(value) ? value : fallback;
  const words = value => value.replaceAll("_", " ");
  const plural = (n, one, many = one + "s") => n + " " + (n === 1 ? one : many);
  // Display text from a plugin: one line, no control characters, bounded.
  const shown = (value, max) => {
    const line = text(value).replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]+/g, " ").replace(/\s+/g, " ").trim();
    return line.length > max ? line.slice(0, max - 1) + "…" : line;
  };

  const states = ["installed", "starting", "running", "degraded", "backoff", "failed", "stopping", "stopped", "removed"];
  const tiers = ["declarative", "process", "builtin"];
  const points = ["handler", "permission_rule"];
  // manifest.Manifest.ID: the form that names the Settings group plugins.<id>.
  const idForm = /^[a-z][a-z0-9-]{0,38}[a-z0-9]$/;
  const hashForm = /^sha256:[0-9a-f]{64}$/;
  const plainCode = /^[a-z0-9][a-z0-9_.-]{0,63}$/;
  const namePattern = /^[a-z0-9*][a-z0-9_.*-]{0,95}$/;
  const maxLetters = 20;       // MaxPluginLetters
  const crashLoop = 5;         // restarts in 10 minutes before "failed" (ADR, "Phase 5 limits")

  // Event patterns, event types and command names are identifiers. One that
  // is not is left out rather than shown.
  const names = value => [...new Set(list(value).filter(item => typeof item === "string" && namePattern.test(item)))];

  function pluginsState(raw) {
    raw = record(raw);
    if (!raw) return null;
    const seen = new Set();
    const plugins = [];
    for (const item of list(raw.plugins).map(record)) {
      if (!item || !text(item.id) || seen.has(item.id)) continue;
      seen.add(item.id);
      const permissions = record(item.permissions) || {};
      const extensions = [];
      for (const extension of list(item.extensions).map(record)) {
        if (!extension || !points.includes(extension.point)) continue;
        extensions.push({
          point: extension.point, name: shown(extension.name, 64),
          on: names(extension.on), invokes: names(extension.invokes), annotates: extension.annotates === true,
          deny: shown(extension.deny, 512)
        });
      }
      plugins.push({
        id: item.id, valid: idForm.test(item.id),
        name: shown(item.name, 64) || item.id, description: shown(item.description, 512),
        version: shown(item.version, 40), tier: pick(item.tier, tiers, "unknown"),
        hash: hashForm.test(text(item.hash)) ? item.hash : "", installed: time(item.installed_at),
        enabled: item.enabled === true, disabledHere: item.disabled_here === true,
        state: pick(item.state, states, text(item.state) ? "unknown" : ""), code: text(item.code),
        since: time(item.since), restarts: count(item.restarts), retry: time(item.retry_at),
        delivered: count(item.delivered), deadLetters: count(item.dead_letters), dropped: count(item.dropped),
        invocations: count(item.invocations), published: count(item.published),
        extensions,
        permissions: {subscribe: names(permissions.subscribe), publish: names(permissions.publish), invoke: names(permissions.invoke)}
      });
    }
    const letterIDs = new Set();
    const letters = [];
    for (const letter of list(raw.dead_letters).map(record)) {
      if (!letter || !text(letter.plugin) || letters.length >= maxLetters) continue;
      const key = letter.plugin + "/" + text(letter.event_id) + "/" + text(letter.time);
      if (letterIDs.has(key)) continue;
      letterIDs.add(key);
      letters.push({key, plugin: letter.plugin, event: text(letter.event_id), type: namePattern.test(text(letter.event_type)) ? letter.event_type : "", code: text(letter.code), at: time(letter.time)});
    }
    return {host: raw.host === true, plugins, letters};
  }

  // One tag and one mark per plugin. The label carries the meaning; the tone
  // and the mark's shape repeat it. attention marks what the rail counts.
  const stateViews = {
    "": {label: "Not running", tone: "", attention: false},
    installed: {label: "Not enabled", tone: "", attention: false},
    starting: {label: "Starting", tone: "", attention: false},
    running: {label: "Running", tone: "good", attention: false},
    degraded: {label: "Degraded", tone: "warn", attention: true},
    backoff: {label: "Restarting", tone: "warn", attention: true},
    failed: {label: "Failed", tone: "bad", attention: true},
    stopping: {label: "Stopping", tone: "", attention: false},
    stopped: {label: "Stopped", tone: "", attention: false},
    removed: {label: "Removed", tone: "", attention: false},
    unknown: {label: "Unknown state", tone: "", attention: false}
  };

  // Lifecycle codes are fixed identifiers (bus.PluginCode*), never a
  // plugin's own error text. Each has a short clause that completes "It …".
  const codes = {
    exited: "its program exited",
    crashed: "its program crashed",
    timeout: "a request to it timed out",
    protocol: "it sent a message Agentflow could not read",
    ping: "it stopped answering health checks",
    launch: "its program could not be started",
    overflow: "it fell behind and events were dropped",
    crash_loop: "it restarted " + crashLoop + " times in 10 minutes",
    hash_mismatch: "its installed files changed",
    manifest_invalid: "its installed manifest is no longer valid"
  };
  function codeShort(code) {
    if (!code || code === "enabled" || code === "disabled") return "";
    return codes[code] || (plainCode.test(code) ? "reason code " + code : "the reason is not readable");
  }
  const sentence = clause => clause ? clause[0].toUpperCase() + clause.slice(1) + "." : "";

  // Dead letter codes (ADR, "Phase 5 Dead Letter Channel").
  const letterCodes = {
    timeout: "timed out", crashed: "the plugin crashed", exited: "the plugin exited", protocol: "unreadable reply",
    action: "an action failed", rpc_error: "the plugin returned an error"
  };
  const letterShort = code => letterCodes[code] || (code && plainCode.test(code) ? words(code) : "unknown reason");

  const tierViews = {
    declarative: ["Declarative", "YAML rules that Agentflow reads. It runs no code of its own."],
    process: ["Process", "Its own program, started by the daemon. It runs as you."],
    builtin: ["Built in", "Part of Agentflow itself."],
    unknown: ["Unknown tier", "Agentflow does not know this kind of plugin."]
  };

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
  // "in 1 min 05 s", as the approvals countdown reads.
  function remaining(ms) {
    const total = Math.max(0, Math.ceil(ms / 1000));
    const minutes = Math.floor(total / 60), seconds = total % 60;
    return minutes ? minutes + " min " + String(seconds).padStart(2, "0") + " s" : seconds + " s";
  }

  // What the card says under the name: what the state means and what to do.
  function detail(plugin, host, now) {
    const why = codeShort(plugin.code);
    const again = "It starts again when you change its settings, turn it off and on, or install it again.";
    switch (plugin.state) {
      case "":
        return host ? "The plugin host has not reported this plugin yet." : "The plugin host is not running, so this plugin does not run.";
      case "installed":
        return "Installed and turned off. It does nothing until you enable it.";
      case "starting":
        return "Agentflow is starting it.";
      case "running":
        return plugin.disabledHere ? "Running, but turned off in this repository: it gets no events from here and cannot act here." : "";
      case "degraded":
        return "Running, but " + (plugin.code === "timeout" ? "a delivery to it timed out" : plugin.code === "ping" || !why ? "it missed health checks" : why) + ". Agentflow restarts it if this continues.";
      case "backoff": {
        const left = plugin.retry - now;
        const when = !plugin.retry ? "Agentflow restarts it shortly." : left > 0 ? "Restarts in " + remaining(left) + " (at " + clock(plugin.retry, now, true) + ")." : "Restarting now.";
        const tries = plugin.restarts ? " Restart " + Math.min(plugin.restarts, crashLoop) + " of " + crashLoop + " in 10 minutes." : "";
        return (sentence(why) ? sentence(why) + " " : "") + when + tries;
      }
      case "failed":
        if (plugin.code === "hash_mismatch") return "Its installed files changed after you installed it, so Agentflow will not run it. Check the plugin, then install it again from its folder.";
        if (plugin.code === "manifest_invalid") return "Its installed manifest is no longer valid, so Agentflow will not run it. Install it again from its folder.";
        if (plugin.code === "crash_loop") return "It restarted " + crashLoop + " times in 10 minutes, so Agentflow stopped it. " + again;
        if (plugin.code === "protocol") return "It answered in a protocol version Agentflow does not speak, so Agentflow stopped it. " + again;
        return (sentence(why) || "It failed. No reason was recorded.") + " Agentflow stopped it. " + again;
      case "stopping":
        return "Agentflow is stopping it.";
      case "stopped":
        return plugin.enabled ? "Stopped with the daemon. It starts when the daemon starts." : "Turned off.";
      case "removed":
        return "Removed. Its card leaves with the next update.";
    }
    return "Agentflow reported a state this console does not know.";
  }

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }

  // options: {root, announce(message), now(), copy(text), visible()}. root
  // holds the page markup of index.html.
  function createPlugins(options) {
    const find = id => options.root.querySelector("#" + id);
    const now = () => options.now ? options.now() : Date.now();
    const timers = new WeakMap();
    let current = null, live = {state: "connecting"}, ticker = 0;

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

    // Replace a sublist only when what it shows changed. Sublists hold no
    // focusable element, so replacing one never moves focus.
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
        const label = button.querySelector(".pg-copy-label") || button;
        clearTimeout(timers.get(button));
        label.textContent = "Copied";
        timers.set(button, setTimeout(() => { label.textContent = "Copy"; }, 2000));
      } catch {
        options.announce("The browser did not allow copying. Select the command to copy it.");
      }
    }

    // A terminal command the reader copies: the text and one Copy button.
    function command(value, what) {
      const box = element("div", "", "pg-command");
      const code = element("code", value);
      const pre = element("pre");
      pre.append(code);
      const button = element("button", "", "button secondary small");
      button.type = "button";
      button.append(element("span", "Copy", "pg-copy-label"));
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

    function buildCard(plugin) {
      const card = element("li", "", "pg-card panel");
      const title = element("h2", "", "pg-name");
      title.id = "plugin-" + plugin.id + "-name";
      title.tabIndex = -1;
      title.append(slot("name", {marked: true, klass: "fact"}));
      const head = element("div", "", "pg-head");
      head.append(title, slot("status", {klass: "tag"}));
      card.setAttribute("aria-labelledby", title.id);

      const facts = element("p", "", "pg-facts");
      const id = element("code", plugin.id);
      facts.append(id, slot("version"), slot("tier"), slot("since", {tag: "time"}));

      const enable = element("div", "", "pg-enable");
      const flags = element("p", "", "pg-flags");
      flags.append(slot("enabled", {klass: "fact", marked: true}), slot("here", {klass: "tag"}));
      const link = element("a", "", "button secondary small");
      link.dataset.part = "settings";
      enable.append(flags, link, slot("no-settings", {tag: "p", klass: "muted pg-hint"}));

      const columns = element("div", "", "pg-columns");
      const adds = element("section");
      const addsTitle = element("h3", "What it adds");
      addsTitle.id = "plugin-" + plugin.id + "-adds";
      const extensions = element("ul", "", "pg-extensions");
      extensions.setAttribute("aria-labelledby", addsTitle.id);
      adds.append(addsTitle, extensions);
      const may = element("section");
      const mayTitle = element("h3", "What it may do");
      mayTitle.id = "plugin-" + plugin.id + "-may";
      const permissions = element("dl", "", "pg-permissions");
      permissions.setAttribute("aria-labelledby", mayTitle.id);
      may.append(mayTitle, permissions, element("p", "These limits apply to what it does through Agentflow.", "muted pg-hint"));
      columns.append(adds, may);

      const activity = element("section", "", "pg-activity");
      const activityTitle = element("h3", "Since the daemon started");
      activityTitle.id = "plugin-" + plugin.id + "-activity";
      const counters = element("dl", "", "pg-counters");
      counters.setAttribute("aria-labelledby", activityTitle.id);
      for (const [key, label] of [["delivered", "Events delivered"], ["dead", "Failed deliveries"], ["dropped", "Events dropped"], ["invocations", "Commands run"], ["published", "Annotations added"]]) {
        counters.append(term(label, slot(key, {tag: "dd"})));
      }
      activity.append(activityTitle, counters);

      const failures = element("details", "", "pg-letters");
      failures.dataset.part = "letters";
      const failuresSummary = element("summary");
      failuresSummary.append(slot("letters-title"));
      const letters = element("ul", "", "pg-letter-list");
      failures.append(failuresSummary, letters, slot("letters-note", {tag: "p", klass: "muted pg-hint"}));

      const about = element("details", "", "pg-about");
      const aboutSummary = element("summary", "Installed files and removal");
      const aboutFacts = element("dl", "", "pg-about-facts");
      aboutFacts.append(term("Content hash", slot("hash", {tag: "dd", klass: "pg-hash"})), term("Installed", slot("installed", {tag: "dd"})));
      const remove = element("div", "", "pg-remove");
      remove.dataset.part = "remove";
      about.append(aboutSummary, aboutFacts, remove);

      card.append(head, facts, slot("description", {tag: "p", klass: "pg-description"}), slot("detail", {tag: "p", klass: "pg-detail"}), enable, columns, activity, failures, about);
      return card;
    }

    function extensionRow(extension) {
      const item = element("li");
      const kind = extension.point === "handler" ? "Handler" : "Permission rule";
      const head = element("p", "", "pg-extension-head");
      head.append(element("span", kind, "pg-kind"), " ", element("strong", extension.name || "unnamed"));
      item.append(head);
      const lines = element("ul", "", "pg-extension-lines");
      const line = (label, values) => {
        const row = element("li");
        row.append(label + " ");
        values.forEach((value, index) => row.append(index ? ", " : "", element("code", value)));
        lines.append(row);
      };
      if (extension.point === "handler") {
        if (extension.on.length) line("Reacts to", extension.on);
        if (extension.invokes.length) line("Runs", extension.invokes);
        if (extension.annotates) lines.append(element("li", "Adds notes to the timeline"));
        if (!extension.on.length && !extension.invokes.length && !extension.annotates) lines.append(element("li", "Declares no events or actions"));
      } else {
        const row = element("li", "Denies matching operations with the message: ");
        // The message is the plugin's text: shown as a quotation, as text.
        row.append(element("q", extension.deny || "(no message)"));
        lines.append(row);
        lines.append(element("li", "A rule can only deny. It never allows an operation."));
      }
      item.append(lines);
      return item;
    }

    function permissionRows(permissions) {
      const row = (label, values, none) => {
        const value = element("dd");
        if (!values.length) value.append(element("span", none, "muted"));
        values.forEach((item, index) => value.append(index ? ", " : "", element("code", item)));
        return term(label, value);
      };
      return [
        row("Receive events", permissions.subscribe, "None"),
        row("Publish events", permissions.publish, "None"),
        row("Run commands", permissions.invoke, "None")
      ];
    }

    function letterRow(letter, name) {
      const item = element("li");
      const head = element("p", "", "pg-letter-head");
      if (name) head.append(element("strong", name), " ");
      head.append(element("code", letter.type || "unknown event"), " ", element("span", letterShort(letter.code), "pg-letter-code"));
      const when = element("time", clock(letter.at, now(), true));
      if (letter.at) {
        when.dateTime = new Date(letter.at).toISOString();
        when.title = new Date(letter.at).toLocaleString();
      }
      head.append(" ", when);
      item.append(head);
      if (letter.event) {
        const id = element("p", "Event ID ", "pg-letter-id");
        id.append(element("code", letter.event));
        item.append(id);
      }
      return item;
    }

    function settingsRoute(plugin) {
      return plugin.valid ? "#settings?group=" + encodeURIComponent("plugins." + plugin.id) : "";
    }

    function updateCard(card, plugin, flash) {
      const view = stateViews[plugin.state] || stateViews.unknown;
      const at = now();
      card.dataset.state = plugin.state || "none";
      fact(card, "name", plugin.name, {state: plugin.state || "none"});
      fact(card, "status", view.label, {state: view.tone, flash});
      fact(card, "version", plugin.version ? "Version " + plugin.version : "");
      const [tier, tierHint] = tierViews[plugin.tier];
      fact(card, "tier", tier);
      card.querySelector('[data-fact="tier"]').title = tierHint;
      const steady = plugin.state && plugin.since ? view.label + " since " + clock(plugin.since, at) : "";
      fact(card, "since", steady);
      if (plugin.since) card.querySelector('time[data-fact="since"]').dateTime = new Date(plugin.since).toISOString();
      fact(card, "description", plugin.description);
      // The countdown ticks once a second: only a new sentence is highlighted.
      const explained = detail(plugin, current.host, at);
      const node = card.querySelector('[data-fact="detail"]');
      const basis = plugin.state + "|" + plugin.code + "|" + plugin.restarts + "|" + plugin.retry + "|" + plugin.disabledHere + "|" + plugin.enabled;
      const moved = node.dataset.basis !== basis;
      node.dataset.basis = basis;
      fact(card, "detail", explained, {flash: flash && moved});
      node.dataset.tone = view.tone;

      fact(card, "enabled", plugin.enabled ? "Enabled" : "Turned off", {state: plugin.enabled ? "on" : "off", flash});
      fact(card, "here", plugin.disabledHere ? "Turned off in this repository" : "", {state: "warn", flash});
      const link = card.querySelector('[data-part="settings"]');
      const href = settingsRoute(plugin);
      link.hidden = !href;
      if (href) {
        link.textContent = plugin.enabled ? "Open its settings" : "Enable in Settings";
        link.setAttribute("href", href);
        link.setAttribute("aria-label", (plugin.enabled ? "Open the settings of " : "Enable in Settings: ") + plugin.name);
        link.classList.toggle("primary", !plugin.enabled);
        link.classList.toggle("secondary", plugin.enabled);
      }
      fact(card, "no-settings", href ? "" : "This plugin's ID is not valid, so it has no Settings group.");

      fill(card.querySelector(".pg-extensions"), JSON.stringify(plugin.extensions), () => plugin.extensions.length
        ? plugin.extensions.map(extensionRow)
        : [element("li", "Nothing. This plugin declares no handler and no permission rule.", "muted")]);
      fill(card.querySelector(".pg-permissions"), JSON.stringify(plugin.permissions), () => permissionRows(plugin.permissions));

      fact(card, "delivered", String(plugin.delivered), {flash});
      fact(card, "dead", String(plugin.deadLetters), {state: plugin.deadLetters ? "bad" : "", flash});
      fact(card, "dropped", String(plugin.dropped), {state: plugin.dropped ? "warn" : "", flash});
      fact(card, "invocations", String(plugin.invocations), {flash});
      fact(card, "published", String(plugin.published), {flash});

      const letters = current.letters.filter(letter => letter.plugin === plugin.id);
      const failures = card.querySelector('[data-part="letters"]');
      failures.hidden = !letters.length && !plugin.deadLetters;
      fact(card, "letters-title", letters.length ? "Recent failed deliveries (" + letters.length + ")" : "Failed deliveries", {flash});
      const changed = fill(card.querySelector(".pg-letter-list"), JSON.stringify(letters), () => letters.map(letter => letterRow(letter, "")));
      if (changed && flash && letters.length && !failures.open) mark(failures.querySelector("summary"));
      fact(card, "letters-note", !letters.length
        ? "Its failed deliveries are older than the " + maxLetters + " newest this page lists. Run agentflow plugin list --json to see them."
        : "An event that could not be delivered is kept, not sent again. Newest first.");

      fact(card, "hash", plugin.hash || "Not recorded");
      fact(card, "installed", plugin.installed ? new Date(plugin.installed).toLocaleString([], {dateStyle: "medium", timeStyle: "short"}) : "Not recorded");
      fill(card.querySelector('[data-part="remove"]'), plugin.id + "|" + plugin.valid, () => plugin.valid
        ? [element("p", "To remove this plugin, its files, its data and its settings, run this in a terminal. Agentflow asks you to confirm."), command("agentflow plugin remove " + plugin.id, "Remove command")]
        : [element("p", "Run agentflow plugin list in a terminal to see this plugin, then remove it by its ID.", "muted")]);
    }

    // Bring the card list in line with the plugins without rebuilding the
    // cards that stay, so focus, open details and highlights survive.
    function reconcile(target, plugins, flash) {
      const focused = document.activeElement;
      const cards = new Map();
      for (const card of [...target.children]) cards.set(card.dataset.key, card);
      let previous = null;
      for (const plugin of plugins) {
        let card = cards.get(plugin.id);
        const fresh = !card;
        if (fresh) {
          card = buildCard(plugin);
          card.dataset.key = plugin.id;
        }
        cards.delete(plugin.id);
        updateCard(card, plugin, flash && !fresh);
        const place = previous ? previous.nextSibling : target.firstChild;
        if (card !== place) target.insertBefore(card, place);
        if (fresh && flash) mark(card);
        previous = card;
      }
      for (const card of cards.values()) {
        // A card that goes away must not take keyboard focus with it.
        if (card.contains(focused)) find("plugins-title").focus({preventScroll: true});
        card.remove();
      }
      if (focused && focused !== document.activeElement && target.contains(focused)) focused.focus({preventScroll: true});
    }

    function attention(state) {
      return state.plugins.filter(plugin => (stateViews[plugin.state] || stateViews.unknown).attention).length;
    }

    function summary(state) {
      const total = state.plugins.length;
      if (!total) return "";
      const running = state.plugins.filter(plugin => plugin.state === "running" || plugin.state === "degraded").length;
      const off = state.plugins.filter(plugin => !plugin.enabled).length;
      const needs = attention(state);
      const failed = state.plugins.reduce((sum, plugin) => sum + plugin.deadLetters, 0);
      const parts = [plural(total, "plugin") + " installed", running + " running"];
      if (off) parts.push(off + " turned off");
      if (needs) parts.push(needs === 1 ? "1 needs attention" : needs + " need attention");
      if (failed) parts.push(plural(failed, "failed delivery", "failed deliveries") + " since the daemon started");
      return parts.join(" · ") + ".";
    }

    function render(flash) {
      const state = current;
      find("plugins-body").hidden = false;
      find("plugins-host").hidden = state.host;
      const line = find("plugins-summary");
      const totals = summary(state);
      if (line.textContent !== totals) line.textContent = totals;
      line.hidden = !totals;
      const target = find("plugins-list");
      reconcile(target, state.plugins, flash);
      target.hidden = !state.plugins.length;
      find("plugins-add-title").textContent = state.plugins.length ? "Add a plugin" : "No plugins installed";
      find("plugins-add-lead").hidden = Boolean(state.plugins.length);

      // Letters of a plugin that is no longer installed have no card.
      const installed = new Set(state.plugins.map(plugin => plugin.id));
      const orphans = state.letters.filter(letter => !installed.has(letter.plugin));
      find("plugins-orphans").hidden = !orphans.length;
      fill(find("plugins-orphan-list"), JSON.stringify(orphans), () => orphans.map(letter => letterRow(letter, idForm.test(letter.plugin) ? letter.plugin : "Unknown plugin")));

      const needs = attention(state);
      const badge = find("plugins-nav-count");
      if (badge) {
        badge.textContent = needs ? String(needs) : "";
        badge.hidden = !needs;
        find("plugins-nav-label").textContent = needs ? (needs === 1 ? ", 1 plugin needs attention" : ", " + needs + " plugins need attention") : "";
      }
      tick();
      status(live);
    }

    // One timer runs once a second while a restart counts down and the page
    // is open, and stops otherwise. A tick is never highlighted or announced.
    function counting() {
      return Boolean(current) && (!options.visible || options.visible()) && current.plugins.some(plugin => plugin.state === "backoff" && plugin.retry > now());
    }
    function tick() {
      if (counting()) {
        if (!ticker) ticker = setInterval(() => {
          if (current) for (const card of find("plugins-list").children) {
            const plugin = current.plugins.find(item => item.id === card.dataset.key);
            if (plugin?.state === "backoff") fact(card, "detail", detail(plugin, current.host, now()));
          }
          if (!counting()) tick();
        }, 1000);
      } else if (ticker) {
        clearInterval(ticker);
        ticker = 0;
      }
    }

    // What to say about a live change: lifecycle changes and new failures.
    function changes(before, after) {
      const old = new Map(before.plugins.map(plugin => [plugin.id, plugin]));
      const out = [];
      for (const plugin of after.plugins) {
        const was = old.get(plugin.id);
        old.delete(plugin.id);
        if (!was) { out.push("Plugin " + plugin.name + " installed"); continue; }
        if (was.state !== plugin.state) {
          const why = codeShort(plugin.code);
          if (plugin.state === "failed") out.push("Plugin " + plugin.name + " failed" + (why ? ": " + why : ""));
          else if (plugin.state === "backoff") out.push("Plugin " + plugin.name + " is restarting" + (why ? ": " + why : ""));
          else if (plugin.state === "degraded") out.push("Plugin " + plugin.name + " is degraded");
          else if (plugin.state === "running") out.push("Plugin " + plugin.name + " is running");
          else if (plugin.state === "stopped" || plugin.state === "installed") out.push("Plugin " + plugin.name + " stopped");
        } else if (plugin.deadLetters > was.deadLetters) {
          out.push("Plugin " + plugin.name + ": " + plural(plugin.deadLetters - was.deadLetters, "delivery", "deliveries") + " failed");
        }
        if (was.enabled !== plugin.enabled && was.state === plugin.state) out.push("Plugin " + plugin.name + (plugin.enabled ? " enabled" : " turned off"));
      }
      for (const gone of old.values()) out.push("Plugin " + gone.name + " removed");
      if (before.host && !after.host) out.push("The plugin host stopped");
      return out;
    }

    function say(sentences) {
      if (!sentences.length || (options.visible && !options.visible())) return;
      const first = sentences.slice(0, 3);
      if (sentences.length > first.length) first.push("and " + plural(sentences.length - first.length, "more change"));
      options.announce(first.join(". ") + ".");
    }

    // Show the plugins state. With flash, values that differ from the ones on
    // screen are highlighted and changes are announced. null keeps the last
    // values.
    function show(raw, flash = false) {
      const next = pluginsState(raw);
      if (!next) return current;
      const said = flash && current ? changes(current, next) : [];
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
      if (live.state === "offline") note = live.offline || "Live updates are offline. " + (have ? "The page shows the last plugin states it received." : "Plugins appear when they return.") + " The console keeps retrying.";
      else if (live.state === "live" && live.ready && !live.present) note = "The live stream has no plugin state for this repository yet. Plugins need a console daemon that runs the plugin host.";
      find("plugins-note").textContent = note;
      find("plugins-note").hidden = !note;
      find("plugins-wait").hidden = have || Boolean(note);
    }

    const add = find("plugins-add-command");
    if (add) add.replaceWith(command("agentflow plugin add PATH", "Install command"));

    return {
      show, status, tick,
      get plugins() { return current; }
    };
  }

  scope.AgentflowPlugins = {pluginsState, codeShort, letterShort, detail, remaining, createPlugins};

  // ---------------------------------------------------------------------
  // Wiring. Lane 5.4 owns this file but not app.js, so the page attaches
  // itself here. Each block names the app.js change that replaces it.
  // ---------------------------------------------------------------------
  if (!scope.document || !scope.document.getElementById("page-plugins")) return;

  const offlineNotes = {
    unsupported: "This console does not serve live updates. Use Resync to reload the checkout.",
    forbidden: "The console refused the live stream for this worktree. Use Resync to try again.",
    busy: "Too many live streams are open for this console. Close another console tab, or use Resync.",
    unavailable: "Live updates are not available for this worktree yet. The console keeps retrying."
  };
  const shell = document.getElementById("console-shell");
  const main = document.getElementById("main");
  const section = document.getElementById("page-plugins");
  const entry = document.querySelector('.rail nav a[data-page="plugins"]');
  const open = () => !shell.hidden && /^#plugins(\?|$)/.test(location.hash);

  // WIRING 1: announcements. app.js owns #live-announcer through announce();
  // it loads after this file, so it is looked up when a message is sent.
  function announce(message) {
    if (typeof scope.announce === "function") { scope.announce(message); return; }
    const region = document.getElementById("live-announcer");
    region.textContent = "";
    requestAnimationFrame(() => { region.textContent = message; });
  }

  const page = createPlugins({root: document, announce, visible: open});

  // WIRING 2: the stream. The console holds one stream (live-stream.md, "Use
  // the shared stream, never open your own"), which app.js opens. This file
  // loads before app.js and passes the same frames on to the page. It adds
  // no topic: the plugins state rides in the repository's frames (ADR,
  // phase 5 decision 10). Replace with a showRepository("plugins") call in
  // app.js's onSnapshot and onDelta.
  let stream = {state: "connecting", reason: ""}, ready = false, present = false;
  const plugins = states => list(states).find(item => item && item.projection === "plugins" && !item.worktree);
  function report() {
    page.status({state: stream.state, offline: stream.state === "offline" ? offlineNotes[stream.reason] || "" : "", ready, present});
  }
  const events = scope.AgentflowEvents;
  if (events && typeof events.connect === "function") {
    const connect = events.connect;
    events.connect = options => connect({
      ...options,
      onStatus(status) {
        options.onStatus?.(status);
        stream = record(status) || stream;
        report();
      },
      onSnapshot(message) {
        options.onSnapshot?.(message);
        ready = true;
        const state = plugins(message?.states)?.state ?? null;
        present = Boolean(state);
        page.show(state, false);
        report();
      },
      onDelta(message) {
        options.onDelta?.(message);
        const next = plugins(message?.states);
        if (!next) return;
        const had = present;
        present = Boolean(next.state);
        page.show(next.state, had && present);
        report();
      }
    });
  }

  // WIRING 3: the route. app.js does not know #plugins: it shows the
  // overview for it. While #plugins is open this file shows its own page
  // instead (plugins.css hides the other pages) and marks its rail entry.
  // Replace with a "plugins" entry in screenNames and pages in app.js, and
  // in destination.go so `agentflow ui --screen plugins` opens it.
  function route(focus) {
    const on = open();
    main.classList.toggle("plugins-open", on);
    if (section.hidden === on) section.hidden = !on;
    for (const link of document.querySelectorAll(".rail nav a[data-page]")) {
      const here = on && link === entry;
      if (here && link.getAttribute("aria-current") !== "page") link.setAttribute("aria-current", "page");
      if (!here && (on || link === entry) && link.hasAttribute("aria-current")) link.removeAttribute("aria-current");
    }
    page.tick();
    if (on && focus) {
      main.focus({preventScroll: true});
      scope.scrollTo(0, 0);
    }
  }
  scope.addEventListener("hashchange", () => route(true));
  // app.js marks the overview's entry whenever it shows a page, and hides
  // the shell on the reconnect screen: follow both.
  const watch = new MutationObserver(() => route(false));
  watch.observe(document.querySelector(".rail nav"), {attributes: true, subtree: true, attributeFilter: ["aria-current"]});
  watch.observe(shell, {attributes: true, attributeFilter: ["hidden"]});
  route(false);
})(globalThis);
