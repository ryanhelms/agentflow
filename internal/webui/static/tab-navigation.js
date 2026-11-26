"use strict";

// A console tab on the event stream (docs/events.md, "Tabs on the stream").
// The tab attaches with its ID, the shared stream lists that ID in tab= so
// the console keeps it attached, and a navigation for this tab arrives in
// the worktree's "tabs" state. The Agent Commander capability travels only
// in the attach request's header: the stream and its worker never see it.
function consoleTabs({credential, commander, unauthorized, navigate, request, send = (path, options) => fetch(path, options)}) {
  const firstDelay = 250, lastDelay = 10000;
  const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, "0")).join("");
  const acted = []; // navigation IDs this tab has answered; a snapshot may repeat one
  let active = false, epoch = 0, pending = null;

  function post(path, key, extra = {}) {
    return send(path, {method: "POST", cache: "no-store", credentials: "omit", redirect: "error", ...extra,
      headers: {"Content-Type": "application/json", "X-Agentflow-Request": "console", Authorization: "Bearer " + key, ...extra.headers},
      body: JSON.stringify({tab: id})});
  }

  // Attach until the console accepts the tab. A refused tab (400, 403, 404,
  // 503) stays detached; a busy console (429) or a network error is retried.
  async function attach(run) {
    let delay = firstDelay;
    while (active && run === epoch) {
      const key = credential();
      if (!key) return;
      const actor = commander();
      let status = 0;
      try { status = (await post("api/tabs/attach", key, {headers: actor ? {"X-Agentflow-Commander": actor} : {}})).status; }
      catch { /* The console is unreachable: retry. */ }
      if (!active || run !== epoch) return;
      if (status === 204 || status === 200) return;
      if (status === 401) {
        if (!(await unauthorized(key))) return;
        continue;
      }
      // 503: the console has no event stream for this worktree, so it cannot
      // navigate this tab. receive() attaches again once a snapshot arrives.
      if (status === 400 || status === 403 || status === 404 || status === 503) return;
      await new Promise(resolve => setTimeout(resolve, delay));
      delay = Math.min(delay * 2, lastDelay);
    }
  }

  function ensure() {
    if (!active || pending) return;
    const mine = attach(epoch);
    pending = mine;
    mine.finally(() => { if (pending === mine) pending = null; });
  }

  async function act(event) {
    const result = navigate(event);
    // Keep foreground inputs stable between saving a cross-worktree
    // snapshot and committing the acknowledged navigation.
    const switching = Boolean(event.target && result?.perform);
    if (switching) document.body.inert = true;
    try {
      await request("api/tabs/ack", {tab: id, id: event.id, handled: result?.handled === true, blocked: result?.blocked});
      result?.perform?.();
    } catch { /* The request expired: the caller opens the destination itself. */ }
    finally { if (switching) document.body.inert = false; }
  }

  return {
    // The ID to list in the stream's tab= parameter.
    id,
    get active() { return active; },
    start() {
      if (active) return;
      active = true;
      ensure();
    },
    // Attach again with the current Commander capability.
    refresh() {
      if (!active) return;
      epoch++;
      pending = null;
      ensure();
    },
    // The "tabs" state of this tab's worktree, from a snapshot or a delta.
    receive(state) {
      if (!active || state === null || typeof state !== "object") return;
      const attached = Array.isArray(state.tabs) && state.tabs.some(entry => entry?.tab === id);
      // The console dropped this tab (its stream was away past the grace period).
      if (!attached) ensure();
      const event = state.navigation;
      if (event === null || typeof event !== "object" || event.tab !== id || typeof event.id !== "string" || acted.includes(event.id)) return;
      acted.push(event.id);
      if (acted.length > 32) acted.shift();
      act(event);
    },
    stop() {
      if (!active) return;
      active = false;
      epoch++;
      pending = null;
      const key = credential();
      if (key) post("api/tabs/leave", key, {keepalive: true}).catch(() => {});
    }
  };
}
