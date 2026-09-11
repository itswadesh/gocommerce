/**
 * One variant's own fields, as a form and as a patch.
 *
 * The single-variant product editor has held these for a long time, inside
 * `{#if !hasOptions}` — so the moment a product gained an option axis, barcode,
 * compare-at, cost, taxable, tracking, continue-selling, origin, HS code, the
 * weight unit and metadata all became unreachable. The variant detail drawer is
 * where they live for a product that has options, and this module is the part
 * of it that has no markup: what a variant looks like as a form, and what the
 * difference between two of those looks like as a `PATCH /api/admin/variants`.
 *
 * It is a plain module on purpose. The panel's tests run under `node --test`
 * with no browser and no component runtime, so the rules that decide what
 * reaches the wire — an emptied compare-at meaning null rather than nothing, a
 * country folded to upper case, stock never riding in the patch — are testable
 * exactly where they are most expensive to get wrong.
 */

/* Relative rather than `$lib/`, which is the convention everywhere else here:
   `$lib` is Vite's alias and `node --test` has no resolver for it, so a module
   importing it cannot be imported by the panel's own test suite. */
import { fromMinor, isValidMoney, toMinor } from "./format.js";

/**
 * The number to show in a variant's weight box, in the variant's own unit.
 *
 * The engine already renders the stored mass the way its unit wants to be read
 * — "2.5 kg" — so the figure is taken back out of that string rather than
 * computed here. One less place that could come to disagree with weight.go
 * about how many grams a pound is.
 */
export function weightNumber(variant) {
    const shown = parseFloat(String(variant?.weight ?? ""));
    return isFinite(shown) ? shown : (variant?.weight_grams ?? 0);
}

/**
 * The operator's own metafields, as rows.
 *
 * Namespaced under `custom` for the same reason the product editor namespaces
 * its own: the top level of `metadata` is the engine's extension point, and a
 * module keeping data on a variant there must not be overwritten by somebody
 * typing a key into a free-form box.
 */
export function metafieldRows(variant) {
    const custom = variant?.metadata?.custom ?? {};
    return Object.entries(custom).map(([key, value]) => ({
        key,
        // Anything a module wrote as a number or an object is shown as JSON
        // rather than "[object Object]", so it round-trips.
        value: typeof value === "string" ? value : JSON.stringify(value),
    }));
}

/** The metafield rows as the object that goes on the wire, under `custom`. */
export function metafieldsObject(rows) {
    const out = {};
    for (const row of rows ?? []) {
        const key = String(row.key ?? "").trim();
        // A row with no name is dropped rather than saved as "": an empty box
        // is a row somebody started and abandoned.
        if (key) out[key] = row.value;
    }
    return out;
}

/** The first metafield name used twice, or "" — two rows would become one key. */
export function duplicateMetafield(rows) {
    const seen = new Set();
    for (const row of rows ?? []) {
        const key = String(row.key ?? "").trim();
        if (!key) continue;
        if (seen.has(key)) return key;
        seen.add(key);
    }
    return "";
}

/**
 * variantShape is the variant as the form holds it.
 *
 * `currency` is the variant's own code — the engine stamps one on every amount
 * it sends — and it is captured rather than derived because it is what the
 * money boxes were filled from and what they will be read back with.
 */
export function variantShape(variant, currency) {
    return {
        sku: variant?.sku ?? "",
        barcode: variant?.barcode ?? "",
        price: variant ? fromMinor(variant.price.amount_minor, currency) : "",
        compare_at: variant?.compare_at_price
            ? fromMinor(variant.compare_at_price.amount_minor, currency)
            : "",
        cost: variant?.cost ? fromMinor(variant.cost.amount_minor, currency) : "",
        taxable: variant?.taxable ?? true,
        track_inventory: variant?.track_inventory ?? true,
        continue_selling: variant?.continue_selling ?? false,
        active: variant?.active ?? true,
        origin_country: variant?.origin_country ?? "",
        hs_code: variant?.hs_code ?? "",
        // Numbers rather than strings, because `bind:value` on a number input
        // writes a number back — a string here would make the field read as
        // changed the first time it was touched.
        stock_on_hand: variant?.stock_on_hand ?? 0,
        position: variant?.position ?? 0,
        weight: weightNumber(variant),
        weight_unit: variant?.weight_unit || "g",
        metafields: metafieldRows(variant),
    };
}

/**
 * validateVariant answers what still needs attention, keyed by field.
 *
 * `isValidMoney` rather than `isNaN(parseFloat(...))`: parseFloat reads "24,99"
 * as 24, and this guard is what keeps toMinor from having to answer null on the
 * way to the wire. The two optional amounts are checked only when they hold
 * something — an empty box is a real answer on both.
 */
export function validateVariant(form, locale) {
    const errors = {};
    if (!String(form.sku ?? "").trim()) errors.sku = "A SKU is required.";
    if (!isValidMoney(form.price, locale)) errors.price = "A price is required.";
    for (const [field, message] of [
        ["compare_at", "Enter a compare-at price, or empty the box."],
        ["cost", "Enter a cost, or empty the box."],
    ]) {
        const raw = String(form[field] ?? "").trim();
        if (raw !== "" && !isValidMoney(raw, locale)) errors[field] = message;
    }
    const duplicate = duplicateMetafield(form.metafields);
    if (duplicate) {
        errors.metafields = `Two metafields are called “${duplicate}”. Only the last would be saved.`;
    }
    return errors;
}

/**
 * variantPatch is the difference between two shapes, as the engine takes it.
 *
 * Only what moved is sent. A patch naming every field would overwrite whatever
 * another operator changed in the meantime with values this drawer read before
 * they did, and would make every save look like a change to the audit trail.
 *
 * Stock is deliberately not part of the body. The engine takes it as a
 * movement, so a sale that lands mid-edit is not silently overwritten by a
 * number read before it happened — `stock` is returned beside the body for the
 * caller to send to the inventory route, or null when it did not move.
 *
 * `metadata` is the variant's metadata as it stands, because the patch replaces
 * that object whole: writing only `{custom: …}` would delete every key a module
 * keeps on this variant.
 */
export function variantPatch(form, snapshot, currency, locale, metadata = {}) {
    const body = {};
    const text = (key) => String(form[key] ?? "").trim();

    if (text("sku") !== snapshot.sku) body.sku = text("sku");
    if (text("barcode") !== snapshot.barcode) body.barcode = text("barcode");
    if (form.price !== snapshot.price) body.price_minor = toMinor(form.price, currency, locale);
    if (form.compare_at !== snapshot.compare_at) {
        // An emptied box is "this is not on sale any more", which the patch
        // expresses as null — not as an absent field, which would leave the
        // struck-through price on the storefront for ever, and not as 0, which
        // would strike through 0.00.
        body.compare_at_price_minor =
            text("compare_at") === "" ? null : toMinor(form.compare_at, currency, locale);
    }
    if (form.cost !== snapshot.cost) {
        // An emptied box is "no cost recorded", which the column stores as null
        // — not as zero, which would report a 100% margin on something nobody
        // has costed.
        body.cost_minor = text("cost") === "" ? null : toMinor(form.cost, currency, locale);
    }
    if (form.taxable !== snapshot.taxable) body.taxable = form.taxable;
    if (form.track_inventory !== snapshot.track_inventory) {
        body.track_inventory = form.track_inventory;
    }
    if (form.continue_selling !== snapshot.continue_selling) {
        body.continue_selling = form.continue_selling;
    }
    if (form.active !== snapshot.active) body.active = form.active;
    if (text("origin_country").toUpperCase() !== snapshot.origin_country) {
        body.origin_country = text("origin_country").toUpperCase();
    }
    if (text("hs_code") !== snapshot.hs_code) body.hs_code = text("hs_code");
    if (form.weight !== snapshot.weight || form.weight_unit !== snapshot.weight_unit) {
        // The value and its unit, never grams. resolveWeight() in the engine
        // owns the conversion; a second copy of the factors on this side of the
        // wire is how the two would eventually disagree about a pound.
        body.weight = parseFloat(form.weight) || 0;
        body.weight_unit = form.weight_unit;
    }
    const position = parseInt(form.position, 10);
    if (isFinite(position) && position >= 0 && position !== snapshot.position) {
        body.position = position;
    }
    if (JSON.stringify(form.metafields) !== JSON.stringify(snapshot.metafields)) {
        body.metadata = { ...metadata, custom: metafieldsObject(form.metafields) };
    }

    const stock =
        form.track_inventory && form.stock_on_hand !== snapshot.stock_on_hand
            ? parseInt(form.stock_on_hand, 10) || 0
            : null;

    return { body, stock };
}

/**
 * The unit factors, exact by definition rather than by measurement — the same
 * table weight.go holds.
 *
 * They are here for *display* only. What gets saved is always the number and
 * the unit as typed, so the engine remains the single thing that decides how
 * many grams that is; this only answers "the same parcel, read in a different
 * unit", which is a question no request can be made of.
 */
const GRAMS_PER = { g: 1, kg: 1000, oz: 28.349523125, lb: 453.59237 };

/**
 * convertWeight re-reads the same mass in a new unit. 2.5 kg becomes 5.512 lb,
 * because the parcel did not get lighter when the operator changed how they
 * wanted to read it.
 *
 * Grams are stored whole and the engine renders every other unit to three
 * decimals, so matching it means the box says what the record will say once it
 * is saved.
 */
export function convertWeight(value, from, to) {
    const grams = (parseFloat(value) || 0) * (GRAMS_PER[from] ?? 1);
    const out = grams / (GRAMS_PER[to] ?? 1);
    return to === "g" ? Math.round(out) : Math.round(out * 1000) / 1000;
}

/**
 * resequence renumbers a list of variants from zero, and reports only the ones
 * whose stored position has to change.
 *
 * Swapping two positions is not enough on its own: a catalogue imported before
 * position was written per row can hold several variants at 0, and swapping two
 * zeroes moves nothing. Renumbering the whole list is always correct, and
 * sending only the rows that differ keeps the ordinary case — one variant moved
 * one place — at two requests.
 */
export function resequence(ordered) {
    const out = [];
    ordered.forEach((variant, index) => {
        if (variant.position !== index) out.push({ variant, position: index });
    });
    return out;
}

/**
 * moved returns `list` with the item at `from` shifted one place towards `to`.
 * Out-of-range moves return the list unchanged rather than wrapping around:
 * "up" on the first row is a no-op, not a jump to the bottom.
 */
export function moveWithin(list, from, to) {
    if (from < 0 || to < 0 || from >= list.length || to >= list.length || from === to) {
        return [...list];
    }
    const next = [...list];
    const [item] = next.splice(from, 1);
    next.splice(to, 0, item);
    return next;
}
