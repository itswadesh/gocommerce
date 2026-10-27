/**
 * The product editor's two upsell cards (D133): "Complete the set", the
 * add-ons a shopper ticks beside the buy button, and "You may also like", the
 * products suggested with it.
 *
 * An add-on's offer price is typed in the store's currency like every other
 * price on the page and crosses the wire as minor units. An empty box is "no
 * offer price, sell it at its own", which the engine reads from null; never
 * zero, which would give it away.
 */
import { fromMinor, isValidMoney, toMinor } from "./format.js";

/** addOnRows turns saved add-ons into editable rows. */
export function addOnRows(addOns, currency) {
    return (addOns ?? []).map((a) => ({
        variant_id: a.variant_id,
        title: a.item?.title ?? "",
        label: a.item?.variant_label ?? "",
        image: a.item?.image_url ?? "",
        live: a.item?.live !== false,
        price: a.price ?? null,
        offer: a.offer_price ? fromMinor(a.offer_price.amount_minor, currency) : "",
    }));
}

/** rowFromVariant is a row for a variant picked from the search box. */
export function rowFromVariant(v) {
    return {
        variant_id: v.id,
        title: v.product?.title ?? "",
        // A variant with no options has no label; its product's title says
        // it all.
        label: v.label ?? "",
        image: v.product?.image_url ?? "",
        live: true,
        price: v.price ?? null,
        offer: "",
    };
}

/**
 * offerProblem judges one row's offer box: an error stops the save, a
 * message without one is advice.
 */
export function offerProblem(row, currency) {
    const text = String(row.offer ?? "").trim();
    if (text === "") return { error: false, message: "" };
    if (!isValidMoney(text)) return { error: true, message: "Enter an amount, or empty the box." };
    const minor = toMinor(text, currency);
    if (minor === null || minor < 0) return { error: true, message: "An offer price cannot be below zero." };
    if (row.price && minor >= row.price.amount_minor) {
        return { error: false, message: "This is not below its own price, so it saves the shopper nothing." };
    }
    return { error: false, message: "" };
}

/** addOnsBody is the PUT body, or throws naming the first unreadable offer. */
export function addOnsBody(rows, currency) {
    return {
        add_ons: rows.map((row) => {
            const problem = offerProblem(row, currency);
            if (problem.error) throw new Error(`The offer price for ${row.title}: ${problem.message}`);
            const text = String(row.offer ?? "").trim();
            return { variant_id: row.variant_id, price_minor: text === "" ? null : toMinor(text, currency) };
        }),
    };
}

/** moved swaps item i with its neighbour in direction by (-1 or 1). */
export function moved(list, i, by) {
    const j = i + by;
    if (j < 0 || j >= list.length) return list;
    const out = [...list];
    [out[i], out[j]] = [out[j], out[i]];
    return out;
}
