/*
 * The agent screen asks for the rights its routes are behind.
 *
 * ext/mcp mounts two (mcp.go, Register): GET /api/admin/x/mcp/audit behind
 * agent.read, which is the list of recorded calls, and POST /api/admin/x/mcp
 * behind agent.dispatch, which the screen's tool list is a tools/list call to.
 * Both were lifted off store.operate and the nav moved to agent.read with them,
 * but the screen kept asking for store.operate: a role granted agent.read was
 * offered the link and then told it had no access, and a role holding
 * store.operate alone was shown a screen whose every call was refused.
 *
 * node:test cannot import a .svelte file, so these read the source, as
 * roles.test.js does.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

// Code only: a comment is free to name the right a gate used to be.
const blank = (m) => m.replace(/[^\n]/g, " ");
const code = (src) =>
    src.replace(/<!--[\s\S]*?-->/g, blank).replace(/\/\*[\s\S]*?\*\//g, blank).replace(/\/\/.*$/gm, blank);

const screen = code(read("../src/routes/dash/settings/agent/+page.svelte"));
const nav = code(read("../src/lib/nav.js"));
const modules = code(read("../src/lib/modules.svelte.js"));

test("the agent screen is gated on agent.read, as its nav entry and its audit route are", () => {
    const entry = nav.match(/\{[^{}]*href:\s*"\/dash\/settings\/agent"[^{}]*\}/)?.[0] ?? "";
    assert.match(entry, /right:\s*"agent\.read"/, "the nav entry");

    // The probe pokes the audit route, so it is made for whoever that route
    // would answer.
    const probe = modules.match(/\bmcp:\s*\{[^{}]*\}/)?.[0] ?? "";
    assert.match(probe, /right:\s*"agent\.read"/, "the module probe");

    assert.match(screen, /<NoAccess\s[^>]*right="agent\.read"/, "the screen's own refusal");
    assert.doesNotMatch(screen, /store\.operate/, "the screen still asks for store.operate");
});

test("each half of the agent screen is loaded only for a role its route will answer", () => {
    const gate = (right) => {
        const name = screen.match(
            new RegExp(`const (\\w+) = \\$derived\\(can\\("${right.replace(".", "\\.")}"\\)\\)`),
        )?.[1];
        assert.ok(name, `no gate on ${right}`);
        return name;
    };
    const readable = gate("agent.read");
    const dispatchable = gate("agent.dispatch");

    const calls = (fn) =>
        screen.split("\n").filter((line) => line.includes(`${fn}()`) && !line.includes("function"));
    for (const [fn, name, right] of [
        ["loadAudit", readable, "agent.read"],
        ["loadStatus", dispatchable, "agent.dispatch"],
    ]) {
        assert.ok(calls(fn).length > 0, `${fn} is never called`);
        for (const line of calls(fn)) {
            assert.ok(line.includes(name), `${fn} is called without checking ${right}: ${line.trim()}`);
        }
    }
});
