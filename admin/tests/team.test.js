/*
 * The Team screen hands out the store's roles, not a list compiled into the
 * panel, and invites people Dokploy's way: one form with how they arrive (a
 * link emailed to them, or a password set now), their email and their role —
 * and no email-provider picker, because the link goes out by the store's own
 * provider. Source-reading, as tests/roles.test.js is, because node:test
 * cannot import a .svelte file.
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

test("inviting asks how they arrive, their email and their role, in one form", () => {
    assert.match(team, />\s*Invite member\s*</);
    assert.match(team, /Invite method/);
    assert.match(team, /Invitation link — emailed to them/);
    assert.match(team, /Set a password now/);
    assert.match(team, /Send invitation/);
    assert.doesNotMatch(team, /User Account Not Found/);
});

test("there is no email provider to choose", () => {
    assert.doesNotMatch(team, /Email Provider|email provider<\/label/i);
});

test("invitations are listed with their status, expiry and actions", () => {
    for (const col of ["Email", "Role", "Status", "Expires", "Actions"]) {
        assert.match(team, new RegExp(`<th[^>]*>${col}</th>`));
    }
    assert.match(team, /copyInviteLink\(inv\)/);
    assert.match(team, /resendInvite\(inv\)/);
    assert.match(team, /revokeInvite\(inv\)/);
});

// Below 550px the page header shows its buttons as bare icons, so the name a
// screen reader announces has to be on the button itself.
test("the header's add buttons keep a name when they shrink to an icon", () => {
    assert.match(team, /aria-label="Invite member"/);
    const list = readFileSync(
        new URL("../src/routes/dash/settings/roles/+page.svelte", import.meta.url),
        "utf8",
    );
    assert.match(list, /aria-label="Add role"/);
});

test("the invitation feedback has a reduced-motion path", () => {
    assert.match(team, /@media \(prefers-reduced-motion: reduce\)/);
});
