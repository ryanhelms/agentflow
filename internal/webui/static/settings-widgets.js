"use strict";

// The controls of the Settings page (docs/design/ui/settings.md, "Controls").
// A registry maps FieldMeta.widget (internal/config/field_meta.go) to a
// factory that builds one control from a catalog field: a Factory Method keyed
// by widget kind. Every factory returns the same small interface, so the page
// never needs to know which control it holds.
//
//   create(field, options) -> {node, main, set(value), get(), focus(), disable(flag)}
//
// field is a CatalogField: {schema, meta, columns}. options:
//   id          the id of the element a <label for> points at
//   label       the field's label, for controls inside a table
//   labelledBy  the id of the visible label, when there is one
//   inherit()   returns the label of the "inherit" choice, or is absent
//   suggest(k)  returns names for "branches", "files" or "variables"
//   change(n)   called on every edit with {value}, {inherit: true} (remove
//               the value from the layer) or {incomplete: "why"} (the input
//               cannot be read as a value yet)
// Values are JSON values. Text is always set with textContent.
(function (scope) {
  const text = value => typeof value === "string" ? value : "";
  const list = value => Array.isArray(value) ? value : [];
  const record = value => value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
  const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  let serial = 0;
  const unique = prefix => prefix + "-" + (++serial);
  const registry = new Map();

  function element(tag, content = "", klass = "") {
    const node = document.createElement(tag);
    if (content !== "") node.textContent = content;
    if (klass) node.className = klass;
    return node;
  }

  function button(label, klass, run) {
    const node = element("button", label, klass);
    node.type = "button";
    node.addEventListener("click", run);
    return node;
  }

  // Name a control: by its visible label when it has one, else by text.
  function name(node, options) {
    if (options.labelledBy) node.setAttribute("aria-labelledby", options.labelledBy);
    else node.setAttribute("aria-label", options.label || "Value");
  }

  function disableAll(node, flag) {
    for (const part of node.querySelectorAll("input, select, button, textarea")) part.disabled = flag;
    if ("disabled" in node) node.disabled = flag;
  }

  // Offer names in a native list. The names are read when the input gets
  // focus, so branches and files that load later still appear.
  function suggestions(input, wrap, read) {
    const options = element("datalist");
    options.id = unique("st-names");
    input.setAttribute("list", options.id);
    wrap.append(options);
    const fill = () => {
      const names = [...new Set(read().filter(item => typeof item === "string" && item))].slice(0, 200);
      if (options.dataset.names === names.join("\n")) return;
      options.dataset.names = names.join("\n");
      options.replaceChildren(...names.map(item => { const option = element("option"); option.value = item; return option; }));
    };
    input.addEventListener("focus", fill);
    fill();
  }

  // Go durations ("90s", "5m", "1h30m") as seconds, or null.
  const units = {ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600};
  function seconds(value) {
    if (typeof value !== "string" || !value) return null;
    let total = 0, rest = value;
    const part = /^(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/;
    while (rest) {
      const found = part.exec(rest);
      if (!found) return null;
      total += Number(found[1]) * units[found[2]];
      rest = rest.slice(found[0].length);
    }
    return total;
  }
  // The largest unit that shows a duration as a whole number.
  function split(total) {
    for (const [unit, size] of [["h", 3600], ["m", 60]]) if (total >= size && total % size === 0) return {count: total / size, unit};
    return {count: total, unit: "s"};
  }
  const unitNames = {s: ["second", "seconds"], m: ["minute", "minutes"], h: ["hour", "hours"]};
  function durationText(value) {
    const total = seconds(value);
    if (total === null) return text(value);
    const {count, unit} = split(total);
    return count + " " + unitNames[unit][count === 1 ? 0 : 1];
  }

  // A duration as the picker writes it: "10m0s" is "10m", "1h30m0s" is "90m".
  function durationShort(value) {
    const total = seconds(value);
    if (total === null || !Number.isInteger(total)) return text(value);
    const {count, unit} = split(total);
    return count + unit;
  }

  // The widget for a schema node that has no metadata of its own: a table
  // cell, or a field of a provider definition.
  function derive(schema) {
    schema = record(schema) || {};
    if (schema.type === "boolean") return "toggle";
    if (schema.type === "integer") return "stepper";
    if (schema.type === "string") return list(schema.enum).length ? "select" : "text";
    if (schema.type === "array" && ["string", "integer"].includes(record(schema.items)?.type)) return "chips";
    if (schema.type === "array" && record(schema.items)?.type === "object") return "list";
    if (schema.type === "object" && record(schema.additional_properties)) return "kv-table";
    return "readonly";
  }

  function kindOf(field) {
    const wanted = text(record(field.meta)?.widget);
    return registry.has(wanted) ? wanted : derive(field.schema);
  }

  function create(field, options) {
    const control = registry.get(kindOf(field))(field, options);
    control.kind = kindOf(field);
    control.main ||= control.node;
    control.disable ||= flag => disableAll(control.node, flag);
    control.focus ||= () => control.main.focus();
    return control;
  }

  // toggle: a switch for a boolean.
  registry.set("toggle", (field, options) => {
    const node = element("button", "", "st-toggle");
    node.type = "button";
    node.setAttribute("role", "switch");
    if (options.id) node.id = options.id;
    name(node, options);
    const track = element("span", "", "st-toggle-track");
    track.setAttribute("aria-hidden", "true");
    track.append(element("span", "", "st-toggle-knob"));
    const state = element("span", "", "st-toggle-text");
    state.setAttribute("aria-hidden", "true");
    node.append(track, state);
    let value;
    const paint = () => {
      node.setAttribute("aria-checked", String(value === true));
      state.textContent = value === true ? "On" : "Off";
    };
    node.addEventListener("click", () => {
      value = value !== true;
      paint();
      options.change({value});
    });
    paint();
    return {node, set(next) { value = typeof next === "boolean" ? next : undefined; paint(); }, get: () => value};
  });

  // select and provider-picker: one of the schema's allowed values.
  function choice(field, options, describe = value => value) {
    const node = element("select", "", "st-select");
    if (options.id) node.id = options.id;
    name(node, options);
    const allowed = list(field.schema?.enum).filter(item => typeof item === "string");
    let value;
    const fill = () => {
      const choices = [];
      if (options.inherit) choices.push(["", options.inherit()]);
      else if (value === undefined) choices.push(["", "Choose a value"]);
      for (const item of allowed) choices.push(["v:" + item, describe(item)]);
      if (typeof value === "string" && !allowed.includes(value)) choices.push(["v:" + value, value + " (not an allowed value)"]);
      const wanted = JSON.stringify(choices);
      if (node.dataset.choices !== wanted) {
        node.dataset.choices = wanted;
        node.replaceChildren(...choices.map(([key, label]) => { const option = element("option", label); option.value = key; return option; }));
      }
      node.value = value === undefined ? "" : "v:" + value;
    };
    node.addEventListener("change", () => {
      value = node.value === "" ? undefined : node.value.slice(2);
      if (value !== undefined) options.change({value});
      else options.change(options.inherit ? {inherit: true} : {incomplete: "Choose a value."});
      fill();
    });
    fill();
    return {node, set(next) { value = typeof next === "string" ? next : undefined; fill(); }, get: () => value, refresh: fill};
  }
  registry.set("select", (field, options) => choice(field, options));
  registry.set("provider-picker", (field, options) => {
    const fallback = field.schema?.default;
    const control = choice(field, options, value => value === "none" ? "none (role switched off)" : value + (value === fallback ? " (default)" : ""));
    control.node.classList.add("st-provider");
    return control;
  });

  // stepper: a whole number with its limits.
  registry.set("stepper", (field, options) => {
    const meta = record(field.meta) || {};
    const min = Number.isSafeInteger(meta.min) ? meta.min : field.schema?.nonnegative ? 0 : null;
    const max = Number.isSafeInteger(meta.max) ? meta.max : null;
    const step = Number.isSafeInteger(meta.step) && meta.step > 0 ? meta.step : 1;
    const node = element("div", "", "st-stepper");
    const input = element("input");
    input.type = "number";
    input.inputMode = "numeric";
    input.step = String(step);
    if (min !== null) input.min = String(min);
    if (max !== null) input.max = String(max);
    if (options.id) input.id = options.id;
    name(input, options);
    const limits = min !== null && max !== null ? "Enter a whole number from " + min + " to " + max + "." : min !== null ? "Enter a whole number, " + min + " or more." : max !== null ? "Enter a whole number, " + max + " or less." : "Enter a whole number.";
    const read = () => {
      const value = input.value.trim() === "" ? NaN : Number(input.value);
      return Number.isSafeInteger(value) && (min === null || value >= min) && (max === null || value <= max) ? value : undefined;
    };
    const report = () => {
      const value = read();
      if (value !== undefined) options.change({value});
      else if (input.value.trim() === "" && options.inherit) options.change({inherit: true});
      else options.change({incomplete: limits});
    };
    const move = by => {
      const from = read() ?? (by > 0 ? (min ?? 0) - step : (max ?? 0) + step);
      let next = from + by;
      if (min !== null) next = Math.max(min, next);
      if (max !== null) next = Math.min(max, next);
      input.value = String(next);
      report();
    };
    const less = button("−", "st-step", () => move(-step));
    const more = button("+", "st-step", () => move(step));
    less.setAttribute("aria-label", "Decrease " + (options.label || "value"));
    more.setAttribute("aria-label", "Increase " + (options.label || "value"));
    input.addEventListener("input", report);
    node.append(less, input, more);
    const ghost = value => { input.placeholder = Number.isSafeInteger(value) ? String(value) : ""; };
    return {node, main: input, ghost, set(next) { input.value = Number.isSafeInteger(next) ? String(next) : ""; }, get: read};
  });

  // duration: a count and a unit, saved as a Go duration ("5m").
  registry.set("duration", (field, options) => {
    const meta = record(field.meta) || {};
    const least = seconds(meta.min_duration), most = seconds(meta.max_duration);
    const node = element("div", "", "st-duration");
    node.setAttribute("role", "group");
    name(node, options);
    const count = element("input");
    count.type = "number";
    count.inputMode = "numeric";
    count.min = "0";
    count.step = "1";
    if (options.id) count.id = options.id;
    count.setAttribute("aria-label", (options.label || "Duration") + ": amount");
    const unit = element("select");
    unit.setAttribute("aria-label", (options.label || "Duration") + ": unit");
    for (const key of ["s", "m", "h"]) { const option = element("option", unitNames[key][1]); option.value = key; unit.append(option); }
    unit.value = "m";
    let raw; // the value as saved, kept until the reader edits it
    const range = least !== null && most !== null ? "Choose a duration from " + durationText(meta.min_duration) + " to " + durationText(meta.max_duration) + "."
      : least !== null ? "Choose a duration of " + durationText(meta.min_duration) + " or more." : most !== null ? "Choose a duration of " + durationText(meta.max_duration) + " or less." : "Enter a whole number.";
    const read = () => {
      if (raw !== undefined) return raw;
      const amount = count.value.trim() === "" ? NaN : Number(count.value);
      if (!Number.isSafeInteger(amount) || amount < 0) return undefined;
      const total = amount * units[unit.value];
      return (least === null || total >= least) && (most === null || total <= most) ? amount + unit.value : undefined;
    };
    const report = () => {
      raw = undefined;
      const value = read();
      if (value !== undefined) options.change({value});
      else if (count.value.trim() === "" && options.inherit) options.change({inherit: true});
      else options.change({incomplete: range});
    };
    count.addEventListener("input", report);
    unit.addEventListener("change", report);
    node.append(count, unit);
    const hint = element("span", "", "st-hint");
    if (least !== null || most !== null) { hint.textContent = range.replace("Choose a duration ", "").replace(/^./, first => first.toUpperCase()); node.append(hint); }
    return {
      node, main: count,
      // The inherited duration shows as a placeholder, in its own unit.
      ghost(value) {
        const total = seconds(value);
        const whole = total !== null && Number.isInteger(total) ? split(total) : null;
        count.placeholder = whole ? String(whole.count) : "";
        if (whole && count.value === "") unit.value = whole.unit;
      },
      set(next) {
        const total = seconds(next);
        raw = typeof next === "string" && next ? next : undefined;
        if (total === null || !Number.isInteger(total)) { count.value = ""; return; }
        const shown = split(total);
        count.value = String(shown.count);
        unit.value = shown.unit;
      },
      get: read
    };
  });

  // A one-line value with names to pick from. check returns a message for a
  // value that cannot be saved, or "".
  function line(options, {klass = "", names = null, check = null, placeholder = "", hint = ""} = {}) {
    const node = element("div", "", "st-line " + klass);
    const input = element("input");
    input.type = "text";
    input.autocomplete = "off";
    input.spellcheck = false;
    input.setAttribute("autocapitalize", "off");
    if (placeholder) input.placeholder = placeholder;
    if (options.id) input.id = options.id;
    name(input, options);
    node.append(input);
    if (names) suggestions(input, node, names);
    if (hint) node.append(element("span", hint, "st-hint"));
    input.addEventListener("input", () => {
      const value = input.value;
      if (value === "") { options.change(options.inherit ? {inherit: true} : {value: ""}); return; }
      const problem = check?.(value) || "";
      options.change(problem ? {incomplete: problem} : {value});
    });
    const ghost = value => { input.placeholder = value === undefined ? placeholder : String(value); };
    return {node, main: input, input, ghost, set(next) { input.value = text(next); }, get: () => input.value === "" || check?.(input.value) ? undefined : input.value};
  }

  registry.set("text", (field, options) => line(options, {
    placeholder: text(field.meta?.placeholder),
    names: list(field.schema?.suggestions).length ? () => list(field.schema.suggestions) : null
  }));

  function matchesGlob(path, glob) {
    if (!glob) return true;
    const pattern = glob.replace(/[.+^${}()|[\]\\]/g, "\\$&").replaceAll("**", "\u0000").replaceAll("*", "[^/]*").replaceAll("\u0000", ".*").replaceAll("?", "[^/]");
    try { return new RegExp("^" + pattern + "$").test(path) || new RegExp("^" + pattern + "$").test(path.split("/").pop()); } catch { return true; }
  }

  registry.set("file-picker", (field, options) => {
    const picker = record(field.meta?.picker) || {};
    const home = picker.base === "home";
    const what = picker.kind === "dir" ? "folder" : "file";
    return line(options, {
      klass: "st-path",
      placeholder: text(field.meta?.placeholder) || (home ? "~/path/to/" + what : "path/to/" + what),
      hint: home ? "A " + what + " on this computer. ~/ is your home folder." : "A " + what + " in this repository, from its root.",
      names: () => [...list(field.schema?.suggestions), ...(home || picker.kind === "dir" ? [] : (options.suggest?.("files") || []).filter(path => matchesGlob(path, text(picker.glob))))],
      check: value => !home && (value.startsWith("/") || value.startsWith("~") || value.split("/").includes("..")) ? "Enter a path inside this repository, from its root." : ""
    });
  });

  const variable = /^[A-Za-z_][A-Za-z0-9_]*$/;
  registry.set("credential-ref", (field, options) => line(options, {
    klass: "st-variable",
    placeholder: text(field.meta?.placeholder) || "VARIABLE_NAME",
    hint: "The name of an environment variable. Agentflow reads the secret from it and never stores it.",
    names: () => [...list(field.schema?.suggestions), ...(options.suggest?.("variables") || [])],
    check: value => variable.test(value) ? "" : "Enter the name of an environment variable, such as GITHUB_TOKEN. Do not paste the secret itself."
  }));

  // chips: a list of short strings.
  function chips(field, options, names = null) {
    const kind = record(field.schema?.items) || {};
    const whole = kind.type === "integer";
    const allowed = list(kind.enum).filter(item => typeof item === "string");
    const known = [...list(kind.suggestions), ...list(field.schema?.suggestions)];
    const node = element("div", "", "st-chips");
    node.setAttribute("role", "group");
    name(node, options);
    const items = element("ul", "", "st-chip-list");
    const empty = element("p", "No entries.", "st-hint");
    const add = element("div", "", "st-chip-add");
    const entry = allowed.length ? element("select") : element("input");
    if (!allowed.length) {
      entry.type = whole ? "number" : "text";
      if (whole) entry.step = "1";
      entry.autocomplete = "off";
      entry.spellcheck = false;
      if (field.meta?.placeholder) entry.placeholder = text(field.meta.placeholder);
    }
    if (options.id) entry.id = options.id;
    entry.setAttribute("aria-label", "Add to " + (options.label || "the list"));
    let values = [];
    const offered = () => allowed.filter(item => !values.includes(item));
    const paint = () => {
      items.replaceChildren(...values.map((item, index) => {
        const chip = element("li", "", "st-chip");
        const remove = button("×", "st-chip-remove", () => {
          values = values.filter((_, at) => at !== index);
          paint();
          (items.querySelectorAll(".st-chip-remove")[Math.min(index, values.length - 1)] || entry).focus();
          options.change({value: [...values]});
        });
        remove.setAttribute("aria-label", "Remove " + item);
        chip.append(element("span", String(item)), remove);
        return chip;
      }));
      items.hidden = !values.length;
      empty.hidden = Boolean(values.length);
      if (allowed.length) {
        entry.replaceChildren(...[["", offered().length ? "Choose a value to add" : "Every value is in the list"], ...offered().map(item => [item, item])].map(([key, label]) => { const option = element("option", label); option.value = key; return option; }));
        entry.value = "";
      }
    };
    const commit = () => {
      const typed = entry.value.trim();
      const value = whole ? (typed !== "" && Number.isSafeInteger(Number(typed)) ? Number(typed) : null) : typed;
      entry.value = "";
      if (value === null || value === "" || values.includes(value)) return;
      values = [...values, value];
      paint();
      entry.focus();
      options.change({value: [...values]});
    };
    const confirm = button("Add", "button secondary small", commit);
    if (allowed.length) {
      entry.addEventListener("change", commit);
      confirm.hidden = true;
    } else {
      entry.addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); commit(); } });
      const read = names || (known.length ? () => known : null);
      if (read && !whole) suggestions(entry, add, () => read().filter(item => !values.includes(item)));
    }
    add.prepend(entry, confirm);
    node.append(items, empty, add);
    paint();
    return {
      node, main: node, focus: () => entry.focus(),
      set(next) {
        const wanted = list(next).filter(item => whole ? Number.isSafeInteger(item) : typeof item === "string");
        if (same(wanted, values)) return;
        values = wanted;
        paint();
      },
      get: () => [...values]
    };
  }
  registry.set("chips", (field, options) => chips(field, options));

  // branch-picker: one branch, or a list of branches.
  registry.set("branch-picker", (field, options) => {
    const branches = () => [...list(field.schema?.suggestions), ...(options.suggest?.("branches") || [])];
    if (field.meta?.picker?.multiple || field.schema?.type === "array") return chips(field, options, branches);
    return line(options, {klass: "st-branch", placeholder: text(field.meta?.placeholder) || "Branch name", names: branches,
      check: value => /\s/.test(value) ? "A branch name has no spaces." : ""});
  });

  // A control for one cell of a table: no visible label of its own.
  function cell(schema, meta, label, change, suggest) {
    return create({schema: record(schema) || {}, meta: record(meta) || {}}, {label, change, suggest});
  }

  // A property path: "." separates, "\." is a dot inside a key (the
  // FieldPath grammar of internal/config/field_path.go).
  function pathOf(key) {
    const out = [];
    let current = "";
    for (let at = 0; at < key.length; at++) {
      if (key[at] === "\\" && at + 1 < key.length) current += key[++at];
      else if (key[at] === ".") { out.push(current); current = ""; }
      else current += key[at];
    }
    out.push(current);
    return out;
  }
  function readPath(value, path) {
    for (const key of path) value = record(value)?.[key];
    return value === null ? undefined : value;
  }
  function writePath(target, path, value) {
    for (const key of path.slice(0, -1)) target = target[key] = record(target[key]) || {};
    target[path.at(-1)] = value;
  }

  // The columns of a row object, in metadata order. Column keys are property
  // paths, so a nested property ("native.channel_id") is a column of its own.
  // Without column metadata every scalar, list and map property is a column.
  function columnsOf(schema, columns) {
    const named = Object.keys(record(columns) || {});
    const keys = named.length ? named : Object.keys(record(schema?.properties) || {});
    return keys.map(key => {
      const path = pathOf(key);
      let node = schema;
      for (const part of path) node = record(record(node)?.properties)?.[part];
      const meta = record(record(columns)?.[key]) || {};
      return {key, path, schema: record(node) || {type: "string"}, meta, label: text(meta.label) || key, order: Number.isFinite(meta.order) ? meta.order : 0};
    }).sort((a, b) => a.order - b.order || a.label.localeCompare(b.label));
  }

  // The body shared by kv-table and list: rows of cell controls with their
  // row actions. rowsOf(value) and valueOf(rows) translate to and from JSON.
  function table(field, options, {keyed, columns, single, addLabel, rowName}) {
    const node = element("div", "", "st-table-wrap" + (columns.length > 3 ? " st-stacked" : ""));
    node.setAttribute("role", "group");
    name(node, options);
    const grid = element("table", "", "st-table");
    const head = element("tr");
    if (keyed) head.append(Object.assign(element("th", "Key"), {scope: "col"}));
    if (single) head.append(Object.assign(element("th", "Value"), {scope: "col"}));
    for (const column of columns) {
      const title = Object.assign(element("th", column.label), {scope: "col"});
      if (column.meta.description) title.title = text(column.meta.description);
      head.append(title);
    }
    const tools = Object.assign(element("th"), {scope: "col"});
    tools.append(element("span", "Row actions", "sr-only"));
    head.append(tools);
    const body = element("tbody");
    const top = element("thead");
    top.append(head);
    grid.append(top, body);
    const empty = element("p", keyed ? "No entries." : "No items.", "st-hint");
    const problem = () => {
      if (!keyed) return "";
      const keys = rows.map(row => row.key.value.trim());
      if (keys.some(key => !key)) return "Give every row a key.";
      const twice = keys.find((key, index) => keys.indexOf(key) !== index);
      return twice ? "The key " + twice + " is used twice." : "";
    };
    let rows = [];
    const read = () => {
      if (problem()) return undefined;
      const values = rows.map(row => {
        if (single) return row.cells[0].get();
        const item = {};
        columns.forEach((column, index) => {
          const value = row.cells[index].get();
          // An empty optional property is left out of the row, not saved as empty.
          const blank = value === undefined || value === "" || (typeof value === "object" && !Object.keys(value).length);
          if (!blank) writePath(item, column.path, value);
        });
        return item;
      });
      if (!keyed) return values;
      return Object.fromEntries(rows.map((row, index) => [row.key.value.trim(), values[index]]));
    };
    const report = () => {
      const why = problem();
      const value = read();
      if (why) options.change({incomplete: why});
      else if (single && Object.values(keyed ? value : {}).some(item => item === undefined)) options.change({incomplete: "Give every row a value."});
      else options.change({value});
    };
    const relabel = () => {
      rows.forEach((row, index) => {
        const title = rowName(keyed ? row.key.value.trim() : "", index);
        row.remove.setAttribute("aria-label", "Remove " + title);
        row.up?.setAttribute("aria-label", "Move " + title + " up");
        row.down?.setAttribute("aria-label", "Move " + title + " down");
        if (row.up) row.up.disabled = index === 0;
        if (row.down) row.down.disabled = index === rows.length - 1;
        row.cells.forEach((control, at) => control.main.setAttribute("aria-label", row.labels[at] + ", " + title));
      });
      grid.hidden = !rows.length;
      empty.hidden = Boolean(rows.length);
    };
    const place = () => { body.replaceChildren(...rows.map(row => row.node)); relabel(); };
    function build(key, value) {
      const row = {node: element("tr"), cells: [], labels: []};
      if (keyed) {
        const holder = element("td", "", "st-key");
        holder.dataset.label = "Key";
        row.key = element("input");
        row.key.type = "text";
        row.key.autocomplete = "off";
        row.key.spellcheck = false;
        row.key.value = key;
        row.key.setAttribute("aria-label", "Key");
        holder.append(row.key);
        const known = list(field.schema?.key_suggestions);
        if (known.length) suggestions(row.key, holder, () => known.filter(item => !rows.some(other => other !== row && other.key.value === item)));
        row.key.addEventListener("input", () => { relabel(); report(); });
        row.node.append(holder);
      }
      const parts = single ? [{schema: single, meta: {}, label: "Value", key: null}] : columns;
      for (const part of parts) {
        const holder = element("td");
        const control = cell(part.schema, part.meta, part.label, () => report(), options.suggest);
        row.labels.push(part.label);
        holder.dataset.label = part.label;
        control.set(part.key === null ? value : readPath(value, part.path));
        row.cells.push(control);
        holder.append(control.node);
        row.node.append(holder);
      }
      const actions = element("td", "", "st-row-tools");
      if (!keyed) {
        row.up = button("↑", "st-step", () => swap(row, -1));
        row.down = button("↓", "st-step", () => swap(row, 1));
        actions.append(row.up, row.down);
      }
      row.remove = button("Remove", "button secondary small", () => {
        const at = rows.indexOf(row);
        rows = rows.filter(other => other !== row);
        place();
        (rows[Math.min(at, rows.length - 1)]?.remove || more).focus();
        report();
      });
      actions.append(row.remove);
      row.node.append(actions);
      return row;
    }
    function swap(row, by) {
      const at = rows.indexOf(row), to = at + by;
      if (to < 0 || to >= rows.length) return;
      [rows[at], rows[to]] = [rows[to], rows[at]];
      place();
      (by < 0 && !row.up.disabled ? row.up : by > 0 && !row.down.disabled ? row.down : row.remove).focus();
      report();
    }
    const more = button(addLabel, "button secondary small", () => {
      const row = build("", undefined);
      rows.push(row);
      place();
      (row.key || row.cells[0]).focus();
      report();
    });
    node.append(grid, empty, more);
    place();
    return {
      node, main: node, focus: () => more.focus(),
      set(next) {
        const wanted = keyed ? record(next) || {} : list(next);
        if (!problem() && same(read(), wanted)) return;
        rows = keyed ? Object.entries(wanted).map(([key, value]) => build(key, value)) : wanted.map(value => build("", value));
        place();
      },
      get: read
    };
  }

  // kv-table: a map from a key to a value or a small object.
  registry.set("kv-table", (field, options) => {
    const value = record(field.schema?.additional_properties) || {type: "string"};
    const columns = value.type === "object" && record(value.properties) ? columnsOf(value, field.columns) : [];
    if (derive(value) === "readonly" && !columns.length) return registry.get("readonly")(field, options);
    return table(field, options, {keyed: true, columns, single: columns.length ? null : value, addLabel: "Add entry",
      rowName: (key, index) => key || "row " + (index + 1)});
  });

  // list: an ordered list of objects, one row each.
  registry.set("list", (field, options) => {
    const columns = columnsOf(record(field.schema?.items), field.columns);
    if (!columns.length) return registry.get("readonly")(field, options);
    return table(field, options, {keyed: false, columns, single: null, addLabel: "Add item", rowName: (_, index) => "item " + (index + 1)});
  });

  // A value no control can edit. It is shown, never typed: the document
  // editor on the Configuration page edits it.
  registry.set("readonly", (field, options) => {
    const node = element("div", "", "st-readonly");
    const shown = element("code");
    if (options.id) shown.id = options.id;
    node.append(shown, element("span", "Edit this value on the Configuration page.", "st-hint"));
    let value;
    return {node, main: shown, focus() {}, disable() {}, set(next) { value = next; shown.textContent = next === undefined ? "Not set" : JSON.stringify(next); }, get: () => value};
  });

  scope.AgentflowSettingsWidgets = {
    create, kindOf, derive, seconds, durationText, durationShort,
    // register adds or replaces the factory for a widget kind.
    register(kind, factory) { registry.set(kind, factory); },
    get kinds() { return [...registry.keys()]; }
  };
})(globalThis);
