import { test } from "node:test";
import assert from "node:assert/strict";
import { discountCodeProblem } from "../src/lib/discount-code.js";

test("letters and digits up to 20 pass, and an empty code is allowed", () => {
    for (const code of ["", "  ", "SPRING24", "a1", "A".repeat(20), " SALE10 "]) {
        assert.equal(discountCodeProblem(code), "", code);
    }
});

test("too long or with other characters is refused, and says which", () => {
    assert.match(discountCodeProblem("A".repeat(21)), /20/);
    for (const code of ["SAVE-10", "SAVE_10", "SAVE 10", "SAVE%", "छूट10"]) {
        assert.match(discountCodeProblem(code), /letters and digits/, code);
    }
});

test("a code the discount already has passes until it is changed", () => {
    assert.equal(discountCodeProblem("OLD-CODE", "OLD-CODE"), "");
    assert.match(discountCodeProblem("OLD-CODE2", "OLD-CODE"), /letters and digits/);
});
