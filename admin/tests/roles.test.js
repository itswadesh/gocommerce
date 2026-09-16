/*
 * The roles screens, checked against the one rights table.
 *
 * The screen used to carry its own copy of the right-to-English map, and the
 * copy had fallen a right behind: store.operate had no sentence and no group,
 * so the right that carries the outbox and the maintenance sweeps rendered in
 * an "Other" bucket with a blank help line — on the screen whose whole job is
 * to explain what granting a right means.
 *
 * The Go drift test (core/ops_http_test.go) pins rights.js against the engine.
 * These pin the screens against rights.js, which is the half a Go test cannot
 * see: they read component source, because node:test cannot import a `.svelte`
 * file.
 *
 * There are two screens now — a list of roles and a page per role — and the
 * grid moved to the second. The check that every right is *named* in the
 * source went with it, and was replaced by something stronger: the grid is
 * built by rightsBySection() from what the API sent, so it cannot fall behind
 * at all. What these tests defend is that it stays that way.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { RIGHT_ORDER } from "../src/lib/rights.js";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const list = read("../src/routes/settings/roles/+page.svelte");
const detail = read("../src/routes/settings/roles/[role]/+page.svelte");

test("neither roles screen carries a rights table of its own", () => {
    for (const [name, source] of [
        ["the roles list", list],
        ["the role page", detail],
    ]) {
        assert.ok(!source.includes("RIGHT_TEXT"), `${name} carries a second right-to-English map`);
        assert.ok(
            /from "\$lib\/rights\.js"/.test(source),
            `${name} should take its English from $lib/rights.js`,
        );
    }
    assert.ok(detail.includes("rightScope"), "the role page should show each right's scope");
    assert.ok(list.includes("rightLabel"), "the list should name rights in English, not dotted");
});

test("the grid is derived from the rights table, never enumerated", () => {
    // The old version of this test asserted every right appeared verbatim in
    // the source, which made adding a right to the engine a two-file change
    // and made forgetting the second file silent. Deriving is the fix; this
    // stops anybody quietly going back.
    assert.ok(
        detail.includes("rightsBySection"),
        "the role page should group rights with rightsBySection()",
    );
    // `roles.write` is the right that gates this screen, not a row in its
    // grid, so it is the one dotted name allowed to appear.
    const hardcoded = RIGHT_ORDER.filter(
        (r) => r !== "roles.write" && detail.includes(`"${r}"`),
    );
    assert.deepEqual(
        hardcoded,
        [],
        "the role page hardcodes rights that should come from the rights table: " + hardcoded,
    );
});

test("the diff against the shipped default is rendered, not just counted", () => {
    // `default` was fetched for three releases and used only to disable the
    // Reset button, so a role could be labelled "customised" with no way to
    // see how — and Reset was a control that changed several rights at once
    // without saying which.
    for (const marker of ["added", "removed", "function diff(", "resetTitle"]) {
        assert.ok(detail.includes(marker), `the role page no longer renders ${marker}`);
    }
});

test("a role's name and description are editable, and saved apart from its rights", () => {
    // They are two writes on purpose: the two are edited at different moments,
    // and one Save for both would make a rename overwrite a colleague's grant
    // made a second earlier.
    assert.ok(
        detail.includes("rolesApi.rename(") && detail.includes("rolesApi.save("),
        "the role page should write the label and the grants through two calls, not one",
    );
    assert.ok(
        /textarea/.test(detail),
        "the description should be a textarea, not a single-line box",
    );
});
