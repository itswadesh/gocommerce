/*
 * The Accounts screen asks for the rights its routes are behind.
 *
 * ext/identity mounts three (identity_http.go): listing and reading an account
 * behind accounts.read, and erasing one behind accounts.read and
 * accounts.erase together. They were lifted off customers.read and
 * store.operate, and the nav moved to accounts.read with them, but the screen
 * kept asking for the old two: a role granted accounts.read without
 * customers.read was offered the link and then told it had no access, and the
 * delete button was offered to whoever held store.operate rather than
 * accounts.erase.
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

const screen = code(read("../src/routes/dash/accounts/+page.svelte"));
const nav = code(read("../src/lib/nav.js"));
const modules = code(read("../src/lib/modules.svelte.js"));

test("the Accounts screen is gated on accounts.read, as its nav entry and its routes are", () => {
    const entry = nav.match(/\{[^{}]*href:\s*"\/dash\/accounts"[^{}]*\}/)?.[0] ?? "";
    assert.match(entry, /right:\s*"accounts\.read"/, "the nav entry");

    // The probe pokes the list route, so it is made for whoever that route
    // would answer.
    const probe = modules.match(/\bidentity:\s*\{[^{}]*\}/)?.[0] ?? "";
    assert.match(probe, /right:\s*"accounts\.read"/, "the module probe");

    assert.match(screen, /<NoAccess\s[^>]*right="accounts\.read"/, "the screen's own refusal");
    for (const old of ["customers.read", "store.operate"]) {
        assert.ok(!screen.includes(old), `the screen still asks for ${old}`);
    }
});

test("erasing is offered for accounts.read and accounts.erase together, as the route requires", () => {
    const readable = screen.match(/const (\w+) = \$derived\(can\("accounts\.read"\)\)/)?.[1];
    assert.ok(readable, "no gate on accounts.read");
    const erasable = screen.match(/const erasable = \$derived\(([^;]*)\);/)?.[1] ?? "";
    assert.match(erasable, /can\("accounts\.erase"\)/, "erasing without accounts.erase");
    assert.ok(
        erasable.includes(readable) || erasable.includes('can("accounts.read")'),
        "erasing without accounts.read",
    );
});
