"use strict";

(async () => {
  const byID = id => document.getElementById(id);
  function check(test, message) { if (!test) throw new Error(message); }
  async function until(test, message) {
    const end = Date.now() + 4000;
    while (!test()) {
      check(Date.now() < end,message);
      await new Promise(resolve => setTimeout(resolve,20));
    }
  }
  const openTab = async query => (await fetch("/fixture/navigate?" + query,{method:"POST"})).json();
  const ticket = async () => (await fetch("/fixture/ticket",{method:"POST"})).text();
  const reopen = (key, screen, role) => {
    const params = new URLSearchParams({launch:key,screen});
    if (role) params.set("role",role);
    location.hash = params.toString();
  };

  let result = "passed";
  async function goWorktree(name,phase,screen="providers") {
    sessionStorage.setItem("fixture.phase",phase);
    check((await fetch("/fixture/live")).ok,"worktree tab is missing");
    check(await openTab("worktree=w/" + name + "&screen=" + screen + (screen==="providers"?"&role=task_management":"")),"worktree request was not acknowledged");
  }
  try {
    const phase = sessionStorage.getItem("fixture.phase");
    if (phase==="doc-second") {
      await until(() => !byID("console-shell").hidden,"document switch did not open");
      await goWorktree("first","doc-return","configuration");
      return;
    }
    if (phase==="doc-return") {
      await until(() => [...byID("document-control").querySelectorAll("input[type=text]")].some(n=>n.value==="unfinished"),"document field input was lost");
      check(byID("document-search").value==="commit.types","document search was lost");
      check(!byID("source").value.includes("unfinished"),"document input was staged automatically");
      sessionStorage.setItem("fixture.phase","doc-stale");
      await fetch("/fixture/revise",{method:"POST"});
      location.reload();
      return;
    }
    if (phase==="doc-stale") {
      await until(() => !byID("document-recovery").hidden && [...byID("recovery-control").querySelectorAll("input")].some(n=>n.value==="unfinished"),"stale document field could not be recovered");
      check(byID("preview").disabled,"stale draft became publishable");
      await fetch("/result",{method:"POST",body:result});
      return;
    }
    if (phase==="second") {
      await until(() => location.pathname==="/w/second/" && !byID("console-shell").hidden,"second worktree failed to open");
      check(!localStorage.getItem("agentflow.drafts:/w/second/")?.includes("recover this draft"),"worktree inherited another checkout's state");
      await goWorktree("first","returned");
      return;
    }
    if (phase==="returned" || phase==="reloaded") {
      // Providers is a group of the Settings page now.
      await until(() => location.pathname==="/w/first/" && !byID("console-shell").hidden && !byID("page-settings").hidden,"the original worktree did not reopen at its provider settings");
      if (phase==="returned") {
        sessionStorage.setItem("fixture.phase","reloaded");
        location.reload();
        return;
      }
      document.querySelector('a[data-page="configuration"]').click();
      await until(() => !byID("page-configuration").hidden && byID("source").value.includes("recover this draft"),"staged source did not survive reload");
      check(byID("commit-title").value==="fix: retain workspace input","publication title lost");
      await until(() => !byID("document-fields").hidden,"document fields did not load");
      const query=byID("document-search");
      query.value="commit.types";query.dispatchEvent(new Event("input",{bubbles:true}));
      [...byID("document-keys").querySelectorAll("button")].find(b=>b.textContent.includes("commit.types")).click();
      const editor=byID("document-control");
      const mode=editor.querySelector(".te-mode");
      if (mode) {
        mode.value=[...mode.options].find(o=>o.textContent==="Set value").value;
        mode.dispatchEvent(new Event("change",{bubbles:true}));
      }
      [...editor.querySelectorAll("button")].find(b=>b.textContent==="Add item").click();
      const input=editor.querySelector("input[type=text]");
      input.value="unfinished";input.dispatchEvent(new Event("input",{bubbles:true}));
      await goWorktree("second","doc-second");
      return;
    }
    await until(() => !byID("connection-options").hidden,"missing reconnect actions");
    check(byID("console-shell").hidden,"disconnected tab exposed the console");
    check(byID("chat-prompt").textContent.includes("task management provider editor"),"request lost the destination");
    check(byID("reconnect-command").textContent === "agentflow ui --screen providers --role task_management","incorrect terminal command");

    Object.defineProperty(navigator,"clipboard",{configurable:true,value:{writeText: async () => { throw new Error("disabled"); }}});
    byID("copy-prompt").click();
    await until(() => getSelection().toString() === byID("chat-prompt").textContent,"clipboard fallback did not select the request");

    reopen("invalid","configuration");
    await until(() => byID("connection-message").textContent.includes("already used"),"invalid ticket did not explain recovery");
    check(location.hash === "#configuration","spent ticket retained in address");
    check(!byID("connection").innerText.includes("invalid"),"invalid ticket echoed to the screen");

    const first = await ticket();
    reopen(first,"providers","task_management");
    await until(() => !byID("console-shell").hidden && !byID("page-settings").hidden,"provider settings did not open");
    check(document.querySelector('a[data-page="providers"]').getAttribute("aria-current") === "page" && !byID("page-providers") && !byID("page-autonomy"),"the Providers entry does not open the Settings page");
    check(location.hash === "#providers?role=task_management","role link not restored");
    check(byID("connection").hidden,"connected tab still shows recovery");
    check(sessionStorage.getItem("agentflow.session:/w/first/") && !sessionStorage.getItem("agentflow.session:/w/second/"),"credential shared across worktree routes");
    check((await fetch("/fixture/live")).ok,"tab registration failed");
    check(await openTab("auto&screen=handoff"),"background startup missed active worktree");
    check(location.hash === "#providers?role=task_management","background startup changed the active screen");
    check(await openTab("screen=autonomy"),"could not open Autonomy from chat");
    await until(() => location.hash === "#autonomy" && !byID("page-settings").hidden && document.querySelector('a[data-page="autonomy"]').getAttribute("aria-current") === "page","Autonomy did not open its Settings group");
    check(await openTab("screen=providers&role=task_management"),"could not return to the provider editor");
    const capKey="agentflow.commander:/w/first/";
    const grant=sessionStorage.getItem(capKey);
    check(grant && !Object.values(localStorage).some(v=>v.includes(grant)), "Commander grant escaped this tab");
    check(!sessionStorage.getItem("agentflow.commander:/w/second/"),"Commander grant reached a sibling worktree");
    check(await openTab("screen=overview"),"overview did not open");
    await until(() => !byID("page-overview").hidden,"overview not visible");
    // Worker requests load with the checkout; a navigation clears them until Resync.
    check(!byID("activity") && !document.querySelector(".reply-form") && byID("activity-note").textContent.includes("Resync"),"the pull button remains, or cleared worker requests are not explained");
    byID("refresh").click();
    await until(() => !!document.querySelector(".reply-form"),"bound worker request unavailable");
    check(await openTab("screen=handoff"),"explicit request did not reach the same worktree");
    await until(() => !byID("page-handoff").hidden,"explicit request did not reach Handoff");
    check(!byID("handoff-text").textContent.includes("Fixture worker question") && !document.querySelector(".reply-form"),"explicit navigation retained worker activity");
    check(sessionStorage.getItem(capKey)===grant,"tab navigation replaced the Commander grant");
    check(await openTab("screen=overview"),"overview did not reopen");
    byID("refresh").click();
    await until(() => !!document.querySelector(".reply-form"),"same-context worker question failed to reload");
    const worker=document.querySelector(".reply-form");
    worker.querySelector("textarea").value="fixture answer";
    worker.requestSubmit();
    await until(() => worker.querySelector("button").textContent==="Confirm send","bound reply did not reach review");
    worker.requestSubmit();
    await until(() => !sessionStorage.getItem(capKey) && !document.querySelector(".reply-form"),"expired Commander authority retained worker data");
    check(!byID("console-shell").hidden && byID("activity-note").textContent.includes("Commander"),"Commander expiry disrupted configuration or hid recovery");
    check(!byID("attention-list").textContent.includes("Fixture pull request"),"the console pulled activity without a Commander session");
    check(await openTab("screen=providers&role=notifications"),"existing tab did not acknowledge navigation");
    await until(() => location.hash === "#providers?role=notifications" && !byID("page-settings").hidden,"tool navigation lost its role");
    reopen(first,"handoff");
    await until(() => !byID("page-handoff").hidden && byID("connection").hidden,"valid session could not open a spent link");

    document.querySelector('a[data-page="configuration"]').click();
    await until(() => !byID("page-configuration").hidden,"configuration navigation failed");
    const source = byID("source");
    const draft = source.value + "\n# recover this draft\n";
    source.value = draft;
    source.dispatchEvent(new Event("input",{bubbles:true}));
    check(localStorage.getItem("agentflow.browser"),"browser grant missing");
    await fetch("/fixture/expire",{method:"POST"});
    byID("refresh").click();
    await until(() => !byID("refresh").disabled && !byID("console-shell").hidden,"valid browser grant did not resume automatically");
    await fetch("/fixture/expire?remember",{method:"POST"});
    byID("refresh").click();
    await until(() => !byID("connection").hidden,"expired session failed to show recovery");
    check(byID("console-shell").hidden,"expired checkout stayed visible");
    check(!byID("reconnect-drafts").hidden && byID("draft-downloads").children.length === 1,"staged source not recoverable");
    check(byID("reconnect-command").textContent === "agentflow ui --screen configuration","configuration recovery command missing");

    reopen(await ticket(),"configuration");
    await until(() => !byID("console-shell").hidden && !byID("page-configuration").hidden,"failed to reconnect this tab");
    check(byID("source").value === draft,"same-tab reconnect lost the draft");
    check(location.hash === "#configuration","recovery did not preserve the destination");
    check(sessionStorage.getItem(capKey),"fresh scoped launch failed to restore Commander access");
    reopen(await ticket(),"configuration");
    await until(() => !sessionStorage.getItem(capKey) && !byID("refresh").disabled,"a fresh config-only launch retained another actor’s grant");
    check(byID("source").value===draft,"config-only launch disrupted the worktree draft");
    byID("commit-title").value="fix: retain workspace input";
    document.querySelector('a[data-page="providers"]').click();
    await until(() => location.hash === "#providers" && !byID("page-settings").hidden,"the Providers entry did not open its Settings group");
    const write=Storage.prototype.setItem;
    try {
      Storage.prototype.setItem=function(key,val) {
        if (key.startsWith("agentflow.drafts:")) throw new DOMException("full","QuotaExceededError");
        return write.call(this,key,val);
      };
      const blocked=await fetch("/fixture/navigate?worktree=w/second&screen=configuration",{method:"POST"});
      check(blocked.status===409 && (await blocked.json()).error.includes("storage"),"caller was not told that storage blocked the worktree switch");
      check(location.pathname==="/w/first/" && !byID("page-settings").hidden && !byID("workspace-save-error").hidden,"failed switch displaced the page or did not say why");
    } finally { Storage.prototype.setItem=write; }
    await goWorktree("second","second");
    return;
  } catch (error) { result = error.message; }
  await fetch("/result",{method:"POST",body:result});
})();
