"use strict";
// Tests for content/static/js/nestthermostat_status.js, with a document and a request that are fakes. Run with:
// node js_test/nestthermostat_test.js (the Go test in oldbrowsers_test.go does).
var assert = require("assert");
var fs = require("fs");
var script = require("../content/static/js/nestthermostat_status.js");

var tests = [];
function test(name, fn) { tests.push([name, fn]); }

function fakePage(readyState, withButton) {
    var button = {listeners: {}, addEventListener: function (type, fn) { this.listeners[type] = fn; }};
    var root = {querySelector: function () { return withButton === false ? null : button; }};
    var doc = {
        readyState: readyState, listeners: {},
        querySelector: function () { return root; },
        addEventListener: function (type, fn) { this.listeners[type] = fn; },
        removeEventListener: function (type) { delete this.listeners[type]; }
    };

    return {doc: doc, button: button};
}

function nest(page) {
    var posts = [];
    var went = [];
    var n = new script.NestThermostatStatus(page.doc, {
        post: function (url, done) { posts.push({url: url, done: done}); },
        goTo: function (uri) { went.push(uri); }
    });

    return {n: n, posts: posts, went: went};
}

test("a page that has loaded gets its button at once, and a click goes to the address the plugin gives", function () {
    var page = fakePage("complete");
    var t = nest(page);
    t.n.init();

    assert.ok(page.button.listeners.click);
    assert.deepStrictEqual(Object.keys(page.doc.listeners), []);

    page.button.listeners.click();
    assert.strictEqual(t.posts[0].url, "/plugins/nestthermostat/oauthAttempt");
    t.posts[0].done(null, 200, JSON.stringify({AuthUri: "https://example.com/auth"}));
    assert.deepStrictEqual(t.went, ["https://example.com/auth"]);
});

test("a page still loading is waited for, and then the listener is taken away", function () {
    var page = fakePage("loading");
    var t = nest(page);
    t.n.init();

    assert.ok(page.doc.listeners.readystatechange);
    assert.strictEqual(page.button.listeners.click, undefined);

    page.doc.readyState = "interactive";
    page.doc.listeners.readystatechange();
    assert.ok(page.doc.listeners.readystatechange);

    page.doc.readyState = "complete";
    page.doc.listeners.readystatechange();
    assert.ok(page.button.listeners.click);
    assert.strictEqual(page.doc.listeners.readystatechange, undefined);
});

test("a refusal, no answer and an answer that is not understood go nowhere", function () {
    var page = fakePage("complete");
    var t = nest(page);
    t.n.init();

    page.button.listeners.click();
    t.posts[0].done(null, 500, "boom");
    page.button.listeners.click();
    t.posts[1].done(new Error("the request could not be sent"));
    page.button.listeners.click();
    t.posts[2].done(null, 200, "<html>");

    assert.deepStrictEqual(t.went, []);
});

test("a page without the button is no trouble", function () {
    var t = nest(fakePage("complete", false));
    t.n.init();
});

// The lint every script's test has: what Safari 9 does not have, looked for in the source. Pasted into each test.
test("the script uses nothing Safari 9 does not have", function () {
    var source = fs.readFileSync(__dirname + "/../content/static/js/nestthermostat_status.js", "utf8").replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/.*$/gm, "");

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
