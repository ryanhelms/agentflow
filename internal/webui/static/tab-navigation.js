"use strict";

// Browsers keep a live, authenticated channel only while this checkout is
// open. Using fetch keeps the session credential out of URLs and logs.
function consoleTabs({credential, commander, unauthorized, navigate, request}) {
  let controller, tab;

  async function receive(response, signal) {
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
      while (!signal.aborted) {
        const next = await reader.read();
        if (next.done) return;
        buffer += decoder.decode(next.value,{stream:true});
        let end;
        while ((end = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0,end);
          buffer = buffer.slice(end+2);
          if (!frame.startsWith("event: navigate\ndata: ")) continue;
          const event = JSON.parse(frame.slice("event: navigate\ndata: ".length));
          const result = navigate(event);
          // Keep foreground inputs stable between saving a cross-worktree
          // snapshot and committing the acknowledged navigation.
          const switching = Boolean(event.target && result?.perform);
          if (switching) document.body.inert = true;
          try {
            await request("api/tabs/ack",{tab,id:event.id,handled:result?.handled===true,blocked:result?.blocked});
            result?.perform?.();
          } finally {
            if (switching) document.body.inert = false;
          }
        }
      }
    } finally { reader.releaseLock(); }
  }

  async function listen(signal) {
    let delay = 250;
    while (!signal.aborted) {
      const key = credential();
      if (!key) return;
      try {
        const headers = {Authorization:"Bearer " + key,Accept:"text/event-stream"};
        const actor = commander();
        if (actor) headers["X-Agentflow-Commander"] = actor;
        const response = await fetch("api/tabs/events?tab=" + tab,{signal,cache:"no-store",redirect:"error",credentials:"omit",headers});
        if (signal.aborted) return;
        if (response.status === 401) { if (await unauthorized(key)) continue; return; }
        if (response.status === 404) return;
        if (!response.ok) throw new Error("tab unavailable");
        delay = 250;
        await receive(response,signal);
      } catch {
        if (signal.aborted) return;
        await new Promise(resolve => setTimeout(resolve,delay));
        delay = Math.min(delay*2,10000);
      }
    }
  }

  return {
    start() {
      if (controller) return;
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      tab = Array.from(bytes,n => n.toString(16).padStart(2,"0")).join("");
      controller = new AbortController();
      listen(controller.signal);
    },
    stop() {
      if (!controller) return;
      controller.abort();
      controller = null;
      if (credential()) fetch("api/tabs/leave",{method:"POST",keepalive:true,credentials:"omit",redirect:"error",
        headers:{"Content-Type":"application/json","X-Agentflow-Request":"console",Authorization:"Bearer " + credential()},
        body:JSON.stringify({tab})}).catch(() => {});
    }
  };
}
