"use strict";
(() => {
  const fixture = document.getElementById("fixture");
  const render = (schema, value, options) => createTypedEditor(fixture, schema, value, options);
  const fields = type => [...fixture.querySelectorAll(type)].filter(el => !el.closest("[hidden]"));

  function equal(actual, expected) {
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
      throw new Error("Expected " + JSON.stringify(expected) + ", received " + JSON.stringify(actual));
    }
  }
  function click(label) {
    const control = fields("button").find(el => (el.getAttribute("aria-label") || el.textContent) === label);
    if (!control || control.disabled) throw new Error("Cannot use " + label);
    control.click();
  }
  function enter(input, value) {
    input.value = value;
    input.dispatchEvent(new Event("input", {bubbles: true}));
  }
  function choose(select, label) {
    const option = [...select.options].find(o => o.textContent === label);
    if (!option) throw new Error("Missing option " + label);
    select.value = option.value;
    select.dispatchEvent(new Event("change", {bubbles: true}));
  }
  function expectInvalid(editor, input, message) {
    let reason;
    try { editor.read(); } catch (err) { reason = err.message; }
    if (!reason || !reason.includes(message) || document.activeElement !== input || input.getAttribute("aria-invalid") !== "true") {
      throw new Error("Expected focused validation for " + message + ", received " + reason);
    }
  }

  try {
    let editor = render({type: "array", items: {type: "string"}}, ["first", "second"]);
    click("Add item");
    enter(fields("input")[2], "third");
    click("Move item 3 up");
    equal(editor.read(), ["first", "third", "second"]);
    click("Remove item 1");
    equal(editor.read(), ["third", "second"]);
    click("Remove item 1");
    click("Remove item 1");
    equal(editor.read(), []);

    editor = render({type: "boolean", nullable: true}, undefined);
    equal(editor.read(), undefined);
    choose(fields("select")[0], "Null"); equal(editor.read(), null);
    choose(fields("select")[0], "Set value");
    choose(fields("select")[1], "false"); equal(editor.read(), false);
    choose(fields("select")[0], "Unset"); equal(editor.read(), undefined);

    editor = render({type: "integer", nullable: true, nonnegative: true}, 0);
    equal(editor.read(), 0);
    const count = fields("input")[0];
    enter(count, "-1"); expectInvalid(editor, count, "nonnegative");
    // An invalid draft can be kept while staging an explicit null.
    choose(fields("select")[0], "Null"); equal(editor.read(), null);
    equal(count.matches(":disabled"), true);
    choose(fields("select")[0], "Set value");
    equal(count.value, "-1"); enter(count, "7"); equal(editor.read(), 7);
    enter(count, "0.5"); expectInvalid(editor, count, "whole number");

    editor = render({type: "array", items: {type: "string", enum: ["a", "b", "c"]}}, ["a"], {allowed: ["a", "b"]});
    click("Add item");
    equal([...fields("select")[1].options].map(o => o.textContent), ["Choose a value", "a", "b"]);
    choose(fields("select")[1], "b"); equal(editor.read(), ["a", "b"]);

    editor = render({type: "object", properties: {
      stages: {type: "array", items: {type: "string"}, constraint: {allow: ["dev", "prod"]}},
      protected: {type: "string", constraint: {lock: true}}
    }}, {stages: ["dev"], protected: "preserved"});
    choose(fields("select")[0], "prod");
    equal(fields("input[aria-label=protected]")[0].matches(":disabled"), true);
    equal(editor.read(), {stages: ["prod"], protected: "preserved"});

    const properties = {name: {type: "string", required: true}, enabled: {type: "boolean"}, items: {type: "array", items: {type: "string"}}};
    const migrated = {name: "", enabled: false, items: [], legacy: {flags: [true, 0, null]}};
    editor = render({type: "object", properties}, migrated);
    equal(editor.read(), migrated);
    click("Remove field enabled");
    equal(Object.hasOwn(editor.read(), "enabled"), false);
    choose(fields("select[aria-label='Field to add']")[0], "enabled");
    click("Add field");
    choose(fields("select[aria-label=enabled]")[0], "false");
    equal(editor.read().enabled, false);

    editor = render({type: "object", additional_properties: {type: "string"}}, {"a.b": "v", "other": ""});
    const keys = fields("label.te-key input");
    enter(keys[1], "a.b"); expectInvalid(editor, keys[1], "already in use");
    enter(keys[1], "__proto__"); equal(Object.hasOwn(editor.read(), "__proto__"), true);
    equal(editor.read().__proto__, "");
    click("Add entry"); expectInvalid(editor, fields("label.te-key input")[2], "Enter a key");

    editor = render({type: "array", items: {type: "object", properties: {name: properties.name}}}, null);
    equal(editor.read(), []);
    click("Add item");
    expectInvalid(editor, fields("select[aria-label='Field to add']")[0], "required field");
    click("Add field");
    enter(fields("input[aria-label=name]")[0], "new");
    equal(editor.read(), [{name: "new"}]);

    // A raw snapshot must survive serialization even when staging would fail.
    const entries = {type: "object", additional_properties: {type: "array", items: {type: "integer", nullable: true}}};
    const initial = {review: [1, 2]};
    editor = render(entries, initial);
    click("Move item 2 up");
    click("Add item");
    choose(fields("select.te-mode")[2], "Set value");
    const rawNumber = fields("input[role=spinbutton]")[2];
    enter(rawNumber, "1e-");
    expectInvalid(editor, rawNumber, "whole number");
    let restored = JSON.parse(JSON.stringify(editor.snapshot()));
    editor = render(entries, initial, {restore: restored});
    equal(fields("input[role=spinbutton]").map(input => input.value), ["2", "1", "1e-"]);
    equal(fields("input[role=spinbutton]")[2].getAttribute("aria-invalid"), "true");
    choose(fields("select.te-mode")[2], "Null");
    restored = JSON.parse(JSON.stringify(editor.snapshot()));
    editor = render(entries, initial, {restore: restored});
    equal(editor.read(), {review: [2, 1, null]});
    choose(fields("select.te-mode")[2], "Set value");
    equal(fields("input[role=spinbutton]")[2].value, "1e-");
    enter(fields("input[role=spinbutton]")[2], "13");
    click("Add entry");
    restored = JSON.parse(JSON.stringify(editor.snapshot()));
    editor = render(entries, initial, {restore: restored});
    equal(fields("label.te-key input").map(input => input.value), ["review", ""]);
    expectInvalid(editor, fields("label.te-key input")[1], "Enter a key");
    enter(fields("label.te-key input")[1], "later");
    equal(editor.read(), {review: [2, 1, 13], later: []});

    editor = render({type: "string"}, "first");
    click("Multiple lines");
    enter(fields("textarea")[0], "two\nlines");
    editor = render({type: "string"}, "first", {restore: JSON.parse(JSON.stringify(editor.snapshot()))});
    equal(fields("textarea")[0].value, "two\nlines");

    void fetch("/result", {method: "POST", body: "passed"});
  } catch (err) {
    void fetch("/result", {method: "POST", body: String(err.stack || err)});
  }
})();
