import { test } from "node:test";
import assert from "node:assert/strict";

import { parseItemList } from "../src/lib/pastelist.js";

test("a pasted list reads the price off the end of each line", () => {
    assert.deepEqual(
        parseItemList("Chocolate cake - 450\n\n  Veg puff ₹25\n1. Samosa: 15/-\n- Thali Rs. 1,200\nPlum cake 12.50\nJust a name\n"),
        [
            { title: "Chocolate cake", price: "450" },
            { title: "Veg puff", price: "25" },
            { title: "Samosa", price: "15" },
            { title: "Thali", price: "1200" },
            { title: "Plum cake", price: "12.50" },
            { title: "Just a name", error: "No price at the end of the line" },
        ],
    );
});

test("a number that is the whole line is not a product", () => {
    assert.equal(parseItemList("450")[0].error, "No price at the end of the line");
});
