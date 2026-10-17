/*
 * The "your session ended" notice sits inside the sign-in card, above the
 * form. It used to be a sibling of the card inside .page, a flex row: the
 * notice took the row's whole width and height and squeezed the card to a
 * strip 88px wide. Source-reading, because node:test cannot render a .svelte
 * file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const layout = read("../src/routes/+layout.svelte");
const login = read("../src/lib/components/Login.svelte");

test("the shell does not render the notice beside the sign-in card", () => {
    const branch = layout.slice(layout.indexOf("{#if onLogin}"), layout.indexOf("{:else if isPublic}"));
    assert.doesNotMatch(branch, /alert warning/);
    assert.match(branch, /<Login[^>]*notice=/);
});

test("the sign-in card renders the notice inside its own shell, above the form", () => {
    const shell = login.indexOf("<AuthShell>");
    const notice = login.indexOf("{#if notice}");
    const form = login.indexOf("<form");
    assert.ok(shell >= 0 && notice > shell && notice < form, "notice must be inside the shell, before the form");
});
