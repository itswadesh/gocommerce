/*
 * A module's descriptor screen offers delete only to a role the route would
 * let delete.
 *
 * The screen as a whole is gated on the right the descriptor names, which is a
 * read right. Deleting is not a read: Shopper accounts names accounts.read for
 * the screen and accounts.erase for its delete, and before the descriptor
 * could say so the button was drawn for anybody who could read the list and
 * then refused by the engine.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { listOf, mayDelete } from "../src/lib/screenform.js";

const holding = (...rights) => (right) => rights.includes(right);

const accounts = {
    slug: "identity-accounts",
    right: "accounts.read",
    form: { delete: "/api/admin/x/identity/customers/{id}", delete_right: "accounts.erase" },
};

test("a delete that names a right is offered only with it", () => {
    assert.equal(mayDelete(accounts, holding("accounts.read")), false);
    assert.equal(mayDelete(accounts, holding("accounts.read", "accounts.erase")), true);
    // Rights are all-of on the route: erasing without reading is refused too.
    assert.equal(mayDelete(accounts, holding("accounts.erase")), false);
});

test("a delete that names no right is the screen's own", () => {
    const plain = { right: "pages.read", form: { delete: "/api/admin/x/cms/pages/{id}" } };
    assert.equal(mayDelete(plain, holding("pages.read")), true);
    assert.equal(mayDelete(plain, holding()), false);
});

test("a screen with no delete offers none", () => {
    assert.equal(mayDelete({ right: "x.read", form: {} }, () => true), false);
    assert.equal(mayDelete({ right: "x.read" }, () => true), false);
    assert.equal(mayDelete(null, () => true), false);
});

test("the descriptor screen asks mayDelete", () => {
    const page = readFileSync(new URL("../src/routes/dash/x/[slug]/+page.svelte", import.meta.url), "utf8");
    assert.match(page, /from "\$lib\/screenform\.js"/);
    assert.match(page, /const canDelete = \$derived\(mayDelete\(screen, can\)\)/);
});

/*
 * GET /api/admin/screens answers {"data": [...]} with no meta, and api.get
 * hands such a response back as the bare array. Both readers of it took
 * `.data` off that array, so no module screen ever reached the nav and every
 * /dash/x/ page said "Not here", to the owner as well.
 */
test("a list reads the same whether it came back bare or with its meta", () => {
    const rows = [{ slug: "identity-accounts" }];
    assert.deepEqual(listOf(rows), rows);
    assert.deepEqual(listOf({ data: rows, meta: { total: 1 } }), rows);
    assert.deepEqual(listOf(null), []);
    assert.deepEqual(listOf({}), []);
});

test("both readers of the screens listing, and the rows, go through listOf", () => {
    const page = readFileSync(new URL("../src/routes/dash/x/[slug]/+page.svelte", import.meta.url), "utf8");
    const nav = readFileSync(new URL("../src/lib/screens.svelte.js", import.meta.url), "utf8");
    assert.match(page, /screen = listOf\(result\)\.find/);
    assert.match(page, /rows = listOf\(result\)/);
    assert.match(nav, /s\.list = listOf\(result\)/);
});
