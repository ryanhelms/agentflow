"use strict";

// The console's event stream (ADR event-driven-platform, "SSE wire format").
// A browser holds one stream per console origin. This file runs in two places:
//   - as a SharedWorker, where it owns the stream and every tab is a port;
//   - in the page, where it is the client for that worker and, when
//     SharedWorker is unavailable, runs the same hub for this tab alone.
// The stream is read with fetch so the session credential stays in a header
// and never reaches a URL or log.
(function (scope) {
  const firstDelay = 250, lastDelay = 10000;
  const silence = 45000;      // three missed 15 s heartbeats
  const snapshotWait = 5000;  // a snapshot must follow a resync frame
  const offlineAfter = 3;     // failed attempts before "reconnecting" becomes "offline"
  const maxTopics = 64;
  const endpointPath = /^\/w\/[a-z0-9][a-z0-9-]{0,62}\/api\/events$/;
  const topicForm = /^(\*|[0-9a-z]{1,32})\/(\*|[a-z0-9][a-z0-9-]{0,62})\/[a-z0-9_.*-]{1,96}$/;

  // Parse a text/event-stream body. push returns the items completed by the
  // new text: {type:"event",event,data,id} and {type:"retry",ms}.
  function createParser() {
    let buffer = "", name = "", data = [], lastID = "";
    function line(text, out) {
      if (text === "") {
        if (data.length) out.push({type: "event", event: name || "message", data: data.join("\n"), id: lastID});
        name = "";
        data = [];
        return;
      }
      if (text[0] === ":") return;
      const colon = text.indexOf(":");
      const field = colon < 0 ? text : text.slice(0, colon);
      let value = colon < 0 ? "" : text.slice(colon + 1);
      if (value[0] === " ") value = value.slice(1);
      if (field === "event") name = value;
      else if (field === "data") data.push(value);
      else if (field === "id") { if (!value.includes("\u0000")) lastID = value; }
      else if (field === "retry" && /^\d+$/.test(value)) out.push({type: "retry", ms: Number(value)});
    }
    return {
      push(text) {
        const out = [];
        buffer += text;
        for (;;) {
          const end = buffer.search(/[\r\n]/);
          if (end < 0) break;
          // A carriage return at the end of a chunk may be half of a CRLF.
          if (buffer[end] === "\r" && end === buffer.length - 1) break;
          line(buffer.slice(0, end), out);
          buffer = buffer.slice(buffer[end] === "\r" && buffer[end + 1] === "\n" ? end + 2 : end + 1);
        }
        return out;
      }
    };
  }

  function stateKey(entry) {
    return entry.projection + "/" + entry.repo + "/" + (entry.worktree || "");
  }

  function validState(entry) {
    return entry !== null && typeof entry === "object" && typeof entry.projection === "string" && entry.projection !== "" && typeof entry.repo === "string";
  }

  // The snapshot store: the latest state of every projection key the stream
  // selects. apply takes one parsed frame and reports what it changed.
  function createStore() {
    const states = new Map();
    let cursor = "", ready = false;
    function put(entry, changed) {
      const key = stateKey(entry);
      const next = {key, projection: entry.projection, repo: entry.repo, worktree: entry.worktree || "", state: entry.state ?? null};
      if (next.state === null) {
        if (states.delete(key)) changed.push(next);
        return;
      }
      const previous = states.get(key);
      if (previous && JSON.stringify(previous.state) === JSON.stringify(next.state)) return;
      states.set(key, next);
      changed.push(next);
    }
    return {
      get cursor() { return cursor; },
      get ready() { return ready; },
      list() { return [...states.values()]; },
      get(key) { return states.get(key) || null; },
      forget() { cursor = ""; },
      apply(frame) {
        if (frame.event !== "snapshot" && frame.event !== "delta" && frame.event !== "resync") return null;
        let body;
        try { body = JSON.parse(frame.data); } catch { return {kind: "invalid"}; }
        if (body === null || typeof body !== "object") return {kind: "invalid"};
        if (frame.event === "resync") {
          states.clear();
          cursor = "";
          ready = false;
          return {kind: "resync", reason: typeof body.reason === "string" ? body.reason : ""};
        }
        const entries = body.states ?? [];
        if (!Array.isArray(entries) || !entries.every(validState)) return {kind: "invalid"};
        const changed = [];
        if (frame.event === "snapshot") {
          const kept = new Set(entries.filter(entry => (entry.state ?? null) !== null).map(stateKey));
          for (const [key, old] of states) if (!kept.has(key)) put({...old, state: null}, changed);
          for (const entry of entries) put(entry, changed);
          cursor = typeof body.cursor === "string" ? body.cursor : frame.id;
          ready = true;
          return {kind: "snapshot", changed};
        }
        for (const entry of entries) put(entry, changed);
        // A transient event repeats the previous cursor, so this never moves back.
        if (frame.id) cursor = frame.id;
        return {kind: "delta", changed, event: body.event ?? null};
      }
    };
  }

  // The hub owns the stream and fans it out to ports. A port is a tab: it
  // posts hello {endpoint, credential, topics}, credential, resync and bye,
  // and receives status, snapshot, delta and unauthorized.
  function createHub() {
    const ports = new Map();
    const store = createStore();
    let status = {state: "idle", reason: ""};
    let controller = null, running = "", failures = 0, denied = 0, delay = firstDelay, retry = firstDelay, snapshotTimer = 0;

    function send(port, message) {
      try { port.postMessage(message); } catch { /* The tab is gone. */ }
    }
    function broadcast(message) {
      for (const port of ports.keys()) send(port, message);
    }
    function setStatus(state, reason = "") {
      if (status.state === state && status.reason === reason) return;
      status = {state, reason};
      broadcast({type: "status", status});
    }
    function leader() {
      for (const [port, tab] of ports) if (tab.credential && tab.endpoint) return {port, tab};
      return null;
    }
    function topics() {
      const all = new Set();
      for (const tab of ports.values()) for (const topic of tab.topics) all.add(topic);
      return [...all].sort().slice(0, maxTopics);
    }
    // What the running stream was opened with. A change needs a reconnect.
    function plan() {
      const lead = leader();
      return lead ? lead.tab.endpoint + "?" + topics().map(topic => "topic=" + encodeURIComponent(topic)).join("&") : "";
    }
    function stop() {
      clearTimeout(snapshotTimer);
      if (controller) controller.abort();
      controller = null;
      running = "";
    }
    function restart() {
      stop();
      if (!plan()) { setStatus("idle"); return; }
      controller = new AbortController();
      if (status.state !== "resyncing") setStatus(store.ready ? "reconnecting" : "connecting");
      run(controller.signal);
    }
    function wait(ms, signal) {
      return new Promise(resolve => {
        const timer = setTimeout(resolve, ms);
        signal.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, {once: true});
      });
    }

    function handle(frame, signal) {
      const result = store.apply(frame);
      if (!result) return;
      if (result.kind === "invalid") {
        store.forget();
        throw new Error("invalid frame");
      }
      if (result.kind !== "delta") clearTimeout(snapshotTimer);
      if (result.kind === "resync") {
        setStatus("resyncing", result.reason);
        // Without the promised snapshot, ask for one on a new connection.
        snapshotTimer = setTimeout(() => { if (!signal.aborted) restart(); }, snapshotWait);
      } else if (result.kind === "snapshot") {
        failures = 0;
        denied = 0;
        delay = retry;
        broadcast({type: "snapshot", cursor: store.cursor, states: store.list()});
        setStatus("live");
      } else {
        broadcast({type: "delta", states: result.changed, event: result.event});
      }
    }

    async function receive(response, signal, connection) {
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      const parser = createParser();
      let watchdog = 0;
      const alive = () => {
        clearTimeout(watchdog);
        watchdog = setTimeout(() => connection.abort(), silence);
      };
      try {
        alive();
        for (;;) {
          const next = await reader.read();
          if (next.done || signal.aborted) return;
          alive();
          for (const item of parser.push(decoder.decode(next.value, {stream: true}))) {
            if (item.type === "retry") retry = Math.min(Math.max(item.ms, firstDelay), lastDelay);
            else handle(item, signal);
          }
        }
      } finally {
        clearTimeout(watchdog);
        try { reader.releaseLock(); } catch { /* The read was aborted. */ }
      }
    }

    async function run(signal) {
      while (!signal.aborted) {
        const lead = leader();
        if (!lead) { setStatus("idle"); return; }
        running = plan();
        const key = lead.tab.credential;
        const connection = new AbortController();
        signal.addEventListener("abort", () => connection.abort(), {once: true});
        let reason = "";
        try {
          const headers = {Authorization: "Bearer " + key, Accept: "text/event-stream"};
          if (store.cursor) headers["Last-Event-ID"] = store.cursor;
          const response = await fetch(running, {signal: connection.signal, cache: "no-store", redirect: "error", credentials: "omit", headers});
          if (signal.aborted) return;
          if (response.status === 401) {
            // Ask the owning tab for a fresh session; another tab may lead meanwhile.
            if (lead.tab.credential === key) lead.tab.credential = "";
            send(lead.port, {type: "unauthorized", credential: key});
            // A renewed session that is refused again must not spin.
            if (++denied > 1) { await wait(delay, signal); delay = Math.min(delay * 2, lastDelay); }
            continue;
          }
          if (response.status === 404 || response.status === 403) {
            setStatus("offline", response.status === 404 ? "unsupported" : "forbidden");
            return;
          }
          if (response.status === 429) reason = "busy";
          if (response.status === 503) reason = "unavailable"; // no event store yet: retry
          if (!response.ok || !response.body) throw new Error("stream unavailable");
          await receive(response, signal, connection);
        } catch {
          if (signal.aborted) return;
        }
        if (signal.aborted) return;
        clearTimeout(snapshotTimer);
        failures++;
        const offline = failures >= offlineAfter || reason !== "" || scope.navigator?.onLine === false;
        setStatus(offline ? "offline" : "reconnecting", offline ? reason || "network" : "");
        await wait(delay, signal);
        delay = Math.min(delay * 2, lastDelay);
      }
    }

    function hello(port, message) {
      let endpoint = "";
      try {
        const url = new URL(String(message.endpoint), scope.location.href);
        if (url.origin === scope.location.origin && endpointPath.test(url.pathname) && !url.search && !url.hash && !url.username && !url.password) endpoint = url.href;
      } catch { /* Not a console address. */ }
      const wanted = Array.isArray(message.topics) ? message.topics.filter(topic => typeof topic === "string" && topicForm.test(topic)) : [];
      ports.set(port, {endpoint, credential: typeof message.credential === "string" ? message.credential : "", topics: wanted.slice(0, maxTopics)});
      send(port, {type: "status", status});
      if (store.ready) send(port, {type: "snapshot", cursor: store.cursor, states: store.list()});
      if (!controller || plan() !== running) restart();
    }

    function receiveMessage(port, message) {
      if (message === null || typeof message !== "object") return;
      if (message.type === "hello") { hello(port, message); return; }
      const tab = ports.get(port);
      if (!tab) return;
      if (message.type === "credential") {
        tab.credential = typeof message.credential === "string" ? message.credential : "";
        if (!controller || plan() !== running || status.state === "idle") restart();
      } else if (message.type === "resync") {
        store.forget();
        failures = 0;
        delay = firstDelay;
        setStatus("resyncing", "requested");
        restart();
      } else if (message.type === "bye") {
        ports.delete(port);
        if (!ports.size) { stop(); status = {state: "idle", reason: ""}; }
        else if (plan() !== running) restart();
      }
    }

    scope.addEventListener?.("online", () => {
      if (ports.size && status.state !== "live") { failures = 0; delay = firstDelay; restart(); }
    });
    scope.addEventListener?.("offline", () => {
      if (ports.size) setStatus("offline", "network");
    });

    return {
      attach(port) {
        port.onmessage = event => receiveMessage(port, event.data);
        port.start?.();
      }
    };
  }

  // The page's side. start and stop follow the console session; every
  // callback is optional. options.shared === false forces a stream for this
  // tab alone, which is also the fallback when SharedWorker is unavailable.
  function connect(options) {
    let port = null, mode = "", started = false, delivered = false;
    // Read the stream in this tab: SharedWorker is missing, failed to start,
    // or cannot reach the console (some embedded browsers).
    function alone() {
      if (mode === "shared" && port) port.postMessage({type: "bye"});
      port = local();
      listen(port);
      if (started) greet();
    }
    function local() {
      const channel = new MessageChannel();
      createHub().attach(channel.port1);
      mode = "tab";
      return channel.port2;
    }
    function listen(target) {
      target.onmessage = async event => {
        const message = event.data;
        if (!started || target !== port || message === null || typeof message !== "object") return;
        if (message.type === "status") {
          if (mode === "shared" && !delivered && message.status.state === "offline" && message.status.reason === "network") { alone(); return; }
          options.onStatus?.(message.status);
        } else if (message.type === "snapshot") { delivered = true; options.onSnapshot?.(message); }
        else if (message.type === "delta") options.onDelta?.(message);
        else if (message.type === "unauthorized") {
          const renewed = await options.unauthorized?.(message.credential);
          if (started && target === port) target.postMessage({type: "credential", credential: renewed ? options.credential() : ""});
        }
      };
      target.start?.();
    }
    function greet() {
      port.postMessage({type: "hello", endpoint: options.endpoint, credential: options.credential(), topics: options.topics()});
    }
    function open() {
      if (options.shared !== false && typeof scope.SharedWorker === "function") {
        try {
          const worker = new scope.SharedWorker(options.worker || "event-worker.js", {name: "agentflow-events"});
          mode = "shared";
          worker.onerror = () => { if (mode === "shared") alone(); };
          return worker.port;
        } catch { /* Blocked by the browser: fall through. */ }
      }
      return local();
    }
    return {
      get mode() { return mode; },
      start() {
        if (!port) { port = open(); listen(port); }
        started = true;
        greet();
      },
      stop() {
        if (!started) return;
        started = false;
        port.postMessage({type: "bye"});
      },
      resync() {
        if (started) port.postMessage({type: "resync"});
      }
    };
  }

  scope.AgentflowEvents = {createParser, createStore, createHub, connect, stateKey};

  if (typeof scope.SharedWorkerGlobalScope === "function" && scope instanceof scope.SharedWorkerGlobalScope) {
    const hub = createHub();
    scope.onconnect = event => { for (const port of event.ports) hub.attach(port); };
  }
})(globalThis);
