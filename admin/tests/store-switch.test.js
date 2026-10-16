/*
 * Switching store keeps the page: the section stays, anything that names a
 * row of the old store goes. These pin the mapping the sidebar switcher uses,
 * which is computed from the route id so a page added later is covered
 * without anyone remembering to list it.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { switchTarget, sectionFor, droppedNote } from "../src/lib/store-switch.js";

const at = (routeId, href) => switchTarget(routeId, new URL(href, "http://panel.test"));

test("a list stays the same list, filters and all", () => {
    assert.deepEqual(at("/dash/orders", "/dash/orders"), { href: "/dash/orders", dropped: null });
    assert.deepEqual(at("/dash/orders", "/dash/orders?status=paid"), {
        href: "/dash/orders?status=paid",
        dropped: null,
    });
    assert.deepEqual(at("/dash/settings/taxes", "/dash/settings/taxes"), {
        href: "/dash/settings/taxes",
        dropped: null,
    });
});

test("a page of the old store's list is not a page of the new one's", () => {
    assert.equal(at("/dash/orders", "/dash/orders?page=4").href, "/dash/orders");
    assert.equal(
        at("/dash/orders", "/dash/orders?status=paid&cursor=abc&after=1&before=2").href,
        "/dash/orders?status=paid",
    );
});

test("a row of the old store is cut back to its section", () => {
    assert.deepEqual(at("/dash/orders/[id]", "/dash/orders/1234"), {
        href: "/dash/orders",
        dropped: "1234",
    });
    assert.deepEqual(at("/dash/orders/[id]/print", "/dash/orders/1234/print?copy=2"), {
        href: "/dash/orders",
        dropped: "1234",
    });
    assert.equal(at("/dash/settings/roles/[role]", "/dash/settings/roles/packer").href, "/dash/settings/roles");
});

test("a plugin's screen is named by the plugin, not by a row, and stays", () => {
    assert.deepEqual(at("/dash/x/[slug]", "/dash/x/agent"), { href: "/dash/x/agent", dropped: null });
});

test("route groups are not part of the address", () => {
    assert.deepEqual(at("/(app)/dash/orders/[id]", "/dash/orders/9"), { href: "/dash/orders", dropped: "9" });
});

test("anywhere without a route lands at home", () => {
    assert.deepEqual(switchTarget(null, new URL("http://panel.test/nowhere")), { href: "/dash", dropped: null });
});

const NAV = [
    { href: "/dash", label: "Home", exact: true },
    {
        href: "/dash/orders",
        label: "Orders",
        right: "orders.read",
        children: [{ href: "/dash/invoices", label: "Invoices", right: "invoices.read", module: "invoices" }],
    },
    { href: "/dash/settings", label: "Settings", right: "settings.read" },
    { href: "/dash/settings/taxes", label: "Taxes", right: "taxes.read" },
];

test("a page's section is the longest nav entry that holds it", () => {
    assert.equal(sectionFor("/dash/orders", NAV).label, "Orders");
    assert.equal(sectionFor("/dash/invoices", NAV).module, "invoices");
    assert.equal(sectionFor("/dash/settings/taxes", NAV).right, "taxes.read");
    assert.equal(sectionFor("/dash/settings/team", NAV).label, "Settings");
    // A whole segment, never part of one.
    assert.equal(sectionFor("/dash/ordersx", NAV)?.label, "Home");
});

test("the note names the row in the singular", () => {
    assert.equal(droppedNote({ label: "Orders" }, "1234", "Store A"), "Order 1234 belongs to Store A");
    assert.equal(droppedNote({ label: "Companies" }, "7", "North"), "Company 7 belongs to North");
    assert.equal(droppedNote(null, "7", "North"), "7 belongs to North");
});
