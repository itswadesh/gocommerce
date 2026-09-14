/*
 * The rules that decide what reaches PATCH /api/admin/variants/{id}.
 *
 * Every assertion here pins something that was unreachable or wrong before the
 * variant detail drawer existed: ten fields that a product with an option axis
 * could not set at all, an emptied compare-at that has to travel as an explicit
 * null, and stock that must never ride in the patch.
 *
 * The locale is explicit throughout. Without it these would assert against
 * whatever CI happens to run in, which is the same class of mistake the money
 * parsing in format.js was making.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    convertDimension,
    convertWeight,
    dimensionNumbers,
    duplicateMetafield,
    metafieldRows,
    metafieldsObject,
    moveWithin,
    resequence,
    validateVariant,
    variantPatch,
    variantShape,
    weightNumber,
} from "../src/lib/variantform.js";

const EN = "en-US";

/** A variant as the engine sends one, with every optional field filled in. */
function variant(overrides = {}) {
    return {
        id: 7,
        sku: "TEE-M",
        barcode: "5012345678900",
        price: { amount_minor: 2500, currency: "USD" },
        compare_at_price: { amount_minor: 3000, currency: "USD" },
        cost: { amount_minor: 900, currency: "USD" },
        taxable: true,
        track_inventory: true,
        continue_selling: false,
        active: true,
        origin_country: "IN",
        hs_code: "610910",
        stock_on_hand: 12,
        stock_reserved: 2,
        available: 10,
        position: 3,
        weight_grams: 2500,
        weight_unit: "kg",
        weight: "2.5 kg",
        metadata: { custom: { bin: "A12" }, invoices: { ref: "keep me" } },
        ...overrides,
    };
}

test("the shape carries every field the matrix has no column for", () => {
    const form = variantShape(variant(), "USD");
    assert.equal(form.barcode, "5012345678900");
    assert.equal(form.compare_at, "30.00");
    assert.equal(form.cost, "9.00");
    assert.equal(form.taxable, true);
    assert.equal(form.track_inventory, true);
    assert.equal(form.continue_selling, false);
    assert.equal(form.active, true);
    assert.equal(form.origin_country, "IN");
    assert.equal(form.hs_code, "610910");
    assert.equal(form.weight_unit, "kg");
    assert.equal(form.position, 3);
    assert.deepEqual(form.metafields, [{ key: "bin", value: "A12" }]);
});

test("an absent compare-at or cost is an empty box, not a zero", () => {
    // Zero is a price. "Nobody has recorded one" is not, and a 0.00 cost
    // reports a 100% margin on something nobody has costed.
    const form = variantShape(
        variant({ compare_at_price: undefined, cost: undefined }),
        "USD",
    );
    assert.equal(form.compare_at, "");
    assert.equal(form.cost, "");
});

test("the weight box reads the engine's own rendering of the mass", () => {
    // "2.5 kg" rather than 2500 grams: weight.go decides how many grams a
    // kilogram is, and a second copy of that table here is how the two come to
    // disagree about a pound.
    assert.equal(weightNumber({ weight: "2.5 kg", weight_grams: 2500 }), 2.5);
    // Nothing rendered — an older record — falls back to the grams.
    assert.equal(weightNumber({ weight_grams: 400 }), 400);
    assert.equal(weightNumber(null), 0);
});

test("an unchanged form sends nothing at all", () => {
    const shape = variantShape(variant(), "USD");
    const { body, stock } = variantPatch(shape, variantShape(variant(), "USD"), "USD", EN);
    assert.deepEqual(body, {});
    assert.equal(stock, null);
});

test("an emptied compare-at is an explicit null, not an absent field", () => {
    // The one that kept a struck-through price on a storefront for ever: an
    // absent field means "leave it", and only null means "this is not on sale
    // any more".
    const snapshot = variantShape(variant(), "USD");
    const form = { ...snapshot, compare_at: "" };
    const { body } = variantPatch(form, snapshot, "USD", EN);
    assert.equal(body.compare_at_price_minor, null);
    assert.ok("compare_at_price_minor" in body);
});

test("an emptied cost is null too, and a typed one is minor units", () => {
    const snapshot = variantShape(variant(), "USD");
    assert.equal(variantPatch({ ...snapshot, cost: "" }, snapshot, "USD", EN).body.cost_minor, null);
    assert.equal(
        variantPatch({ ...snapshot, cost: "12.34" }, snapshot, "USD", EN).body.cost_minor,
        1234,
    );
});

test("the ten fields the option-axis products could not reach all travel", () => {
    const snapshot = variantShape(variant(), "USD");
    const form = {
        ...snapshot,
        barcode: "  9999  ",
        compare_at: "40",
        cost: "5",
        taxable: false,
        track_inventory: false,
        continue_selling: true,
        active: false,
        origin_country: " de ",
        hs_code: " 6109.10 ",
        weight: 3,
        weight_unit: "lb",
        position: 0,
        metafields: [{ key: "bin", value: "B7" }],
    };
    const { body } = variantPatch(form, snapshot, "USD", EN);
    assert.equal(body.barcode, "9999");
    assert.equal(body.compare_at_price_minor, 4000);
    assert.equal(body.cost_minor, 500);
    assert.equal(body.taxable, false);
    assert.equal(body.track_inventory, false);
    assert.equal(body.continue_selling, true);
    assert.equal(body.active, false);
    // Folded, because the column holds ISO codes and "de" is the same country.
    assert.equal(body.origin_country, "DE");
    assert.equal(body.hs_code, "6109.10");
    // The value and its unit, never grams — the engine owns the conversion.
    assert.equal(body.weight, 3);
    assert.equal(body.weight_unit, "lb");
    assert.equal(body.position, 0);
    // Whole, not merged: the module's own key has to ride along or the patch
    // would delete it.
    assert.deepEqual(body.metadata, { custom: { bin: "B7" } });
});

test("a metafield edit carries a module's own keys across", () => {
    // The engine replaces `metadata` whole rather than merging it, so a patch
    // that wrote only `{custom: …}` would delete whatever ext/invoices keeps
    // beside it — silently, and only noticed much later.
    const v = variant();
    const snapshot = variantShape(v, "USD");
    const form = { ...snapshot, metafields: [{ key: "bin", value: "C3" }] };
    const { body } = variantPatch(form, snapshot, "USD", EN, v.metadata);
    assert.deepEqual(body.metadata, {
        custom: { bin: "C3" },
        invoices: { ref: "keep me" },
    });
});

test("changing only the unit still sends both halves", () => {
    // The unit is how the mass is read back, and the engine resolves the pair.
    // Sending the unit alone would re-read 2.5 as 2.5 grams.
    const snapshot = variantShape(variant(), "USD");
    const { body } = variantPatch({ ...snapshot, weight_unit: "g" }, snapshot, "USD", EN);
    assert.equal(body.weight, 2.5);
    assert.equal(body.weight_unit, "g");
});

test("stock never rides in the patch", () => {
    const snapshot = variantShape(variant(), "USD");
    const { body, stock } = variantPatch(
        { ...snapshot, stock_on_hand: 40 },
        snapshot,
        "USD",
        EN,
    );
    // It goes to the inventory route as a movement, so a sale landing mid-edit
    // is not overwritten by a number read before it happened.
    assert.deepEqual(body, {});
    assert.equal(stock, 40);
});

test("an untracked variant's stock box is not a stock take", () => {
    const snapshot = variantShape(variant({ track_inventory: false }), "USD");
    const { stock } = variantPatch({ ...snapshot, stock_on_hand: 40 }, snapshot, "USD", EN);
    assert.equal(stock, null);
});

test("a position that is not a number is left alone", () => {
    // An emptied number input binds through as "" or null, and sending that as
    // a position would ask the engine to file the variant at zero.
    const snapshot = variantShape(variant(), "USD");
    assert.equal("position" in variantPatch({ ...snapshot, position: "" }, snapshot, "USD", EN).body, false);
    assert.equal(
        "position" in variantPatch({ ...snapshot, position: -2 }, snapshot, "USD", EN).body,
        false,
    );
});

test("validation refuses what the old guards let through", () => {
    const form = variantShape(variant(), "USD");
    assert.deepEqual(validateVariant(form, EN), {});
    // parseFloat("12abc") is 12, so every isNaN guard in the panel passed it.
    assert.ok(validateVariant({ ...form, price: "12abc" }, EN).price);
    assert.ok(validateVariant({ ...form, sku: "   " }, EN).sku);
    // An empty optional amount is a real answer; an unreadable one is not.
    assert.deepEqual(validateVariant({ ...form, cost: "" }, EN), {});
    assert.ok(validateVariant({ ...form, cost: "abc" }, EN).cost);
    assert.ok(
        validateVariant(
            { ...form, metafields: [{ key: "bin", value: "1" }, { key: "bin", value: "2" }] },
            EN,
        ).metafields,
    );
});

test("metafields keep a module's keys and drop the abandoned rows", () => {
    // The top level of `metadata` is the engine's extension point, so the
    // operator's own fields are namespaced under `custom` and everything beside
    // them has to survive the round trip.
    assert.deepEqual(metafieldRows(variant()), [{ key: "bin", value: "A12" }]);
    // Anything a module wrote as an object shows as JSON rather than
    // "[object Object]", so it round-trips.
    assert.deepEqual(metafieldRows({ metadata: { custom: { size: { w: 1 } } } }), [
        { key: "size", value: '{"w":1}' },
    ]);
    assert.deepEqual(
        metafieldsObject([
            { key: " keep ", value: "yes" },
            { key: "", value: "abandoned" },
        ]),
        { keep: "yes" },
    );
    assert.equal(duplicateMetafield([{ key: "a" }, { key: " a " }]), "a");
    assert.equal(duplicateMetafield([{ key: "a" }, { key: "b" }]), "");
});

test("switching units re-reads the same parcel", () => {
    // 2.5 kg did not get lighter because somebody wanted to read it in pounds.
    assert.equal(convertWeight(2.5, "kg", "lb"), 5.512);
    assert.equal(convertWeight(2.5, "kg", "g"), 2500);
    // Grams are stored whole, so the box says what the record will say.
    assert.equal(convertWeight(1.4, "oz", "g"), 40);
});

test("moving a variant renumbers the list and sends only what moved", () => {
    const rows = [
        { id: 1, position: 0 },
        { id: 2, position: 1 },
        { id: 3, position: 2 },
    ];
    const next = moveWithin(rows, 2, 0);
    assert.deepEqual(next.map((v) => v.id), [3, 1, 2]);
    assert.deepEqual(
        resequence(next).map((w) => [w.variant.id, w.position]),
        [
            [3, 0],
            [1, 1],
            [2, 2],
        ],
    );
});

test("a list that was never numbered is renumbered whole", () => {
    // An import can leave every variant at 0, and swapping two zeroes moves
    // nothing — which is what the old "swap the positions" shape would have
    // done for ever.
    const rows = [
        { id: 1, position: 0 },
        { id: 2, position: 0 },
    ];
    assert.deepEqual(
        resequence(moveWithin(rows, 1, 0)).map((w) => [w.variant.id, w.position]),
        [[1, 1]],
    );
});

test("a move off the end of the list is not a wrap-around", () => {
    const rows = [{ id: 1 }, { id: 2 }];
    assert.deepEqual(moveWithin(rows, 0, -1).map((v) => v.id), [1, 2]);
    assert.deepEqual(moveWithin(rows, 1, 2).map((v) => v.id), [1, 2]);
});

/*
 * The parcel: three sides plus the unit they are read in.
 *
 * The distinction every assertion below turns on is unmeasured versus zero. A
 * side nobody has measured is paperwork still to do; a side of zero is a flat
 * parcel, and a carrier will quote for one.
 */

test("a parcel is read back out of what the engine rendered", () => {
    assert.deepEqual(dimensionNumbers({ size: "30 × 20 × 45 cm" }), {
        length: 30,
        width: 20,
        height: 45,
    });
    // An em dash is the engine saying that side was never measured, and it has
    // to come back as an empty box rather than as a zero.
    assert.deepEqual(dimensionNumbers({ size: "12 × — × 11 cm" }), {
        length: 12,
        width: "",
        height: 11,
    });
    // Nothing measured at all, and a variant from a store that predates the
    // field, are the same empty three boxes.
    assert.deepEqual(dimensionNumbers({ size: "" }), { length: "", width: "", height: "" });
    assert.deepEqual(dimensionNumbers({}), { length: "", width: "", height: "" });
    assert.deepEqual(dimensionNumbers(undefined), { length: "", width: "", height: "" });
});

test("switching units re-reads the same box", () => {
    // 30 cm did not shrink because somebody wanted to read it in inches.
    assert.equal(convertDimension(30, "cm", "in"), 11.811);
    assert.equal(convertDimension(30, "cm", "mm"), 300);
    // Millimetres are stored whole, so the box says what the record will say.
    assert.equal(convertDimension(1, "in", "mm"), 25);
    // An unmeasured side stays unmeasured. Converting "" to 0 would turn "we
    // have not measured the width" into "the width is zero" on a unit switch.
    assert.equal(convertDimension("", "cm", "in"), "");
});

test("an emptied side travels as null, not as zero", () => {
    const shape = variantShape(variant({ size: "30 × 20 × 45 cm", dimension_unit: "cm" }), "USD");
    assert.deepEqual(
        { length: shape.length, width: shape.width, height: shape.height },
        { length: 30, width: 20, height: 45 },
    );

    const form = { ...shape, width: "" };
    const { body } = variantPatch(form, shape, "USD", EN);
    assert.deepEqual(body.size, { length: 30, width: null, height: 45 });
    assert.equal(body.dimension_unit, "cm");
});

test("a parcel nobody touched is not in the patch", () => {
    const shape = variantShape(variant({ size: "30 × 20 × 45 cm", dimension_unit: "cm" }), "USD");
    const { body } = variantPatch({ ...shape }, shape, "USD", EN);
    assert.equal("size" in body, false);
    assert.equal("dimension_unit" in body, false);
});

test("all three sides travel together when one moves", () => {
    const shape = variantShape(variant({ size: "30 × 20 × 45 cm", dimension_unit: "cm" }), "USD");
    // Only the height moved, but the unit beside it applies to all three: a
    // patch naming the height alone would leave the other two to be re-read in
    // a unit they were never converted to.
    const { body } = variantPatch({ ...shape, height: 50 }, shape, "USD", EN);
    assert.deepEqual(body.size, { length: 30, width: 20, height: 50 });
});
