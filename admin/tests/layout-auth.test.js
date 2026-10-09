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

// kitcommerce.store's "Live demo" button opens /#email=…&password=…, and the
// login form fills itself from that fragment. Redirecting to the login address
// must carry the fragment, or the demo opens on an empty form.
test("the redirect to sign-in keeps the address's fragment", () => {
    const layout = read("../src/routes/+layout.svelte");
    assert.match(layout, /\$\{LOGIN\}\?next=\$\{encodeURIComponent\(here\)\}\$\{page\.url\.hash\}/);
});

// A catch-all route rather than the root layout's load: an address with no
// route is a 404 to the router before any layout runs, so a redirect there
// arrives after "Not found" has already been logged.
test("old addresses redirect through dashPath from a catch-all route, keeping the query", () => {
    const load = read("../src/routes/[...legacy]/+page.js");
    assert.match(load, /dashPath\(/);
    assert.match(load, /url\.search/);
    assert.match(load, /error\(404/);
    // SvelteKit's dev server throws on event.url.hash inside load; the panel
    // never renders on a server, so the fragment is read from location.
    assert.doesNotMatch(load, /url\.hash/);
    assert.match(load, /location\.hash/);
    assert.doesNotMatch(read("../src/routes/+layout.js"), /dashPath/);
});
