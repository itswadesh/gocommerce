/*
 * A card on Payment methods or Shipping providers: the plugin that switches
 * the provider, and what its label says. Cash on delivery is built in and
 * switched by a plugin of its own (D102), named by the engine because core is
 * no module to look a plugin up by.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { providerCard } from "../src/lib/provider-cards.js";

const cod = { code: "cod", name: "Cash on delivery", module: "core", configured: true, plugin: "payments-cod" };
const codPlugin = (enabled) => ({ key: "payments-cod", module: "", enabled });
const stripePlugin = (enabled) => ({ key: "payments-stripe", module: "payments-stripe", enabled });

test("cash on delivery is switched by the plugin the engine names, and is green when on", () => {
    const card = providerCard(cod, [stripePlugin(true), codPlugin(true)]);
    assert.equal(card.plugin.key, "payments-cod");
    assert.deepEqual(card.state, { label: "Active", cls: "label-success" });
});

test("switched off, cash on delivery says Off", () => {
    const card = providerCard({ ...cod, configured: false }, [codPlugin(false)]);
    assert.deepEqual(card.state, { label: "Off", cls: "" });
});

test("a built-in with no plugin of its own has nothing to switch", () => {
    const manual = { code: "manual", name: "Manual", module: "core", configured: true };
    const card = providerCard(manual, [codPlugin(true)]);
    assert.equal(card.plugin, null);
    assert.deepEqual(card.state, { label: "Built in", cls: "" });
});

test("a module's provider is still switched by its module's plugin", () => {
    const stripe = { code: "stripe", name: "Stripe", module: "payments-stripe", configured: false };
    assert.equal(providerCard(stripe, [stripePlugin(true)]).state.label, "Needs settings");
    assert.equal(providerCard(stripe, [stripePlugin(false)]).state.label, "Inactive");
    assert.deepEqual(providerCard({ ...stripe, configured: true }, [stripePlugin(true)]).state, { label: "Active", cls: "label-success" });
});
