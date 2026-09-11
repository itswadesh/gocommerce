/**
 * Running one action over a selection.
 *
 * There is no batch endpoint and this deliberately does not invent one: a bulk
 * action is N calls to the same per-row route a single row uses, so each lands
 * in its own transaction and emits its own event exactly as it does today.
 * Nothing here is a second write path.
 *
 * Partial failure is the NORMAL outcome of a mixed selection, not a bug. Marking
 * delivered is a 409 unless the order is shipped, a fulfillment can only be
 * created against a confirmed order, marking paid is a 409 on a cancelled one.
 * So a screen filters the selection by an `eligible(row)` predicate before
 * calling — the operator should not be told about failures they could have been
 * spared — and still reports whatever the engine refuses, row by row.
 */

import { toast } from "$lib/toast.svelte.js";
import { pluralize } from "$lib/format.js";

/**
 * runBulk applies `fn` to each row in turn and reports every failure.
 *
 * Sequential, and never aborting on the first error. Aborting is the wrong
 * shape here: half the selection would already have changed with nothing saying
 * which half. Sequential rather than parallel because these land on the same
 * rows, so concurrency buys nothing and costs the engine lock contention.
 *
 *     const { done, failures } = await runBulk(selected, (o) => api.post(...), {
 *         describe: "Marked paid",
 *         noun: "order",
 *         label: (o) => o.number,
 *     });
 *
 * `describe` is what to call what happened, and passing it is what makes this
 * report: one success naming the count, then one error per failed row. The noun
 * travels with it because "3 items" is a worse sentence than "3 orders" and
 * because the alternative is eight screens each pluralising for themselves.
 *
 * Omitting `describe` runs silently and hands back the same result, for a
 * caller whose reporting is not a toast.
 */
export async function runBulk(rows, fn, options = {}) {
    const { describe = "", noun = "item", plural = "", label = (row) => row?.id } = options;

    let done = 0;
    const failures = [];

    for (const row of rows ?? []) {
        try {
            await fn(row);
            done++;
        } catch (err) {
            // The engine's own message, not a summary of it: "only 2 left in
            // stock" is the sentence the operator needs, and the row it belongs
            // to is the other half of it.
            failures.push({ row, label: label(row), message: err?.message || "failed" });
        }
    }

    if (describe) {
        if (done) toast.success(`${describe} on ${done} ${pluralize(done, noun, plural)}`);
        // One toast per failure rather than one summary: a summary of four
        // different refusals says nothing the operator can act on.
        for (const failure of failures) {
            toast.error(failure.label ? `${failure.label}: ${failure.message}` : failure.message);
        }
    }

    return { done, failures };
}
