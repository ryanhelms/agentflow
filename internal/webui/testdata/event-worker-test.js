"use strict";

(async () => {
  const {createParser, createStore, connect} = AgentflowEvents;
  function check(test, message) { if (!test) throw new Error(message); }
  async function until(test, message) {
    const end = Date.now() + 6000;
    while (!(await test())) {
      check(Date.now() < end,message);
      await new Promise(resolve => setTimeout(resolve,20));
    }
  }
  const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  const post = (path, body) => fetch("/fixture/stream/" + path,{method:"POST",body});
  const requests = async () => (await fetch("/fixture/stream/requests")).json();
  const overview = (branch, worktree = "first") => ({projection:"overview",repo:"3f2a9c1b7d4e5f60",worktree,state:{branch}});
  const frame = (event, id, data) => "event: " + event + "\n" + (id ? "id: " + id + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const snapshot = (seq, states) => frame("snapshot","v1:3f2a9c1b7d4e5f60=" + seq,{cursor:"v1:3f2a9c1b7d4e5f60=" + seq,states});
  const delta = (seq, states) => frame("delta","v1:3f2a9c1b7d4e5f60=" + seq,{event:{id:"e" + seq,type:"git.head.changed",repo:"3f2a9c1b7d4e5f60",worktree:"first",seq},states});

  let result = "passed";
  try {
    // Frame parsing.
    let parser = createParser();
    check(same(parser.push("retry: 1000\n\n: ping\n\nevent: snap"),[{type:"retry",ms:1000}]),"retry or comment parsed incorrectly");
    check(same(parser.push("shot\nid: v1:a=1\ndata: {\"a\""),[]),"an unfinished frame was delivered");
    check(same(parser.push(":1}\n\n"),[{type:"event",event:"snapshot",data:"{\"a\":1}",id:"v1:a=1"}]),"a frame split across chunks was lost");
    check(same(parser.push("data: one\ndata: two\n\n"),[{type:"event",event:"message",data:"one\ntwo",id:"v1:a=1"}]),"multi-line data or the remembered id is wrong");
    parser = createParser();
    check(same(parser.push("event: delta\r\ndata:x\r"),[]),"a split CRLF ended the line early");
    check(same(parser.push("\n\r\nevent: resync\rdata: {}\r\r"),[{type:"event",event:"delta",data:"x",id:""}]),"CRLF line endings parsed incorrectly");
    check(same(parser.push("event: a\n\nid: 7\nretry: soon\nunknown: 1\ndata\n\n"),[{type:"event",event:"resync",data:"{}",id:""},{type:"event",event:"message",data:"",id:"7"}]),"CR endings, empty frames or unknown fields parsed incorrectly");

    // Snapshot store.
    const store = createStore();
    check(!store.ready && store.cursor === "","a new store is not empty");
    let applied = store.apply({event:"snapshot",id:"v1:a=1",data:JSON.stringify({cursor:"v1:a=1",states:[overview("main"),overview("side","second")]})});
    check(applied.kind === "snapshot" && applied.changed.length === 2 && store.ready && store.cursor === "v1:a=1","snapshot not stored");
    check(store.get("overview/3f2a9c1b7d4e5f60/first").state.branch === "main","state is not keyed by projection, repository and worktree");
    applied = store.apply({event:"delta",id:"v1:a=2",data:JSON.stringify({event:{type:"git.head.changed"},states:[overview("next"),overview("side","second")]})});
    check(applied.kind === "delta" && applied.changed.length === 1 && applied.changed[0].state.branch === "next" && applied.event.type === "git.head.changed","delta did not report exactly the changed state");
    check(store.cursor === "v1:a=2","delta did not advance the cursor");
    applied = store.apply({event:"delta",id:"v1:a=3",data:JSON.stringify({event:{type:"git.worktrees.changed"},states:[{...overview("x","second"),state:null}]})});
    check(applied.changed[0].state === null && store.list().length === 1,"a null state did not remove its key");
    applied = store.apply({event:"delta",id:"v1:a=4",data:JSON.stringify({event:{type:"git.index.changed"},states:null})});
    check(applied.kind === "delta" && !applied.changed.length && store.cursor === "v1:a=4","a replayed event without states was not accepted");
    applied = store.apply({event:"snapshot",id:"v1:a=9",data:JSON.stringify({cursor:"v1:a=9",states:[overview("side","second")]})});
    check(store.list().length === 1 && store.list()[0].worktree === "second" && applied.changed.some(entry => entry.worktree === "first" && entry.state === null),"snapshot did not replace the whole store");
    check(store.apply({event:"future",id:"",data:"{}"}) === null,"an unknown frame was not ignored");
    check(store.apply({event:"delta",id:"",data:"{"}).kind === "invalid" && store.apply({event:"snapshot",id:"",data:"{\"states\":[{}]}"}).kind === "invalid","a malformed frame was accepted");
    applied = store.apply({event:"resync",id:"",data:"{\"reason\":\"overflow\"}"});
    check(applied.reason === "overflow" && !store.ready && !store.list().length && store.cursor === "","resync kept old state");

    // One shared stream for every tab of the origin.
    check(typeof SharedWorker === "function","this browser has no SharedWorker");
    let credential = "good";
    const seen = {status:[],snapshots:[],deltas:[],renewed:[]};
    const options = (log, topics) => ({
      endpoint: new URL("api/events",location.href).href, credential: () => credential, topics: () => topics,
      async unauthorized(previous) { log.renewed.push(previous); credential = "fresh"; return true; },
      onStatus(status) { log.status.push(status.state); }, onSnapshot(message) { log.snapshots.push(message); }, onDelta(message) { log.deltas.push(message); }
    });
    const first = connect(options(seen,["*/first/git.**","*/first/config.**"]));
    first.start();
    check(first.mode === "shared","the stream did not start in a SharedWorker");
    await post("emit",snapshot(1,[overview("main")]));
    await until(() => seen.status.at(-1) === "live" && seen.snapshots.length === 1,"the first snapshot did not make the stream live");
    check(seen.status[0] !== "live" && seen.snapshots[0].states[0].state.branch === "main","the snapshot did not reach the tab");
    let calls = await requests();
    check(calls.length === 1 && calls[0].authorization === "Bearer good" && calls[0].accept === "text/event-stream" && !calls[0].last_event_id,"the stream request is not a Bearer fetch");
    check(!calls[0].query.includes("good") && same(new URLSearchParams(calls[0].query).getAll("topic"),["*/first/config.**","*/first/git.**"]),"topics or the credential are wrong in the address");

    await post("emit",": ping\n\n" + delta(2,[overview("feature")]));
    await until(() => seen.deltas.length === 1,"a delta did not reach the tab");
    check(seen.deltas[0].states[0].state.branch === "feature" && seen.deltas[0].event.type === "git.head.changed","the delta lost its state or event");

    await post("emit",frame("resync","",{reason:"overflow"}));
    await until(() => seen.status.at(-1) === "resyncing","resync was not reported");
    await post("emit",snapshot(5,[overview("resynced")]));
    await until(() => seen.status.at(-1) === "live" && seen.snapshots.at(-1).states[0].state.branch === "resynced","the snapshot after resync was not applied");

    await post("drop");
    await until(() => seen.status.at(-1) === "reconnecting","a dropped stream was not reported");
    await until(async () => (await requests()).length === 2,"the worker did not reconnect");
    calls = await requests();
    check(calls[1].last_event_id === "v1:3f2a9c1b7d4e5f60=5","the reconnect did not resume from the last cursor");
    await post("emit",snapshot(5,[overview("resynced")]));
    await until(() => seen.status.at(-1) === "live","the stream did not return to live");

    // A second tab joins the same stream; new topics reconnect with the union.
    const other = {status:[],snapshots:[],deltas:[],renewed:[]};
    const second = connect(options(other,["*/first/git.**","*/first/config.**"]));
    second.start();
    await until(() => other.snapshots.length === 1 && other.status.at(-1) === "live","a joining tab did not get the current snapshot");
    check((await requests()).length === 2,"a second tab opened a second stream");
    second.stop();
    const third = connect(options(other,["*/second/git.**"]));
    third.start();
    await until(async () => (await requests()).length === 3,"new topics did not reconnect the stream");
    calls = await requests();
    check(same(new URLSearchParams(calls[2].query).getAll("topic"),["*/first/config.**","*/first/git.**","*/second/git.**"]),"the stream does not carry the union of its tabs' topics");
    await post("emit",snapshot(6,[overview("resynced"),overview("side","second")]));
    await until(() => seen.status.at(-1) === "live" && other.snapshots.at(-1).states.length === 2,"tabs did not share the multiplexed snapshot");
    third.stop();
    await until(async () => (await requests()).length === 4,"leaving did not narrow the topics");
    await post("emit",snapshot(6,[overview("resynced")]));
    await until(() => seen.status.at(-1) === "live","the stream did not settle after a tab left");

    // An ended session is renewed through the tab that owns it.
    await fetch("/fixture/stream/token?value=fresh",{method:"POST"});
    await post("drop");
    await until(() => seen.renewed.length === 1,"the worker did not ask the tab for a fresh session");
    check(seen.renewed[0] === "good","the tab was not told which session ended");
    await post("emit",snapshot(7,[overview("renewed")]));
    await until(() => seen.status.at(-1) === "live" && seen.snapshots.at(-1).states[0].state.branch === "renewed","the renewed session did not resume the stream");
    check((await requests()).at(-1).authorization === "Bearer fresh","the renewed credential was not used");

    // Resync asks for a snapshot without a cursor.
    first.resync();
    await until(() => seen.status.at(-1) === "resyncing","a requested resync was not reported");
    await until(async () => (await requests()).at(-1).last_event_id === "" && (await requests()).length > 5,"a requested resync resumed from the old cursor");
    await post("emit",snapshot(8,[overview("renewed")]));
    await until(() => seen.status.at(-1) === "live","the requested resync did not finish");

    // A refused or failing endpoint goes offline; 404 stops retrying.
    await post("status?code=503");
    await post("drop");
    await until(() => seen.status.at(-1) === "offline","repeated failures did not turn offline");
    await post("status?code=0");
    await post("emit",snapshot(9,[overview("back")]));
    await until(() => seen.status.at(-1) === "live","the stream did not recover from offline");
    first.stop();

    // Without SharedWorker each tab reads its own stream.
    const alone = {status:[],snapshots:[],deltas:[],renewed:[]};
    const tab = connect({...options(alone,["*/first/git.**"]),shared:false});
    tab.start();
    check(tab.mode === "tab","the fallback did not run in the tab");
    await post("emit",snapshot(10,[overview("tab")]));
    await until(() => alone.status.at(-1) === "live" && alone.snapshots.at(-1).states[0].state.branch === "tab","the per-tab stream did not deliver a snapshot");
    await post("status?code=404");
    await post("drop");
    await until(() => alone.status.at(-1) === "offline","a missing endpoint was not reported offline");
    const settled = (await requests()).length;
    await new Promise(resolve => setTimeout(resolve,700));
    check((await requests()).length === settled,"a missing endpoint was retried");
    tab.stop();
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
