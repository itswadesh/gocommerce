/**
 * Prices typed straight into the products list.
 *
 * A product row has one price box that sets every variant, and one for the
 * MRP (the struck-through compare-at price); its expander has the same two per
 * variant. These are the rules those boxes follow, kept out of the component
 * so node can test them: what a typed value means, what a row shows when its
 * variants disagree, and which products may be published.
 *
 * Money stays minor units plus the currency on the wire (rule 6); the boxes
 * hold the decimal a person types, through toMinor and fromMinor.
 */

import { fromMinor, toMinor } from "./format.js";

/**
 * A product needs a price while any variant is at 0. A product drafted from
 * photos is created that way, and the list's "Needs a price" filter is the
 * engine's `price_max=0`, which asks the same question.
 */
export function needsPrice(product) {
    return (product?.variants ?? []).some((v) => (v.price?.amount_minor ?? 0) === 0);
}

/**
 * The value a row's box shows: the variants' common price as a decimal, or ""
 * when they differ (the box then says "Mixed") or none is set. `key` is
 * "price" or "compare_at_price".
 */
export function commonValue(variants, key, currency) {
    const amounts = (variants ?? []).map((v) => v[key]?.amount_minor ?? null);
    if (!amounts.length || amounts.some((a) => a !== amounts[0])) return "";
    if (amounts[0] === null || (key === "price" && amounts[0] === 0)) return "";
    return fromMinor(amounts[0], currency);
}

/** Whether the variants disagree on `key`, so an empty box means "Mixed". */
export function mixed(variants, key) {
    const amounts = (variants ?? []).map((v) => v[key]?.amount_minor ?? null);
    return amounts.some((a) => a !== amounts[0]);
}

/**
 * readPrice turns a price box into minor units. An empty box is no change,
 * not 0: 0 is what "needs a price" means, so it cannot be typed as a price.
 */
export function readPrice(text, currency) {
    const t = String(text ?? "").trim();
    if (!t) return { empty: true };
    const minor = toMinor(t, currency);
    if (minor === null) return { error: "That is not a price." };
    if (minor <= 0) return { error: "A price is more than 0." };
    return { minor };
}

/**
 * readMRP turns an MRP box into minor units, or null for an emptied box, which
 * takes the MRP off. An MRP is what the price is struck through against, so
 * one at or under the price says nothing true and is refused.
 */
export function readMRP(text, currency, priceMinor) {
    const t = String(text ?? "").trim();
    if (!t) return { minor: null };
    const minor = toMinor(t, currency);
    if (minor === null || minor < 0) return { error: "That is not an amount." };
    if (priceMinor != null && priceMinor > 0 && minor <= priceMinor) {
        return { error: "The MRP is the higher price the sale price is shown against." };
    }
    return { minor };
}

/**
 * splitPublishable is "Publish selected": the products that may go live, and
 * the ones refused because a variant has no price yet.
 */
export function splitPublishable(products) {
    const ok = [];
    const refused = [];
    for (const p of products ?? []) {
        if (p.status === "active") continue;
        (needsPrice(p) ? refused : ok).push(p);
    }
    return { ok, refused };
}

/** withAmount is a variant as it reads once an amount is saved, for the optimistic row. */
export function withAmount(variant, key, minor, currency) {
    return { ...variant, [key]: minor === null ? null : { amount_minor: minor, currency } };
}
