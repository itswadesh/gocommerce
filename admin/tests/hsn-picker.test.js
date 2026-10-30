/*
 * The HSN picker is bound to fields that can be missing (D171, D172). Svelte
 * stops the whole screen when a bound prop with a fallback is given
 * undefined, which is how Categories went blank. These read source, as the
 * other tests here do, because node:test cannot import a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("the HSN picker's value has no fallback, so a missing field cannot stop a screen", () => {
    assert.match(read("../src/lib/components/HsnPicker.svelte"), /value = \$bindable\(\),/);
});

test("the category form starts with an HSN code", () => {
    assert.match(read("../src/routes/dash/categories/+page.svelte"), /let form = \$state\(\{[^}]*hsn_code: ""/);
});
