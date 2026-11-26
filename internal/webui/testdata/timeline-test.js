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
  const at = ms => new Date(Date.now() + ms).toISOString();
  const frame = (event, seq, data) => "event: " + event + "\n" + (seq ? "id: v1:" + repo + "=" + seq + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const entry = (projection, state) => ({projection,repo,state});
  const overview = {projection:"overview",repo,worktree:"first",state:{branch:"main",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]}}};
  const snapshot = (seq, states) => frame("snapshot",seq,{cursor:"v1:" + repo + "=" + seq,states:[overview,...states]});
  const delta = (seq, type, states) => frame("delta",seq,{event:{id:"e" + seq,type,repo,seq},states});

  // TimelineState from projection_timeline_state.go, as the fixture events
  // of lanes 3.1 to 3.4 will produce it.
  const step = (id, type, state, ms, code) => ({event_id:id,seq:1,type,time:at(ms),state,...(code ? {code} : {})});
  const op = (id, state, extra = {}) => ({id,operation:"commit",action:"commit",class:"local",client:"cli",worktree:"first",subject:"feat: " + id,state,started:at(-600000),updated:at(-599000),
    steps:[step(id + "-1","op.requested","requested",-600000),step(id + "-2","op." + state,state,-599000)],...extra});
  const pushID = "op_0123456789abcdef0123456789abcdef";
  const waiting = (extra = {}) => op(pushID,"awaiting_approval",{operation:"push",action:"push",class:"remote",client:"claude-code",subject:"Push issue-57",task:"ryanhelms/agentflow#57",branch:"issue-57",
    approval:{state:"pending",expires_at:at(290000)},started:at(-120000),updated:at(-118000),
    steps:[step("p1","op.requested","requested",-120000),step("p2","op.awaiting_approval","awaiting_approval",-119000),step("p3","approval.requested","awaiting_approval",-118000)],...extra});
  const applied = op("op-applied","applied",{links:[
    {kind:"commit",id:"9f8e7d6c5b4a39281706",url:"https://github.com/example/agentflow-console/commit/9f8e7d6c5b4a39281706"},
    {kind:"branch",id:"main"},{kind:"task",id:"example/agentflow-console#37",url:"https://github.com/example/agentflow-console/issues/37"},
    {kind:"pr",id:"41",url:"javascript:alert(1)"},{kind:"mystery",id:"x"}],task:"example/agentflow-console#37",branch:"main",started:at(-300000),updated:at(-299000)});
  const failed = op("op-failed","failed",{code:"operation_changed",client:"codex",operation:"pr.open",action:"pr_open_draft",class:"remote",subject:"Open the timeline PR",started:at(-400000),updated:at(-399000),
    steps:[step("f1","op.requested","requested",-400000),step("f2","op.failed","failed",-399000,"operation_changed")]});
  const elsewhere = op("op-elsewhere","suggested",{worktree:"second",code:"suggest_only",started:at(-500000),updated:at(-499000)});
  const wide = op("op-wide","cancelled",{worktree:"",operation:"push",action:"push",client:"claude-code",approval:{state:"cancelled"},started:at(-700000),updated:at(-640000),
    steps:[step("w1","approval.requested","awaiting_approval",-700000),step("w2","approval.resolved","cancelled",-640000)]});
  const timeline = (operations, extra = {}) => ({operations,...extra});
  const base = () => [waiting(),applied,failed,elsewhere,wide];
  const approval = (id, state, extra = {}) => ({operation_id:id,action:"push",class:"remote",client:"claude-code",policy_source:".agentflow/config.yml",issued_at:at(-118000),expires_at:at(290000),state,...extra});

  const rows = () => [...byID("timeline-list").children];
  const keys = () => rows().map(node => node.dataset.key).join();
  const row = key => rows().find(node => node.dataset.key === String(key));
  const fact = (key, name) => row(key)?.querySelector('[data-fact="' + name + '"]');
  const steps = key => [...row(key).querySelectorAll(".timeline-steps > li")].map(node => node.querySelector(".fact-text")?.textContent || node.textContent);
  const said = () => byID("live-announcer").textContent;
  const label = () => byID("stream-label").textContent;
  const choose = (id, value) => { byID(id).value = value; byID(id).dispatchEvent(new Event("change",{bubbles:true})); };
  const options = id => [...byID(id).options].map(option => option.value).join();

  let result = "passed";
  try {
    // The reader is defensive and the labels are fixed text.
    const T = AgentflowTimeline;
    check(T.timelineState(null) === null && T.timelineState("x") === null && T.timelineState({}).operations.length === 0,"a missing state is not read safely");
    const sparse = T.timelineState({operations:[{id:"a",state:"future",steps:[{event_id:"s",state:"odd"},{event_id:"s"},{}],links:[{kind:"pr",id:"1",url:"http://example.com"},{kind:"pr",id:"1"},null],approval:"x"},{id:"a"},{state:"applied"},7],truncated:"yes"});
    check(sparse.operations.length === 1 && sparse.operations[0].state === "unknown" && sparse.operations[0].steps.length === 1 && sparse.operations[0].steps[0].state === "unknown" && sparse.operations[0].links.length === 1 && sparse.operations[0].links[0].url === "" && sparse.operations[0].approval === null && sparse.truncated === false,"a damaged state is not read safely");
    check(T.origin("cli").kind === "cli" && T.origin("claude-code").kind === "agent" && T.origin("daemon").kind === "console" && T.origin("").kind === "unknown","origins are not told apart");
    check(T.codeText("failed","approval_expired").startsWith("The approval expired") && T.codeText("failed","new_code") === "The operation failed. Reason code: new_code." && T.codeText("failed","<b>x</b> y") === "The operation failed. Reason code: not readable." && T.codeText("applied","error") === "" && T.codeText("suggested","").includes("only suggests"),"failure codes are not readable text");
    check(T.stepLabel({type:"op.requested",state:"requested"},false) === "Requested again" && T.stepLabel({type:"approval.resolved",state:"consumed"}) === "Approval used" && T.stepLabel({type:"op.failed",state:"failed",code:"timeout"}) === "Failed: timed out","step labels are wrong");
    check(T.linkLabel({kind:"commit",id:"9f8e7d6c5b4a39281706"}) === "Commit 9f8e7d6" && T.linkLabel({kind:"pr",id:"41"}) === "Pull request #41" && T.linkLabel({kind:"release",id:"v1.2.0"}) === "Release v1.2.0","link labels are wrong");

    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");

    // Before the first snapshot the panel says it waits.
    check(!byID("timeline-wait").hidden && byID("timeline-body").hidden && byID("timeline-note").hidden,"the timeline does not show that it waits for the stream");

    // The first snapshot fills the panel without highlights or announcements.
    await post("emit",snapshot(1,[entry("timeline",timeline(base())),entry("approvals",{open:[approval(pushID,"pending")],recent:[]})]));
    await until(() => label() === "Live" && !byID("timeline-body").hidden,"the snapshot did not fill the timeline");
    check(byID("timeline-wait").hidden && byID("timeline-note").hidden && !document.querySelector("#timeline-panel .live-changed") && !said(),"the first timeline was highlighted, announced or marked stale");
    const topics = new URLSearchParams((await requests())[0].query).getAll("topic");
    check(topics.includes("*/*/op.**") && topics.includes("*/*/approval.**"),"the timeline topics are missing from the stream");

    // Newest first as the server orders them; this worktree and the whole repository by default.
    check(keys() === [pushID,"op-applied","op-failed","op-wide"].join(),"the default view is not this worktree's operations in the server's order: " + keys());
    check(byID("timeline-count").textContent === "5" && byID("timeline-summary").textContent === "4 operations · 5 operations in the repository." && byID("timeline-more").hidden && byID("timeline-reset").hidden && byID("timeline-truncated").hidden,"the counts are wrong: " + byID("timeline-summary").textContent);
    check(byID("timeline-worktree").value === "own" && options("timeline-worktree") === "own,all,wt:second" && options("timeline-origin") === "all,cli,agent,client:claude-code,client:codex" && byID("timeline-state").options.length === 7,"the filters do not offer what the state holds: " + options("timeline-origin"));

    // A waiting operation: what, who, where, steps with times, and the way to its approval.
    check(fact(pushID,"what").textContent === "Push a branch" && fact(pushID,"what").dataset.state === "awaiting_approval" && fact(pushID,"what").querySelector(".state-mark").getAttribute("aria-hidden") === "true","the operation is not named with a state mark");
    check(fact(pushID,"status").textContent === "Waiting for approval" && fact(pushID,"status").dataset.state === "warn" && fact(pushID,"subject").textContent === "Push issue-57","the state or subject is wrong");
    check(fact(pushID,"origin").textContent === "From claude-code (agent)" && fact(pushID,"worktree").textContent === "This worktree" && fact(pushID,"task").textContent === "Task ryanhelms/agentflow#57" && fact(pushID,"branch").textContent === "on issue-57" && fact(pushID,"class").textContent === "remote write","origin, worktree, task, branch or class is wrong");
    check(fact(pushID,"when").textContent.startsWith("Started ") && fact(pushID,"when").dateTime && fact(pushID,"reason").hidden,"the start time is missing, or a reason shows without a failure");
    check(steps(pushID).join("|") === "Requested|Waiting for approval|Approval requested","the steps are wrong: " + steps(pushID).join("|"));
    const stepTime = row(pushID).querySelector(".timeline-steps time");
    check(/\d{1,2}[:.]\d{2}[:.]\d{2}/.test(stepTime.textContent) && stepTime.dateTime && row(pushID).querySelector(".timeline-steps").getAttribute("aria-label") === "Steps of Push a branch","a step has no time to the second, or the list has no name");
    const part = row(pushID).querySelector("[data-part=approval]");
    check(!part.hidden && part.textContent.includes("A person has to approve it") && part.textContent.includes("expires at") && row("op-applied").querySelector("[data-part=approval]").hidden,"the approval hint is wrong");
    const jump = part.querySelector("button");
    check(jump.textContent === "Show in Approvals" && jump.getAttribute("aria-label") === "Show in Approvals: Push a branch","the approval button is not named");
    jump.click();
    const request = [...byID("approval-open").children].find(node => node.dataset.key === pushID);
    check(document.activeElement === byID("approvals-title") && request.classList.contains("live-changed") && !location.hash.includes("approval"),"the jump did not reach the open approval");
    await until(() => said().includes("Approvals. The request is highlighted."),"the jump was not announced");

    // An applied operation: links to what it touched, and its audit ID.
    check(fact("op-applied","what").textContent === "Create a local commit" && fact("op-applied","status").textContent === "Applied" && fact("op-applied","status").dataset.state === "good" && fact("op-applied","origin").textContent === "From the command line","the applied operation is wrong");
    const links = [...row("op-applied").querySelectorAll(".timeline-links > li")];
    check(links.map(node => node.textContent).join("|") === "Commit 9f8e7d6|Branch main|Task example/agentflow-console#37|Pull request #41","the links are wrong: " + links.map(node => node.textContent).join("|"));
    const commit = links[0].querySelector("a");
    check(commit.href.endsWith("/commit/9f8e7d6c5b4a39281706") && commit.target === "_blank" && commit.rel === "noopener noreferrer" && links[2].querySelector("a").href.endsWith("/issues/37"),"the commit or task link is wrong");
    check(!links[1].querySelector("a") && !links[3].querySelector("a") && !row("op-applied").innerHTML.includes("javascript:"),"a link without a safe address was made clickable");
    check(fact("op-applied","task").hidden && fact("op-applied","branch").hidden,"a linked task or branch is repeated as a fact");
    check(row("op-applied").querySelector(".timeline-audit code").textContent === "op-applied" && row(pushID).querySelector(".timeline-links").hidden,"the audit ID is missing, or an empty link list shows");

    // A failed operation says why in words; a code is never shown raw when it is known.
    check(fact("op-failed","what").textContent === "Open a draft pull request" && fact("op-failed","status").textContent === "Failed" && fact("op-failed","status").dataset.state === "bad" && fact("op-failed","what").dataset.state === "failed","the failed operation is wrong");
    check(fact("op-failed","reason").textContent.startsWith("The request changed after it was approved") && fact("op-failed","reason").dataset.tone === "bad" && steps("op-failed").join("|") === "Requested|Failed: request changed","the failure is not explained: " + fact("op-failed","reason").textContent);
    check(fact("op-wide","worktree").textContent === "Whole repository" && fact("op-wide","status").textContent === "Cancelled" && steps("op-wide").join("|") === "Approval requested|Cancelled","the cancelled approval-only operation is wrong");

    // The approved request applies live: the row stays, the changed values are highlighted, the result is announced.
    const kept = row(pushID), untouched = row("op-applied");
    kept.querySelector(".timeline-audit button").focus();
    const done = waiting({state:"applied",approval:{state:"consumed"},updated:at(0),links:[{kind:"branch",id:"issue-57",url:"https://github.com/example/agentflow-console/tree/issue-57"}],
      steps:[step("p1","op.requested","requested",-120000),step("p2","op.awaiting_approval","awaiting_approval",-119000),step("p3","approval.requested","awaiting_approval",-118000),step("p4","approval.resolved","approved",-5000),step("p5","op.requested","requested",-1000),step("p6","op.applied","applied",0)]});
    await post("emit",delta(2,"op.applied",[entry("timeline",timeline([done,applied,failed,elsewhere,wide]))]));
    await until(() => fact(pushID,"status").textContent === "Applied","the applied result did not arrive live");
    check(row(pushID) === kept && document.activeElement === kept.querySelector(".timeline-audit button"),"the row was rebuilt or lost focus");
    check(fact(pushID,"status").classList.contains("live-changed") && !kept.classList.contains("live-changed") && !untouched.querySelector(".live-changed"),"the change is not highlighted, or an unchanged row is");
    check(steps(pushID).join("|") === "Requested|Waiting for approval|Approval requested|Approved|Requested again|Applied" && [...kept.querySelectorAll(".timeline-steps > li")].filter(node => node.classList.contains("live-changed")).length === 3,"the new steps are not added and highlighted");
    check(kept.querySelector("[data-part=approval]").hidden && kept.querySelector(".timeline-links a").href.endsWith("/tree/issue-57") && fact(pushID,"branch").hidden && fact(pushID,"when").textContent.includes(", last step "),"the finished operation still waits, or its link is missing");
    await until(() => said().includes("claude-code: Push a branch applied: Push issue-57."),"the result was not announced: " + said());

    // A new operation arrives at the top; a request alone is not announced, a failure is.
    const fresh = op("op-new","requested",{steps:[step("n1","op.requested","requested",0)],started:at(0),updated:at(0),subject:"fix: a new one"});
    await post("emit",delta(3,"op.requested",[entry("timeline",timeline([fresh,done,applied,failed,elsewhere,wide]))]));
    await until(() => rows()[0]?.dataset.key === "op-new","the new operation is not first");
    check(row("op-new").classList.contains("live-changed") && fact("op-new","status").textContent === "In progress" && fact("op-new","what").dataset.state === "requested" && byID("timeline-count").textContent === "6","the new operation is not highlighted or described");
    await new Promise(resolve => setTimeout(resolve,100));
    check(!said().includes("fix: a new one"),"a bare request was announced");
    await post("emit",delta(4,"op.failed",[entry("timeline",timeline([{...fresh,state:"failed",code:"blocked",steps:[step("n1","op.requested","requested",0),step("n2","op.failed","failed",0,"blocked")]},done,applied,failed,elsewhere,wide]))]));
    await until(() => fact("op-new","status").textContent === "Failed" && said().includes("Command line: Create a local commit failed: blocked by a check."),"the live failure was not shown and announced: " + said());
    check(fact("op-new","reason").textContent === "A check blocked the operation, so nothing ran." && fact("op-new","reason").classList.contains("live-changed"),"the live failure is not explained");

    // Filters: worktree, state and origin, each announced through the one live region.
    choose("timeline-worktree","all");
    check(keys().includes("op-elsewhere") && rows().length === 6 && fact("op-elsewhere","worktree").textContent === "Worktree second" && !byID("timeline-reset").hidden,"all worktrees are not shown");
    check(fact("op-elsewhere","status").textContent === "Suggested only" && fact("op-elsewhere","reason").textContent.includes("only suggests") && fact("op-elsewhere","reason").dataset.tone === "","the suggestion is not explained");
    await until(() => said() === "6 operations.","the filter result was not announced: " + said());
    check(!row("op-elsewhere").classList.contains("live-changed") && !row("op-elsewhere").querySelector(".live-changed"),"a filter change highlighted the row it revealed");
    choose("timeline-worktree","wt:second");
    check(keys() === "op-elsewhere","one worktree is not selected");
    choose("timeline-worktree","all");
    choose("timeline-state","failed");
    check(keys() === "op-new,op-failed" && byID("timeline-summary").textContent === "2 operations · 6 operations in the repository.","the state filter is wrong: " + keys());
    choose("timeline-origin","cli");
    check(keys() === "op-new","the origin filter is wrong: " + keys());
    choose("timeline-origin","client:codex");
    check(keys() === "op-failed","one agent is not selected");
    choose("timeline-origin","agent");
    choose("timeline-state","ended");
    check(keys() === "op-wide","cancelled operations are not selected");
    choose("timeline-state","waiting");
    check(rows().length === 0 && byID("timeline-list").hidden && byID("timeline-empty").textContent === "No operations match these filters." && !byID("timeline-empty").hidden,"an empty filter result is not explained");
    await until(() => said().endsWith("No operations match these filters · 6 operations in the repository."),"the empty result was not announced");
    // A live change that the filters hide is not announced.
    const hidden = said();
    await post("emit",delta(5,"op.applied",[entry("timeline",timeline([op("op-hidden","applied",{started:at(0),updated:at(0)}),{...fresh,state:"failed",code:"blocked"},done,applied,failed,elsewhere,wide]))]));
    await until(() => byID("timeline-count").textContent === "7","the hidden change did not arrive");
    await new Promise(resolve => setTimeout(resolve,100));
    check(said() === hidden && rows().length === 0,"a filtered-out change was shown or announced");
    byID("timeline-reset").click();
    check(byID("timeline-worktree").value === "own" && byID("timeline-state").value === "all" && byID("timeline-origin").value === "all" && byID("timeline-reset").hidden && document.activeElement === byID("timeline-worktree") && rows().length === 6,"Clear filters did not restore the default view");

    // More than a page: ten rows, then the rest on request; the server's cut is stated.
    const many = Array.from({length:14},(_, i) => op("op-" + String(i).padStart(2,"0"),"applied",{started:at(-i * 1000),updated:at(-i * 1000)}));
    await post("emit",delta(6,"op.applied",[entry("timeline",timeline(many,{truncated:true}))]));
    await until(() => rows().length === 10,"the list is not cut to a page");
    check(byID("timeline-more").textContent === "Show 4 more" && !byID("timeline-more").hidden && byID("timeline-summary").textContent === "Showing 10 of 14 operations." && !byID("timeline-truncated").hidden,"paging or the truncation note is wrong: " + byID("timeline-summary").textContent);
    byID("timeline-more").focus();
    byID("timeline-more").click();
    check(rows().length === 14 && byID("timeline-more").hidden && document.activeElement === byID("timeline-title") && byID("timeline-summary").textContent === "14 operations.","Show more did not show the rest or dropped focus");
    check(document.documentElement.scrollWidth <= document.documentElement.clientWidth,"the timeline makes the page scroll sideways");

    // Offline: the values stay and the panel says they may be stale.
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Offline" && !byID("timeline-note").hidden,"the timeline does not say that it is offline");
    check(rows().length === 14 && byID("timeline-note").textContent.includes("not available"),"the timeline was cleared offline");
    await post("status?code=0");
    await post("emit",snapshot(7,[entry("timeline",timeline(base()))]));
    await until(() => label() === "Live" && byID("timeline-note").hidden && rows().length === 4,"the timeline did not recover");

    // A requested Resync replaces the values without highlights or announcements.
    await until(() => !document.querySelector(".live-changed"),"a highlight did not fade");
    const opened = (await requests()).length, before = said();
    byID("refresh").click();
    await until(async () => (await requests()).length > opened,"Resync did not restart the stream");
    await post("emit",snapshot(8,[entry("timeline",timeline([op("op-after","applied",{started:at(0),updated:at(0)}),...base()]))]));
    await until(() => label() === "Live" && Boolean(row("op-after")),"Resync did not reload the timeline");
    check(!document.querySelector("#timeline-panel .live-changed") && said() === before,"a requested resync highlighted or announced values");

    // A snapshot without the timeline keeps the values and says so; an empty timeline explains itself.
    await post("emit",frame("resync",0,{reason:"overflow"}));
    await post("emit",snapshot(9,[]));
    await until(() => !byID("timeline-note").hidden && byID("timeline-note").textContent.includes("no timeline for this repository"),"a stream without the timeline is not explained");
    check(rows().length === 5,"the last values were cleared");
    await post("emit",delta(10,"op.requested",[entry("timeline",timeline([]))]));
    await until(() => !byID("timeline-empty").hidden && byID("timeline-empty").textContent.startsWith("No operations recorded in the last 7 days."),"the empty timeline is not explained");
    check(byID("timeline-count").textContent === "" && byID("timeline-summary").hidden && byID("timeline-note").hidden,"an empty timeline still shows counts or a stale note");

    // Shapes that grow or arrive damaged do not break the panel, and text stays text.
    await post("emit",delta(11,"op.applied",[entry("timeline",{operations:[{id:"odd",state:"brand_new",operation:"docs.publish",client:"",subject:"<img src=x onerror=alert(1)>",code:"x",steps:[{event_id:"o1",type:"op.future",state:"later",time:"never"}],links:"none",future:true},{state:"applied"},null],extra:1})]));
    await until(() => Boolean(row("odd")),"an unknown shape was not read defensively");
    check(fact("odd","what").textContent === "Docs publish" && fact("odd","status").textContent === "Unknown state" && fact("odd","origin").textContent === "Unknown origin" && steps("odd").join() === "Unknown step" && !row("odd").querySelector("img") && fact("odd","subject").textContent.startsWith("<img") && rows().length === 1,"a sparse operation is not shown safely");
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
