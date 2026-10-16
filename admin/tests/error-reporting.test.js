import { test } from "node:test";
import assert from "node:assert/strict";
import { reportFor, createReporter } from "../src/lib/error-reporting.js";

const err = (name, message, stack) => Object.assign(new Error(message), { name, stack });

test("a thrown error becomes a report of its kind, message, stack and page", () => {
    const e = err("TypeError", "x is undefined", "TypeError: x is undefined\n    at render (http://h/_app/nodes/5.js:10:2)");
    const r = reportFor({ error: e }, "/dash/orders?page=2");
    assert.deepEqual(r, { kind: "TypeError", message: "x is undefined", stack: e.stack, url: "/dash/orders?page=2" });
});

test("a rejected promise with a plain value still says what it was", () => {
    const r = reportFor({ reason: "network down" }, "/dash");
    assert.equal(r.kind, "UnhandledRejection");
    assert.equal(r.message, "network down");
});

test("noise is not reported: extensions and ResizeObserver's warning", () => {
    assert.equal(reportFor({ error: err("Error", "boom", "Error: boom\n at chrome-extension://abc/x.js:1:1") }, "/dash"), null);
    assert.equal(reportFor({ message: "ResizeObserver loop completed with undelivered notifications." }, "/dash"), null);
    assert.equal(reportFor({}, "/dash"), null);
});

test("the same crash is sent once, and a page sends at most ten", async () => {
    const sent = [];
    const reporter = createReporter((body) => sent.push(body));
    const e = err("TypeError", "x is undefined", "at a (http://h/a.js:1:1)");
    reporter.handle({ error: e }, "/dash");
    reporter.handle({ error: e }, "/dash");
    assert.equal(sent.length, 1);
    for (let i = 0; i < 20; i++) reporter.handle({ error: err("Error", "boom " + i, "") }, "/dash");
    assert.equal(sent.length, 10);
});

test("a report that fails to send is swallowed: reporting must not crash the panel", () => {
    const reporter = createReporter(() => {
        throw new Error("offline");
    });
    assert.doesNotThrow(() => reporter.handle({ error: err("Error", "boom", "") }, "/dash"));
});

test("a failed API call is not a crash: the server's own error is already recorded", () => {
    const apiError = Object.assign(new Error("internal error"), { name: "ApiError", status: 500 });
    assert.equal(reportFor({ reason: apiError }, "/dash"), null);
});
