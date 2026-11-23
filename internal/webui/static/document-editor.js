"use strict";

// Each edit composes a candidate against the exact source used to draw it.
// Only app.js stages the returned text, previews it, and asks for publication.
(() => {
  const id = (name) => document.getElementById(name);
  const own = (value, key) => value != null && Object.hasOwn(value, key);
  const pathID = (path) => JSON.stringify(path);
  const pretty = (path) => path.map((key, i) => /^\d+$/.test(key) ? "[" + key + "]" :
    /^[a-zA-Z_][\w-]*$/.test(key) ? (i ? "." : "") + key : "[" + JSON.stringify(key) + "]").join("");
  const samePath = (a, b) => pathID(a) === pathID(b);
  const get = (value, keys) => keys.reduce((v, k) => own(v, k) ? v[k] : undefined, value);
  function dom(tag, text = "", klass = "") {
    const node = document.createElement(tag);
    node.textContent = text;
    if (klass) node.className = klass;
    return node;
  }
  function action(label, run, klass = "") {
    const button = dom("button", label, klass);
    button.type = "button";
    button.addEventListener("click", run);
    return button;
  }
  function note(name, msg = "", error = false) {
    const node = id(name);
    node.textContent = msg;
    node.hidden = !msg;
    node.classList.toggle("error", error);
  }
  function infer(value) {
    if (Array.isArray(value)) return {type: "array"};
    if (value === null || value === undefined) return {type: "any", nullable: true};
    return {type: typeof value};
  }
  function childSchema(schema, value, key) {
    if (own(schema?.properties, key)) return schema.properties[key];
    if (Array.isArray(value) || schema?.type === "array") return schema?.items || infer(value?.[key]);
    return schema?.additional_properties || infer(own(value,key) ? value[key] : undefined);
  }

  let options, identity, model, baseModel, pending, fieldEditor, lastGood;
  let fileName = "", mode = "fields", folder = [], selected, sequence = 0, timer;
  let busy = false, dirtyInput = false, loaded = false, viewLimit = 80, diagnostic;
  // Keep a draft of the UI as well as the staged source. The saved widget is
  // reconstructed with its original schema when its checkout base has changed.
  const savedViews = new Map();
  let restoring, pendingField, recovered, recoveryEditor;

  function viewSnapshot() {
    if (!identity || !fileName) return;
    if (restoring) return restoring;
    const source = id("source");
    let field = pendingField;
    if (fieldEditor && model && selected) {
      const {schema,value} = nodeAt(selected);
      field = {keys:selected,schema,value,raw:fieldEditor.snapshot(),dirty:dirtyInput,
        message:id("document-field-message").textContent};
    }
    return {base:identity.base,mode,folder,selected,viewLimit,
      query:id("document-search").value,key:id("document-key").value,field,
      recovered:recovered ? {...recovered,raw:recoveryEditor?.snapshot() || recovered.raw} : undefined,
      cursor:{start:source.selectionStart,end:source.selectionEnd,direction:source.selectionDirection,
        top:source.scrollTop,left:source.scrollLeft,focused:document.activeElement===source}};
  }

  window.snapshotDocumentEditor = () => {
    const current = viewSnapshot();
    if (current) savedViews.set(fileName,current);
    return Array.from(savedViews);
  };
  window.restoreDocumentEditor = entries => {
    if (!Array.isArray(entries)) return;
    for (const row of entries) if (Array.isArray(row) && typeof row[0]==="string" && row[1]) savedViews.set(row[0],row[1]);
  };

  function restoreCursor(saved) {
    if (saved && Number.isSafeInteger(saved.start) && Number.isSafeInteger(saved.end)) {
      const source = id("source");
      source.setSelectionRange(saved.start,saved.end,saved.direction);
      source.scrollTop = saved.top || 0; source.scrollLeft = saved.left || 0;
      if (saved.focused && mode==="source") source.focus({preventScroll:true});
      cursor();
    }
  }

  function showRecovered(field) {
    recovered = field;
    recoveryEditor = null;
    id("document-recovery").hidden = !field;
    id("recovery-control").replaceChildren();
    if (!field) return;
    id("recovery-field-path").textContent = pretty(field.keys) + " changed on disk. Copy your saved input before editing the current value.";
    recoveryEditor = createTypedEditor(id("recovery-control"),field.schema,field.value,{restore:field.raw,path:field.keys,label:"Saved input"});
  }
  id("dismiss-recovered-field").addEventListener("click",() => showRecovered(null));

  function stamp(opts) {
    return {path: opts.file?.path || "", base: opts.draft.base_digest, content: opts.draft.content,
      editable: Boolean(opts.file?.editable && opts.file.digest === opts.draft.base_digest)};
  }
  function matches(a, b) {
    return Boolean(a && b && a.path === b.path && a.base === b.base && a.content === b.content && a.editable === b.editable);
  }
  function current() { return identity?.editable && !options.disabled && !busy; }
  function nodeAt(keys) {
    let value = model.value, schema = model.schema;
    for (const key of keys) {
      schema = childSchema(schema, value, key);
      value = own(value, key) ? value[key] : undefined;
    }
    return {value, schema: schema || infer(value)};
  }
  function entries(keys) {
    const {schema, value} = nodeAt(keys);
    if (Array.isArray(value)) return value.map((_, i) => String(i));
    const existing = value && typeof value === "object" ? Object.keys(value) : [];
    return [...new Set([...existing, ...Object.keys(schema.properties || {})])];
  }
  function containerAt(keys) {
    const {schema, value} = nodeAt(keys);
    return schema.type === "object" || schema.type === "array" || (value !== null && typeof value === "object");
  }
  function setMode(next) {
    if (mode === next) return;
    mode = next;
    showMode();
    if (mode === "source") {
      id("source").focus();
      cursor();
    } else if (model) {
      renderNavigation();
      renderField();
    }
  }
  function showMode() {
    const editable = Boolean(options?.file?.editable);
    const raw = !editable || mode === "source";
    id("document-modes").hidden = !editable;
    id("document-source").hidden = !raw;
    id("document-source-tools").hidden = !raw || !current() || !lastGood;
    id("document-fields").hidden = raw || !model;
    id("document-mode-fields").setAttribute("aria-pressed", String(!raw));
    id("document-mode-source").setAttribute("aria-pressed", String(raw));
    if (!raw || !current()) id("document-completions").hidden = true;
  }
  function searchPaths(query) {
    const result = [];
    function walk(keys) {
      if (result.length > viewLimit || keys.length > 32) return;
      for (const key of entries(keys)) {
        const path = [...keys, key];
        if (pretty(path).toLowerCase().includes(query)) result.push(path);
        if (result.length > viewLimit) return;
        if (containerAt(path)) walk(path);
      }
    }
    walk([]);
    return result;
  }
  function confirmLeave() {
    return !dirtyInput || window.confirm("Discard the unstaged input for this field?");
  }
  function choose(keys, browse = false) {
    if (!browse && selected && samePath(keys,selected)) return;
    if (!confirmLeave()) return;
    dirtyInput = false; pendingField=null;
    if (browse) {
      folder = [...keys];
      selected = undefined;
      viewLimit = 80;
      id("document-search").value = "";
    } else selected = [...keys];
    renderNavigation();
    renderField();
  }
  function renderNavigation() {
    if (!model) return;
    const query = id("document-search").value.trim().toLowerCase();
    const target = id("document-keys"), crumb = id("document-breadcrumb");
    target.replaceChildren(); crumb.replaceChildren();
    const home = action("Document", () => choose([], true));
    home.setAttribute("aria-label", "Browse document fields");
    crumb.append(home);
    folder.forEach((key, i) => {
      crumb.append(dom("span", "›"), action(key, () => choose(folder.slice(0, i + 1), true)));
    });
    const rows = query ? searchPaths(query) : entries(folder).map((key) => [...folder, key]);
    if (!selected) selected = rows[0] || [...folder];
    const list = nodeAt(folder).schema.type === "array" || Array.isArray(nodeAt(folder).value);
    if ((folder.length || list) && !query) {
      const group = dom("div", "", "document-key-row");
      const btn = action(list ? "Edit list" : "Edit this section", () => choose(folder));
      btn.setAttribute("aria-pressed", String(samePath(selected, folder)));
      group.append(btn); target.append(group);
    }
    for (const path of rows.slice(0,viewLimit)) {
      const row = dom("div", "", "document-key-row");
      const text = query ? pretty(path) : path.at(-1);
      const button = action(text, () => choose(path));
      button.setAttribute("aria-pressed", String(samePath(selected,path)));
      if (nodeAt(path).value === undefined) button.append(dom("small","Unset"));
      row.append(button);
      if (containerAt(path)) {
        const open = action("›", () => choose(path,true));
        open.setAttribute("aria-label","Browse " + pretty(path));
        row.append(open);
      }
      target.append(row);
    }
    if (rows.length > viewLimit) target.append(action("Show more fields", () => { viewLimit += 80; renderNavigation(); }, "button secondary small"));
    if (!rows.length) target.append(dom("p",query ? "No matching fields." : "No fields in this section.", "muted"));
    const {schema, value} = nodeAt(folder);
    const array = Array.isArray(value) || schema.type === "array";
    id("document-add").hidden = Boolean(query) || (!array && !schema.additional_properties) || !current();
    id("document-key-label").hidden = array;
    id("document-add-button").textContent = array ? "Add item" : "Add property";
    id("document-key-names").replaceChildren(...(schema.key_suggestions || []).filter((k) => !own(value,k)).map((k) => {
      const option = dom("option"); option.value = k; return option;
    }));
  }
  function renderField() {
    const control = id("document-control");
    if (!model || !selected) return;
    control.replaceChildren();
    const {value, schema} = nodeAt(selected);
    const name = selected.at(-1) || "Document";
    id("document-field-name").textContent = name;
    const meta = [pretty(selected), schema.type, schema.required ? "Required" : "",
      value === undefined ? "Absent from source" : ""].filter(Boolean);
    id("document-field-path").textContent = meta.join(" · ");
    note("document-field-message");
    dirtyInput = false;
    const locked = !current() || schema.constraint?.lock === true;
    id("document-actions").hidden = locked;
    for (const button of id("document-actions").querySelectorAll("button")) button.disabled = locked;
    id("document-remove").hidden = !selected.length || schema.required || value === undefined;
    const original = baseModel && get(baseModel.value,selected);
    id("document-reset").hidden = !baseModel || JSON.stringify(original) === JSON.stringify(value);
    const location = "/" + selected.map(k => k.replaceAll("~","~0").replaceAll("/","~1")).join("/");
    const pos = model.positions?.[selected.length ? location : ""];
    if (pos?.line > 0) {
      id("document-field-path").append(document.createTextNode(" · "), action("Line " + pos.line, () => {
        if (!confirmLeave()) return;
        dirtyInput = false;
        setMode("source");
        const source = id("source");
        const start = source.value.split("\n").slice(0,pos.line-1).reduce((n,line) => n + line.length + 1, 0);
        const offset = Math.min(source.value.length,start + Math.max(0,pos.column-1));
        source.setSelectionRange(offset,offset); cursor();
      }, "document-line-link"));
    }
    // Inline settings depend on the selected provider, so changing a selector
    // uses the same staged switch flow as the Providers page. A whole-section
    // object editor would keep a stale settings type alongside a new selector.
    const roleNames = ["code_hosting","task_management","knowledge_base","secops","notifications"];
    const nestedRole = selected[0] === "devops" && ["checks","deployments","releases"].includes(selected[1]);
    const roleSize = nestedRole ? 2 : roleNames.includes(selected[0]) ? 1 : 0;
    const rolePath = selected.slice(0,roleSize);
    const isCore = fileName === ".devflow/config.yml";
    const isRole = isCore && roleSize > 0 && (selected.length === roleSize ||
      (selected.length === roleSize + 1 && selected.at(-1) === "provider"));
    if (isRole || (isCore && selected.length === 1 && selected[0] === "devops")) {
      id("document-actions").hidden = true;
      fieldEditor = null;
      if (isRole) {
        control.append(dom("p","Stage a provider switch in Providers to load its settings.", "muted"));
        control.append(action("Manage provider", () => options.openProvider?.(rolePath.join(".")), "button secondary small"));
      }
      if (containerAt(selected)) control.append(action("Browse section fields",() => choose(selected,true), "button secondary small"));
      return;
    }
    if (!selected.length && !Array.isArray(value) && containerAt([]) && entries([]).length) {
      id("document-actions").hidden = true;
      fieldEditor = null;
      control.append(dom("p","Select a field to edit it.", "muted"));
      return;
    }
    const preserved = pendingField && samePath(pendingField.keys,selected) ? pendingField : null;
    fieldEditor = createTypedEditor(control, schema, value, {
      restore:preserved?.raw,path: [...selected], label: name, suggestions: options.suggestions, allowed: schema.constraint?.allow
    });
    if (preserved) {
      dirtyInput=Boolean(preserved.dirty);
      if (preserved.message) note("document-field-message",preserved.message,true);
      pendingField=null;
    }
    if (locked) {
      for (const input of control.querySelectorAll("input,select,textarea,button")) input.disabled = true;
      if (schema.constraint?.lock === true) note("document-field-message","This value is locked by its configuration policy.");
    }
  }
  function retainPath() {
    while (folder.length && !containerAt(folder)) folder.pop();
    if (selected && !samePath(selected, folder)) {
      const parent = selected.slice(0, -1), key = selected.at(-1);
      const {value, schema} = nodeAt(parent);
      if (!own(value,key) && !own(schema.properties,key)) selected = undefined;
    }
  }
  async function load(revision, ref) {
    try {
      const info = await options.request("api/document", {path: ref.path, base_digest: ref.base, content: ref.content});
      if (revision !== sequence || !matches(identity,ref)) return;
      model = info;
      lastGood = info;
      if (ref.content === options.file.content) baseModel = info;
      note("document-message");
      if (!restoring) retainPath();
      const savedCursor = restoring?.cursor;
      restoring = null;
      renderNavigation(); renderField(); showMode(); restoreCursor(savedCursor);
      if (!baseModel && ref.content !== options.file.content) {
        const base = await options.request("api/document",{path: ref.path,base_digest: ref.base,content: options.file.content}).catch(() => null);
        if (revision === sequence && base) {
          baseModel = base;
          if (!dirtyInput) renderField();
        }
      }
    } catch (e) {
      if (revision !== sequence) return;
      model = null;
      const savedCursor = restoring?.cursor;
      if (pendingField?.dirty) showRecovered(pendingField);
      restoring = null; pendingField = null;
      mode = "source";
      const source = id("source");
      diagnostic = /(?:line\s+|at line\s*)(\d+)(?:[,: ]+column\s+(\d+))?/i.exec(e.message);
      note("document-message",e.message,true);
      if (diagnostic) id("document-message").append(document.createTextNode(" "), action("Go to line " + diagnostic[1],() => {
        source.focus();
        const lines = source.value.split("\n");
        const line = Math.min(lines.length,Math.max(1,Number(diagnostic[1])));
        const start = lines.slice(0,line-1).reduce((n,s) => n+s.length+1,0);
        source.setSelectionRange(start,start + lines[line-1].length);
        cursor();
      },"button secondary small"));
      showMode(); restoreCursor(savedCursor);
    }
  }
  window.renderDocumentEditor = (opts) => {
    options = opts;
    const next = stamp(opts);
    const visible = !id("page-configuration").hidden;
    const lockChanged = identity && options.disabled !== identity.disabled;
    next.disabled = Boolean(opts.disabled);
    if (matches(identity,next)) {
      identity.disabled = next.disabled;
      if (lockChanged && model) { renderNavigation(); renderField(); }
      showMode();
      if (!visible || loaded || !next.editable) return;
    }
    const revision = ++sequence;
    id("document-completions").hidden = true;
    clearTimeout(timer);
    const changedFile = next.path !== fileName || next.base !== identity?.base;
    if (changedFile) {
      const previous=viewSnapshot();
      if (previous && fileName) savedViews.set(fileName,previous);
      fileName=next.path;
      restoring=savedViews.get(fileName);
      folder=Array.isArray(restoring?.folder) ? restoring.folder : [];
      selected=Array.isArray(restoring?.selected) ? restoring.selected : undefined;
      baseModel=null; lastGood=null; dirtyInput=false;
      viewLimit=Number.isSafeInteger(restoring?.viewLimit) ? restoring.viewLimit : 80;
      id("document-search").value=restoring?.query || "";
      id("document-key").value=restoring?.key || "";
      mode=restoring?.mode==="source" ? "source" : "fields";
      const stale=restoring && restoring.base!==opts.file?.digest;
      pendingField=stale ? null : restoring?.field;
      showRecovered(stale && restoring.field?.dirty ? restoring.field : restoring?.recovered);
      if(stale) restoring={...restoring,field:null,recovered};
    }
    identity = next;
    loaded = visible;
    model = null;
    diagnostic = null;
    pending = null;
    busy = false;
    fieldEditor = null;
    note("document-message");
    showMode();
    if (!next.editable) {
      if (opts.file?.editable) mode="source";
      const savedCursor=restoring?.cursor;
      restoring=null;
      showMode(); restoreCursor(savedCursor); return;
    }
    if (!visible) return;
    if (mode === "fields") note("document-message","Loading fields…");
    timer = setTimeout(() => load(revision,next), mode === "source" ? 240 : 0);
  };

  async function stageField(operation) {
    if (!current() || !model || !selected) return;
    const ref = identity, keys = [...selected], field = nodeAt(keys);
    let value;
    try {
      if (operation === "restore") {
        value = get(baseModel.value,keys);
        operation = value === undefined ? "remove" : "set";
      } else if (operation === "set") {
        value = fieldEditor.read();
        if (value === undefined) throw new Error("Set a value before staging this field.");
      } else if (field.schema.required) throw new Error("A required field needs a value.");
      busy = true;
      const token = {};
      pending = token;
      for (const button of id("document-actions").querySelectorAll("button")) button.disabled = true;
      note("document-field-message","Staging the field…");
      const payload={path:ref.path, content:ref.content, base_digest:ref.base, keys, operation};
      if (operation === "set") payload.value=value;
      const out=await options.request("api/document/edit",payload);
      if (!matches(ref,identity) || pending !== token || options.disabled) return;
      if (typeof out.content !== "string") throw new Error("The server returned no editable source.");
      dirtyInput=false;
      options.stage(out);
    } catch (e) {
      if (matches(ref,identity)) note("document-field-message",e.message,true);
    } finally {
      if (matches(ref,identity)) {
        busy=false;
        for (const button of id("document-actions").querySelectorAll("button")) button.disabled = Boolean(options.disabled);
      }
    }
  }
  id("document-mode-fields").addEventListener("click",() => { if (!model) { mode="fields"; loaded=false; renderDocumentEditor(options); } else setMode("fields"); });
  id("document-mode-source").addEventListener("click",() => { if (dirtyInput && !confirmLeave()) return; dirtyInput=false; setMode("source"); });
  id("document-search").addEventListener("input",() => { viewLimit=80; renderNavigation(); });
  id("document-control").addEventListener("input",() => { dirtyInput=true; });
  id("document-control").addEventListener("change",() => { dirtyInput=true; });
  id("document-set").addEventListener("click",() => stageField("set"));
  id("document-remove").addEventListener("click",() => stageField("remove"));
  id("document-reset").addEventListener("click",() => stageField("restore"));
  id("document-add").addEventListener("submit",(event) => {
    event.preventDefault();
    if (!model || !current()) return;
    const {value,schema}=nodeAt(folder), array=Array.isArray(value) || schema.type === "array";
    const key = array ? String(Array.isArray(value) ? value.length : 0) : id("document-key").value;
    if (!array && !key.trim()) { id("document-key").focus(); return; }
    if (!array && own(value,key)) { choose([...folder,key]); return; }
    if (confirmLeave()) {
      dirtyInput=false; selected=[...folder,key]; renderNavigation(); renderField();
      id("document-key").value="";
      id("document-control").querySelector("button,input,select,textarea")?.focus();
    }
  });
  window.documentEditorFocus = () => {
    if (mode === "source" || !model) id("source").focus();
    else (id("document-control").querySelector("input,select,textarea,button") || id("document-search")).focus();
  };

  function cursor() {
    const input=id("source"), prefix=input.value.slice(0,input.selectionStart);
    const line=prefix.split("\n").length, column=prefix.length-prefix.lastIndexOf("\n");
    id("document-cursor").textContent="Line " + line + " · column " + column;
  }
  function schemaAt(doc, keys) {
    let schema=doc.schema, value=doc.value;
    for (const key of keys) {
      schema=childSchema(schema,value,key);
      value=own(value,key) ? value[key] : undefined;
    }
    return {schema,value};
  }

  // Resolve a JSON cursor using its enclosing object/array stack. Incomplete
  // quoted keys and values are expected while the developer is typing.
  function jsonContext(source, caret) {
    const stack=[];
    let initial=true;
    const token=(frame,start,end,prefix) => {
      if (!frame) return initial ? {keys:[],value:true,start,end,prefix} : null;
      if (frame.kind === "object" && frame.phase === "key") return {keys:frame.keys,value:false,start,end,prefix};
      if (frame.phase === "value") return {keys:[...frame.keys,frame.kind === "array" ? String(frame.index) : frame.key],value:true,start,end,prefix};
      return null;
    };
    for (let i=0; i<caret;) {
      const ch=source[i], frame=stack.at(-1);
      if (/\s/.test(ch)) { i++; continue; }
      if (ch === "{" || ch === "[") {
        const at=token(frame,i,i,"");
        if (!at?.value) return null;
        if (frame) frame.phase="after";
        initial=false;
        stack.push({keys:at.keys,kind:ch === "{" ? "object" : "array",phase:ch === "{" ? "key" : "value",index:0});
        i++; continue;
      }
      if (ch === "}" || ch === "]") { if (!frame) return null; stack.pop(); i++; continue; }
      if (ch === ":") { if (!frame || frame.phase !== "colon") return null; frame.phase="value"; i++; continue; }
      if (ch === ",") { if (!frame || frame.phase !== "after") return null; frame.index++; frame.phase=frame.kind === "object" ? "key" : "value"; i++; continue; }
      const start=i;
      let text, closed=true;
      if (ch === '"') {
        i++; closed=false;
        while (i<source.length && source[i] !== "\n") {
          if (source[i] === '"') { i++; closed=true; break; }
          i+=source[i] === "\\" ? 2 : 1;
        }
        if (i>=caret) return token(frame,start,i,source.slice(start+1,Math.min(caret,closed ? i-1 : i)));
        if (!closed) return null;
        try { text=JSON.parse(source.slice(start,i)); } catch {return null;}
      } else {
        while (i<source.length && /[a-zA-Z0-9_.+-]/.test(source[i])) i++;
        if (i===start) return null;
        if (i>=caret) return token(frame,start,i,source.slice(start,caret));
      }
      if (frame?.phase === "key") { if (text===undefined) return null; frame.key=text; frame.phase="colon"; }
      else if (frame?.phase === "value") frame.phase="after";
      else return null;
    }
    return token(stack.at(-1),caret,caret,"");
  }

  // Offer YAML completions for block mapping keys and scalar values. Leave
  // flow-style YAML, block strings and multi-line quotes to the source editor.
  function yamlContext(source,caret) {
    const start=caret ? source.lastIndexOf("\n",caret-1)+1 : 0;
    const newline=source.indexOf("\n",caret), end=newline<0 ? source.length : newline;
    const line=source.slice(start,end), prefix=source.slice(start,caret);
    const indent=prefix.match(/^ */)[0].length, stack=[];
    const mapKey=/^( *)([A-Za-z_][\w.-]*|'[^']+'|"(?:\\.|[^"\\])*")\s*:(?:\s+(.*)|\s*)$/;
    const keyName=(key) => {
      if (key.startsWith('"')) { try {return JSON.parse(key);} catch {return key;} }
      return key.startsWith("'") ? key.slice(1,-1).replaceAll("''","'") : key;
    };
    for (const old of source.slice(0,start).split("\n")) {
      if (!old.trim() || /^\s*#/.test(old)) continue;
      const depth=old.match(/^ */)[0].length;
      while (stack.length && stack.at(-1).indent>=depth) stack.pop();
      const m=mapKey.exec(old);
      if (m && (!m[3] || /^\s*#/.test(m[3]))) stack.push({indent:depth,key:keyName(m[2])});
      else if (/^\s*-|:\s*[|>]/.test(old)) stack.push({indent:depth,unsupported:true});
    }
    while (stack.length && stack.at(-1).indent>=indent) stack.pop();
    if (stack.some((p) => p.unsupported)) return null;
    const keys=stack.map((part) => part.key);
    const keyOnly=/^ *([A-Za-z_][\w.-]*)?$/.exec(prefix);
    if (keyOnly) {
      const rest=line.slice(indent).match(/^[\w.-]*/)[0];
      return {keys,value:false,start:start+indent,end:start+indent+rest.length,prefix:keyOnly[1] || ""};
    }
    const scalar=/^ *([A-Za-z_][\w.-]*|'[^']+'|"(?:\\.|[^"\\])*")\s*:\s*(["']?)([\w./ -]*)$/.exec(prefix);
    if (!scalar) return null;
    const tail=line.slice(line.indexOf(":")+1), leading=tail.match(/^ */)[0].length;
    const from=start+line.indexOf(":")+1+leading;
    const rest=source.slice(from,end).match(/^(?:"(?:\\.|[^"\\])*"?|'[^']*'?|[^#,[\]{}]*)/)[0].trimEnd();
    return {keys:[...keys,keyName(scalar[1])],value:true,start:from,end:from+rest.length,prefix:scalar[3]};
  }

  function showSuggestions() {
    if (!lastGood || !current()) return;
    const input=id("source"), source=input.value, caret=input.selectionStart;
    const json=lastGood.format === "json";
    const at=json ? jsonContext(source,caret) : yamlContext(source,caret);
    const container=id("document-completions");
    container.replaceChildren(); container.hidden=false;
    let candidates=[];
    if (at) {
      const {schema,value}=schemaAt(lastGood,at.keys);
      if (at.value) {
        const hints=options.suggestions?.(at.keys,schema) || [];
        const values=schema.constraint?.allow || schema.enum || (schema.type === "boolean" ? [true,false] : [...(schema.suggestions || []),...hints]);
        candidates=[...values,...(schema.nullable ? [null] : [])];
      } else candidates=[...Object.keys(schema.properties || {}),...(schema.key_suggestions || [])].filter((k) => !own(value,k) || (at.prefix && k.toLowerCase().startsWith(at.prefix.toLowerCase())));
      candidates=[...new Set(candidates)].filter((v) => (v===null || ["string","number","boolean"].includes(typeof v)) &&
        String(v).toLowerCase().startsWith(at.prefix.toLowerCase()));
    }
    if (!candidates.length) {
      container.append(dom("p","No suggested keys or values at this position. Browse Fields to see the document schema.","muted"));
      return;
    }
    for (const value of candidates.slice(0,40)) {
      const choice=action(String(value),() => {
        if (input.value!==source || !current()) return;
        let replacement;
        if (!at.value) {
          const key = !json && /^[A-Za-z_][\w.-]*$/.test(value) ? value : JSON.stringify(value);
          replacement=key + (/^\s*:/.test(source.slice(at.end)) ? "" : ": ");
        } else replacement=JSON.stringify(value);
        input.setRangeText(replacement,at.start,at.end,"end");
        input.focus(); input.dispatchEvent(new Event("input",{bubbles:true}));
        cursor(); container.hidden=true;
      },"button secondary small");
      container.append(choice);
    }
  }
  id("document-complete").addEventListener("click",showSuggestions);
  id("source").addEventListener("keydown",(event) => {
    if (event.ctrlKey && event.code==="Space") { event.preventDefault(); showSuggestions(); }
    if (event.key==="Escape") id("document-completions").hidden=true;
  });
  for (const event of ["click","keyup","input","select"]) id("source").addEventListener(event,cursor);
})();
