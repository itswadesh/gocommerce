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

// Below 550px the page header shows its buttons as bare icons, so the name a
// screen reader announces has to be on the button itself.
test("the header's add buttons keep a name when they shrink to an icon", () => {
    assert.match(team, /aria-label="Add team member"/);
    const list = readFileSync(
        new URL("../src/routes/dash/settings/roles/+page.svelte", import.meta.url),
        "utf8",
    );
    assert.match(list, /aria-label="Add role"/);
});

// --primaryColor is near-black in the dark theme; a checked card drawn in it
// vanished into the dialog, and the unchecked one looked chosen instead.
test("the checked way-to-join card is drawn in a colour both themes can see", () => {
    const checked = team.match(/\.choice-card\[aria-checked="true"\] \{[^}]*\}/)?.[0] ?? "";
    assert.match(checked, /--surfaceTxtColor/);
    assert.doesNotMatch(checked, /--primaryColor/);
});
