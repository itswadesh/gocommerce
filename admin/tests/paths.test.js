/*
 * The panel's addresses moved under /dash. dashPath is the one mapping from
 * an old address to its new one, used by the redirect for bookmarks and
 * emailed links, so these pin what it must and must not touch.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { dashPath, safeNext } from "../src/lib/paths.js";

test("plain screens move under /dash with their names", () => {
    assert.equal(dashPath("/"), "/dash");
    assert.equal(dashPath("/orders"), "/dash/orders");
    assert.equal(dashPath("/orders/42"), "/dash/orders/42");
    assert.equal(dashPath("/discounts"), "/dash/discounts");
    assert.equal(dashPath("/cms/7"), "/dash/cms/7");
    assert.equal(dashPath("/settings/api-keys"), "/dash/settings/api-keys");
    assert.equal(dashPath("/settings/roles/manager"), "/dash/settings/roles/manager");
    assert.equal(dashPath("/b2b/companies/3"), "/dash/b2b/companies/3");
    assert.equal(dashPath("/x/agent"), "/dash/x/agent");
});

test("the renamed screens take their new names", () => {
    assert.equal(dashPath("/shipping"), "/dash/shipping-settings");
    assert.equal(dashPath("/shipping/providers"), "/dash/shipping-settings/providers");
    assert.equal(dashPath("/carts"), "/dash/checkouts");
    assert.equal(dashPath("/locations"), "/dash/warehouses");
    assert.equal(dashPath("/settings/superusers"), "/dash/settings/teams");
    // A prefix match is a whole segment, never part of one.
    assert.equal(dashPath("/shippingzones"), "/dash/shippingzones");
});

test("auth screens move under /admin/auth", () => {
    assert.equal(dashPath("/reset-password"), "/admin/auth/reset-password");
    assert.equal(dashPath("/reset-password/abc"), "/admin/auth/reset-password/abc");
    assert.equal(dashPath("/accept-invite/tok"), "/admin/auth/accept-invite/tok");
});

test("new paths, other apps and assets are left alone", () => {
    for (const p of [
        "/dash", "/dash/orders", "/admin/auth/login", "/select-store", "/stores/create",
        "/platform", "/platform/acme", "/portal", "/portal/orders",
        "/_app/immutable/x.js", "/images/logo.svg", "/fonts/inter.woff2",
        "/favicon.svg", "/theme.js", "/api/admin/orders",
    ]) {
        assert.equal(dashPath(p), null, p);
    }
});

test("safeNext only ever returns a path inside /dash on this origin", () => {
    assert.equal(safeNext("/dash/orders?status=open"), "/dash/orders?status=open");
    assert.equal(safeNext(null), "/dash");
    assert.equal(safeNext(""), "/dash");
    assert.equal(safeNext("//evil.example/dash"), "/dash");
    assert.equal(safeNext("https://evil.example/dash"), "/dash");
    assert.equal(safeNext("/platform"), "/dash");
    assert.equal(safeNext("/orders"), "/dash/orders");
    assert.equal(safeNext("/dash/orders?a=1#top"), "/dash/orders?a=1#top");
});
