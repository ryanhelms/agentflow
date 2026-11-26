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

  // ActivityState and ApprovalsState from projection_states.go.
  const pull = (number, extra = {}) => ({number,title:"Pull " + number,url:"https://github.com/example/agentflow-console/pull/" + number,state:"open",draft:false,head_ref:"issue-" + number,head_sha:"abc",base_ref:"main",author:"octocat",updated_at:at(-60000),...extra});
  const commit = (id, subject, extra = {}) => ({event_id:id,type:"commit.created",time:at(-30000),worktree:"first",commit:{oid:"9f8e7d6c5b4a",branch:"main",subject,type:"feat",...extra}});
  const blocked = {event_id:"h1",type:"hook.ran",time:at(-90000),worktree:"second",hook:{hook:"pre-bash",client:"claude-code",outcome:"blocked",reason:"repo_boundary",duration_ms:12}};
  const activity = (extra = {}) => ({
    pulls:[pull(40,{review_decision:"approved",merge_state:"clean",ci:"success",worktree:"first"}),pull(41,{draft:true,ci:"pending",base_ref:"issue-40",depends_on:40,worktree:"second"})],
    issues:[{number:37,title:"Live panels",url:"https://github.com/example/agentflow-console/issues/37",state:"open",labels:["ui"],assignees:["octocat"],updated_at:at(-60000),worktree:"first"}],
    recent:[commit("c1","feat: first commit"),blocked],
    github:{state:"ok"},handoff:"Pull 40 — ready https://github.com/example/agentflow-console/pull/40\n",...extra
  });
  const approval = (id, state, extra = {}) => ({operation_id:id,action:"push",class:"remote",client:"claude-code",policy_source:".agentflow/config.yml",issued_at:at(-60000),expires_at:at(290000),state,...extra});
  const approvals = (open, recent = []) => ({open,recent});
  const row = (list, key) => [...byID(list).children].find(node => node.dataset.key === String(key));
  const fact = (list, key, name) => row(list,key)?.querySelector('[data-fact="' + name + '"]');
  const said = () => byID("live-announcer").textContent;
  const label = () => byID("stream-label").textContent;

  let result = "passed";
  try {
    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");

    // Before the first snapshot: no pull button, and the panels say they are waiting.
    check(!byID("activity") && !document.body.textContent.includes("Check activity") && byID("refresh").textContent === "Resync","the pull button was not removed, or Resync is missing");
    check(!byID("activity-wait").hidden && byID("activity-body").hidden && !byID("approvals-wait").hidden && byID("approvals-body").hidden,"the panels do not show that they wait for the stream");

    // The first snapshot fills the panels without highlights or announcements.
    await post("emit",snapshot(1,[entry("activity",activity()),entry("approvals",approvals([approval("op-1","pending")],[approval("op-0","consumed",{action:"commit",class:"local"})]))]));
    await until(() => label() === "Live" && !byID("activity-body").hidden && !byID("approvals-body").hidden,"the snapshot did not fill the panels");
    check(byID("activity-wait").hidden && byID("activity-live-note").hidden && byID("approvals-note").hidden,"a live panel still shows a waiting or stale note");
    check(!document.querySelector(".live-changed") && !said(),"the first live values were highlighted or announced");
    const calls = await requests();
    const topics = new URLSearchParams(calls[0].query).getAll("topic");
    check(["*/*/gh.**","*/*/commit.created","*/*/hook.ran","*/*/approval.**","*/*/ui.**"].every(topic => topics.includes(topic)),"the panel topics are missing from the stream");

    // Pull requests: readiness, checks, review, dependency and worktree.
    check([...byID("pull-list").children].map(node => node.dataset.key).join() === "40,41" && byID("pull-count").textContent === "2","the pull requests are not listed in the server's order");
    const link = row("pull-list",40).querySelector("a");
    check(link.href.endsWith("/pull/40") && link.target === "_blank" && link.rel === "noopener noreferrer" && link.textContent === "#40 Pull 40","the pull request link is wrong");
    check(fact("pull-list",40,"status").textContent === "Ready to merge" && fact("pull-list",40,"status").dataset.state === "good","merge readiness is not shown");
    check(fact("pull-list",40,"ci").textContent === "Checks passing" && fact("pull-list",40,"ci").dataset.state === "success" && fact("pull-list",40,"ci").querySelector(".state-mark").getAttribute("aria-hidden") === "true","the CI state is not shown as text with a mark");
    check(fact("pull-list",40,"review").textContent === "Approved" && fact("pull-list",40,"worktree").textContent === "This worktree" && fact("pull-list",40,"depends").hidden && fact("pull-list",40,"failed").hidden,"review, worktree or empty facts are wrong");
    check(fact("pull-list",41,"status").textContent === "Draft" && fact("pull-list",41,"ci").textContent === "Checks running" && fact("pull-list",41,"depends").textContent === "Depends on #40 (ready to merge)" && fact("pull-list",41,"worktree").textContent === "Worktree second","the dependent draft is not described");
    check(fact("pull-list",41,"branch").textContent === "issue-41 into issue-40","the branches are not shown");

    // Issues, recent commits and stopped hooks, GitHub health.
    check(fact("issue-list",37,"status").textContent === "Open" && fact("issue-list",37,"labels").textContent === "ui" && fact("issue-list",37,"assignees").textContent === "Assigned to octocat" && byID("issue-count").textContent === "1","the bound issue is not shown");
    check([...byID("recent-list").children].map(node => node.dataset.key).join() === "c1,h1","recent entries are not newest first");
    check(fact("recent-list","c1","what").textContent === "feat: first commit" && fact("recent-list","c1","oid").textContent === "9f8e7d6" && fact("recent-list","c1","branch").textContent === "on main" && row("recent-list","c1").querySelector("time").dateTime,"the commit is not described");
    check(fact("recent-list","h1","what").textContent === "pre-bash hook blocked: repository boundary" && fact("recent-list","h1","what").dataset.state === "blocked" && fact("recent-list","h1","client").textContent === "from claude-code","the blocked hook is not described");
    check(byID("github-health").dataset.state === "ok" && byID("github-label").textContent === "GitHub up to date" && byID("github-note").hidden,"GitHub health is not shown");

    // Approvals: open with a countdown, recent outcomes.
    check(fact("approval-open","op-1","action").textContent === "Push a branch" && fact("approval-open","op-1","status").textContent === "Waiting for approval" && byID("approval-count").textContent === "1 waiting","the open approval is not shown");
    check(/^Expires in 4 min \d\d s$/.test(fact("approval-open","op-1","expiry").textContent) && fact("approval-open","op-1","class").textContent === "remote write" && fact("approval-open","op-1","client").textContent === "Asked by claude-code" && fact("approval-open","op-1","id").textContent === "op-1","the approval countdown or its details are wrong");
    check(fact("approval-recent","op-0","status").textContent === "Used" && fact("approval-recent","op-0","action").textContent === "Create a local commit" && fact("approval-recent","op-0","expiry").hidden,"the recent outcome is not shown");
    const shownExpiry = fact("approval-open","op-1","expiry").textContent;
    await until(() => fact("approval-open","op-1","expiry").textContent !== shownExpiry,"the countdown does not run");
    check(!fact("approval-open","op-1","expiry").classList.contains("live-changed") && !said(),"the countdown tick was highlighted or announced");

    // The handoff brief follows the stream.
    check(byID("handoff-text").textContent.includes("Pull 40 — ready") && byID("handoff-updated").hidden && !byID("copy-handoff").disabled,"the live handoff is not in the brief");

    // A CI failure arrives live: changed in place, highlighted and announced.
    const kept = row("pull-list",40);
    link.focus();
    await post("emit",delta(2,"gh.ci.updated",[entry("activity",activity({pulls:[pull(40,{review_decision:"approved",merge_state:"clean",ci:"success",worktree:"first"}),pull(41,{ci:"failure",failed:["lint","test"],base_ref:"issue-40",depends_on:40,worktree:"second"})]}))]));
    await until(() => fact("pull-list",41,"status").textContent === "Checks failing","the CI failure did not arrive live");
    check(fact("pull-list",41,"status").dataset.state === "bad" && fact("pull-list",41,"ci").dataset.state === "failure" && fact("pull-list",41,"failed").textContent === "Failed: lint, test","the failed checks are not shown");
    check(fact("pull-list",41,"status").classList.contains("live-changed") && fact("pull-list",41,"ci").classList.contains("live-changed"),"the changed values are not highlighted");
    check(row("pull-list",40) === kept && !kept.querySelector(".live-changed") && document.activeElement === link,"an unchanged row was rebuilt, highlighted or lost focus");
    await until(() => said().includes("Pull request #41: checks failing"),"the CI failure was not announced");

    // GitHub rate limiting shows the retry time and keeps the last values.
    await post("emit",delta(3,"gh.pr.updated",[entry("activity",activity({github:{state:"rate_limited",retry_at:at(95000)}}))]));
    await until(() => byID("github-health").dataset.state === "rate_limited","the rate limit is not shown");
    check(/^GitHub rate limited · retries at .+ \(in 1 min \d\d s\)$/.test(byID("github-label").textContent) && !byID("github-note").hidden && byID("github-note").textContent.includes("last values"),"the retry time or the stale note is missing");
    check(byID("pull-list").children.length === 2,"the pull requests were cleared while rate limited");
    await until(() => said().includes("GitHub is rate limited"),"the rate limit was not announced");
    const retry = byID("github-label").textContent;
    await until(() => byID("github-label").textContent !== retry,"the retry time does not count down");

    // A new commit is added at the top, highlighted and announced.
    await post("emit",delta(4,"commit.created",[entry("activity",activity({recent:[commit("c2","fix: second commit",{type:"fix",breaking:true}),commit("c1","feat: first commit"),blocked]}))]));
    await until(() => byID("recent-list").firstElementChild?.dataset.key === "c2","the new commit is not first");
    check(row("recent-list","c2").classList.contains("live-changed") && !row("recent-list","c1").classList.contains("live-changed") && fact("recent-list","c2","kind").textContent === "fix · breaking change","the new commit is not highlighted or described");
    await until(() => said().includes("New commit on main: fix: second commit") && said().includes("GitHub updates restored"),"the new commit or the GitHub recovery was not announced");

    // A new approval close to expiry: announced, marked, then expired by the clock.
    await post("emit",delta(5,"approval.requested",[entry("approvals",approvals([approval("op-1","pending"),approval("op-2","pending",{action:"pr_open_draft",expires_at:at(2500)})],[approval("op-0","consumed",{action:"commit",class:"local"})]))]));
    await until(() => Boolean(row("approval-open","op-2")),"the new approval did not arrive live");
    check(row("approval-open","op-2").classList.contains("live-changed") && fact("approval-open","op-2","expiry").dataset.state === "soon" && byID("approval-count").textContent === "2 waiting","the new approval is not highlighted, or its short time is not marked");
    await until(() => said().includes("Approval requested: Open a draft pull request. Expires in"),"the approval request was not announced");
    await until(() => fact("approval-open","op-2","status").textContent === "Expired","the approval did not expire on screen");
    check(fact("approval-open","op-2","status").dataset.state === "bad" && fact("approval-open","op-2","expiry").hidden && byID("approval-count").textContent === "1 waiting","an expired approval still counts down or counts as waiting");
    await until(() => said().includes("Approval for Open a draft pull request expired"),"the expiry was not announced");

    // Outcomes move to the recent list; an approved request stays open.
    await post("emit",delta(6,"approval.resolved",[entry("approvals",approvals([approval("op-1","approved")],[approval("op-2","expired",{action:"pr_open_draft",expires_at:at(-1000)}),approval("op-0","consumed",{action:"commit",class:"local"})]))]));
    await until(() => fact("approval-open","op-1","status").textContent === "Approved, not used yet","the approval was not shown as approved");
    check(!row("approval-open","op-2") && fact("approval-recent","op-2","status").textContent === "Expired" && byID("approval-count").textContent === "","the expired approval did not move to the recent outcomes");
    await until(() => said().includes("Push a branch approved") && !said().includes("pull request expired"),"the approval was not announced, or the expiry was announced twice");
    await post("emit",delta(7,"approval.resolved",[entry("approvals",approvals([],[approval("op-1","cancelled"),approval("op-2","expired",{action:"pr_open_draft",expires_at:at(-1000)})]))]));
    await until(() => byID("approval-open").textContent === "Nothing is waiting for approval.","the empty approvals state is not shown");
    check(fact("approval-recent","op-1","status").textContent === "Cancelled" && !row("approval-recent","op-0"),"the cancelled approval is not in the recent outcomes");
    await until(() => said().includes("Approval for Push a branch cancelled"),"the cancellation was not announced");

    // A merged pull request leaves the list; focus moves to the panel heading.
    row("pull-list",41).querySelector("a").focus();
    await post("emit",delta(8,"gh.pr.updated",[entry("activity",activity({pulls:[pull(40,{review_decision:"approved",merge_state:"clean",ci:"success",worktree:"first"})],issues:[{number:37,title:"Live panels",url:"https://github.com/example/agentflow-console/issues/37",state:"closed",state_reason:"completed",updated_at:at(0),worktree:"first"}]}))]));
    await until(() => !row("pull-list",41),"the merged pull request stayed in the list");
    check(document.activeElement === byID("activity-title") && byID("pull-count").textContent === "1","focus was lost with the removed row");
    check(fact("issue-list",37,"status").textContent === "Closed · completed" && fact("issue-list",37,"status").classList.contains("live-changed"),"the closed issue is not shown");
    await until(() => said().includes("Pull request #41 is no longer open") && said().includes("Issue #37 closed"),"the removal or the closed issue was not announced");

    // The handoff screen updates live and says so.
    location.hash = "#handoff";
    await until(() => !byID("page-handoff").hidden,"the handoff screen did not open");
    await post("emit",delta(9,"gh.pr.updated",[entry("activity",activity({pulls:[],handoff:"Pull 42 — review https://github.com/example/agentflow-console/pull/42\n"}))]));
    await until(() => byID("handoff-text").textContent.includes("Pull 42 — review"),"the handoff did not follow the stream");
    check(!byID("handoff-text").textContent.includes("Pull 40") && byID("handoff-text").classList.contains("live-changed") && !byID("handoff-updated").hidden && byID("handoff-updated").textContent.startsWith("Updated live at"),"the live handoff change is not marked");
    await until(() => said().includes("The repository brief changed."),"the handoff change was not announced");
    location.hash = "#overview";
    await until(() => !byID("page-overview").hidden,"the overview did not reopen");
    check(byID("pull-list").textContent === "No open pull requests." && byID("pull-count").textContent === "","the empty pull request state is not shown");

    // Offline: the panels keep their values and say they may be stale.
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Offline" && !byID("activity-live-note").hidden && !byID("approvals-note").hidden,"the panels do not say that they are offline");
    check(Boolean(row("issue-list",37)) && Boolean(row("approval-recent","op-1")) && !byID("handoff-note").hidden,"the panels were cleared offline, or the handoff does not say it is stale");
    await post("status?code=0");
    await post("emit",snapshot(9,[entry("activity",activity({pulls:[],handoff:"Pull 42 — review https://github.com/example/agentflow-console/pull/42\n"})),entry("approvals",approvals([]))]));
    await until(() => label() === "Live" && byID("activity-live-note").hidden && byID("handoff-note").hidden,"the panels did not recover");

    // A requested Resync replaces the values without highlights.
    await until(() => !document.querySelector(".live-changed"),"a highlight did not fade");
    const opened = (await requests()).length;
    byID("refresh").click();
    await until(async () => (await requests()).length > opened,"Resync did not restart the stream");
    await post("emit",snapshot(10,[entry("activity",activity({github:{state:"unavailable"}})),entry("approvals",approvals([approval("op-3","pending")]))]));
    await until(() => label() === "Live" && byID("pull-list").children.length === 2 && Boolean(row("approval-open","op-3")),"Resync did not reload the panels");
    check(!document.querySelector(".live-changed"),"a requested resync highlighted values");
    check(byID("github-health").dataset.state === "unavailable" && byID("github-label").textContent === "GitHub unavailable" && !byID("github-note").hidden,"an unavailable GitHub is not explained");

    // A snapshot without the repository states keeps the values and says so.
    await post("emit",frame("resync",0,{reason:"overflow"}));
    await post("emit",snapshot(11,[]));
    await until(() => !byID("activity-live-note").hidden && byID("activity-live-note").textContent.includes("no activity for this repository"),"a stream without activity is not explained");
    check(byID("pull-list").children.length === 2,"the last values were cleared");

    // Shapes that grow or arrive damaged do not break the panels.
    await post("emit",delta(12,"gh.pr.updated",[entry("activity",{pulls:[{number:50,future:true},{title:"no number"},null],issues:"none",recent:[{event_id:"x"}],github:{state:"new_state"},extra:1})]));
    await until(() => Boolean(row("pull-list",50)),"an unknown shape was not read defensively");
    check(fact("pull-list",50,"title").textContent === "Untitled pull request" && !row("pull-list",50).querySelector("a").hasAttribute("href") && fact("pull-list",50,"status").textContent === "Open" && byID("pull-list").children.length === 1,"a sparse pull request is not shown safely");
    check(byID("github-label").textContent === "GitHub state unknown" && byID("issue-list").textContent.includes("No issues") && byID("recent-list").textContent.includes("No commits"),"unknown values are not handled");
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
