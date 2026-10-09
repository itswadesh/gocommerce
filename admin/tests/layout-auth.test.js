/*
 * Signing in, resetting a password and accepting an invitation live under
 * /admin/auth, as the KitCommerce admin's sign-in does, and old addresses
 * redirect. These read source, as tests/roles.test.js does, because
 * node:test cannot import a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";

const at = (p) => new URL(p, import.meta.url);
const read = (p) => readFileSync(at(p), "utf8");

test("auth screens live under /admin/auth", () => {
    assert.ok(existsSync(at("../src/routes/admin/auth/login/+page.svelte")));
    assert.ok(existsSync(at("../src/routes/admin/auth/reset-password/[[token]]/+page.svelte")));
    assert.ok(existsSync(at("../src/routes/admin/auth/accept-invite/[token]/+page.svelte")));
});

test("the layout treats /admin/auth as public and sends signed-out visitors there", () => {
    const layout = read("../src/routes/+layout.svelte");
    assert.match(layout, /PUBLIC_PREFIXES = \[[^\]]*"\/admin\/auth"/);
    assert.match(layout, /LOGIN = "\/admin\/auth\/login"/);
    assert.match(layout, /\$\{LOGIN\}\?next=/);
    assert.match(layout, /safeNext\(/);
});

test("old addresses redirect through dashPath, keeping the query", () => {
    const load = read("../src/routes/+layout.js");
    assert.match(load, /dashPath\(/);
    assert.match(load, /url\.search/);
});
