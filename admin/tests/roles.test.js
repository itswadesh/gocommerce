/*
 * The roles matrix, checked against the one rights table.
 *
 * The screen used to carry its own copy of the right-to-English map, and the
 * copy had fallen a right behind: store.operate had no sentence and no group,
 * so the right that carries the outbox and the maintenance sweeps rendered in
 * an "Other" bucket with a blank help line — on the screen whose whole job is
 * to explain what granting a right means.
 *
 * The Go drift test (core/ops_http_test.go) pins rights.js against the engine.
 * This one pins the screen against rights.js, which is the half a Go test
 * cannot see: it reads the component's source, because GROUPS lives inside a
 * `.svelte` file and node:test cannot import one.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { RIGHT_ORDER } from "../src/lib/rights.js";

const source = readFileSync(
    new URL("../src/routes/settings/roles/+page.svelte", import.meta.url),
    "utf8",
);

test("the matrix has no rights table of its own", () => {
    assert.ok(
        !source.includes("RIGHT_TEXT"),
        "settings/roles carries a second right-to-English map; it should import rightScope",
    );
    assert.ok(
        source.includes("rightScope"),
        "settings/roles should take its scope sentences from $lib/rights.js",
    );
});

test("every right the engine has is placed in a group", () => {
    // The screen tolerates an unknown right — it falls into "Other" rather
    // than disappearing, which is the right behaviour at runtime. This is what
    // stops that fallback from quietly becoming where new rights live.
    for (const right of RIGHT_ORDER) {
        assert.ok(
            source.includes(`"${right}"`),
            `${right} is not named in the roles matrix, so it renders under "Other"`,
        );
    }
});

test("the diff against the shipped default is rendered, not just counted", () => {
    // `default` was fetched for three releases and used only to disable the
    // Reset button, so a role could be labelled "customised" with no way to
    // see how — and Reset was a control that changed several rights at once
    // without saying which.
    for (const marker of ["added", "removed", "cellDiff", "resetTitle"]) {
        assert.ok(source.includes(marker), `the matrix no longer renders ${marker}`);
    }
});
