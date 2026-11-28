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
  const drafted = async route => (await (await fetch("/fixture/draft/requests")).json()).filter(item => !route || item.route === route);
  const answer = (route, status, body) => fetch("/fixture/draft/answer?route=" + route + "&status=" + status,{method:"POST",body:JSON.stringify(body)});
  const others = async () => (await fetch("/fixture/ai/requests")).json();
  const repo = "3f2a9c1b7d4e5f60";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const frame = (event, seq, data) => "event: " + event + "\n" + (seq ? "id: v1:" + repo + "=" + seq + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const entry = state => ({projection:"ai",repo,state});
  const overview = {projection:"overview",repo,worktree:"first",state:{branch:"main",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]}}};
  const snapshot = (seq, states) => frame("snapshot",seq,{cursor:"v1:" + repo + "=" + seq,states:[overview,...states]});
  const delta = (seq, type, states) => frame("delta",seq,{event:{id:"e" + seq,type,repo,seq},states});

  // AIState.Features and AIState.Drafts from the ADR ("Phase 8 AI projection
  // and console routes"), as lane 8.1's projection sends them.
  const id = n => "ais_" + String(n).repeat(32);
  const hostile = "<img src=x onerror=\"document.title='owned'\"><script>document.title='owned'</script> [click](javascript:alert(1)) https://evil.example/x";
  const never = ["environment values","credentials and tokens agentflow holds","whole files (only diff hunks)"];
  const parts = [
    {kind:"files",label:"changed files",source:"git",bytes:120,original_bytes:120},
    {kind:"diff",label:"staged changes",source:"git",bytes:98304,original_bytes:246000,cut:true,redacted:3,excluded:[{path:".env",reason:"secret_file"},{path:"go.sum",reason:"generated"},{path:"docs/" + hostile,reason:"binary"},{path:"gnp<U+202E>txt.exe",reason:"future_reason"}]},
    {kind:"future_kind",label:"something new",source:"jira",bytes:1,original_bytes:1},
    {kind:"hint",label:"your hint",source:"caller",bytes:13,original_bytes:13}
  ];
  const manifest = (extra = {}) => ({feature:"commit",harness:"claude-acp",harness_name:"Claude Agent",version:"1.2.3",path:"~/bin/claude-acp",worktree:"~/wt/one",branch:"issue-1",started_by:{kind:"person",surface:"console"},
    parts,targets:[{index:1,kind:"thread",id:"T1",label:"src/a.go:12, " + hostile}],hint:"keep it short",bytes:101000,prompt_sha256:"ab".repeat(32),max_duration:180e9,never_sent:never,...extra});
  const confirmID = n => "cf_" + String(n).repeat(32);
  const feature = (name, extra = {}) => ({id:name,label:{commit:"Commit message",pr:"PR title and body",ticket:"Ticket",ac:"Acceptance-criteria check",reply:"Review replies"}[name],harness:"claude-acp",harness_name:"Claude Agent",available:true,...extra});
  const features = [feature("commit"),feature("pr",{harness_name:hostile}),feature("ticket",{harness:"",harness_name:"",available:false,code:"feature_off",fix:"Choose a harness for this feature in Settings (ai.features.ticket.harness)."}),
    feature("ac",{harness:"codex-acp",harness_name:"",available:false,code:"not_ready",fix:"Check the harness on the Settings page."}),feature("reply")];
  const record = (n, name, state, extra = {}) => ({session:id(n),feature:name,harness:"claude-acp",harness_name:"Claude Agent",worktree:"~/wt/one",worktree_id:"first",branch:"issue-1",state,started_by:"person",created_at:at(-60000 * n),manifest:manifest({feature:name}),denied:0,...extra});
  const message = "feat: it's \"done\"\n\n$(rm -rf ~) `id` " + hostile;
  const ready = n => record(n,"commit","ready",{ended_at:at(0),draft:{session:id(n),feature:"commit",state:"ready",finished:at(0),note:"x",candidates:[
    {operation:"commit.prepare",tool:"agentflow_commit_prepare",input:{message},cli:["agentflow","commit","--prepare","-m",message],fields:[{name:"message",label:"Message",value:message}],warnings:[{code:"link",field:"message",message:"The text has a link: check where it goes before you use it."}]},
    {operation:"review.reply",tool:"agentflow_review_reply",input:null,cli:null,target:{index:2,kind:"thread",id:"T2",label:"b.go:3, bob<U+200B>"},fields:[{name:"reply",label:"Reply",value:""}],issues:[{code:"secret_in_draft",field:"reply",message:"The drafted text looks like it holds a secret, so it is not offered."}]},
    {operation:"future.op",cli:["agentflow","commit","-m","hidden ‮ text"],fields:[{name:"message",label:"Message",value:"hidden ‮ text"}]}]}});
  const state = (drafts, extra = {}) => ({sessions:[],running:0,max_running:2,drafts,features,...extra});

  const page = byID("page-ai"), box = byID("ai-draft");
  const rows = () => [...byID("ai-draft-list").querySelectorAll(".ai-row")];
  const row = n => byID("ai-draft-list").querySelector('.ai-row[data-draft="' + id(n) + '"]');
  const card = name => byID("ai-draft-features").querySelector('[data-feature="' + name + '"]');
  const open = name => card(name).querySelector(".ai-draft-open");
  const said = () => byID("live-announcer").textContent;
  const label = () => byID("stream-label").textContent;
  const note = () => byID("ai-draft-status").textContent;
  const body = () => byID("ai-draft-item-body");
  const armed = () => !byID("ai-draft-confirm").hidden && !byID("ai-draft-send").hidden && !byID("ai-draft-send").hasAttribute("aria-disabled");
  // Untrusted text may never become an element of the page.
  const inert = () => document.title !== "owned" && !box.querySelector("img, script, iframe, a[href^='javascript'], a[href^='http']");

  let result = "passed";
  try {
    // A command line is quoted for a POSIX shell: nothing in a word is read by the shell.
    const D = AgentflowAIDraft;
    check(D.shellWord("") === "''" && D.shellWord("a b") === "'a b'" && D.shellWord("--prepare") === "--prepare" && D.shellWord("$HOME") === "'$HOME'" && D.shellWord("a;b") === "'a;b'" && D.shellWord("it's") === "'it'\\''s'"
      && D.shellWord("-m") === "-m" && D.shellWord("a\nb") === "'a\nb'" && D.shellWord("~") === "'~'" && D.shellWord("*") === "'*'","a word is not quoted for a shell");
    check(D.commandLine(["agentflow","commit","-m","it's `x` $(y)"]) === "agentflow commit -m 'it'\\''s `x` $(y)'","a command line is not quoted for a shell");
    check(D.stateView({state:"ready"}).label === "Ready to read" && D.stateView({state:"invalid"}).tone === "warn" && D.stateView({state:"failed",code:"auth_required"}).detail.includes("sign-in") && D.stateView({state:"failed",code:"new_code"}).detail.includes("Reason code new_code.")
      && D.stateView({state:"running"}).live && D.stateView({state:"unknown",code:""}).label === "Unknown state" && D.stateView({state:"cancelled"}).tone === "neutral","the draft states are not told apart");

    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");
    document.querySelector('.rail nav a[data-page="ai"]').click();
    await until(() => !page.hidden,"the AI sessions page did not open");
    check(box.hidden,"the draft view shows before the first state");

    // The readers are defensive: a missing or damaged value never throws.
    const R = aiPage.drafts.read;
    check(R.state(null) === null && R.state("x") === null && R.state([]) === null,"a missing state is not read as none");
    const bare = R.state({});
    check(bare.features.length === 0 && bare.drafts.length === 0 && bare.offers === false && R.state({features:[]}).offers === true,"an empty state is not read safely");
    const odd = R.state({features:[{id:"commit",available:"yes",code:"<b>",label:7},{id:"commit"},{id:"bogus"},null,7],drafts:[{session:id(1),feature:"bogus",state:"future",code:"<b>",started_by:"root",created_at:"never",manifest:"x",draft:"y",denied:-1,warnings:"z"},{session:id(1)},{session:"../etc"},null]});
    check(odd.features.length === 1 && odd.features[0].available === false && odd.features[0].code === "" && odd.features[0].label === "Commit message" && odd.drafts.length === 1 && odd.drafts[0].state === "unknown" && odd.drafts[0].code === ""
      && odd.drafts[0].startedBy === "" && odd.drafts[0].created === 0 && odd.drafts[0].manifest === null && odd.drafts[0].result === null && odd.drafts[0].denied === 0 && odd.drafts[0].warnings.length === 0 && odd.drafts[0].label === "Draft","a damaged state is not read safely: " + JSON.stringify(odd).slice(0,500));
    // Only a manifest the page can show in full can be sent from here.
    const whole = extra => R.manifest(manifest(extra)).complete;
    check(whole({}) === true && whole({parts:"x"}) === false && whole({parts:[]}) === false && whole({parts:[{kind:"diff",label:"x",source:"git"}]}) === false && whole({parts:[null]}) === false && whole({parts:[{kind:"diff",label:"x",bytes:-1}]}) === false
      && whole({parts:Array(40).fill(parts[0])}) === false && whole({parts:[{...parts[0],excluded:"all"}]}) === false && whole({harness:""}) === false && whole({feature:"bogus"}) === false && whole({bytes:0}) === false && whole({targets:"x"}) === false
      && whole({targets:undefined,hint:undefined,never_sent:undefined,version:undefined}) === true && R.manifest(null) === null && R.manifest("x") === null,"a manifest the page cannot show in full can be sent");
    // What is sent is shown whole, or the draft cannot be sent from here.
    const long = "<U+200B>".repeat(16384);
    const sent = R.manifest(manifest({feature:"ticket",text:long,hint:long}));
    check(sent.complete === true && sent.text === long && sent.hint === long && whole({text:"x".repeat(300000)}) === false && whole({hint:"x".repeat(300000)}) === false,"a long description is cut, or can be sent without being shown");
    const read = R.manifest(manifest());
    check(read.parts.length === 4 && read.parts[1].cut && read.parts[1].redacted === 3 && read.parts[1].excluded.length === 4 && read.parts[1].excluded[0].reason === "looks like a secret file" && read.parts[1].excluded[3].reason === "reason code future_reason"
      && read.parts[2].kind === "other" && read.parts[2].source === "jira" && read.parts[3].source === "you" && read.limit === 180000 && read.name === "Claude Agent" && read.hash.length === 64 && read.never.length === 3,"the manifest is not read: " + JSON.stringify(read).slice(0,400));
    // A command is offered only when the candidate has no issue and the page can show all of it as it is.
    const line = raw => R.candidate(raw).command;
    check(line({cli:["agentflow","commit","-m","ok then"]}) === "agentflow commit -m 'ok then'" && line({cli:["agentflow","commit","-m","a‮b"]}) === "" && line({cli:["agentflow","commit","-m","a\u001b[201~b"]}) === "" && line({cli:["rm","-rf","/"]}) === ""
      && line({cli:null}) === "" && line({cli:"agentflow"}) === "" && line({cli:["agentflow",7]}) === "" && line({cli:[]}) === "" && line({cli:["agentflow","x".repeat(80000)]}) === "" && line({cli:Array(80).fill("agentflow")}) === ""
      && line({cli:["agentflow","commit","-m","ok"],issues:[{code:"secret_in_draft",message:"m"}]}) === "" && R.candidate({cli:["rm"]}).hidden === true && R.candidate({cli:["agentflow"],issues:[{code:"x"}]}).hidden === false
      && line({cli:["agentflow","pr","--body","a\r\nb\tc"]}) === "agentflow pr --body 'a\nb\tc'" && R.candidate(null) === null,"a command the page cannot show as it is is offered");

    // A daemon that sends no features says so; the view never guesses one.
    await post("emit",snapshot(1,[entry({sessions:[],running:0,max_running:2})]));
    await until(() => !box.hidden,"the draft view did not show with the first state");
    check(!byID("ai-draft-none").hidden && byID("ai-draft-none").textContent.includes("does not offer drafts") && byID("ai-draft-features").children.length === 0 && !byID("ai-draft-empty").hidden && byID("ai-draft-results").hidden,"a state without features is not explained");

    // The features: a ready one can be asked for, an off one says why and how to turn it on, and has no button.
    await post("emit",snapshot(2,[entry(state([]))]));
    await until(() => byID("ai-draft-features").children.length === 5,"the features did not show");
    check(byID("ai-draft-none").hidden && open("commit") && open("pr") && open("reply") && !open("ticket") && !open("ac"),"a feature that is off or not ready can be asked for");
    check(card("commit").textContent.includes("Ready") && card("commit").textContent.includes("Its harness is Claude Agent.") && open("commit").getAttribute("aria-label") === "Draft: Commit message" && open("commit").getAttribute("aria-expanded") === "false","the ready feature is wrong: " + card("commit").textContent);
    check(card("ticket").textContent.includes("Off") && card("ticket").textContent.includes("nothing can be sent for it") && card("ticket").textContent.includes("ai.features.ticket.harness") && card("ticket").querySelector("a").getAttribute("href") === "#settings?group=ai"
      && card("ac").textContent.includes("Not ready") && card("ac").textContent.includes("The harness codex-acp is not ready.") && card("ac").textContent.includes("Check the harness on the Settings page."),"an unavailable feature does not say why: " + card("ticket").textContent + " | " + card("ac").textContent);
    check(card("pr").textContent.includes("<img src=x") && inert(),"a harness name was read as markup");
    check(byID("ai-draft-compose").hidden && byID("ai-draft-confirm").hidden && !byID("ai-draft-empty").hidden && !said(),"the view asks or announces before the reader does anything");
    check((await drafted()).length === 0,"the view sent a request by itself");

    // Asking: the request opens in place, takes focus, and limits the hint.
    open("commit").click();
    check(!byID("ai-draft-compose").hidden && document.activeElement === byID("ai-draft-compose-title") && byID("ai-draft-compose-title").textContent === "Request a draft: Commit message" && open("commit").getAttribute("aria-expanded") === "true"
      && byID("ai-draft-hint") && !byID("ai-draft-text") && !byID("ai-draft-thread") && byID("ai-draft-hint-count").textContent === "0 of 2,048 bytes","the request did not open: " + byID("ai-draft-compose").textContent.slice(0,200));
    byID("ai-draft-hint").value = "é".repeat(1025);
    byID("ai-draft-hint").dispatchEvent(new Event("input"));
    check(byID("ai-draft-hint-count").textContent.includes("2 bytes too long") && byID("ai-draft-hint-count").dataset.state === "bad","a hint over the limit is not counted in bytes");
    byID("ai-draft-prepare").click();
    check(note() === "The hint is too long." && byID("ai-draft-status").dataset.state === "bad" && document.activeElement === byID("ai-draft-hint") && (await drafted()).length === 0,"a hint over the limit was sent");
    byID("ai-draft-hint").value = "  keep it short ";
    byID("ai-draft-hint").dispatchEvent(new Event("input"));

    // A refused request shows the console's own message and fix, and sends nothing.
    await answer("draft",409,{code:"nothing_to_draft",message:"Nothing is staged. " + hostile,fix:"Stage a change first."});
    byID("ai-draft-prepare").click();
    await until(async () => (await drafted("draft")).length === 1 && note().startsWith("Nothing was sent."),"the refusal did not show: " + note());
    let request = (await drafted("draft"))[0];
    check(request.body === JSON.stringify({feature:"commit",input:{hint:"keep it short"}}) && request.console === "console" && request.authorized === "true" && request.content_type === "application/json","the draft request is wrong: " + JSON.stringify(request));
    check(note().includes("Nothing is staged.") && note().includes("Stage a change first.") && byID("ai-draft-status").dataset.state === "bad" && !byID("ai-draft-compose").hidden && byID("ai-draft-confirm").hidden && inert(),"the refusal is wrong: " + note());
    await answer("draft",429,{error:"too many draft requests: wait a minute"});
    byID("ai-draft-prepare").click();
    await until(() => note().includes("too many draft requests"),"a rate limit did not show: " + note());

    // The list of what would be sent: nothing is sent to get it, and it takes focus.
    await answer("draft",200,{confirm:confirmID(1),manifest:manifest()});
    byID("ai-draft-prepare").click();
    await until(() => !byID("ai-draft-confirm").hidden,"the list of what would be sent did not show");
    const list = byID("ai-draft-confirm");
    check(byID("ai-draft-compose").hidden && document.activeElement === byID("ai-draft-confirm-title") && (await drafted("confirm")).length === 0 && note() === "","the list did not take the reader's attention, or something was sent");
    await until(() => said().includes("Nothing has been sent yet"),"the list was not announced: " + said());
    check(byID("ai-draft-confirm-lead").textContent.startsWith("Nothing has been sent yet.") && list.textContent.includes("Claude Agent (claude-acp), version 1.2.3") && list.textContent.includes("~/bin/claude-acp") && list.textContent.includes("model provider")
      && list.textContent.includes("~/wt/one") && list.textContent.includes("issue-1") && list.textContent.includes("after 3 min.") && list.textContent.includes("Agentflow sends nothing"),"the list does not say where the data goes: " + list.textContent.slice(0,600));
    const held = list.querySelector(".ai-draft-held"), table = list.querySelector(".ai-draft-parts");
    check(held.textContent === "4 files are left out: the path is sent, the content is not. 3 values that look like tokens are replaced with [redacted]. 1 part is cut to fit." && held.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING,"what is held back does not come first: " + held.textContent);
    check(table.caption.textContent === "What would be sent: 4 parts, 98.6 KiB with Agentflow's instructions" && table.tBodies[0].rows.length === 4 && table.tHead.rows[0].cells.length === 3 && table.tBodies[0].rows[0].cells[0].tagName === "TH"
      && table.tBodies[0].rows[0].textContent === "changed filesgit120 bytes" && table.tBodies[0].rows[1].textContent.includes("Cut to fit: 96.0 KiB of 240.2 KiB is sent.") && table.tBodies[0].rows[1].textContent.includes("3 values that look like a token are replaced with [redacted].")
      && table.tBodies[0].rows[1].textContent.includes("Left out, content not sent (4 files):") && table.tBodies[0].rows[2].textContent === "something newjira1 byte" && table.tBodies[0].rows[3].textContent === "your hintyou13 bytes","the parts are wrong: " + table.textContent.slice(0,500));
    const excluded = [...table.querySelectorAll(".ai-draft-excluded > li")];
    check(excluded.length === 4 && excluded[0].textContent === ".envlooks like a secret file" && excluded[1].textContent === "go.sumgenerated file" && excluded[2].querySelector("code").textContent === "docs/" + hostile && excluded[2].textContent.endsWith("binary file")
      && excluded[3].querySelector(".ai-mark").textContent === "<U+202E>" && excluded[3].querySelector(".ai-mark").title.includes("right-to-left override") && inert(),"the files left out are wrong: " + excluded.map(item => item.textContent).join(" | "));
    check(list.querySelector(".ai-draft-targets").textContent.includes("1 item") && list.querySelector(".ai-draft-target-list code").textContent.startsWith("src/a.go:12, <img") && list.querySelector(".ai-draft-own").textContent === "keep it short"
      && list.querySelector(".ai-draft-never-list").children.length === 3 && !list.querySelector(".ai-draft-never").open && list.textContent.includes("ab".repeat(32)) && inert(),"the targets, the hint or the never-sent list are wrong");

    // Send answers only after it has stood still; Discard sends nothing and keeps the request.
    const send = byID("ai-draft-send"), discard = byID("ai-draft-discard");
    check(!send.hidden && send.textContent === "Send to Claude Agent" && send.getAttribute("aria-disabled") === "true" && send.classList.contains("secondary") && discard.classList.contains("secondary") && byID("ai-draft-confirm-time").textContent.startsWith("Send within 4 min"),"Send is not offered as the list says: " + send.textContent);
    send.click();
    check(byID("ai-draft-confirm-status").textContent === "The button just appeared. Read the list, then choose again." && (await drafted("confirm")).length === 0 && !list.hidden,"Send answered before the reader could read");
    discard.click();
    check(list.hidden && !byID("ai-draft-compose").hidden && note() === "Discarded. Nothing was sent." && byID("ai-draft-hint").value === "  keep it short " && document.activeElement === byID("ai-draft-compose-title") && (await drafted("confirm")).length === 0,"Discard sent something or lost the request");

    // A list the page cannot show in full cannot be sent from here.
    await answer("draft",200,{confirm:confirmID(2),manifest:manifest({parts:[{kind:"diff",label:"staged changes",source:"git"}]})});
    byID("ai-draft-prepare").click();
    await until(() => !list.hidden,"the damaged list did not show");
    check(send.hidden && !byID("ai-draft-confirm-blocked").hidden && byID("ai-draft-confirm-blocked").textContent.includes("cannot send this draft") && byID("ai-draft-confirm-blocked").textContent.includes("agentflow ai draft commit") && !discard.hidden,"a list the page cannot show in full can be sent");
    discard.click();
    await answer("draft",200,{confirm:"not a confirm id",manifest:manifest()});
    byID("ai-draft-prepare").click();
    await until(() => !list.hidden,"the list without a confirmation did not show");
    check(send.hidden && !byID("ai-draft-confirm-blocked").hidden,"a list without a confirmation can be sent");
    discard.click();

    // A manifest that changed is refused by the console: nothing is sent and the request stays.
    await answer("draft",200,{confirm:confirmID(3),manifest:manifest()});
    await answer("confirm",409,{code:"operation_changed",message:"What the draft would send changed since you saw it, so nothing was sent.",fix:"Request the draft again and read the new manifest."});
    byID("ai-draft-prepare").click();
    await until(armed,"Send did not become ready");
    send.click();
    await until(async () => (await drafted("confirm")).length === 1 && list.hidden,"the refused Send did not come back");
    check(note().includes("changed since you saw it") && note().includes("Request the draft again") && byID("ai-draft-status").dataset.state === "bad" && !byID("ai-draft-compose").hidden && byID("ai-draft-hint").value === "  keep it short ","a refused Send is not explained: " + note());
    check((await drafted("confirm"))[0].body === JSON.stringify({confirm:confirmID(3)}),"the confirm request is wrong");

    // Send: the confirmation goes to the console, and the draft arrives over the stream.
    await answer("draft",200,{confirm:confirmID(4),manifest:manifest()});
    await answer("confirm",202,{});
    byID("ai-draft-prepare").click();
    await until(armed,"Send did not become ready again");
    send.click();
    await until(async () => (await drafted("confirm")).length === 2 && list.hidden,"Send was not sent");
    request = (await drafted("confirm"))[1];
    check(request.body === JSON.stringify({confirm:confirmID(4)}) && request.console === "console" && request.authorized === "true","the confirm request is wrong: " + JSON.stringify(request));
    check(note() === "Sent to Claude Agent. The draft appears under Drafts when it starts." && byID("ai-draft-compose").hidden && document.activeElement === byID("ai-draft-title") && byID("ai-draft-results").hidden && open("commit").getAttribute("aria-expanded") === "false","the page does not say that the draft was sent: " + note());

    // The draft the page sent arrives, is selected and can be cancelled.
    await post("emit",delta(3,"ai.session.started",[entry(state([record(1,"commit","starting",{created_at:at(0)})]))]));
    await until(() => rows().length === 1,"the draft did not arrive");
    check(row(1).getAttribute("aria-current") === "true" && byID("ai-draft-item-name").textContent === "Commit message" && byID("ai-draft-item-state").textContent === "Starting" && note() === "" && byID("ai-draft-empty").hidden
      && document.activeElement === byID("ai-draft-item-title") && row(1).textContent.includes("Requested by you"),"the new draft is wrong: " + byID("ai-draft-item").textContent.slice(0,200));
    await until(() => said().includes("Commit message draft started"),"the new draft was not announced: " + said());
    await post("emit",delta(4,"ai.session.updated",[entry(state([record(1,"commit","running",{created_at:at(0),denied:2})]))]));
    await until(() => byID("ai-draft-item-state").textContent === "Writing","the draft did not start writing");
    check(byID("ai-draft-item-detail").textContent.includes("Nothing is applied") && body().querySelector(".ai-draft-denied").textContent.startsWith("The harness asked 2 times") && body().querySelector(".ai-draft-sent").textContent.includes("What was sent: 4 parts")
      && !body().querySelector(".ai-draft-candidates") && !body().querySelector(".ai-draft-again"),"the running draft is wrong: " + body().textContent.slice(0,300));
    const stop = body().querySelector('button[data-action="cancel"]');
    check(stop && stop.textContent === "Cancel draft","a running draft cannot be cancelled");
    stop.focus();
    stop.click();
    await until(async () => (await others()).length === 1,"the cancel was not sent");
    request = (await others())[0];
    check(request.route === "cancel" && request.body === JSON.stringify({session:id(1)}) && request.console === "console","the cancel request is wrong: " + JSON.stringify(request));
    await until(() => body().querySelector(".ai-cancel-status").textContent === "Cancel sent. The draft stops within a few seconds.","the page does not say that the cancel was sent");
    check(!body().querySelector('button[data-action="cancel"]') && document.activeElement === byID("ai-draft-item-title"),"Cancel can be sent twice, or focus was lost");
    await post("emit",delta(5,"ai.session.updated",[entry(state([record(1,"commit","cancelling",{created_at:at(0)})]))]));
    await until(() => byID("ai-draft-item-state").textContent === "Cancelling","the draft did not show that it stops");
    check(!body().querySelector('button[data-action="cancel"]'),"a draft that stops can be cancelled again");
    await post("emit",delta(6,"ai.session.ended",[entry(state([record(1,"commit","cancelled",{created_at:at(0),ended_at:at(0),code:"cancelled"})]))]));
    await until(() => byID("ai-draft-item-state").textContent === "Cancelled","the cancelled draft did not show");
    check(byID("ai-draft-item-detail").textContent.includes("Nothing was applied") && body().querySelector(".ai-draft-again"),"the cancelled draft is wrong");
    await until(() => said().includes("Commit message draft: cancelled"),"the cancelled draft was not announced: " + said());

    // A finished draft: the model's words as text in their own box, and the command to copy. Nothing applies it.
    await post("emit",delta(7,"ai.session.started",[entry(state([record(2,"commit","running",{created_at:at(0)}),record(1,"commit","cancelled",{code:"cancelled"})]))]));
    await until(() => rows().length === 2,"the second draft did not arrive");
    check(row(1).getAttribute("aria-current") === "true" && !row(2).hasAttribute("aria-current"),"an update changed the reader's draft");
    row(2).click();
    check(row(2).getAttribute("aria-current") === "true" && byID("ai-draft-item-state").textContent === "Writing" && document.activeElement === byID("ai-draft-item-title"),"another draft cannot be chosen");
    await post("emit",delta(8,"ai.session.ended",[entry(state([ready(2),record(1,"commit","cancelled",{code:"cancelled"})]))]));
    await until(() => byID("ai-draft-item-state").textContent === "Ready to read","the finished draft did not show");
    const cards = [...body().querySelectorAll(".ai-draft-candidate")];
    check(cards.length === 3 && body().querySelector(".ai-draft-part-title").textContent === "3 candidates" && inert(),"the candidates are wrong");
    await until(() => said().includes("Commit message draft: ready to read, 3 candidates"),"the finished draft was not announced: " + said());
    const value = cards[0].querySelector(".ai-draft-value"), command = cards[0].querySelector(".ai-draft-command code");
    check(cards[0].querySelector("h5").textContent === "1. Commit message" && value.textContent === message && value.tabIndex === 0 && value.closest(".ai-said") && value.children.length === 0
      && cards[0].querySelector(".ai-said-label").textContent.startsWith("Written by the model.") && cards[0].querySelector(".ai-draft-warnings").textContent.includes("has a link"),"the model's words are not shown as its own text: " + cards[0].textContent.slice(0,300));
    check(command.textContent === "agentflow commit --prepare -m '" + message.replaceAll("'","'\\''") + "'" && command.children.length === 0 && !command.closest(".ai-said")
      && [...cards[0].querySelectorAll("button")].map(node => node.textContent).join() === "Copy text,Copy command" && cards[0].textContent.includes("This page runs nothing."),"the command to copy is wrong: " + command.textContent);
    check(cards[1].querySelector(".ai-draft-about code").textContent.startsWith("b.go:3, bob") && cards[1].querySelector(".ai-draft-about .ai-mark").textContent === "<U+200B>" && cards[1].textContent.includes("Agentflow took this text out.") && cards[1].querySelector(".ai-draft-issues").textContent.includes("looks like it holds a secret")
      && !cards[1].querySelector(".ai-draft-command") && cards[1].querySelectorAll("button").length === 0,"a candidate with an issue is offered: " + cards[1].textContent.slice(0,300));
    check(cards[2].querySelector("h5").textContent === "3. Candidate" && !cards[2].querySelector(".ai-draft-command") && cards[2].querySelectorAll("button").length === 0 && cards[2].querySelector(".ai-draft-value").textContent === "hidden � text"
      && cards[2].textContent.includes("cannot show the whole command") && cards[2].textContent.includes("agentflow ai status " + id(2)) && cards[2].textContent.includes("not offered to copy"),"text with a hidden character is offered to copy: " + cards[2].textContent.slice(0,300));
    check(!body().querySelector(".ai-draft-again") && ![...body().querySelectorAll("button")].some(node => /apply|commit|use it|run/i.test(node.textContent)) && inert(),"the page offers to apply a draft");

    // The other states: not usable, failed with the model's answer, failed with a code, without its list, and one this console does not know.
    const invalid = record(3,"ac","invalid",{started_by:"agent",ended_at:at(0),warnings:[{code:"worktree_changed",message:"The worktree changed while the draft ran: " + hostile}],draft:{session:id(3),feature:"ac",state:"invalid",candidates:[],issues:[{code:"not_met",message:"Criterion 2 is not met."},{code:"future_code"}],answer:"{\"checks\": []} " + hostile,finished:at(0)}});
    const failed = record(4,"pr","failed",{code:"unparsable",ended_at:at(0),draft:{session:id(4),feature:"pr",state:"failed",code:"unparsable",candidates:[],answer:"Sure! Here is your PR:\n" + hostile,finished:at(0)}});
    const signin = record(5,"reply","failed",{code:"auth_required",manifest:null,ended_at:at(0)});
    const future = record(6,"commit","future_state",{code:"new_code"});
    await post("emit",delta(9,"ai.draft.finished",[entry(state([invalid,failed,signin,future,ready(2)]))]));
    await until(() => rows().length === 5,"the other drafts did not arrive");
    check(row(2).getAttribute("aria-current") === "true" && byID("ai-draft-item-state").textContent === "Ready to read","new drafts changed the reader's draft");
    row(3).click();
    check(byID("ai-draft-item-state").textContent === "Not usable" && row(3).textContent.includes("Requested by an agent") && body().querySelector(".ai-draft-warnings").textContent.includes("The worktree changed while the draft ran: <img") && body().querySelector(".ai-draft-issues").textContent === "Criterion 2 is not met.Reason code future_code."
      && body().querySelector(".ai-draft-value").textContent.startsWith("{\"checks\": []} <img") && body().textContent.includes("Agentflow could not use it.") && body().textContent.includes("The draft has no candidate.") && !body().querySelector(".ai-draft-again") && inert(),"the draft that is not usable is wrong: " + body().textContent.slice(0,400));
    row(4).click();
    check(byID("ai-draft-item-state").textContent === "Failed" && byID("ai-draft-item-detail").textContent.includes("not answer with the one JSON object") && body().querySelector(".ai-draft-value").textContent === "Sure! Here is your PR:\n" + hostile && body().querySelector(".ai-draft-again") && inert(),"the failed draft is wrong: " + body().textContent.slice(0,300));
    row(5).click();
    check(byID("ai-draft-item-detail").textContent.includes("needs a sign-in") && body().querySelector(".ai-draft-sent").textContent.includes("did not keep the list") && body().querySelector(".ai-draft-again").textContent === "Request a new draft","the draft without its list is wrong: " + body().textContent.slice(0,300));
    row(6).click();
    check(byID("ai-draft-item-state").textContent === "Unknown state" && byID("ai-draft-item-detail").textContent.includes("Reason code new_code.") && !body().querySelector('button[data-action="cancel"]'),"an unknown state is not shown safely");

    // Request a new draft opens the request of that feature. A ticket needs its description; a thread ID is checked.
    row(5).click();
    body().querySelector(".ai-draft-again").click();
    check(!byID("ai-draft-compose").hidden && byID("ai-draft-compose-title").textContent === "Request a draft: Review replies" && document.activeElement === byID("ai-draft-compose-title") && byID("ai-draft-thread") && byID("ai-draft-hint").value === "","a new draft cannot be requested from a failed one");
    byID("ai-draft-thread").value = "T 1; rm";
    byID("ai-draft-prepare").click();
    check(note() === "This does not look like a thread ID." && document.activeElement === byID("ai-draft-thread") && (await drafted("draft")).length === 7,"a thread ID that is not one was sent: " + (await drafted("draft")).length);
    byID("ai-draft-thread").value = "PRRT_kwDOabc123";
    await answer("draft",202,{});
    byID("ai-draft-prepare").click();
    await until(async () => (await drafted("draft")).length === 8,"the reply request was not sent");
    check((await drafted("draft"))[7].body === JSON.stringify({feature:"reply",input:{thread:"PRRT_kwDOabc123"}}),"the reply request is wrong: " + (await drafted("draft"))[7].body);
    // The console started it without a question: the reader's autonomy settings allow that, and the page says so.
    await until(() => note().includes("started without a question"),"a draft that started at once is not explained: " + note());
    check(byID("ai-draft-compose").hidden && list.hidden,"the request stayed open after the draft started");
    features[2] = feature("ticket");
    await post("emit",delta(10,"ai.session.started",[entry(state([record(7,"reply","running",{created_at:at(0)}),invalid,failed,signin,future,ready(2)]))]));
    await until(() => rows().length === 6 && open("ticket"),"the reply draft or the ticket feature did not arrive");
    check(row(7).getAttribute("aria-current") === "true" && note() === "","the draft that started at once was not chosen");
    open("ticket").click();
    check(byID("ai-draft-text") && byID("ai-draft-text-count").textContent === "0 of 16,384 bytes" && !byID("ai-draft-thread"),"a ticket does not ask for its description");
    byID("ai-draft-prepare").click();
    check(note() === "Write a description first." && document.activeElement === byID("ai-draft-text") && (await drafted("draft")).length === 8,"a ticket without a description was sent");
    byID("ai-draft-text").value = "The page needs a dark theme.\n<b>bold</b>";
    await answer("draft",200,{confirm:confirmID(5),manifest:manifest({feature:"ticket",text:"The page needs a dark theme.\n<b>bold</b>"})});
    byID("ai-draft-prepare").click();
    await until(() => !list.hidden,"the ticket's list did not show");
    check((await drafted("draft"))[8].body === JSON.stringify({feature:"ticket",input:{text:"The page needs a dark theme.\n<b>bold</b>"}}) && byID("ai-draft-confirm-lead").textContent.includes("a ticket draft")
      && [...list.querySelectorAll(".ai-draft-own")].some(node => node.textContent === "The page needs a dark theme.\n<b>bold</b>") && inert(),"the ticket request is wrong: " + (await drafted("draft"))[8].body);

    // A feature that goes away closes its request; the list the reader decides on stays until they decide.
    // Offline: the list and a running draft say that the page may be behind.
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Offline" && !byID("ai-draft-confirm-offline").hidden,"the list does not say that live updates are offline");
    row(7).click();
    check(body().querySelector(".ai-ask-offline").textContent.includes("may have moved on") && rows().length === 6 && !list.hidden,"the offline page dropped its values");
    await post("status?code=0");
    await post("emit",snapshot(20,[entry(state([record(7,"reply","running",{created_at:at(0)}),invalid,failed,signin,future,ready(2)]))]));
    await until(() => label() === "Live" && byID("ai-draft-confirm-offline").hidden,"the stream did not come back");
    check(!body().querySelector(".ai-ask-offline"),"the draft still says that it is offline");
    discard.click();
    byID("ai-draft-close").click();
    check(byID("ai-draft-compose").hidden && document.activeElement === open("ticket") && open("ticket").getAttribute("aria-expanded") === "false","Close did not return to the feature");

    // A draft finishes while the reader is on another page: it is announced, because this page sent it.
    location.hash = "#overview";
    await until(() => !byID("page-overview").hidden && page.hidden,"the overview did not reopen");
    const done = record(7,"reply","ready",{created_at:at(0),ended_at:at(0),draft:{session:id(7),feature:"reply",state:"ready",candidates:[{operation:"review.reply",cli:["agentflow","review","reply","T1","ok"],fields:[{name:"reply",label:"Reply",value:"ok"}]}],finished:at(0)}});
    await post("emit",delta(21,"ai.session.ended",[entry(state([done,invalid,failed,signin,future,ready(2)]))]));
    await until(() => said().includes("Review replies draft: ready to read, 1 candidate. Read it on the AI sessions page."),"a finished draft was not announced on another page: " + said());

    // The page sent only the three requests, and nothing made the model's or the repository's text an element.
    check((await drafted()).every(item => item.route === "draft" || item.route === "confirm") && (await drafted("confirm")).length === 2 && (await others()).length === 1 && (await others())[0].route === "cancel","the page sent another request");
    check(inert(),"untrusted text became part of the page");
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
