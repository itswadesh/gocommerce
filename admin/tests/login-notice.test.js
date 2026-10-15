/*
 * The "your session ended" notice sits inside the login card, above the form.
 * It used to be a sibling of the card inside .page, a flex row: the notice
 * took the row's whole width and height and squeezed the card to a strip
 * 88px wide. Source-reading, because node:test cannot render a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const layout = read("../src/routes/+layout.svelte");
const login = read("../src/lib/components/Login.svelte");

test("the shell does not render the notice beside the login card", () => {
    const page = layout.slice(layout.indexOf('<div class="page">'), layout.indexOf("{:else if isPublic}"));
    assert.doesNotMatch(page, /alert warning/);
    assert.match(page, /<Login[^>]*notice=/);
});

test("the login card renders the notice inside its own wrapper, above the form", () => {
    const wrapper = login.indexOf('<div class="wrapper sm m-auto p-b-base">');
    const notice = login.indexOf("{#if notice}");
    const form = login.indexOf("<form");
    assert.ok(wrapper >= 0 && notice > wrapper && notice < form, "notice must be inside the wrapper, before the form");
});
