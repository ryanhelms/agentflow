"use strict";

(async () => {
  function check(test, message) { if (!test) throw new Error(message); }
  async function until(test, message) {
    const end = Date.now() + 6000;
    while (!(await test())) {
      check(Date.now() < end,message);
      await new Promise(resolve => setTimeout(resolve,20));
    }
  }
  const pause = ms => new Promise(resolve => setTimeout(resolve,ms));
  const post = (path, body) => fetch("/fixture/stream/" + path,{method:"POST",body});
  const requests = async () => (await fetch("/fixture/stream/requests")).json();
  const repo = "3f2a9c1b7d4e5f60";
  const frame = (event, seq, data) => "event: " + event + "\nid: v1:" + repo + "=" + seq + "\ndata: " + JSON.stringify(data) + "\n\n";
  const tabsState = (state, worktree = "first") => ({projection:"tabs",repo,worktree,state});

  let result = "passed";
  try {
    // A scripted console: every tab request is recorded and answered from a queue.
    let credential = "good", actor = "launch-capability";
    const calls = [], answers = [], renewed = [], acks = [], navigations = [];
    let ackFails = false, outcome = () => ({handled:true});
    const send = async (path, options) => {
      calls.push({path,options,body:JSON.parse(options.body)});
      const status = answers.length ? answers.shift() : 204;
      if (status === 0) throw new TypeError("network");
      return {status};
    };
    const tabs = consoleTabs({
      credential: () => credential, commander: () => actor, send,
      async unauthorized(previous) { renewed.push(previous); credential = "fresh"; return true; },
      async request(path, body) { acks.push({path,body}); if (ackFails) throw new Error("expired"); return {}; },
      navigate(event) { navigations.push(event); return outcome(event); }
    });
    const attaches = () => calls.filter(call => call.path === "api/tabs/attach");
    check(/^[0-9a-f]{32}$/.test(tabs.id),"the tab ID is not 32 hex characters");

    // Nothing is sent before the console session starts the tab.
    tabs.receive({tabs:[],navigation:null});
    await pause(50);
    check(!calls.length && !tabs.active,"a stopped tab contacted the console");

    // Attach: the session and the Commander capability go in headers, never the address.
    tabs.start();
    await until(() => attaches().length === 1,"the tab did not attach");
    await pause(30);
    let call = attaches()[0];
    check(call.options.method === "POST" && call.body.tab === tabs.id && Object.keys(call.body).length === 1,"the attach body is wrong");
    check(call.options.headers.Authorization === "Bearer good" && call.options.headers["X-Agentflow-Commander"] === "launch-capability" && call.options.headers["X-Agentflow-Request"] === "console","the attach headers are wrong");
    check(call.options.credentials === "omit" && call.options.redirect === "error" && !call.path.includes(tabs.id) && !call.path.includes("launch-capability"),"the attach request leaks a credential or follows redirects");
    tabs.start();
    await pause(50);
    check(attaches().length === 1,"starting twice attached twice");

    // The tabs state: attached and quiet, then dropped by the console, then another tab's navigation.
    tabs.receive({tabs:[{tab:tabs.id,attached:"2026-11-26T10:00:00Z"}],navigation:null});
    tabs.receive(null);
    tabs.receive({tabs:[{tab:tabs.id}],navigation:{id:"n0",tab:"f".repeat(32),screen:"handoff"}});
    await pause(50);
    check(attaches().length === 1 && !navigations.length && !acks.length,"the tab reacted to a state that does not concern it");
    tabs.receive({tabs:[{tab:"f".repeat(32)}],navigation:null});
    await until(() => attaches().length === 2,"a tab the console dropped did not attach again");
    await pause(30);

    // A busy console (429) and a network error are retried; a renewed session is used.
    answers.push(429,0,401,204);
    tabs.receive({tabs:[],navigation:null});
    await until(() => attaches().length === 6,"attach was not retried after 429, a network error and 401");
    check(renewed.join() === "good" && attaches().at(-1).options.headers.Authorization === "Bearer fresh","the renewed session was not used to attach");
    await pause(400);
    check(attaches().length === 6,"attach continued after it succeeded");

    // Without a capability the header is absent; refresh attaches again with the new one.
    actor = "";
    tabs.refresh();
    await until(() => attaches().length === 7,"refresh did not attach again");
    check(!("X-Agentflow-Commander" in attaches().at(-1).options.headers),"an empty capability was sent");

    // A refused tab stays detached until the console reports it missing again.
    answers.push(404);
    tabs.refresh();
    await until(() => attaches().length === 8,"refresh did not reach the console");
    await pause(400);
    check(attaches().length === 8,"a refused attach was retried");

    // Navigation for this tab: act once, acknowledge, then perform.
    const order = [];
    outcome = () => ({handled:true,perform() { order.push("perform"); }});
    const nav = {id:"n1",tab:tabs.id,screen:"providers",role:"notifications"};
    tabs.receive({tabs:[{tab:tabs.id}],navigation:nav});
    tabs.receive({tabs:[{tab:tabs.id}],navigation:nav}); // a snapshot repeats the pending navigation
    await until(() => order.length === 1,"the navigation was not performed");
    check(navigations.length === 1 && navigations[0].screen === "providers" && navigations[0].role === "notifications","the navigation was not delivered exactly once");
    check(acks.length === 1 && acks[0].path === "api/tabs/ack" && acks[0].body.tab === tabs.id && acks[0].body.id === "n1" && acks[0].body.handled === true && acks[0].body.blocked === undefined,"the acknowledgement is wrong");

    // A blocked navigation reports its reason and performs nothing.
    outcome = () => ({blocked:"publishing"});
    tabs.receive({tabs:[{tab:tabs.id}],navigation:{id:"n2",tab:tabs.id,screen:"configuration"}});
    await until(() => acks.length === 2,"a blocked navigation was not acknowledged");
    check(acks[1].body.handled === false && acks[1].body.blocked === "publishing" && order.length === 1,"a blocked navigation was performed or misreported");

    // A destination the page rejects is acknowledged as not handled.
    outcome = () => null;
    tabs.receive({tabs:[{tab:tabs.id}],navigation:{id:"n3",tab:tabs.id,screen:"unknown"}});
    await until(() => acks.length === 3,"a rejected navigation was not acknowledged");
    check(acks[2].body.handled === false && acks[2].body.blocked === undefined,"a rejected navigation was reported as handled");

    // An expired acknowledgement must not move the page; a cross-worktree switch holds input meanwhile.
    ackFails = true;
    let inert = null;
    outcome = () => ({handled:true,perform() { order.push("late"); }});
    const acked = acks.length;
    tabs.receive({tabs:[{tab:tabs.id}],navigation:{id:"n4",tab:tabs.id,screen:"overview",target:location.origin + "/w/second/"}});
    inert = document.body.inert;
    await until(() => acks.length === acked + 1,"the switch was not acknowledged");
    await pause(50);
    check(inert === true && document.body.inert === false && !order.includes("late"),"an expired navigation moved the page, or input was not held during a switch");
    ackFails = false;

    // The shared stream lists this tab; a second tab of the browser joins the same stream.
    const options = (tab, log) => ({
      endpoint: new URL("api/events",location.href).href, credential: () => "good", topics: () => ["*/*/ui.**"], tab: () => tab,
      unauthorized: async () => true, onSnapshot(message) { log.push(message); }
    });
    const first = [], second = [];
    const one = AgentflowEvents.connect(options(tabs.id,first));
    one.start();
    await post("emit",frame("snapshot",1,{cursor:"v1:" + repo + "=1",states:[tabsState({tabs:[],navigation:null})]}));
    await until(() => first.length === 1,"the stream did not deliver the tabs state");
    let seen = await requests();
    check(new URLSearchParams(seen.at(-1).query).getAll("tab").join() === tabs.id && !seen.at(-1).query.includes("good"),"the stream does not list the attached tab");
    const other = "0123456789abcdef0123456789abcdef";
    const two = AgentflowEvents.connect(options(other,second));
    two.start();
    await until(async () => new URLSearchParams((await requests()).at(-1).query).getAll("tab").length === 2,"a second tab did not join the stream's tab list");
    seen = await requests();
    check(new URLSearchParams(seen.at(-1).query).getAll("tab").join() === [tabs.id,other].sort().join(),"the tab list is not the sorted set of tabs");
    const before = seen.length;
    const three = AgentflowEvents.connect(options("not-a-tab",[]));
    three.start();
    await pause(300);
    seen = await requests();
    check(seen.length === before && !seen.at(-1).query.includes("not-a-tab"),"a malformed tab ID reached the address");
    two.stop();
    three.stop();
    await until(async () => new URLSearchParams((await requests()).at(-1).query).getAll("tab").join() === tabs.id,"a closed tab stayed in the stream's tab list");
    one.stop();

    // Leaving tells the console at once, survives the page closing, and ends the flow.
    const total = calls.length;
    tabs.stop();
    check(calls.length === total + 1 && calls.at(-1).path === "api/tabs/leave" && calls.at(-1).body.tab === tabs.id && calls.at(-1).options.keepalive === true && calls.at(-1).options.headers.Authorization === "Bearer fresh","leaving did not notify the console");
    tabs.receive({tabs:[],navigation:{id:"n5",tab:tabs.id,screen:"overview"}});
    tabs.stop();
    await pause(100);
    check(calls.length === total + 1 && navigations.length === 4,"a stopped tab kept reacting");
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
