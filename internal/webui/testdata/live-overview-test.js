"use strict";

(async () => {
  const byID = id => document.getElementById(id);
  function check(test, message) { if (!test) throw new Error(message); }
  async function until(test, message) {
    const end = Date.now() + 8000;
    while (!(await test())) {
      check(Date.now() < end,message);
      await new Promise(resolve => setTimeout(resolve,20));
    }
  }
  const post = (path, body) => fetch("/fixture/stream/" + path,{method:"POST",body});
  const requests = async () => (await fetch("/fixture/stream/requests")).json();
  const repo = "3f2a9c1b7d4e5f60";
  const overview = state => ({projection:"overview",repo,worktree:"first",state});
  // OverviewState from projection_overview.go; a null upstream is dropped as the server omits it.
  const facts = (extra = {}) => {
    const state = {branch:"main",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,upstream:{name:"origin/main",ahead:0,behind:0},changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]},...extra};
    if (state.upstream === null) delete state.upstream;
    return state;
  };
  const frame = (event, seq, data) => "event: " + event + "\n" + (seq ? "id: v1:" + repo + "=" + seq + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const snapshot = (seq, state) => frame("snapshot",seq,{cursor:"v1:" + repo + "=" + seq,states:state === undefined ? [] : [overview(state)]});
  const delta = (seq, type, state) => frame("delta",seq,{event:{id:"e" + seq,type,repo,worktree:"first",seq},states:[overview(state)]});
  const field = key => byID("repo-metadata").querySelector('[data-field="' + key + '"]');
  const label = () => byID("stream-label").textContent;

  let result = "passed";
  try {
    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");

    // The loaded checkout is shown before the stream is live.
    check(byID("refresh").textContent === "Resync" && byID("refresh").getAttribute("aria-describedby") === "stream-state","the Resync control is missing");
    check(field("branch").textContent === "main" && byID("working-state").textContent === "Uncommitted changes","the loaded checkout is not shown");
    check(byID("stream-state").dataset.state !== "live" && byID("live-note").hidden,"the stream claimed to be live before a snapshot");
    const loadedHead = field("revision").textContent;

    await post("emit",snapshot(1,facts({changed:1,untracked:1})));
    await until(() => label() === "Live","the snapshot did not make the overview live");
    check(byID("stream-state").dataset.state === "live" && byID("live-note").hidden,"live state is not shown");
    check(field("changes").textContent === "1 untracked","the change counts are not shown");
    check(!document.querySelector(".live-changed") && !byID("live-announcer").textContent,"the first live values were announced as changes");
    let calls = await requests();
    check(calls.length === 1 && calls[0].path === "/api/events" && !calls[0].query.includes("Bearer") && calls[0].authorization.startsWith("Bearer "),"the stream request is wrong");
    check(new URLSearchParams(calls[0].query).getAll("topic").join() === "*/first/config.**,*/first/git.**","the overview topics are wrong");
    check(!calls[0].query.includes(sessionStorage.getItem("agentflow.session:/w/first/")),"the session reached the address");

    // A branch switch arrives live, is highlighted and announced.
    await post("emit",delta(2,"git.head.changed",facts({branch:"feature/live",head:"2222222",oid:"2222222bbbbbbb2222222bbbbbbb2222222bbbb",upstream:{name:"origin/feature/live",ahead:2,behind:1},changed:3,staged:1,unstaged:2,task:"ryanhelms/agentflow#29"})));
    await until(() => byID("context-branch").textContent === "feature/live","the branch did not change live");
    check(field("branch").textContent === "feature/live · 2 ahead, 1 behind origin/feature/live" && field("revision").textContent !== loadedHead,"branch or revision did not follow the stream");
    check(field("changes").textContent === "1 staged · 2 unstaged","the change counts did not follow the stream");
    check(field("branch").classList.contains("live-changed") && byID("context-branch").classList.contains("live-changed") && !field("cli").classList.contains("live-changed"),"the changed values are not highlighted");
    await until(() => byID("live-announcer").textContent.includes("Branch changed to feature/live"),"the change was not announced");
    check(byID("live-announcer").getAttribute("aria-live") === "polite","changes are not announced politely");
    check(byID("attention-list").textContent.includes("1 staged, 2 unstaged in this worktree"),"uncommitted work did not follow the stream");

    // Configuration problems, a git operation and a clean worktree.
    await post("emit",delta(3,"git.operation.changed",facts({branch:"feature/live",head:"2222222",upstream:null,operation:"rebase",config:{valid:false,problems:["commit.types must be a list"]}})));
    await until(() => !byID("git-operation").hidden && byID("git-operation").textContent === "Rebase in progress","the git operation is not shown");
    check(byID("working-state").textContent === "Clean worktree" && field("changes").textContent === "None","a clean worktree is not shown");
    check(field("branch").textContent === "feature/live","a branch without an upstream still shows ahead or behind");
    check(byID("attention-list").textContent.includes("commit.types must be a list") && !byID("attention-list").textContent.includes("Uncommitted work"),"configuration problems did not follow the stream");
    await until(() => byID("live-announcer").textContent.includes("The configuration has 1 problem"),"the configuration problem was not announced");

    // Resync from the server keeps the values and shows the state.
    await post("emit",frame("resync",0,{reason:"overflow"}));
    await until(() => label() === "Resyncing","a resync is not shown");
    check(byID("context-branch").textContent === "feature/live","the overview was cleared while resyncing");
    await post("emit",snapshot(4,facts({branch:"feature/live",head:"2222222"})));
    await until(() => label() === "Live" && byID("git-operation").hidden,"the snapshot after a resync was not shown");

    // A dropped stream reconnects from its cursor; repeated failures go offline.
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Reconnecting" || label() === "Offline","a dropped stream is not shown");
    await until(() => label() === "Offline" && !byID("live-note").hidden,"the offline state is not shown");
    check(byID("live-note").textContent.includes("not available for this worktree yet") && byID("context-branch").textContent === "feature/live","a missing store is not explained, or the last values are missing");
    await until(() => byID("live-announcer").textContent.includes("offline"),"going offline was not announced");
    calls = await requests();
    check(calls.at(-1).last_event_id === "v1:" + repo + "=4","the reconnect did not resume from the last cursor");
    await post("status?code=0");
    await post("emit",snapshot(4,facts({branch:"feature/live",head:"2222222"})));
    await until(() => label() === "Live" && byID("live-note").hidden,"the overview did not recover from offline");
    await until(() => byID("live-announcer").textContent === "Live updates restored.","the recovery was not announced");

    // A worktree the stream no longer reports falls back to the loaded values.
    await post("emit",delta(5,"git.worktrees.changed",null));
    await until(() => !byID("live-note").hidden && byID("live-note").textContent.includes("no data for this worktree"),"the empty state is not shown");
    check(field("branch").textContent === "main","the loaded values did not return");

    // Resync reloads the checkout and asks for a snapshot without a cursor.
    await until(() => !document.querySelector(".live-changed"),"a highlight did not fade");
    const before = (await requests()).length;
    byID("refresh").focus();
    check(document.activeElement === byID("refresh"),"Resync is not reachable by keyboard");
    byID("refresh").click();
    await until(async () => (await requests()).length > before,"Resync did not restart the stream");
    check((await requests()).at(-1).last_event_id === "","Resync resumed from the old cursor");
    await post("emit",snapshot(6,facts()));
    await until(() => label() === "Live" && byID("live-note").hidden && !byID("refresh").disabled,"Resync did not return to live");
    check(!document.querySelector(".live-changed"),"a requested resync highlighted values");

    // A console without the endpoint stays usable.
    await post("status?code=404");
    await post("drop");
    await until(() => label() === "Offline" && byID("live-note").textContent.includes("does not serve live updates"),"a missing endpoint is not explained");
    check(!byID("console-shell").hidden && field("branch").textContent === "main","the console stopped working without live updates");
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
