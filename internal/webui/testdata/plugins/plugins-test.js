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
  const requests = async () => (await fetch("/fixture/stream/requests")).json();
  const repo = "3f2a9c1b7d4e5f60";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const frame = (event, seq, data) => "event: " + event + "\n" + (seq ? "id: v1:" + repo + "=" + seq + "\n" : "") + "data: " + JSON.stringify(data) + "\n\n";
  const entry = (projection, state) => ({projection,repo,state});
  const overview = {projection:"overview",repo,worktree:"first",state:{branch:"main",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]}}};
  const snapshot = (seq, states) => frame("snapshot",seq,{cursor:"v1:" + repo + "=" + seq,states:[overview,...states]});
  const delta = (seq, type, states) => frame("delta",seq,{event:{id:"e" + seq,type,repo:"global",seq},states});

  // PluginsState from projection_plugins_state.go, as lane 5.2's projection
  // will send it.
  const hash = letter => "sha256:" + letter.repeat(64);
  const plugin = (id, extra = {}) => ({id,name:id,version:"0.1.0",tier:"declarative",hash:hash("a"),installed_at:at(-86400000),enabled:true,state:"running",since:at(-3600000),
    delivered:0,dead_letters:0,dropped:0,invocations:0,published:0,extensions:[],permissions:{subscribe:[],publish:[],invoke:[]},settings_group:"plugins." + id,...extra});
  const notifier = plugin("ci-notify",{name:"CI failure notifier",description:"Sends a notification when a check fails.",delivered:124,invocations:3,
    extensions:[{point:"handler",name:"ci-failed",on:["gh.ci.updated"],invokes:["notify"]}],permissions:{subscribe:["gh.ci.updated"],publish:[],invoke:["notify"]}});
  const annotator = (extra = {}) => plugin("timeline-notes",{name:"Timeline annotator",version:"0.3.2",tier:"process",hash:hash("b"),state:"backoff",code:"exited",restarts:3,retry_at:at(90000),since:at(-20000),
    delivered:58,dead_letters:2,published:41,extensions:[{point:"handler",name:"annotate",on:["op.applied","op.failed"],annotates:true}],
    permissions:{subscribe:["op.applied","op.failed"],publish:["plugin.annotation.added"],invoke:[]},...extra});
  const freeze = plugin("friday-freeze",{name:"Friday freeze",version:"1.0.0",enabled:false,state:"installed",since:undefined,
    extensions:[{point:"permission_rule",name:"no-friday-push",deny:"No pushes on Fridays."}]});
  const checker = plugin("release-check",{name:"Release checker",tier:"process",state:"failed",code:"hash_mismatch",disabled_here:true,dead_letters:1,dropped:4});
  const letter = (id, event, code, ms, type = "op.applied") => ({plugin:id,event_id:event,event_type:type,event_repo:repo,code,time:at(ms)});
  const letters = [letter("timeline-notes","ev-2","exited",-20000),letter("timeline-notes","ev-1","timeout",-340000,"op.failed"),letter("release-check","ev-0","rpc_error",-5400000)];
  const plugins = (list, extra = {}) => ({host:true,plugins:list,dead_letters:letters,...extra});
  const base = () => [notifier,freeze,checker,annotator()];

  const cards = () => [...byID("plugins-list").children];
  const keys = () => cards().map(node => node.dataset.key).join();
  const card = key => cards().find(node => node.dataset.key === key);
  const fact = (key, name) => card(key)?.querySelector('[data-fact="' + name + '"]');
  const shows = (key, name) => { const node = fact(key,name); return node && !node.hidden ? node.textContent : ""; };
  const said = () => byID("live-announcer").textContent;
  const label = () => byID("stream-label").textContent;
  const visible = node => getComputedStyle(node).display !== "none";
  const current = () => [...document.querySelectorAll(".rail nav a[aria-current=page]")].map(node => node.dataset.page).join();
  const resources = () => performance.getEntriesByType("resource").filter(item => !item.name.includes("/fixture/") && !item.name.endsWith("/result")).length;

  let result = "passed";
  try {
    // The reader is defensive, and codes become fixed sentences.
    const P = AgentflowPlugins;
    check(P.pluginsState(null) === null && P.pluginsState("x") === null && P.pluginsState({}).plugins.length === 0 && P.pluginsState({}).host === false,"a missing state is not read safely");
    const sparse = P.pluginsState({host:"yes",plugins:[{id:"a",state:"future",tier:"wasm",hash:"md5:1",extensions:[{point:"widget",name:"w"},{point:"handler",name:"h",on:["ok.event","<b>"],invokes:"all"},null],permissions:"all",restarts:-2,retry_at:"never",delivered:"9"},{id:"a"},{name:"no id"},7],
      dead_letters:[{plugin:"a",event_id:"e",event_type:"<i>",code:"x",time:"never"},{plugin:"a",event_id:"e",time:"never"},{event_id:"f"},null]});
    const odd = sparse.plugins[0];
    check(sparse.plugins.length === 1 && sparse.host === false && odd.state === "unknown" && odd.tier === "unknown" && odd.hash === "" && odd.name === "a" && odd.extensions.length === 1 && odd.extensions[0].on.join() === "ok.event" && odd.extensions[0].invokes.length === 0
      && odd.permissions.subscribe.length === 0 && odd.restarts === 0 && odd.retry === 0 && odd.delivered === 0 && odd.enabled === false && odd.valid === false,"a damaged plugin is not read safely");
    check(sparse.letters.length === 1 && sparse.letters[0].type === "" && sparse.letters[0].at === 0,"damaged letters are not read safely");
    check(P.codeShort("exited") === "its program exited" && P.codeShort("new_code") === "reason code new_code" && P.codeShort("<b>x</b>") === "the reason is not readable" && P.codeShort("enabled") === "" && P.letterShort("rpc_error") === "the plugin returned an error" && P.letterShort("<b>") === "unknown reason","codes are not readable text");
    check(P.remaining(65000) === "1 min 05 s" && P.remaining(9200) === "10 s" && P.remaining(-5) === "0 s","the countdown text is wrong");

    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"overview"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-overview").hidden,"the overview did not open");
    check(byID("page-plugins").hidden && current() === "overview" && visible(byID("page-overview")),"the Plugins page shows before it is opened");

    // The rail entry opens the page; the other pages step aside.
    const navigation = document.querySelector('.rail nav a[data-page="plugins"]');
    check(navigation.getAttribute("href") === "#plugins" && navigation.textContent.trim() === "Plugins","the rail has no Plugins entry");
    navigation.click();
    await until(() => !byID("page-plugins").hidden && current() === "plugins","the Plugins page did not open");
    check(!visible(byID("page-overview")) && !visible(byID("page-settings")) && visible(byID("page-plugins")) && document.activeElement === byID("main"),"another page shows beside Plugins, or focus did not move to the page");

    // Before the first snapshot the page says it waits; the warning is always there.
    check(!byID("plugins-wait").hidden && byID("plugins-body").hidden && byID("plugins-note").hidden,"the page does not show that it waits for the stream");
    check(visible(document.querySelector(".pg-trust")) && document.querySelector(".pg-trust").textContent.includes("A plugin runs as you and is not sandboxed."),"the page does not say that a plugin runs as you, unsandboxed");

    // The first snapshot fills the page without highlights or announcements.
    await post("emit",snapshot(1,[entry("plugins",plugins(base()))]));
    await until(() => label() === "Live" && !byID("plugins-body").hidden,"the snapshot did not fill the page");
    check(byID("plugins-wait").hidden && byID("plugins-note").hidden && byID("plugins-host").hidden && !document.querySelector("#page-plugins .live-changed") && !said(),"the first state was highlighted, announced or marked stale");
    check(keys() === "ci-notify,friday-freeze,release-check,timeline-notes","the cards are not in the server's order: " + keys());
    check(byID("plugins-summary").textContent === "4 plugins installed · 1 running · 1 turned off · 2 need attention · 3 failed deliveries since the daemon started.","the summary is wrong: " + byID("plugins-summary").textContent);
    check(byID("plugins-nav-count").textContent === "2" && !byID("plugins-nav-count").hidden && byID("plugins-nav-label").textContent === ", 2 plugins need attention","the rail does not count the plugins that need attention");
    check(byID("plugins-add-title").textContent === "Add a plugin" && byID("plugins-add-lead").hidden && byID("plugins-orphans").hidden,"the add panel or the orphan list is wrong");

    // The stream carries the state: the plugins state has no topic of its own, and the page sends no request.
    const topics = new URLSearchParams((await requests())[0].query).getAll("topic");
    check((await requests()).length === 1 && topics.filter(topic => topic.includes("plugin")).join() === "*/*/plugin.annotation.added","the page opened a stream, or the stream's plugin topics are not the annotation topic alone: " + topics.join());

    // A running declarative plugin: name, version, tier, state, what it adds, what it may do, counters.
    check(shows("ci-notify","name") === "CI failure notifier" && fact("ci-notify","name").dataset.state === "running" && fact("ci-notify","name").querySelector(".state-mark").getAttribute("aria-hidden") === "true","the plugin is not named with a state mark");
    check(shows("ci-notify","status") === "Running" && fact("ci-notify","status").dataset.state === "good" && shows("ci-notify","version") === "Version 0.1.0" && shows("ci-notify","tier") === "Declarative" && fact("ci-notify","tier").title.includes("no code of its own"),"state, version or tier is wrong");
    check(card("ci-notify").querySelector(".pg-facts code").textContent === "ci-notify" && shows("ci-notify","since").startsWith("Running since ") && fact("ci-notify","since").dateTime && shows("ci-notify","description") === "Sends a notification when a check fails." && shows("ci-notify","detail") === "","ID, since, description or detail is wrong");
    check(shows("ci-notify","enabled") === "Enabled" && shows("ci-notify","here") === "","enabled is wrong");
    const adds = card("ci-notify").querySelector(".pg-extensions");
    check(adds.textContent.includes("Handler") && adds.textContent.includes("ci-failed") && adds.textContent.includes("Reacts to gh.ci.updated") && adds.textContent.includes("Runs notify"),"the handler is not described: " + adds.textContent);
    const may = [...card("ci-notify").querySelectorAll(".pg-permissions > div")].map(node => node.textContent).join("|");
    check(may === "Receive eventsgh.ci.updated|Publish eventsNone|Run commandsnotify","the permissions are wrong: " + may);
    check(shows("ci-notify","delivered") === "124" && shows("ci-notify","dead") === "0" && shows("ci-notify","dropped") === "0" && shows("ci-notify","invocations") === "3" && shows("ci-notify","published") === "0" && card("ci-notify").querySelector("[data-part=letters]").hidden,"the counters are wrong, or an empty failure list shows");
    check(shows("ci-notify","hash") === hash("a") && shows("ci-notify","installed") !== "Not recorded","the hash or install time is missing");

    // Enable is a Settings change: the control is a link to the plugin's group. Nothing installs or removes from here.
    const enable = card("friday-freeze").querySelector("[data-part=settings]");
    check(enable.tagName === "A" && enable.getAttribute("href") === "#settings?group=plugins.friday-freeze" && enable.textContent === "Enable in Settings" && enable.getAttribute("aria-label") === "Enable in Settings: Friday freeze","Enable does not lead to the Settings group");
    check(card("ci-notify").querySelector("[data-part=settings]").textContent === "Open its settings" && card("ci-notify").querySelector("[data-part=settings]").getAttribute("href") === "#settings?group=plugins.ci-notify","an enabled plugin has no settings link");
    check(shows("friday-freeze","status") === "Not enabled" && shows("friday-freeze","enabled") === "Turned off" && shows("friday-freeze","detail").startsWith("Installed and turned off.") && shows("friday-freeze","since") === "","an installed plugin is not described");
    const rule = card("friday-freeze").querySelector(".pg-extensions");
    check(rule.textContent.includes("Permission rule") && rule.querySelector("q").textContent === "No pushes on Fridays." && rule.textContent.includes("A rule can only deny."),"the permission rule is not described");
    const buttons = [...byID("page-plugins").querySelectorAll("button")].map(node => node.textContent);
    check(buttons.length === 5 && !byID("page-plugins").querySelector("[data-part=approvals]:not([hidden])") && buttons.every(text => text === "Copy") && !byID("page-plugins").querySelector("form, input, select"),"the page has a control other than Copy: " + buttons.join());

    // A failed plugin says why in words, and that this repository turned it off.
    check(shows("release-check","status") === "Failed" && fact("release-check","status").dataset.state === "bad" && shows("release-check","detail").startsWith("Its installed files changed after you installed it") && fact("release-check","detail").dataset.tone === "bad","the failure is not explained: " + shows("release-check","detail"));
    check(shows("release-check","here") === "Turned off in this repository" && fact("release-check","dead").dataset.state === "bad" && fact("release-check","dropped").dataset.state === "warn" && shows("release-check","dropped") === "4","turned off here, or the failure counters, are wrong");

    // A restarting plugin counts down to its retry time; its dead letters are listed newest first.
    check(shows("timeline-notes","status") === "Restarting" && fact("timeline-notes","status").dataset.state === "warn" && shows("timeline-notes","tier") === "Process","the restarting plugin is wrong");
    check(/^Its program exited\. Restarts in 1 min \d\d s \(at .+\)\. Restart 3 of 5 in 10 minutes\.$/.test(shows("timeline-notes","detail")),"the restart is not explained: " + shows("timeline-notes","detail"));
    const before = shows("timeline-notes","detail");
    await until(() => shows("timeline-notes","detail") !== before,"the countdown does not tick");
    check(!fact("timeline-notes","detail").classList.contains("live-changed") && !said(),"a tick was highlighted or announced");
    const failures = card("timeline-notes").querySelector("[data-part=letters]");
    check(!failures.hidden && shows("timeline-notes","letters-title") === "Recent failed deliveries (2)" && shows("timeline-notes","dead") === "2","the dead letters are not counted");
    const rows = [...failures.querySelectorAll(".pg-letter-list > li")];
    check(rows.length === 2 && rows[0].textContent.includes("op.applied") && rows[0].textContent.includes("the plugin exited") && rows[0].textContent.includes("ev-2") && rows[0].querySelector("time").dateTime && rows[1].textContent.includes("op.failed") && rows[1].textContent.includes("timed out"),"the dead letters are wrong: " + rows.map(node => node.textContent).join("|"));

    // Install and remove are commands to copy, never buttons that act.
    let copied = "";
    Object.defineProperty(navigator,"clipboard",{configurable:true,value:{writeText:async value => { copied = value; }}});
    const install = byID("page-plugins").querySelector(".pg-add .pg-command");
    check(install.querySelector("code").textContent === "agentflow plugin add PATH" && install.querySelector("button").getAttribute("aria-label") === "Copy the command: agentflow plugin add PATH","the install command is missing");
    install.querySelector("button").click();
    await until(() => copied === "agentflow plugin add PATH" && said() === "Install command copied." && install.querySelector("button").textContent === "Copied","the install command was not copied: " + said());
    const remove = card("ci-notify").querySelector("[data-part=remove]");
    check(remove.querySelector("code").textContent === "agentflow plugin remove ci-notify","the remove command is missing");
    card("ci-notify").querySelector(".pg-about").open = true;
    remove.querySelector("button").click();
    await until(() => copied === "agentflow plugin remove ci-notify" && said() === "Remove command copied.","the remove command was not copied");
    Object.defineProperty(navigator,"clipboard",{configurable:true,value:{writeText:async () => { throw new Error("denied"); }}});
    remove.querySelector("button").click();
    await until(() => said().startsWith("The browser did not allow copying."),"a refused copy is not explained");

    // A plugin's write waits for the person: the card and the Approvals panel show the command to copy, never an Approve button.
    const opID = "op_0123456789abcdef0123456789abcdef";
    const approval = (id, extra = {}) => ({operation_id:id,action:"notify",class:"remote",client:"",policy_source:"plugin ceiling",issued_at:at(-5000),expires_at:at(290000),state:"pending",...extra});
    const asked = approval(opID,{requested_by:"plugin:timeline-notes"});
    const agent = approval("op_ffffffffffffffffffffffffffffffff",{client:"claude-code",action:"push"});
    check(P.pluginApprovals({open:[asked,agent,approval("op_1",{requested_by:"plugin:timeline-notes"}),approval(opID,{requested_by:"plugin:timeline-notes"}),approval("op_" + "a".repeat(32),{requested_by:"plugin:Bad ID"}),approval("op_" + "b".repeat(32),{requested_by:"plugin:ci-notify",state:"approved"}),approval("op_" + "c".repeat(32) + "; rm -rf ~",{requested_by:"plugin:ci-notify"}),null]}).map(item => item.plugin + " " + item.id).join() === "timeline-notes " + opID
      && P.pluginApprovals(null).length === 0 && P.pluginApprovals({open:[agent]}).length === 0 && P.approveCommand("op_1; rm") === "" && P.requester("claude-code") === "" && P.requester(undefined) === "","plugin approvals are not read safely");
    Object.defineProperty(navigator,"clipboard",{configurable:true,value:{writeText:async value => { copied = value; }}});
    await post("emit",delta(20,"approval.requested",[entry("approvals",{open:[agent],recent:[]})]));
    await until(() => byID("approval-open").children.length === 1,"the agent's approval did not arrive");
    check(!byID("page-plugins").querySelector("[data-part=approvals]:not([hidden])") && byID("approval-open").querySelector("[data-part=plugin]").hidden && !byID("approvals-panel").querySelector("button"),"an approval without requested_by shows a plugin command");
    await post("emit",delta(21,"approval.requested",[entry("approvals",{open:[asked,agent],recent:[]})]));
    const asks = card("timeline-notes").querySelector("[data-part=approvals]");
    await until(() => !asks.hidden,"the plugin's approval did not reach its card");
    check(asks.querySelector("code").textContent === "agentflow plugin approve " + opID && asks.querySelector("strong").textContent === "Notify" && asks.querySelector("time").dateTime && asks.textContent.includes("only after you approve it in your own terminal") && card("ci-notify").querySelector("[data-part=approvals]").hidden,"the card does not show the approve command: " + asks.textContent);
    const approveButtons = [...asks.querySelectorAll("button")];
    check(approveButtons.length === 1 && approveButtons[0].textContent === "Copy" && approveButtons[0].getAttribute("aria-label") === "Copy the command: agentflow plugin approve " + opID,"the card has a control other than Copy for the approval");
    approveButtons[0].click();
    await until(() => copied === "agentflow plugin approve " + opID && said().includes("Approve command copied."),"the approve command was not copied: " + said());
    const panelRow = [...byID("approval-open").children].find(node => node.dataset.key === opID);
    const panelPart = panelRow.querySelector("[data-part=plugin]");
    check(!panelPart.hidden && panelPart.querySelector("code").textContent === "agentflow plugin approve " + opID && panelRow.querySelector("[data-fact=client]").textContent === "Asked by plugin timeline-notes","the Approvals panel does not show the approve command");
    const panelButtons = [...byID("approvals-panel").querySelectorAll("button")];
    check(panelButtons.length === 1 && panelButtons[0].textContent === "Copy" && [...byID("approval-open").children].find(node => node.dataset.key === agent.operation_id).querySelector("[data-part=plugin]").hidden,"the Approvals panel has an Approve button, or an agent's request shows the plugin command");
    // The request keeps its Copy button while it waits, and leaves when it is answered.
    approveButtons[0].focus();
    await post("emit",delta(22,"approval.requested",[entry("approvals",{open:[asked,agent,approval("op_" + "d".repeat(32),{requested_by:"plugin:ci-notify"})],recent:[]})]));
    await until(() => !card("ci-notify").querySelector("[data-part=approvals]").hidden,"a second plugin's approval did not arrive");
    check(document.activeElement === approveButtons[0],"an update rebuilt a waiting request");
    await post("emit",delta(23,"approval.resolved",[entry("approvals",{open:[agent],recent:[{...asked,state:"consumed"}]})]));
    await until(() => asks.hidden && card("ci-notify").querySelector("[data-part=approvals]").hidden,"an answered approval stays on the card");
    check(![...byID("approval-recent").children].some(node => !node.querySelector("[data-part=plugin]").hidden),"an answered approval still shows the command");
    await until(() => !document.querySelector("#page-plugins .live-changed"),"a highlight did not fade");

    // A live change: the card stays, focus stays, the changed values are highlighted and announced.
    const kept = card("timeline-notes"), untouched = card("ci-notify");
    const focus = kept.querySelector("[data-part=settings]");
    focus.focus();
    await post("emit",delta(2,"plugin.lifecycle.changed",[entry("plugins",plugins([notifier,freeze,checker,annotator({state:"running",code:"",retry_at:undefined,restarts:0,since:at(0)})]))]));
    await until(() => shows("timeline-notes","status") === "Running","the live state did not arrive");
    check(card("timeline-notes") === kept && document.activeElement === focus,"the card was rebuilt or lost focus");
    check(fact("timeline-notes","status").classList.contains("live-changed") && !kept.classList.contains("live-changed") && !untouched.querySelector(".live-changed") && shows("timeline-notes","detail") === "","the change is not highlighted, or an unchanged card is");
    await until(() => said() === "Plugin Timeline annotator is running.","the change was not announced: " + said());
    check(byID("plugins-nav-count").textContent === "1" && byID("plugins-summary").textContent.includes("2 running") && byID("plugins-summary").textContent.includes("1 needs attention"),"the counts did not follow");

    // A new failure: the counter and the letter arrive live; a new plugin is highlighted as a whole.
    const more = [letter("timeline-notes","ev-3","crashed",0),...letters];
    await post("emit",delta(3,"plugin.delivery.failed",[entry("plugins",plugins([notifier,plugin("fresh",{name:"Fresh plugin",enabled:false,state:"installed",since:undefined}),freeze,checker,annotator({state:"degraded",code:"ping",retry_at:undefined,dead_letters:3})],{dead_letters:more}))]));
    await until(() => shows("timeline-notes","dead") === "3" && Boolean(card("fresh")),"the failure did not arrive live");
    check(shows("timeline-notes","status") === "Degraded" && shows("timeline-notes","detail").startsWith("Running, but it missed health checks.") && fact("timeline-notes","dead").classList.contains("live-changed") && shows("timeline-notes","letters-title") === "Recent failed deliveries (3)","the degraded plugin is wrong: " + shows("timeline-notes","detail"));
    check(card("fresh").classList.contains("live-changed") && keys() === "ci-notify,fresh,friday-freeze,release-check,timeline-notes","the new plugin is not highlighted in its place");
    await until(() => said() === "Plugin Fresh plugin installed. Plugin Timeline annotator is degraded.","the changes were not announced: " + said());

    // The page waits for the stream; it never asks.
    const loaded = resources(), streams = (await requests()).length;
    await pause(1300);
    check(resources() === loaded && (await requests()).length === streams,"the Plugins page sent a request while it was open");

    // Enable opens the plugin's Settings group; the browser's Back returns.
    enable.click();
    await until(() => !byID("page-settings").hidden && visible(byID("page-settings")) && byID("page-plugins").hidden && current() === "settings","Enable did not open Settings");
    check(location.hash === "#settings?group=plugins.friday-freeze","the Settings group is not addressed: " + location.hash);
    const quiet = said();
    await post("emit",delta(4,"plugin.lifecycle.changed",[entry("plugins",plugins([notifier,freeze,checker,annotator({state:"failed",code:"crash_loop",retry_at:undefined,dead_letters:3})],{dead_letters:more}))]));
    await until(() => byID("plugins-nav-count").textContent === "2","the rail did not follow a change while another page is open");
    await pause(100);
    check(said() === quiet,"a plugin change was announced while the page is closed");
    location.hash = "#plugins";
    await until(() => !byID("page-plugins").hidden && current() === "plugins" && !visible(byID("page-settings")),"the page did not reopen");
    check(shows("timeline-notes","detail").startsWith("It restarted 5 times in 10 minutes, so Agentflow stopped it.") && !card("fresh"),"the crash loop is not explained, or a removed plugin stays");

    // A removed card hands focus to the page title.
    card("release-check").querySelector("[data-part=settings]").focus();
    await post("emit",delta(5,"plugin.lifecycle.changed",[entry("plugins",plugins([notifier,freeze,annotator({state:"failed",code:"crash_loop",retry_at:undefined,dead_letters:3})],{dead_letters:more}))]));
    await until(() => !card("release-check"),"the removed plugin stays");
    check(document.activeElement === byID("plugins-title"),"focus was dropped with the removed card");
    await until(() => said() === "Plugin Release checker removed.","the removal was not announced: " + said());
    check(!byID("plugins-orphans").hidden && byID("plugins-orphan-list").children.length === 1 && byID("plugins-orphan-list").textContent.includes("release-check"),"a removed plugin's dead letters are lost");

    // Plugin text is text. Names, descriptions, deny messages and codes never become markup.
    const hostile = plugin("evil",{name:"<img src=x onerror=alert(1)>",description:"<script>alert(2)</script> <a href=\"javascript:alert(3)\">click</a>",code:"<b>bold</b>",state:"failed",
      extensions:[{point:"permission_rule",name:"<i>rule</i>",deny:"<img src=y onerror=alert(4)>"},{point:"handler",name:"h",on:["<svg onload=alert(5)>","op.applied"],invokes:["notify","rm -rf /"]}],
      permissions:{subscribe:["<b>"],publish:[],invoke:["notify"]},settings_group:"javascript:alert(6)"});
    const strange = plugin("Not/An-ID",{name:"Strange",settings_group:"plugins.other"});
    await post("emit",delta(6,"plugin.lifecycle.changed",[entry("plugins",plugins([hostile,strange],{dead_letters:[letter("evil","<u>id</u>","<b>code</b>",0,"<em>type</em>")]}))]));
    await until(() => Boolean(card("evil")),"the hostile plugin did not arrive");
    const evil = card("evil");
    check(!evil.querySelector("img, script, i, b, u, em, svg, a[href^='javascript']") && shows("evil","name") === "<img src=x onerror=alert(1)>" && shows("evil","description").startsWith("<script>alert(2)</script>") && evil.querySelector("q").textContent === "<img src=y onerror=alert(4)>","plugin text became markup");
    check(shows("evil","detail").startsWith("The reason is not readable.") && !evil.textContent.includes("<svg") && !evil.textContent.includes("rm -rf") && evil.querySelector("[data-part=settings]").getAttribute("href") === "#settings?group=plugins.evil" && evil.querySelector(".pg-letter-list").textContent.includes("unknown event"),"an unsafe code, pattern, command or settings address is shown");
    const lost = card("Not/An-ID");
    check(lost.querySelector("[data-part=settings]").hidden && shows("Not/An-ID","no-settings").includes("not valid") && !lost.querySelector("[data-part=remove] button"),"a plugin with an invalid ID offers a Settings link or a remove command");

    // No plugins: the page says so and shows how to add one.
    await post("emit",delta(7,"plugin.lifecycle.changed",[entry("plugins",{host:true,plugins:[],dead_letters:[]})]));
    await until(() => byID("plugins-list").hidden && byID("plugins-add-title").textContent === "No plugins installed","the empty state is missing");
    check(!byID("plugins-add-lead").hidden && byID("plugins-summary").hidden && byID("plugins-nav-count").hidden && byID("plugins-nav-label").textContent === "" && byID("page-plugins").querySelector(".pg-add code:not(p code)").textContent === "agentflow plugin add PATH" && byID("plugins-orphans").hidden,"the empty state does not show the add command");

    // No plugin host: the registry is listed and the page says nothing runs.
    await post("emit",delta(8,"plugin.lifecycle.changed",[entry("plugins",{host:false,plugins:[{...notifier,state:"",since:undefined}],dead_letters:[]})]));
    await until(() => !byID("plugins-host").hidden && Boolean(card("ci-notify")),"a missing plugin host is not explained");
    check(shows("ci-notify","status") === "Not running" && shows("ci-notify","detail") === "The plugin host is not running, so this plugin does not run.","a plugin without a host is wrong: " + shows("ci-notify","detail"));
    check(document.documentElement.scrollWidth <= document.documentElement.clientWidth,"the page scrolls sideways");

    // Offline: the values stay and the page says they may be stale.
    await until(() => !document.querySelector("#page-plugins .live-changed"),"a highlight did not fade");
    await post("status?code=503");
    await post("drop");
    await until(() => label() === "Offline" && !byID("plugins-note").hidden,"the page does not say that it is offline");
    check(cards().length === 1 && byID("plugins-note").textContent.includes("not available"),"the page was cleared offline");
    await post("status?code=0");
    await post("emit",snapshot(9,[entry("plugins",plugins(base()))]));
    await until(() => label() === "Live" && byID("plugins-note").hidden && cards().length === 4,"the page did not recover");

    // A requested Resync replaces the values without highlights or announcements.
    await until(() => !document.querySelector("#page-plugins .live-changed"),"a highlight did not fade");
    const opened = (await requests()).length, hush = said();
    byID("refresh").click();
    await until(async () => (await requests()).length > opened,"Resync did not restart the stream");
    await post("emit",snapshot(13,[entry("plugins",plugins([notifier,annotator({state:"running",code:"",retry_at:undefined})]))]));
    await until(() => label() === "Live" && cards().length === 2,"Resync did not reload the plugins");
    check(!document.querySelector("#page-plugins .live-changed") && said() === hush,"a requested resync highlighted or announced values");
    await post("emit",delta(14,"plugin.lifecycle.changed",[entry("plugins",plugins(base()))]));
    await until(() => cards().length === 4,"the plugins did not return");

    // A snapshot without the plugins state keeps the values and says so.
    await post("emit",frame("resync",0,{reason:"overflow"}));
    await post("emit",snapshot(10,[]));
    await until(() => !byID("plugins-note").hidden && byID("plugins-note").textContent.includes("no plugin state for this repository"),"a stream without the plugins state is not explained");
    check(cards().length === 4,"the last values were cleared");

    // Timeline annotations: a plugin's notes sit under their operation, as text, with the plugin's ID.
    const step = (id, type, state, ms) => ({event_id:id,seq:1,type,time:at(ms),state});
    const note = (id, extra = {}) => ({event_id:id,plugin:"timeline-notes",text:"Note " + id,level:"info",time:at(-1000),...extra});
    const operation = (annotations, extra = {}) => ({id:"op-noted",operation:"push",action:"push",class:"remote",client:"cli",worktree:"first",subject:"Push main",state:"applied",started:at(-60000),updated:at(-59000),
      steps:[step("s1","op.requested","requested",-60000),step("s2","op.applied","applied",-59000)],annotations,...extra});
    const T = AgentflowTimeline;
    const read = T.timelineState({operations:[operation([note("a"),note("a"),{event_id:"b"},null,note("c",{plugin:"Bad ID",level:"loud",url:"http://example.com/x",time:"never"}),note("d",{text:"line one\nline two\u0007"}),"x"])]}).operations[0].annotations;
    check(read.length === 3 && read[0].id === "a" && read[1].plugin === "" && read[1].level === "info" && read[1].url === "" && read[1].at === 0 && read[2].text === "line one line two","annotations are not read safely: " + JSON.stringify(read));
    check(T.timelineState({operations:[operation(Array.from({length:8},(_, i) => note("n" + i)))]}).operations[0].annotations.map(item => item.id).join() === "n3,n4,n5,n6,n7" && T.timelineState({operations:[operation(undefined)]}).operations[0].annotations.length === 0,"annotations are not capped at five, or a missing list breaks the reader");

    location.hash = "#overview";
    await until(() => !byID("page-overview").hidden && visible(byID("page-overview")) && byID("page-plugins").hidden && current() === "overview","the overview did not reopen");
    const plain = {id:"op-plain",operation:"commit",action:"commit",class:"local",client:"cli",worktree:"first",subject:"feat: plain",state:"applied",started:at(-90000),updated:at(-89000),steps:[step("q1","op.requested","requested",-90000)]};
    const notes = [note("a1",{text:"Pushed 3 commits.",url:"https://ci.example.com/runs/412"}),note("a2",{text:"<img src=x onerror=alert(7)>",level:"warning",url:"javascript:alert(8)"}),note("a3",{plugin:"<b>",text:"From nobody",url:"https://user:secret@example.com/"})];
    await post("emit",snapshot(11,[entry("plugins",plugins(base())),entry("timeline",{operations:[operation(notes),plain]})]));
    const opRow = key => [...byID("timeline-list").children].find(node => node.dataset.key === key);
    await until(() => Boolean(opRow("op-noted")),"the timeline did not arrive");
    const noteList = opRow("op-noted").querySelector(".timeline-annotations");
    const items = [...noteList.children];
    check(!noteList.hidden && noteList.getAttribute("aria-label") === "Plugin notes on Push a branch" && items.length === 3 && opRow("op-plain").querySelector(".timeline-annotations").hidden && !document.querySelector("#timeline-panel .live-changed"),"the annotations are not listed under their operation");
    check(items[0].querySelector(".timeline-annotation-from").textContent === "Note from plugin timeline-notes" && items[0].querySelector(".timeline-annotation-text").textContent === "Pushed 3 commits." && items[0].dataset.level === "info" && items[0].querySelector("time").dateTime,"the note is not labelled with its plugin: " + items[0].textContent);
    const link = items[0].querySelector("a");
    check(link.href === "https://ci.example.com/runs/412" && link.textContent === "ci.example.com (link from plugin timeline-notes)" && link.target === "_blank" && link.rel === "noopener noreferrer","the annotation link is wrong: " + link.textContent);
    check(items[1].querySelector(".timeline-annotation-from").textContent === "Warning from plugin timeline-notes" && items[1].dataset.level === "warning" && items[1].querySelector(".timeline-annotation-text").textContent === "<img src=x onerror=alert(7)>" && !noteList.querySelector("img, b") && !items[1].querySelector("a") && !noteList.innerHTML.includes("javascript:"),"annotation text became markup, or an unsafe address became a link");
    check(items[2].querySelector(".timeline-annotation-from").textContent === "Note from plugin an unknown plugin" && !items[2].querySelector("a"),"an annotation without a valid plugin ID or with a credential in its address is not handled");

    // A new annotation arrives live under the same row.
    const same = opRow("op-noted");
    await post("emit",delta(12,"plugin.annotation.added",[entry("timeline",{operations:[operation([...notes,note("a4",{text:"CI passed."})]),plain]})]));
    await until(() => noteList.children.length === 4,"the live annotation did not arrive");
    check(opRow("op-noted") === same && noteList.children[3].classList.contains("live-changed") && !noteList.children[0].classList.contains("live-changed") && noteList.children[3].textContent.includes("CI passed."),"the new annotation is not added in place and highlighted");
    check(document.documentElement.scrollWidth <= document.documentElement.clientWidth,"the annotations make the page scroll sideways");
  } catch (error) {
    result = error.stack || error.message;
  }
  await fetch("/result",{method:"POST",body:String(result).slice(0,4000)});
})();
