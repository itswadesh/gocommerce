/**
 * The review list behind "Fill from GST schedule" on the taxes screen (D175).
 *
 * The engine answers GET /api/admin/gst-rates/catalog with each category that
 * carries an HSN code and each product whose variants carry another, and the
 * schedule entries covering those codes, longest prefix first. This turns that
 * into rows a person ticks: the proposed rate is the longest entry's, every
 * other rate the schedule gives is still offered, and a row says why it needs
 * reading rather than choosing for the operator.
 */

/** "GST 18% · Apparel": the rate as people say it, then what it covers. */
export function ruleName(row) {
    return `GST ${row.options[row.choice].rate_bp / 100}% · ${row.name}`;
}

/**
 * Rows to review, and the targets the schedule has no rate for.
 *
 * `rates` is the tax table as GET /api/admin/tax-rates lists it: an Indian rule
 * for every state already on the same target is the one a row would replace.
 */
export function reviewRows(targets, rates) {
    const rows = [];
    const unmatched = [];
    for (const t of targets) {
        const own = (t.matches ?? []).filter((x) => !x.narrower);
        if (!own.length) {
            unmatched.push({
                ...t,
                reason: t.matches?.length
                    ? "the schedule rates the headings under this code, not the code itself"
                    : "no rate found",
            });
            continue;
        }
        // One option per rate: two entries at the same rate are the same
        // answer, and the first (longest) one's source is the one shown.
        const options = [];
        for (const x of own) {
            if (!options.some((o) => o.rate_bp === x.rate_bp)) options.push(x);
        }
        const check = [];
        if (options[0].conditional) check.push("conditional");
        if (options.length > 1) check.push("another entry rates it differently");
        if (t.codes.length > 1) check.push("its variants carry different codes");
        const existing =
            rates.find(
                (r) =>
                    r.country === "IN" &&
                    !r.state &&
                    (t.kind === "category"
                        ? r.category_id === t.id && !r.product_id
                        : r.product_id === t.id),
            ) ?? null;
        rows.push({ key: `${t.kind}:${t.id}`, kind: t.kind, id: t.id, name: t.name, codes: t.codes, options, choice: 0, check, existing });
    }
    return { rows, unmatched };
}

/** What saving a ticked row sends: an edit of the rule it replaces, or a new one. */
export function saveRequest(row) {
    const name = ruleName(row);
    const rate_bp = row.options[row.choice].rate_bp;
    if (row.existing) {
        return { method: "PATCH", path: `/api/admin/tax-rates/${row.existing.id}`, body: { name, rate_bp } };
    }
    return {
        method: "POST",
        path: "/api/admin/tax-rates",
        body: {
            name,
            rate_bp,
            country: "IN",
            state: "",
            category_id: row.kind === "category" ? row.id : null,
            product_id: row.kind === "product" ? row.id : null,
            active: true,
        },
    };
}
