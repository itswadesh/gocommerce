/*
 * The Team screen hands out the store's roles, not a list compiled into the
 * panel, and adds people the KitCommerce admin's way: email and role first,
 * and only when the address has no account, a choice between making one and
 * sending an invitation. Source-reading, as tests/roles.test.js is, because
 * node:test cannot import a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const team = readFileSync(
    new URL("../src/routes/dash/settings/teams/+page.svelte", import.meta.url),
    "utf8",
);

test("the role picker comes from the store", () => {
    assert.doesNotMatch(team, /const ROLES = \[/);
    assert.match(team, /rolesApi\.names\(\)/);
});

test("adding a member asks for email and role, then how to bring them in", () => {
    assert.match(team, />\s*Add team member\s*</);
    assert.match(team, /User Account Not Found/);
    assert.match(team, />\s*Create Account\s*</);
    assert.match(team, />\s*Send Invite\s*</);
    assert.match(team, /Confirm &amp; Proceed|Confirm & Proceed/);
});
