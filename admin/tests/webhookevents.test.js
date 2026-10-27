/*
 * The webhook event picker. A merchant ticks names from the engine's list;
 * what is stored has to be exactly what was ticked, or an endpoint hears
 * events nobody asked it to.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { eventsToSend, groupEvents, toggleGroup } from "../src/lib/webhookevents.js";

const catalog = [
    { name: "order.created", group: "Orders", description: "" },
    { name: "order.paid", group: "Orders", description: "" },
    { name: "order.unpaid", group: "Corrections", description: "" },
    { name: "cart.abandoned", group: "Carts", description: "" },
];

test("events are grouped in the engine's order", () => {
    assert.deepEqual(
        groupEvents(catalog).map((g) => [g.group, g.events.map((e) => e.name)]),
        [
            ["Orders", ["order.created", "order.paid"]],
            ["Corrections", ["order.unpaid"]],
            ["Carts", ["cart.abandoned"]],
        ],
    );
});

test("what is stored is the ticked names in the list's order, or * for everything", () => {
    assert.deepEqual(eventsToSend({ all: false, picked: ["cart.abandoned", "order.created"] }, catalog), [
        "order.created",
        "cart.abandoned",
    ]);
    assert.deepEqual(eventsToSend({ all: true, picked: ["order.paid"] }, catalog), ["*"]);
    assert.deepEqual(eventsToSend({ all: false, picked: ["order.payed"] }, catalog), [], "a name the engine does not send is dropped");
});

test("a group's box ticks the whole group, and clears it when it is already full", () => {
    const orders = groupEvents(catalog)[0];
    assert.deepEqual(toggleGroup(["cart.abandoned", "order.paid"], orders).sort(), ["cart.abandoned", "order.created", "order.paid"]);
    assert.deepEqual(toggleGroup(["order.created", "order.paid"], orders), []);
});
