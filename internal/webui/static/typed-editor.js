"use strict";
(() => {
  let nextID = 0;
  const id = () => "typed-editor-" + ++nextID;
  const own = (object, key) => Object.hasOwn(object || {}, key);
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value);

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function button(text, action) {
    const node = element("button", "te-button", text);
    node.type = "button";
    node.addEventListener("click", action);
    return node;
  }
  function group(parent, name) {
    const box = element("fieldset", "te-group");
    box.append(element("legend", "te-label", name));
    parent.append(box);
    return box;
  }
  function appendChoice(select, key, label) {
    const opt = element("option", "", label);
    opt.value = key;
    select.append(opt);
    return opt;
  }
  function matches(a, b) {
    if (a === b) return true;
    if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((v, i) => matches(v, b[i]));
    if (!record(a) || !record(b)) return false;
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every(k => own(b, k) && matches(a[k], b[k]));
  }
  function typeOf(value) {
    if (value === null) return "null";
    if (Array.isArray(value)) return "array";
    return typeof value;
  }
  function hints(input, host, get) {
    const list = element("datalist");
    list.id = id();
    host.append(list);
    function refresh() {
      const values = (get() || []).filter(v => ["string", "number", "boolean"].includes(typeof v));
      list.replaceChildren();
      for (const value of new Set(values.map(String))) appendChoice(list, value, value);
      if (list.children.length) input.setAttribute("list", list.id);
      else input.removeAttribute("list");
    }
    refresh();
    input.addEventListener("focus", refresh);
  }
  function clearError(control) {
    if (!control?.dataset?.teError) return;
    const errorID = control.dataset.teError;
    document.getElementById(errorID)?.remove();
    const descriptions = (control.getAttribute("aria-describedby") || "").split(" ").filter(i => i && i !== errorID);
    if (descriptions.length) control.setAttribute("aria-describedby", descriptions.join(" "));
    else control.removeAttribute("aria-describedby");
    delete control.dataset.teError;
    control.removeAttribute("aria-invalid");
  }
  function showError(control, message) {
    clearError(control);
    const error = element("p", "te-error", message);
    error.id = id();
    error.setAttribute("role", "alert");
    control.after(error);
    control.dataset.teError = error.id;
    control.setAttribute("aria-invalid", "true");
    control.setAttribute("aria-describedby", [control.getAttribute("aria-describedby"), error.id].filter(Boolean).join(" "));
  }
  function fail(control, message) {
    showError(control, message);
    control.focus();
    throw new Error(message);
  }
  function variants(schema) {
    if (Array.isArray(schema.one_of) && schema.one_of.length) return schema.one_of;
    if (schema.type && schema.type !== "any") return [schema];
    // Unconstrained map entries still get structured controls for every JSON type.
    return [
      {type: "string"}, {type: "number"}, {type: "boolean"}, {type: "object", additional_properties: {type: "any"}},
      {type: "array", items: {type: "any"}}, {type: "null"}
    ];
  }

  window.createTypedEditor = function createTypedEditor(container, schema, value, options = {}) {
    const root = element("div", "te-editor");
    const live = element("div", "te-sr");
    live.setAttribute("role", "status");
    live.setAttribute("aria-live", "polite");
    root.append(live);
    container.replaceChildren(root);
    const changed = message => {
      live.textContent = message;
      root.dispatchEvent(new Event("input", {bubbles: true}));
    };
    root.addEventListener("input", event => clearError(event.target));
    root.addEventListener("change", event => clearError(event.target));

    function node(host, field, original, label, path, allowed, optionalRoot = false, saved) {
      field = field || {};
      const box = group(host, label);
      const constraint = field.constraint || {};
      if (allowed === undefined) allowed = constraint.allow;
      if (constraint.lock === true) {
        box.disabled = true;
        box.append(element("p", "te-hint", "Locked by release configuration."));
      } else if (constraint.lock === "append") {
        box.append(element("p", "te-hint", "The release’s required entries must stay in this list."));
      }
      if (constraint.pattern) box.append(element("p", "te-hint", "The value must match " + constraint.pattern));
      const types = variants(field);
      const actual = typeOf(original);
      const initialType = types.find(v => v.type === actual || (actual === "number" && v.type === "integer")) || types[0];
      const choices = types.filter(v => v.type !== "null");
      const nullable = field.nullable || choices.length !== types.length;
      let kind = choices.indexOf(initialType);
      if (kind < 0) kind = 0;
      const explicitUnset = optionalRoot && (original === undefined || options.optional === true);
      let mode = original === undefined && explicitUnset ? "unset" : original === null && nullable ? "null" : String(kind);
      if (!choices.length && mode !== "unset") mode = "null";
      let picker;
      if (nullable || explicitUnset || choices.length > 1) {
        picker = element("select", "te-mode");
        picker.setAttribute("aria-label", label + " value type");
        if (explicitUnset) appendChoice(picker, "unset", "Unset");
        if (nullable) appendChoice(picker, "null", "Null");
        choices.forEach((choice, i) => {
          const names = {array: "List", object: "Object", string: "Text", number: "Number", integer: "Integer", boolean: "Boolean"};
          appendChoice(picker, String(i), choices.length === 1 ? "Set value" : names[choice.type] || choice.type);
        });
        picker.value = mode;
        box.append(picker);
      }
      if (picker && Array.from(picker.options).some(option => option.value === saved?.mode)) {
        mode = picker.value = saved.mode;
      }
      // Keep inactive alternatives mounted. Switching a nullable or mixed field
      // back to a previous type restores its unsaved edits, including invalid text.
      const bodies = new Map();
      function show() {
        for (const body of bodies.values()) {
          body.host.hidden = true;
          body.host.disabled = true;
        }
        if (mode === "null" || mode === "unset") return;
        if (!bodies.has(mode)) {
          const panel = element("fieldset", "te-body");
          box.append(panel);
          const selected = choices[Number(mode)];
          const initial = selected === initialType ? original : undefined;
          const previous = saved?.bodies?.[mode];
          let edit;
          if (selected.type === "array") edit = array(panel, selected, initial, path, allowed, previous);
          else if (selected.type === "object") edit = object(panel, selected, initial, path, previous);
          else edit = scalar(panel, selected, initial, label, path, allowed, previous);
          bodies.set(mode, {host: panel, edit});
        }
        const active = bodies.get(mode).host;
        active.hidden = false;
        active.disabled = false;
      }
      show();
      picker?.addEventListener("change", () => { mode = picker.value; show(); });
      return {
        snapshot() {
          const visited = {...saved?.bodies};
          for (const [key, body] of bodies) visited[key] = body.edit.snapshot();
          return {mode, bodies: visited};
        },
        focus() { if (picker) picker.focus(); else bodies.get(mode)?.edit.focus(); },
        read() {
          if (constraint.lock === true) return original;
          if (mode === "unset") return undefined;
          const result = mode === "null" ? null : bodies.get(mode).edit.read();
          if (Array.isArray(allowed)) {
            const values = Array.isArray(result) ? result : [result];
            if (!values.every(v => allowed.some(a => matches(a, v)))) {
              fail(picker || box.querySelector("input,select,button"), label + " must use a permitted value.");
            }
          }
          return result;
        }
      };
    }

    function scalar(host, field, initial, label, path, allowed, saved) {
      const type = field.type;
      const enumeration = Array.isArray(field.enum) ? field.enum : type === "boolean" ? [true, false] : null;
      let choices = enumeration;
      if (Array.isArray(allowed)) choices = allowed.filter(v => typeOf(v) === type || (type === "integer" && Number.isSafeInteger(v)))
        .filter(v => !enumeration || enumeration.some(e => matches(e, v)));
      if (choices) {
        const input = element("select");
        input.setAttribute("aria-label", label);
        appendChoice(input, "", "Choose a value");
        choices.forEach((v, i) => appendChoice(input, String(i), v === "" ? "Empty string" : String(v)));
        const index = choices.findIndex(v => matches(v, initial));
        if (index >= 0) input.value = String(index);
        else if (initial !== null && initial !== undefined) {
          appendChoice(input, "", "Current value outside allowed choices (" + String(initial) + ")").selected = true;
        }
        if (saved && typeof saved.choice === "string") input.value = saved.choice;
        host.append(input);
        return {snapshot: () => ({choice: input.value}), focus: () => input.focus(), read: () => {
          if (input.value === "") fail(input, "Choose a permitted value for " + label + ".");
          return choices[Number(input.value)];
        }};
      }
      if (type === "number" || type === "integer") {
        const input = element("input");
        // Native number inputs erase incomplete text such as "1e" from .value.
        // Keep the raw edit so switching worktrees can restore it character for character.
        input.type = "text";
        input.inputMode = type === "integer" ? "numeric" : "decimal";
        input.setAttribute("role", "spinbutton");
        if (field.nonnegative) input.setAttribute("aria-valuemin", "0");
        input.setAttribute("aria-label", label);
        input.value = typeof saved?.raw === "string" ? saved.raw : typeof initial === "number" ? String(initial) : "";
        const numericValue = () => /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(input.value) ? Number(input.value) : NaN;
        function syncNumber() {
          const n = numericValue();
          if (Number.isFinite(n)) input.setAttribute("aria-valuenow", String(n));
          else input.removeAttribute("aria-valuenow");
        }
        input.addEventListener("input", syncNumber);
        input.addEventListener("keydown", event => {
          if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
          event.preventDefault();
          const n = numericValue();
          let next = (Number.isFinite(n) ? n : 0) + (event.key === "ArrowUp" ? 1 : -1);
          if (field.nonnegative) next = Math.max(next, 0);
          input.value = String(next);
          input.dispatchEvent(new Event("input", {bubbles: true}));
        });
        syncNumber();
        host.append(input);
        hints(input, host, () => [...(field.suggestions || []), ...(options.suggestions?.(path(), field) || [])]);
        return {snapshot: () => ({raw: input.value}), focus: () => input.focus(), read: () => {
          const n = numericValue();
          if (!Number.isFinite(n) || (type === "integer" && !Number.isSafeInteger(n)) || (field.nonnegative && n < 0)) {
            const expected = (field.nonnegative ? "nonnegative " : "") + (type === "integer" ? "whole number" : "number");
            fail(input, "Enter a " + expected + " for " + label + ".");
          }
          return n;
        }};
      }
      const text = initial == null ? "" : String(initial);
      const input = element("input");
      input.type = "text";
      const multiline = element("textarea");
      multiline.rows = 3;
      input.setAttribute("aria-label", label);
      multiline.setAttribute("aria-label", label);
      let expanded = /[\r\n]/.test(text);
      (expanded ? multiline : input).value = text;
      const toggle = button("Multiple lines", () => {
        if (expanded && /[\r\n]/.test(multiline.value)) {
          // A single-line input would silently drop the user's line breaks.
          multiline.focus();
          live.textContent = "Remove the line breaks to switch to single-line editing.";
          return;
        }
        expanded = !expanded;
        (expanded ? multiline : input).value = (expanded ? input : multiline).value;
        sync();
        (expanded ? multiline : input).focus();
      });
      toggle.classList.add("te-text-mode");
      function sync() {
        input.hidden = expanded;
        multiline.hidden = !expanded;
        toggle.setAttribute("aria-pressed", String(expanded));
      }
      host.append(input, multiline, toggle);
      hints(input, host, () => [...(field.suggestions || []), ...(options.suggestions?.(path(), field) || [])]);
      sync();
      // Textareas normalize line endings. Keep untouched source strings exact.
      const baseline = (expanded ? multiline : input).value;
      if (typeof saved?.raw === "string") {
        expanded = Boolean(saved.multiline) || /[\r\n]/.test(saved.raw);
        (expanded ? multiline : input).value = saved.raw;
        sync();
      }
      return {snapshot: () => ({raw: (expanded ? multiline : input).value, multiline: expanded}),
        focus: () => (expanded ? multiline : input).focus(), read: () => {
        const current = (expanded ? multiline : input).value;
        return current === baseline ? text : current;
      }};
    }

    function array(host, field, initial, path, allowed, saved) {
      const rows = [];
      const list = element("div", "te-items");
      const empty = element("p", "te-empty", "Empty list");
      const add = button("Add item", () => {
        const row = insert(undefined);
        refresh();
        row.child.focus();
        changed("Added item " + rows.length + ".");
      });
      host.append(list, empty, add);
      function refresh() {
        empty.hidden = rows.length > 0;
        rows.forEach((row, i) => {
          row.title.textContent = "Item " + (i + 1);
          row.up.disabled = i === 0;
          row.down.disabled = i === rows.length - 1;
          for (const [action, text] of [[row.up, "Move item " + (i + 1) + " up"], [row.down, "Move item " + (i + 1) + " down"], [row.remove, "Remove item " + (i + 1)]]) {
            action.setAttribute("aria-label", text);
          }
        });
      }
      function move(row, direction) {
        const from = rows.indexOf(row), to = from + direction;
        if (to < 0 || to >= rows.length) return;
        if (direction < 0) list.insertBefore(row.box, rows[to].box);
        else list.insertBefore(rows[to].box, row.box);
        rows.splice(from, 1); rows.splice(to, 0, row);
        refresh();
        const action = direction < 0 ? row.up : row.down;
        (action.disabled ? (direction < 0 ? row.down : row.up) : action).focus();
        changed("Moved item " + (from + 1) + " to position " + (to + 1) + ".");
      }
      function insert(value, sourceIndex, previous) {
        const row = {sourceIndex, box: element("div", "te-item")};
        rows.push(row);
        list.append(row.box);
        const bar = element("div", "te-rowbar");
        row.title = element("span", "te-rowname");
        row.title.id = id();
        row.box.setAttribute("role", "group");
        row.box.setAttribute("aria-labelledby", row.title.id);
        row.up = button("Move up", () => move(row, -1));
        row.down = button("Move down", () => move(row, 1));
        row.remove = button("Remove", () => {
          const i = rows.indexOf(row);
          rows.splice(i, 1); row.box.remove(); refresh();
          const next = rows[Math.min(i, rows.length - 1)];
          if (next) next.child.focus(); else add.focus();
          changed("Removed item " + (i + 1) + ".");
        });
        bar.append(row.title, row.up, row.down, row.remove);
        row.box.append(bar);
        row.child = node(row.box, field.items, value, "Value", () => [...path(), rows.indexOf(row)], allowed, false, previous);
        return row;
      }
      if (Array.isArray(saved?.rows)) {
        for (const row of saved.rows) insert(initial?.[row.sourceIndex], row.sourceIndex, row.child);
      } else if (Array.isArray(initial)) {
        initial.forEach((item, index) => insert(item, index));
      }
      refresh();
      return {
        snapshot: () => ({rows: rows.map(row => ({sourceIndex: row.sourceIndex, child: row.child.snapshot()}))}),
        focus: () => add.focus(), read: () => rows.map(row => row.child.read())
      };
    }

    function object(host, field, initial, path, saved) {
      const props = field.properties || {};
      const rows = [];
      const list = element("div", "te-items");
      const empty = element("p", "te-empty", "Empty object");
      const tools = element("div", "te-object-actions");
      const name = element("select");
      name.setAttribute("aria-label", "Field to add");
      const add = button("Add field", () => {
        const row = insert(name.value, undefined, true);
        refresh(); row.child.focus(); changed("Added field " + row.key + ".");
      });
      const openMap = Boolean(field.additional_properties);
      const addEntry = button("Add entry", () => {
        const row = insert("", undefined, false);
        refresh(); row.input.focus(); changed("Added a map entry.");
      });
      tools.append(name, add);
      if (openMap) tools.append(addEntry);
      host.append(list, empty, tools);
      function refresh() {
        empty.hidden = rows.length > 0;
        const available = Object.keys(props).filter(k => props[k].constraint?.lock !== true && !rows.some(r => r.fixed && r.key === k));
        name.replaceChildren();
        for (const key of available) appendChoice(name, key, key + (props[key].required === true ? " (required)" : ""));
        name.hidden = add.hidden = available.length === 0;
      }
      function insert(key, value, fixed, originalKey, previous) {
        const row = {key, fixed, originalKey, emptyKeyExists: originalKey === "" && value !== undefined, box: element("div", "te-entry")};
        rows.push(row); list.append(row.box);
        const actions = element("div", "te-entry-actions");
        const required = fixed && (props[key].required === true || (Array.isArray(field.required) && field.required.includes(key)));
        const remove = button("Remove", () => {
          const i = rows.indexOf(row);
          rows.splice(i, 1); row.box.remove(); refresh();
          if (fixed) { name.value = key; name.focus(); }
          else if (openMap) addEntry.focus();
          else rows[Math.min(i, rows.length - 1)]?.child.focus();
          changed("Removed " + (fixed ? "field " + key : "map entry") + ".");
        });
        remove.setAttribute("aria-label", fixed ? "Remove field " + key : "Remove entry");
        if (!fixed) {
          const keyLabel = element("label", "te-key", "Key");
          row.input = element("input");
          row.input.type = "text"; row.input.value = key;
          row.input.addEventListener("input", () => { row.key = row.input.value; });
          keyLabel.append(row.input); actions.append(keyLabel);
          hints(row.input, row.box, () => [...(field.key_suggestions || []), ...(options.keySuggestions?.(path(), field) || [])]);
        }
        if (!required && !(fixed && props[key].constraint?.lock === true)) actions.append(remove);
        if (actions.children.length) row.box.append(actions);
        const childField = fixed ? props[key] : field.additional_properties;
        row.child = node(row.box, childField, value, fixed ? key : "Value", () => [...path(), row.key], undefined, false, previous);
        return row;
      }
      if (Array.isArray(saved?.rows)) {
        for (const row of saved.rows) {
          if (typeof row.key !== "string" || (row.fixed && !own(props, row.key))) continue;
          const originalValue = typeof row.originalKey === "string" && own(initial, row.originalKey) ? initial[row.originalKey] : undefined;
          insert(row.key, originalValue, row.fixed, row.originalKey, row.child);
        }
      } else if (record(initial)) {
        for (const [key, value] of Object.entries(initial)) insert(key, value, own(props, key), key);
      }
      refresh();
      if (saved?.next && Array.from(name.options).some(o => o.value === saved.next)) name.value = saved.next;
      return {
        snapshot: () => ({next: name.value, rows: rows.map(row => ({
          key: row.key, fixed: row.fixed, originalKey: row.originalKey, child: row.child.snapshot()
        }))}),
        focus() {
          if (!name.hidden) name.focus();
          else if (openMap) addEntry.focus();
          else rows[0]?.child.focus();
        },
        read() {
          const present = new Map();
          for (const row of rows) {
            const key = row.key;
            if (!row.fixed && !key.length && !row.emptyKeyExists) fail(row.input, "Enter a key for this map entry.");
            if ((!row.fixed && own(props, key)) || present.has(key)) fail(row.input, "The key “" + key + "” is already in use. Choose a unique key.");
            present.set(key, row.child.read());
          }
          for (const key of Object.keys(props)) {
            const required = props[key].required === true || (Array.isArray(field.required) && field.required.includes(key));
            if (required && !present.has(key)) {
              name.value = key;
              fail(name, "Add the required field “" + key + "”.");
            }
          }
          return Object.fromEntries(present);
        }
      };
    }

    const saved = options.restore?.version === 1 ? options.restore : null;
    const content = node(root, schema, value, options.label || "Value", () => options.path || [], options.allowed, true, saved?.content);
    const controls = () => Array.from(root.querySelectorAll("button,input,select,textarea")).filter(control => {
      const hidden = control.closest("[hidden]");
      return !hidden || !root.contains(hidden);
    });
    const restored = controls();
    for (const issue of saved?.errors || []) {
      if (restored[issue.index] && typeof issue.message === "string") showError(restored[issue.index], issue.message);
    }
    if (saved?.focus && restored[saved.focus.index]) {
      const target = restored[saved.focus.index];
      requestAnimationFrame(() => {
        if (!target.isConnected || !target.getClientRects().length) return;
        target.focus({preventScroll: true});
        if (typeof saved.focus.start === "number" && typeof target.setSelectionRange === "function") {
          target.setSelectionRange(saved.focus.start, saved.focus.end, saved.focus.direction);
        }
        target.scrollTop = saved.focus.scrollTop || 0;
      });
    }
    function snapshot() {
      const fields = controls();
      const errors = [];
      for (const [index, control] of fields.entries()) {
        const issue = document.getElementById(control.dataset.teError);
        if (issue && root.contains(issue)) errors.push({index, message: issue.textContent});
      }
      const target = document.activeElement;
      const index = fields.indexOf(target);
      const focus = index < 0 ? null : {index, start: target.selectionStart, end: target.selectionEnd,
        direction: target.selectionDirection, scrollTop: target.scrollTop};
      return {version: 1, content: content.snapshot(), errors, focus};
    }
    // Store only editor state, never HTML. A restored draft must pass the normal
    // schema, repository constraints and preview checks before it can be staged.
    return {snapshot, read() {
      root.querySelectorAll("[data-te-error]").forEach(clearError);
      return content.read();
    }};
  };
})();
