/*
 * Two-way control on Setup Telegram (D216): a switch of its own, with a state
 * for loading, success and failure, and Find my chat out of the way while the
 * webhook owns the bot. node:test cannot import a .svelte file, so these read
 * the source, as builtin-features.test.js does.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const screen = read("../src/routes/dash/notifications/telegram/+page.svelte");
const api = read("../src/lib/api.js");

test("the client speaks the module's routes", () => {
    assert.match(api, /commands: \(\) => api\.get\(`\$\{TELEGRAM\}\/commands`\)/);
    assert.match(api, /setCommands: \(on\) => api\.post\(`\$\{TELEGRAM\}\/commands`, \{ on \}\)/);
    assert.match(api, /commandsAudit: /);
});

test("the switch is gated on its own right and on what the engine says blocks it", () => {
    assert.match(screen, /can\("alerts\.commands"\)/);
    assert.match(screen, /disabled=\{!switchesCommands \|\| turning \|\| \(!commands\.on && !!commands\.blocker\)\}/);
});

test("loading, success and failure each show", () => {
    assert.match(screen, /\{#if !commands\}\s*<span class="skeleton-loader"/);
    assert.match(screen, /class:loading=\{turning\}/);
    assert.match(screen, /toast\.success\(next \? "Two-way control on/);
    assert.match(screen, /\{#if commandsProblem\}\s*<p class="tg-result" role="alert" transition:rise>/);
    assert.match(screen, /commands\.paused/);
});

test("Find my chat steps aside while the webhook owns the bot", () => {
    assert.match(screen, /disabled=\{!writable \|\| !tokenSaved \|\| finding \|\| !!commands\?\.on\}/);
});
