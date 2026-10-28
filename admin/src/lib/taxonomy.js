/**
 * The Shopify taxonomy browser's row bookkeeping (D143), kept out of the
 * component so it can be tested.
 *
 * The rows are a flattened tree: each expanded node's children spliced in
 * directly beneath it, with `depth` saying how far in. A node's id is its
 * ancestry — `aa-1-1` sits under `aa-1` under `aa` — which holds for every
 * node of the embedded master, and is what lets a switch flipped on one row
 * update the others without asking the engine again.
 */

/** Whether `id` is `of` itself or anywhere beneath it. */
export function within(id, of) {
    return id === of || id.startsWith(of + "-");
}

/** Splice `children` in under the row at `index`. */
export function openBranch(rows, index, children) {
    return [...rows.slice(0, index + 1), ...children, ...rows.slice(index + 1)];
}

/** Remove everything beneath the row at `index`, however deep it was opened. */
export function closeBranch(rows, index) {
    const depth = rows[index].depth;
    let end = index + 1;
    while (end < rows.length && rows[end].depth > depth) end++;
    return [...rows.slice(0, index + 1), ...rows.slice(end)];
}

/**
 * The rows after the engine answered a switch.
 *
 * Activating `node` activates its ancestors too, so they are drawn on;
 * deactivating it takes its whole branch away, so everything beneath it is
 * drawn off. `category` is the store's category the activation returned.
 */
export function applyActivation(rows, node, active, category = null) {
    return rows.map((row) => {
        if (active && within(node.id, row.id)) {
            if (row.id !== node.id) return row.active ? row : { ...row, active: true };
            return { ...row, active: true, category_id: category?.id ?? row.category_id, title: category?.title ?? row.name };
        }
        if (!active && within(row.id, node.id)) {
            return { ...row, active: false, category_id: null, title: "" };
        }
        return row;
    });
}
