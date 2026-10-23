"use strict";
// Tests for content/static/js/nestthermostat_thermostat.js. Run with: node js_test/nestthermostat_thermostat_test.js (the Go test in
// oldbrowsers_test.go does).
var assert = require("assert");
var fs = require("fs");
var script = require("../content/static/js/nestthermostat_thermostat.js");

var tests = [];
function test(name, fn) { tests.push([name, fn]); }

function fakeButton(id) {
    var icon = {className: "bi bi-copy"};
    return {dataset: {id: id}, icon: icon, querySelector: function () { return icon; }};
}

// A promise that has been kept (or broken), which calls back at once: enough for the clipboard's.
function settled(ok, value) {
    return {then: function (yes, no) { return ok ? yes(value) : no(value); }};
}

test("the id is copied, the icon shows it for a moment and then goes back", function () {
    var button = fakeButton("devices/abc");
    var written = [];
    var timers = [];

    script.copyThermostatId(button, {
        clipboard: {writeText: function (text) { written.push(text); return settled(true); }},
        later: function (fn, ms) { timers.push({fn: fn, ms: ms}); }
    });

    assert.deepStrictEqual(written, ["devices/abc"]);
    assert.strictEqual(button.icon.className, "bi bi-check2");
    assert.strictEqual(timers[0].ms, 1200);
    timers[0].fn();
    assert.strictEqual(button.icon.className, "bi bi-copy");
});

test("with no clipboard the id is shown to be copied by hand", function () {
    var shown = [];

    script.copyThermostatId(fakeButton("devices/abc"), {clipboard: null, prompt: function (m, v) { shown.push([m, v]); }});
    script.copyThermostatId(fakeButton("devices/def"), {clipboard: {}, prompt: function (m, v) { shown.push([m, v]); }});

    assert.deepStrictEqual(shown, [["Thermostat id", "devices/abc"], ["Thermostat id", "devices/def"]]);
});

test("a button with no id does nothing, and a clipboard that refuses is only logged", function () {
    var shown = [];
    script.copyThermostatId(fakeButton(""), {clipboard: null, prompt: function () { shown.push(1); }});
    assert.deepStrictEqual(shown, []);

    var button = fakeButton("devices/abc");
    script.copyThermostatId(button, {clipboard: {writeText: function () { return settled(false, "denied"); }}});
    assert.strictEqual(button.icon.className, "bi bi-copy");
});

// The lint every script's test has: what Safari 9 does not have, looked for in the source. Pasted into each test.
test("the script uses nothing Safari 9 does not have", function () {
    var source = fs.readFileSync(__dirname + "/../content/static/js/nestthermostat_thermostat.js", "utf8").replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/.*$/gm, "");

    [
        [/=>/, "an arrow function"], [/\bclass\s+\w/, "a class"], [/\blet\s/, "let"], [/\bconst\s/, "const"], [/`/, "a template literal"],
        [/\basync\b/, "async"], [/\bawait\b/, "await"], [/\bfetch\s*\(/, "fetch"], [/\.\.\./, "spread"], [/\?\./, "optional chaining"],
        [/\?\?/, "nullish coalescing"], [/\bObject\.(entries|values)\b/, "Object.entries or values"], [/\.padStart\b|\.padEnd\b/, "padStart"]
    ].forEach(function (pair) {
        assert.ok(!pair[0].test(source), "the script has " + pair[1]);
    });

    // forEach is only for arrays: a NodeList has none before Safari 10, which is what toArray is for.
    (source.match(/(\w+)\.forEach/g) || []).forEach(function (use) {
        assert.ok([].indexOf(use) >= 0, "forEach on " + use);
    });
});

var failed = 0;
tests.forEach(function (t) {
    try { t[1](); console.log("ok   " + t[0]); } catch (e) { failed++; console.log("FAIL " + t[0] + "\n     " + (e && e.stack || e)); }
});
if (failed) { process.exit(1); }
