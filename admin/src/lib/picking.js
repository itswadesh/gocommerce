/**
 * Collapsing a run of orders into what has to come off the shelves.
 *
 * It is the one piece of arithmetic on the picking sheet, and it is the one a
 * warehouse acts on without checking: a wrong total here is units that never go
 * in the box, discovered by the customer. So it lives in a module with a test
 * rather than inline in the route.
 */

/**
 * pickLines sums a run of orders by SKU and location, newest grouping rules
 * first:
 *
 *   Keyed on SKU *and* location, because the same SKU held in two warehouses is
 *   two walks and two lines. Merging them would produce a total nobody can act
 *   on — you cannot pick nine from a shelf holding four. A line with no location
 *   keys on the SKU alone, which is the single-location store and most of them.
 *
 *   Sorted by location and then SKU, which is the sequence somebody actually
 *   walks: everything from one shelf together.
 *
 * `numbers` is which orders want the row, so a picker who finds four of
 * something and needs five knows which parcel is short.
 */
export function pickLines(orders) {
    const rows = new Map();
    for (const order of orders ?? []) {
        for (const line of order.line_items ?? []) {
            const key = `${line.sku} ${line.location_id ?? ""}`;
            let row = rows.get(key);
            if (!row) {
                row = {
                    key,
                    sku: line.sku,
                    title: line.title,
                    variantLabel: line.variant_label || "",
                    locationID: line.location_id ?? null,
                    quantity: 0,
                    numbers: [],
                };
                rows.set(key, row);
            }
            row.quantity += line.quantity ?? 0;
            // An order that bought the same variant on two lines — an edit can
            // leave one — is still one parcel to fill, so it is named once.
            if (!row.numbers.includes(order.number)) row.numbers.push(order.number);
        }
    }
    return [...rows.values()].sort(
        (a, b) =>
            String(a.locationID ?? "").localeCompare(String(b.locationID ?? "")) ||
            a.sku.localeCompare(b.sku),
    );
}

/** countUnits is how many things the run is, which is the number a picker
 *  counts back against at the end. */
export function countUnits(orders) {
    let n = 0;
    for (const order of orders ?? []) {
        for (const line of order.line_items ?? []) n += line.quantity ?? 0;
    }
    return n;
}
