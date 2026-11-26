"use strict";

(async () => {
  const byID = id => document.getElementById(id);
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
  const answer = (route, body, reset = true) => fetch("/fixture/settings/answer?route=" + route + (reset ? "&reset" : ""),{method:"POST",body:JSON.stringify(body)});
  // Every request the console sends to a settings endpoint, as the page sent it.
  const calls = [];
  const nativeFetch = window.fetch.bind(window);
  window.fetch = (target, options = {}) => {
    const route = /api\/settings\/(catalog|preview|save|undo)$/.exec(String(target))?.[1];
    if (route) calls.push({route,body:options.body ? JSON.parse(options.body) : null,console:options.headers?.["X-Agentflow-Request"] || "",authorized:String(options.headers?.Authorization || "").startsWith("Bearer ")});
    return nativeFetch(target,options);
  };
  const sent = async route => calls.filter(item => !route || item.route === route);
  const repo = "3f2a9c1b7d4e5f60";
  const frame = (event, seq, data) => "event: " + event + "\nid: v1:" + repo + "=" + seq + "\ndata: " + JSON.stringify(data) + "\n\n";
  const overview = {projection:"overview",repo,worktree:"first",state:{branch:"issue-71",head:"1111111",oid:"1111111aaaaaaa1111111aaaaaaa1111111aaaa",detached:false,changed:0,staged:0,unstaged:0,untracked:0,operation:"",config:{valid:true,problems:[]}}};

  // SettingsState (projection_settings_state.go) as lane 4.2's projection will send it.
  const set = json => ({json,set:true});
  const unset = {set:false};
  const value = (id, layer, effective, source, extra = {}) => ({id,layer,effective,source,active:true,...extra});
  const base = () => [
    value("personal:approval_window",set("10m"),set("10m"),"personal"),
    value("personal:autonomy.actions",set({commit:"auto"}),set({commit:"auto"}),"personal"),
    value("personal:autonomy.local",set("auto"),set("confirm"),"personal",{capped_by:"repo:policy.max_autonomy.local"}),
    value("personal:autonomy.remote",unset,set("confirm"),"default"),
    value("repo:commit.subject_max",set(72),set(72),"repo"),
    value("repo:commit.types",set(["feat","fix"]),set(["feat","fix"]),"repo"),
    value("repo:notifications.provider",set("slack"),set("slack"),"repo"),
    value("repo:notifications.settings.credential_ref",set("SLACK_BOT_TOKEN"),set("SLACK_BOT_TOKEN"),"repo",{provider:"slack"}),
    value("repo:notifications.settings.destination",set("#old"),set("#old"),"repo",{provider:"webhook",active:false}),
    value("repo:policy.allow_auto_pr_publication",unset,set(false),"default"),
    value("repo:policy.max_autonomy.local",set("confirm"),set("confirm"),"repo"),
    value("repo:policy.protected_branches",set(["main"]),set(["main"]),"repo"),
    value("repo:policy.test_cmd",set("make test"),set("make test"),"repo"),
    value("repo:task_management.provider",set("github-issues"),set("github-issues"),"repo"),
    value("repo:task_management.settings.search.limit",unset,set(20),"default",{provider:"github-issues"})
  ];
  const state = (values, extra = {}) => ({revisions:{personal:"sha256:p1",repo:"sha256:r1"},catalog_revision:"sha256:fixture-1",values,problems:[],last_save:null,branch:"issue-71",protected:false,...extra});
  const entry = settings => ({projection:"settings",repo,worktree:"first",state:settings});
  const change = (values, id, layer, effective, source = layer.set ? id.split(":")[0] : "default") => values.map(item => item.id === id ? {...item,layer,effective,source} : item);

  const row = key => [...document.querySelectorAll(".st-field")].find(node => node.dataset.key === key);
  const control = (key, query) => row(key)?.querySelector(".st-control " + query);
  const input = (node, text) => { node.value = text; node.dispatchEvent(new Event("input",{bubbles:true})); };
  const choose = (node, text) => { node.value = text; node.dispatchEvent(new Event("change",{bubbles:true})); };
  const said = () => byID("live-announcer").textContent;
  const shownText = id => byID(id).hidden ? "" : byID(id).textContent;
  const previewOf = (edits, extra = {}) => ({status:"ok",base:{personal:"sha256:p1",repo:"sha256:r1"},changes:edits,effects:[],files:[],diagnostics:[],confirm:[],...extra});

  let result = "passed";
  try {
    window.confirm = () => true;
    const S = AgentflowSettings, W = AgentflowSettingsWidgets;
    const phase = sessionStorage.getItem("fixture.phase");

    if (phase === "reloaded") {
      // The draft survives a reload: the edits, their destination and the bar.
      await until(() => !byID("console-shell").hidden,"the console did not reopen after the reload");
      await answer("preview",previewOf([]));
      await post("emit",frame("snapshot",1,{cursor:"v1:" + repo + "=1",states:[overview,entry(state(change(base(),"repo:policy.test_cmd",set("make all"),set("make all"))))]}));
      await until(() => !byID("settings-bar").hidden && shownText("settings-bar-count") === "2 unsaved changes" && row("repo:policy.test_cmd|"),"the draft did not survive the reload: " + shownText("settings-bar-count"));
      check(control("repo:policy.test_cmd|","input").value === "make check" && row("repo:policy.test_cmd|").dataset.state === "edited","a reloaded edit is not in its control");
      check(control("personal:autonomy.remote|","select").value === "v:suggest" && shownText("settings-bar-where") === "1 to your personal file · 1 to a draft pull request","the reloaded draft lost an edit or its destination: " + shownText("settings-bar-where"));
      byID("settings-discard").click();
      await until(() => byID("settings-bar").hidden && !localStorage.getItem("agentflow.settings:/w/first/"),"Discard did not clear the draft and its storage");
      check(control("repo:policy.test_cmd|","input").value === "make all" && document.activeElement === byID("settings-title"),"Discard did not restore the saved value or move focus");
      await fetch("/result",{method:"POST",body:result});
      return;
    }

    // The readers are defensive.
    check(S.readState(null) === null && S.readState({}).values.size === 0 && S.readCatalog("x") === null,"a missing state or catalog is not read safely");
    const sparse = S.readState({values:[{id:"personal:a",layer:{set:true,json:1},effective:"x",source:"moon"},{},7],last_save:{operation_id:"op",ids:[1,"a"],publication:{url:"javascript:alert(1)",number:-2}},problems:[{message:"m",severity:"loud"},{}]});
    check(sparse.values.get("personal:a|").source === "default" && !sparse.values.get("personal:a|").effective.set && sparse.lastSave.ids.join() === "a" && sparse.lastSave.publication.url === "" && sparse.lastSave.publication.number === 0 && !sparse.lastSave.undoable && sparse.problems.length === 1 && sparse.problems[0].severity === "error","a damaged state is not read safely");
    check(S.segments("a.b\\.c.d").join("|") === "a|b.c|d" && S.matchPath("t.templates.*","t.templates.bug").join() === "bug" && S.matchPath("t.*","t.a.b") === null,"field paths are not read by the FieldPath grammar");
    check(S.show(true) === "on" && S.show(["a","b"]) === "a, b" && S.show({a:"x"}) === "a: x" && S.show(undefined) === "not set" && S.show("10m0s") === "10m" && S.show("1h30m0s") === "90m" && S.show("45s") === "45s" && S.canon({b:1,a:2}) === S.canon({a:2,b:1}),"values are not put in words");
    const built = S.changeSet([{key:"a",id:"repo:x.settings.y",op:"set",value:1,expect:null},{key:"b",id:"repo:x.provider",op:"set",value:"p",expect:"q"},{key:"c",id:"personal:z",op:"unset",expect:"5m"}],{personal:"sha256:p",repo:"sha256:r"},{mode:"worktree",title:"t"},item => item.key === "b");
    check(built.body.edits.map(item => item.id).join() === "repo:x.provider,repo:x.settings.y,personal:z" && built.body.edits[1].expect === null && !("value" in built.body.edits[2]) && built.body.repo.mode === "worktree" && !("title" in built.body.repo) && built.body.base.repo === "sha256:r" && built.keys.join() === "b,a,c","the change set is not ordered or shaped as the contract says");

    // Every widget kind has a control that returns JSON.
    const kinds = ["toggle","select","stepper","duration","chips","kv-table","provider-picker","branch-picker","file-picker","credential-ref","text","list"];
    check(kinds.every(kind => W.kinds.includes(kind)),"a widget kind has no factory: " + W.kinds.join());
    const bench = document.createElement("div");
    document.body.append(bench);
    const make = (schema, meta, columns) => {
      const seen = [];
      const made = W.create({schema,meta,columns},{id:"bench-" + bench.childElementCount,label:"Bench",inherit:() => "Inherit",suggest:() => ["main","develop"],change:next => seen.push(next)});
      bench.append(made.node);
      return {made,seen,last:() => seen.at(-1)};
    };
    let w = make({type:"boolean"},{widget:"toggle"});
    w.made.set(false); w.made.node.click();
    check(w.last().value === true && w.made.node.getAttribute("role") === "switch" && w.made.node.getAttribute("aria-checked") === "true","the toggle is not a switch");
    w = make({type:"string",enum:["suggest","confirm","auto"]},{widget:"select"});
    w.made.set("confirm"); choose(w.made.node,"v:auto");
    check(w.last().value === "auto" && w.made.node.options[0].textContent === "Inherit","the select lost a value or its inherit choice");
    choose(w.made.node,"");
    check(w.last().inherit === true,"the select's inherit choice does not unset");
    w = make({type:"string",enum:["github","none"],default:"github"},{widget:"provider-picker",picker:{role:"secops"}});
    check([...w.made.node.options].map(option => option.textContent).join("|") === "Inherit|github (default)|none (role switched off)","the provider picker does not name its choices");
    w = make({type:"integer"},{widget:"stepper",min:20,max:22,step:1});
    w.made.set(21); w.made.node.querySelector("button:last-child").click(); w.made.node.querySelector("button:last-child").click();
    check(w.last().value === 22 && w.made.get() === 22,"the stepper does not stop at its maximum");
    input(w.made.main,"7");
    check(w.last().incomplete.includes("20 to 22"),"the stepper accepts a number below its minimum");
    w = make({type:"string"},{widget:"duration",min_duration:"1m0s",max_duration:"1h0m0s"});
    w.made.set("1h30m");
    check(w.made.main.value === "90" && w.made.node.querySelector("select").value === "m" && w.made.get() === "1h30m","a duration is not shown in its largest whole unit");
    input(w.made.main,"45");
    check(w.last().value === "45m","the duration is not saved as a Go duration");
    choose(w.made.node.querySelector("select"),"h");
    check(w.last().incomplete.includes("1 minute to 1 hour") && W.seconds("1h0m0s") === 3600,"the duration limit is not checked");
    w = make({type:"array",items:{type:"string",suggestions:["feat","fix"]}},{widget:"chips"});
    w.made.set(["feat"]);
    const adder = w.made.node.querySelector("input");
    input(adder,"fix"); adder.dispatchEvent(new KeyboardEvent("keydown",{key:"Enter",bubbles:true,cancelable:true}));
    check(w.last().value.join() === "feat,fix" && adder.list.options.length === 0,"chips do not add with Enter or still offer a used name");
    w.made.node.querySelector(".st-chip-remove").click();
    check(w.last().value.join() === "fix" && w.made.node.querySelector(".st-chip-remove").getAttribute("aria-label") === "Remove fix","a chip is not removed or its button is not named");
    w = make({type:"array",items:{type:"integer"}},{widget:"chips"});
    input(w.made.node.querySelector("input"),"429"); w.made.node.querySelector(".st-chip-add button").click();
    check(w.last().value[0] === 429,"integer chips are not numbers");
    w = make({type:"object",additional_properties:{type:"string",enum:["suggest","confirm","auto"]},key_suggestions:["commit","push"]},{widget:"kv-table"});
    w.made.set({commit:"auto"});
    [...w.made.node.querySelectorAll("button")].find(node => node.textContent === "Add entry").click();
    check(w.last().incomplete === "Give every row a key.","a map row without a key is accepted");
    const keys = w.made.node.querySelectorAll(".st-key input");
    input(keys[1],"commit");
    check(w.last().incomplete.includes("used twice"),"a duplicate map key is accepted");
    input(keys[1],"push"); choose(w.made.node.querySelectorAll("tbody select")[1],"v:confirm");
    check(JSON.stringify(w.last().value) === '{"commit":"auto","push":"confirm"}' && w.made.node.querySelectorAll("tbody select")[1].getAttribute("aria-label") === "Value, push","the map does not return its rows or name its cells");
    w = make({type:"object",additional_properties:{type:"object",properties:{provider:{type:"string",enum:["slack","webhook"]},native:{type:"object",properties:{channel_id:{type:"string"}}}}}},{widget:"kv-table"},{provider:{label:"Provider",order:1},"native.channel_id":{label:"Channel",order:2}});
    w.made.set({ops:{provider:"slack",native:{channel_id:"C1"}}});
    input(w.made.node.querySelector("tbody .st-line input"),"C2");
    check(JSON.stringify(w.last().value) === '{"ops":{"provider":"slack","native":{"channel_id":"C2"}}}' && [...w.made.node.querySelectorAll("thead th")].map(node => node.textContent).join("|") === "Key|Provider|Channel|Row actions","a column with a property path does not edit the nested value");
    w = make({type:"array",items:{type:"object",properties:{name:{type:"string"},on:{type:"boolean"}}}},{widget:"list"},{name:{label:"Name",order:1},on:{label:"Enabled",order:2}});
    w.made.set([{name:"a",on:true},{name:"b"}]);
    w.made.node.querySelector('button[aria-label="Move item 2 up"]').click();
    check(JSON.stringify(w.last().value) === '[{"name":"b"},{"name":"a","on":true}]',"list rows do not move");
    w = make({type:"string"},{widget:"credential-ref"});
    input(w.made.main,"ghp_secret-value");
    check(w.last().incomplete.includes("Do not paste the secret"),"a credential reference accepts a secret");
    input(w.made.main,"GITHUB_TOKEN");
    check(w.last().value === "GITHUB_TOKEN","a credential reference lost a variable name");
    w = make({type:"string"},{widget:"file-picker",picker:{kind:"file",base:"repo",glob:"*.json"}});
    input(w.made.main,"../outside.json");
    check(w.last().incomplete.includes("inside this repository"),"a repository file picker accepts a path outside it");
    w = make({type:"array",items:{type:"string"}},{widget:"branch-picker",picker:{multiple:true}});
    check(w.made.node.querySelector("input").list.options.length === 2,"the branch picker does not offer branches");
    w = make({type:"string"},{widget:"text"});
    input(w.made.main,"x"); input(w.made.main,"");
    check(w.last().inherit === true,"clearing a text control does not inherit");
    check(W.kindOf({schema:{type:"object"},meta:{widget:"hologram"}}) === "readonly","an unknown widget is not shown read-only");
    bench.remove();

    await until(() => !byID("connection-options").hidden,"the console did not ask for a session");
    const ticket = await (await fetch("/fixture/ticket",{method:"POST"})).text();
    location.hash = new URLSearchParams({launch:ticket,screen:"settings"}).toString();
    await until(() => !byID("console-shell").hidden && !byID("page-settings").hidden,"the Settings page did not open");

    // The retired pages are gone; their rail entries stay and open Settings groups.
    check(!byID("page-providers") && !byID("page-autonomy") && !byID("setting-dialog"),"a retired page is still in the document");
    check(document.querySelector('a[data-page="settings"]').getAttribute("aria-current") === "page" && document.querySelector('a[data-page="providers"]').getAttribute("href") === "#providers","the rail does not show Settings and its groups");

    // Before the first snapshot the page waits; the catalog is read once.
    check(shownText("settings-wait").includes("Waiting for live updates") && byID("settings-body").hidden && byID("settings-bar").hidden,"the page does not show that it waits for the stream");
    await post("emit",frame("snapshot",1,{cursor:"v1:" + repo + "=1",states:[overview,entry(state(base()))]}));
    await until(() => !byID("settings-body").hidden && row("personal:autonomy.local|"),"the snapshot and the catalog did not fill the page");
    check((await sent("catalog")).length === 1 && byID("settings-wait").hidden && byID("settings-note").hidden && !document.querySelector("#page-settings .live-changed") && !said(),"the catalog was read more than once, or the first values were highlighted or announced");

    // Groups come from the catalog, in order; a provider's settings show for the selected provider only.
    const groups = [...byID("settings-groups").querySelectorAll("a")];
    check(groups[0].textContent.startsWith("All settings") && groups[0].getAttribute("aria-current") === "true" && groups[1].textContent.startsWith("Providers") && groups[1].getAttribute("href") === "#providers" && groups.find(link => link.textContent.startsWith("Autonomy")).getAttribute("href") === "#autonomy" && groups.find(link => link.textContent.startsWith("Commits")).getAttribute("href") === "#settings?group=commit","the group links are wrong");
    check(row("repo:task_management.settings.search.limit|github-issues") && !row("repo:task_management.settings.site|jira") && !row("repo:commit.breaking_paths|"),"a setting of another provider or an advanced one shows");
    check(row("repo:notifications.settings.destination|webhook").querySelector(".st-source").textContent.startsWith("Not in use"),"a set value of an unselected provider is not marked");

    // Each control shows its source, inherit, the repository ceiling and its label.
    const local = row("personal:autonomy.local|");
    const localSelect = control("personal:autonomy.local|","select");
    check(local.querySelector(".st-label").textContent === "Local actions" && local.querySelector(".st-label").htmlFor === localSelect.id && localSelect.getAttribute("aria-labelledby") === local.querySelector(".st-label").id && local.querySelector(".st-id").textContent === "personal:autonomy.local","the control is not labelled from the field metadata");
    check(localSelect.value === "v:auto" && local.querySelector(".st-source").textContent === "Set in your personal settings." && local.querySelector(".st-cap").textContent === "The repository ceiling “Local actions ceiling” limits this to confirm.","the source or the ceiling is wrong: " + local.querySelector(".st-cap").textContent);
    check(localSelect.getAttribute("aria-describedby").split(" ").every(id => byID(id)) && !local.querySelector(".st-row-state button:last-child").hidden,"the control is not described, or Inherit is missing for a set value");
    const remote = control("personal:autonomy.remote|","select");
    check(remote.value === "" && remote.options[0].textContent === "Inherit (confirm)" && row("personal:autonomy.remote|").querySelector(".st-source").textContent === "Not set here. Uses the default: confirm.","an unset field does not show what it inherits");
    const limit = control("repo:task_management.settings.search.limit|github-issues","input");
    check(limit.value === "" && limit.placeholder === "20","an inherited number is not a placeholder");

    // The first edit shows the Save bar; live checks are debounced into one preview request.
    await answer("preview",previewOf([{id:"repo:policy.test_cmd",edit:0,applies:"next-run",before:set("make test"),after:set("make check")}],{
      diagnostics:[{severity:"warning",id:"repo:policy.test_cmd",edit:0,code:"reference",message:"The command make check was not found."}],
      files:[{layer:"repo",path:".agentflow/config.yml",before_revision:"sha256:r1",after_revision:"sha256:r2",diff:"--- a/.agentflow/config.yml\n+++ b/.agentflow/config.yml\n@@ -1 +1 @@\n-  test_cmd: make test\n+  test_cmd: make check\n"}],
      repo:{mode:"draft",title:"chore: change the test command",destination:"acme/widgets@main",ci:"This change does not start CI."}}));
    const command = control("repo:policy.test_cmd|","input");
    command.focus();
    for (const typed of ["make c","make ch","make check"]) { input(command,typed); await pause(30); }
    check(!byID("settings-bar").hidden && shownText("settings-bar-count") === "1 unsaved change" && shownText("settings-bar-checks") === "Checking…" && (await sent("preview")).length === 0,"the Save bar did not appear with the first edit, or a check ran before the pause");
    await until(async () => (await sent("preview")).length === 1 && shownText("settings-bar-checks") === "1 warning","the debounced check did not run once: " + shownText("settings-bar-checks"));
    const first = (await sent("preview"))[0];
    check(first.console === "console" && first.authorized && first.body.edits.length === 1 && first.body.edits[0].id === "repo:policy.test_cmd" && first.body.edits[0].op === "set" && first.body.edits[0].value === "make check" && first.body.edits[0].expect === "make test" && first.body.base.repo === "sha256:r1" && first.body.repo.mode === "draft","the preview request is not the change set: " + JSON.stringify(first.body));
    const edited = row("repo:policy.test_cmd|");
    check(edited.dataset.state === "edited" && edited.querySelector(".st-edited").textContent === "Edited · Saves as a draft pull request" && edited.querySelector(".st-diagnostics li").dataset.severity === "warning" && edited.querySelector(".st-diagnostics").textContent.includes("was not found") && byID("settings-bar").dataset.tone === "warn","the edit, where it goes or its diagnostic is not shown");
    check(shownText("settings-bar-where") === "1 to a draft pull request" && document.activeElement === command && command.value === "make check","the bar does not say where the change goes, or typing lost focus");
    await until(() => said().includes("Checked: 1 warning."),"the check was not announced: " + said());

    // Nothing polls: a quiet page sends no request.
    const quiet = (await sent()).length;
    await pause(700);
    check((await sent()).length === quiet,"the page sent a request without an action");

    // A field changed outside updates live; one in the draft conflicts.
    let values = change(change(base(),"repo:commit.subject_max",set(80),set(80)),"repo:policy.test_cmd",set("make ci"),set("make ci"));
    await answer("preview",previewOf([]));
    await post("emit",frame("delta",2,{event:{id:"e2",type:"config.layer.changed",repo,seq:2},states:[entry(state(values,{revisions:{personal:"sha256:p1",repo:"sha256:r2"}}))]}));
    await until(() => control("repo:commit.subject_max|","input").value === "80","the outside edit did not reach its control");
    check(row("repo:commit.subject_max|").querySelector(".st-control").classList.contains("live-changed") && !row("personal:autonomy.local|").querySelector(".live-changed"),"the changed value is not highlighted, or an unchanged one is");
    const clash = edited.querySelector(".st-conflict");
    check(!clash.hidden && edited.dataset.state === "conflict" && clash.textContent.includes("it is now make ci") && clash.textContent.includes("sets make check") && command.value === "make check","the conflict is not shown, or the edit was overwritten");
    await until(() => said().includes("Test command changed outside this page") && said().includes("Subject length changed to 80"),"the outside edits were not announced: " + said());
    check(shownText("settings-bar-checks").startsWith("1 conflict") || shownText("settings-bar-checks") === "Checking…","the bar does not count the conflict: " + shownText("settings-bar-checks"));
    const before = (await sent("preview")).length;
    clash.querySelector("button").click(); // Keep my edit
    await until(async () => (await sent("preview")).length > before && clash.hidden,"keeping the edit did not check it again");
    check((await sent("preview")).at(-1).body.edits[0].expect === "make ci" && (await sent("preview")).at(-1).body.base.repo === "sha256:r2" && edited.dataset.state === "edited","keep mine did not rebase the edit on the new value");
    // Take theirs drops the edit.
    values = change(values,"repo:policy.test_cmd",set("make all"),set("make all"));
    await post("emit",frame("delta",3,{event:{id:"e3",type:"config.layer.changed",repo,seq:3},states:[entry(state(values,{revisions:{personal:"sha256:p1",repo:"sha256:r3"}}))]}));
    await until(() => !clash.hidden,"the second conflict did not show");
    clash.querySelectorAll("button")[1].click();
    await until(() => byID("settings-bar").hidden && command.value === "make all" && edited.dataset.state === "","take theirs did not drop the edit");

    // Search by label, description and ID; the modified filter; a group.
    input(byID("settings-search"),"ceiling local anyone");
    check([...document.querySelectorAll(".st-field")].map(node => node.dataset.key).join() === "repo:policy.max_autonomy.local|" && shownText("settings-summary").startsWith("Showing 1 of "),"search by label did not narrow the page: " + shownText("settings-summary"));
    input(byID("settings-search"),"commit.breaking_paths");
    check(row("repo:commit.breaking_paths|") && document.querySelectorAll(".st-field").length === 1,"search by ID did not find an advanced setting");
    input(byID("settings-search"),"");
    byID("settings-modified").click();
    const modified = [...document.querySelectorAll(".st-field")].map(node => node.dataset.key);
    check(modified.includes("repo:policy.test_cmd|") && modified.includes("repo:notifications.settings.destination|webhook") && !modified.includes("personal:autonomy.remote|") && modified.length === 12,"the modified filter does not show the set fields: " + modified.length);
    await until(() => said().includes("Showing 12 of"),"the filter result was not announced: " + said());
    byID("settings-clear").click();
    check(!byID("settings-modified").checked && document.activeElement === byID("settings-search"),"Clear filters did not reset the filters");
    document.querySelector('a[data-page="autonomy"]').click();
    await until(() => location.hash === "#autonomy" && document.querySelectorAll(".st-group").length === 1,"the Autonomy entry did not open its group");
    check(document.querySelector(".st-group h2").textContent === "Autonomy" && document.querySelector('a[data-page="autonomy"]').getAttribute("aria-current") === "page" && !byID("page-settings").hidden,"the Autonomy group is not the page");
    location.hash = "#providers?role=task_management";
    await until(() => document.querySelector(".st-group h2")?.textContent === "Task management","a provider role link did not open its group");
    location.hash = "#settings";
    await until(() => document.querySelectorAll(".st-group").length > 5,"the Settings entry did not show every group");

    // Review and save: changes, effects, the diff, mode and title, and a tick for each loosening edit.
    const loosening = previewOf([
      {id:"personal:autonomy.remote",edit:0,applies:"live",loosens:true,before:unset,after:set("auto"),effective:{id:"personal:autonomy.remote",before:set("confirm"),after:set("auto"),source_before:"default",source_after:"personal"}},
      {id:"repo:policy.test_cmd",edit:1,applies:"next-run",before:set("make all"),after:set("make check")}],{
      effects:[{id:"personal:autonomy.actions",before:set({commit:"auto"}),after:set({commit:"auto",push:"auto"}),source_before:"personal",source_after:"personal"}],
      files:[{layer:"repo",path:".agentflow/config.yml",before_revision:"sha256:r3",after_revision:"sha256:r4",diff:"--- a/.agentflow/config.yml\n+++ b/.agentflow/config.yml\n@@ -1 +1 @@\n-  test_cmd: make all\n+  test_cmd: make check\n"},{layer:"personal",path:"~/.agentflow/config.yml",before_revision:"sha256:p1",after_revision:"sha256:p2",diff:"secret: personal yaml"}],
      diagnostics:[{severity:"info",id:"personal:autonomy.remote",edit:0,code:"loosens",message:"Allows remote actions without confirmation."}],
      confirm:[{id:"personal:autonomy.remote",reason:"loosens",message:"Allows remote actions without confirmation."}],
      repo:{mode:"draft",title:"chore: change the test command",destination:"acme/widgets@main",ci:"This change does not start CI."}});
    await answer("preview",loosening);
    choose(control("personal:autonomy.remote|","select"),"v:auto");
    input(command,"make check");
    await until(() => shownText("settings-bar-count") === "2 unsaved changes" && shownText("settings-bar-checks") === "No problems found","two edits are not in the bar: " + shownText("settings-bar-checks"));
    byID("settings-review-open").click();
    const dialog = byID("settings-review");
    await until(() => dialog.open && byID("settings-review-changes").children.length === 2 && !byID("settings-save").hidden && byID("settings-save").textContent === "Save 2 changes","the review did not open with the changes");
    check(document.activeElement === byID("settings-review-title") && byID("settings-review-count").textContent === "2 changes","the review did not take focus or count the changes");
    const items = [...byID("settings-review-changes").children];
    check(items[0].dataset.loosens === "true" && items[0].textContent.includes("Remote actions") && items[0].textContent.includes("Personal file") && items[0].textContent.includes("Loosens policy") && items[0].querySelector(".removed").textContent === "not set" && items[0].querySelector(".added").textContent === "auto" && items[0].textContent.includes("Effective value: confirm → auto, from your personal settings."),"the loosening change is not shown with its values and effect: " + items[0].textContent);
    check(!items[1].dataset.loosens && items[1].textContent.includes("Draft pull request") && items[1].textContent.includes("Applies to the next command."),"the repository change does not say where it goes");
    check(!byID("settings-review-effects-section").hidden && byID("settings-review-effects").textContent.includes("Action overrides") && byID("settings-review-effects").textContent.includes("commit: auto, push: auto"),"the effects are missing");
    check(!byID("settings-review-repo").hidden && document.querySelector('input[name="settings-mode"][value="draft"]').checked && byID("settings-review-commit").placeholder === "chore: change the test command" && shownText("settings-review-destination").includes("acme/widgets@main"),"the repository mode, title or destination is missing");
    check(byID("settings-review-diff-text").textContent.includes("+  test_cmd: make check") && byID("settings-review-diff-text").querySelector("[data-line=add]") && !dialog.textContent.includes("personal yaml") && !byID("settings-review-personal").hidden,"the repository diff is missing, or personal YAML is shown");
    const box = items[0].querySelector("input[type=checkbox]");
    check(byID("settings-save").disabled && !box.checked && shownText("settings-save-why").includes("Tick 1 confirmation"),"the save is possible without the loosening tick");
    box.click();
    await until(() => !byID("settings-save").disabled && byID("settings-save-why").hidden,"the tick did not enable the save");

    // The mode is part of the change set: the worktree needs no title.
    const checks = (await sent("preview")).length;
    document.querySelector('input[name="settings-mode"][value="worktree"]').click();
    await until(async () => (await sent("preview")).length > checks && (await sent("preview")).at(-1).body.repo.mode === "worktree" && byID("settings-review-publication").hidden,"choosing the worktree did not check the change again");
    check((await sent("preview")).at(-1).body.repo.mode === "worktree" && shownText("settings-review-destination").startsWith("Writes .agentflow/config.yml in this worktree"),"the worktree mode is not in the change set or the review");
    await until(() => !byID("settings-save").disabled,"the review did not settle after the mode change");

    check(JSON.parse(localStorage.getItem("agentflow.settings:/w/first/")).edits.length === 2,"the draft is not in the browser's storage");

    // The console asks for a confirmation; the same change set goes back with the token.
    await answer("save",{status:"needs_confirmation",approval:"token-1",reason:"Settings saves require confirmation.",policy:{action:"console.settings",level:"confirm",source:"the built-in rule"},summary:{}});
    byID("settings-save").click();
    await until(() => byID("settings-save").textContent === "Confirm and save" && !byID("settings-save").disabled,"the console's confirmation did not show");
    check(shownText("settings-review-confirm").includes("console.settings requires confirm") && document.activeElement === byID("settings-save"),"the confirmation is not explained or focused");
    let saves = await sent("save");
    check(saves.length === 1 && !("approval" in saves[0].body) && saves[0].body.change_set.edits.length === 2 && saves[0].body.change_set.repo.mode === "worktree","the first save request is wrong: " + JSON.stringify(saves[0].body));
    const saved = {status:"saved",operation_id:"op_save1",changes:[{id:"personal:autonomy.remote"},{id:"repo:policy.test_cmd"}],revisions:{personal:"sha256:p2",repo:"sha256:r4"},publication:{mode:"worktree",branch:"issue-71"},undo:true};
    await answer("save",saved);
    byID("settings-save").click();
    await until(() => !dialog.open && shownText("settings-last-text").startsWith("Saved 2 settings."),"the save did not finish: " + shownText("settings-last-text"));
    saves = await sent("save");
    check(saves.length === 2 && saves[1].body.approval === "token-1" && JSON.stringify(saves[1].body.change_set) === JSON.stringify(saves[0].body.change_set),"the confirmed save is not the reviewed change set");
    check(byID("settings-bar-draft").hidden && !localStorage.getItem("agentflow.settings:/w/first/") && shownText("settings-last-link") === "Written to this worktree","the draft was not cleared, or the result is not shown");
    await until(() => said().includes("Saved 2 settings."),"the save was not announced: " + said());

    // Undo and the publication link come from last_save on the stream.
    values = change(change(values,"personal:autonomy.remote",set("auto"),set("auto")),"repo:policy.test_cmd",set("make check"),set("make check"));
    const lastSave = {operation_id:"op_save1",time:new Date().toISOString(),client:"daemon",ids:["personal:autonomy.remote","repo:policy.test_cmd"],publication:{mode:"draft",url:"https://github.com/acme/widgets/pull/12",number:12,branch:"agentflow/settings",state:"open"},undoable:true};
    await post("emit",frame("delta",4,{event:{id:"e4",type:"op.applied",repo,seq:4},states:[entry(state(values,{revisions:{personal:"sha256:p2",repo:"sha256:r4"},last_save:lastSave}))]}));
    await until(() => shownText("settings-last-text").startsWith("Last save: 2 settings") && !byID("settings-undo").hidden,"last_save did not reach the bar: " + shownText("settings-last-text"));
    const link = byID("settings-last-link").querySelector("a");
    check(shownText("settings-last-text").includes("from this console") && link.textContent === "Draft pull request #12" && link.href.endsWith("/pull/12") && link.rel === "noopener noreferrer" && byID("settings-undo").textContent === "Undo" && !byID("settings-undo").disabled,"the last save, its link or Undo is wrong");
    check(control("personal:autonomy.remote|","select").value === "v:auto" && !said().includes("Remote actions changed to"),"the saved value is not shown, or it was announced as an outside edit");

    // Undo asks, shows what it puts back, and saves with its token.
    await answer("undo",{status:"needs_confirmation",approval:"token-2",reason:"Settings saves require confirmation.",summary:previewOf([
      {id:"personal:autonomy.remote",edit:0,applies:"live",before:set("auto"),after:unset},{id:"repo:policy.test_cmd",edit:1,applies:"next-run",before:set("make check"),after:set("make all")}])});
    byID("settings-undo").click();
    await until(() => dialog.open && byID("settings-review-title").textContent === "Undo the last save" && byID("settings-save").textContent === "Confirm and undo","the undo review did not open");
    check(byID("settings-review-changes").children.length === 2 && byID("settings-review-changes").textContent.includes("make all") && byID("settings-review-modes").hidden,"the undo does not show what it puts back");
    let undos = await sent("undo");
    check(undos.length === 1 && !("approval" in undos[0].body) && undos[0].body.undo.repo.mode === "draft","the undo request is wrong: " + JSON.stringify(undos[0].body));
    await answer("undo",{status:"saved",operation_id:"op_undo1",changes:[{id:"personal:autonomy.remote"},{id:"repo:policy.test_cmd"}],revisions:{personal:"sha256:p3",repo:"sha256:r5"},undo:true});
    byID("settings-save").click();
    await until(() => !dialog.open && shownText("settings-last-text").startsWith("Saved 2 settings."),"the undo did not finish");
    undos = await sent("undo");
    check(undos.length === 2 && undos[1].body.approval === "token-2","the confirmed undo did not send its token");
    values = change(change(values,"personal:autonomy.remote",unset,set("confirm")),"repo:policy.test_cmd",set("make all"),set("make all"));
    await post("emit",frame("delta",5,{event:{id:"e5",type:"op.applied",repo,seq:5},states:[entry(state(values,{revisions:{personal:"sha256:p3",repo:"sha256:r5"},last_save:{...lastSave,operation_id:"op_undo1",undo_of:"op_save1",undoable:false}}))]}));
    await until(() => shownText("settings-last-text").startsWith("Last undo:") && byID("settings-undo").textContent === "Redo","an undone save does not offer Redo");
    check(byID("settings-undo").disabled && shownText("settings-undo-why").includes("pull request #12"),"a save that waits in a pull request can be undone, or the reason is missing");

    // A refused save comes back as a preview with its diagnostics.
    await answer("preview",previewOf([{id:"repo:commit.subject_max",edit:0,applies:"next-run",before:set(80),after:set(81)}],{repo:{mode:"worktree",branch:"issue-71"}}));
    control("repo:commit.subject_max|","button:last-child").click();
    await until(() => shownText("settings-bar-checks") === "No problems found","the stepper edit was not checked");
    byID("settings-review-open").click();
    await until(() => dialog.open && !byID("settings-save").disabled,"the second review did not open");
    await answer("save",{...previewOf([{id:"repo:commit.subject_max",edit:0,applies:"next-run",before:set(80),after:set(81)}]),status:"invalid",diagnostics:[{severity:"error",id:"repo:commit.subject_max",edit:0,code:"protected",message:"The branch is protected."}]});
    byID("settings-save").click();
    await until(() => shownText("settings-review-status") === "Fix the errors before you save." && byID("settings-save").disabled,"a refused save does not show why: " + shownText("settings-review-status"));
    check(byID("settings-review-diagnostics").textContent.includes("Subject length: The branch is protected.") && dialog.open,"the refusal's diagnostic is missing");
    byID("settings-review-close").click();
    check(!dialog.open && row("repo:commit.subject_max|").dataset.state === "error" && control("repo:commit.subject_max|","input").getAttribute("aria-invalid") === "true","the error is not on its field");
    row("repo:commit.subject_max|").querySelector(".st-row-state button").click(); // Revert
    await until(() => byID("settings-bar-draft").hidden,"Revert did not drop the edit");

    // Leave a draft, then reload.
    await answer("preview",previewOf([]));
    input(command,"make check");
    choose(control("personal:autonomy.remote|","select"),"v:suggest");
    await until(() => shownText("settings-bar-count") === "2 unsaved changes","the draft for the reload is not in the bar");
    sessionStorage.setItem("fixture.phase","reloaded");
    location.reload();
    return;
  } catch (error) { result = error.message; }
  await fetch("/result",{method:"POST",body:result});
})();
