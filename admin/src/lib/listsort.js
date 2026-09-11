/**
 * A sortable list screen's `sort` and `order`, on top of listState.
 *
 * They are two more parameters in the address bar and nothing else: the screen
 * declares them in its `listState` defaults, the URL holds them, and
 * `list.set()` returns to page 1 on every change — which is the reset an
 * ordering change needs, for the same reason a filter change needs it. Without
 * it the operator lands on page 4 of a list that is no longer the list they
 * were reading.
 *
 *     const list = listState({ q: "", sort: "", order: "", page: 1 });
 *     const sort = $derived(readSort(list.params, SORT_FIELDS));
 *     const sortBy = (field, firstDesc) => list.set(cycleSort(sort, field, firstDesc));
 *     ... list.query({ limit: PER_PAGE, ...sortQuery(sort) })
 *
 * There is no writer here, and there should not be: a second place that writes
 * to the address bar is a second place that can disagree with the first about
 * which parameters a screen owns.
 */

/** No sort: the listing's own order, which no sort/order pair can express. */
export const NO_SORT = { field: "", desc: false };

/**
 * readSort turns the URL's two strings into the one object a header needs.
 *
 * `fields` is the screen's own list, and a key outside it reads as no sort: the
 * engine answers an unknown sort with a 400, and a stale bookmark should
 * degrade to the default order rather than to an error page.
 */
export function readSort(params, fields) {
    const field = params?.sort ?? "";
    if (!field || !fields.includes(field)) return NO_SORT;
    return { field, desc: params?.order === "desc" };
}

/**
 * sortQuery is what goes on the wire, and it is built from the READ sort rather
 * than from the raw parameters.
 *
 * That is the whole reason it exists. A hand-typed `?order=desc` with no `sort`
 * is a 400 at the engine — deliberately, since there is nothing coherent to
 * flip — so passing the URL's strings through unfiltered would turn a mistyped
 * address into an error page instead of the default listing.
 */
export function sortQuery(sort) {
    if (!sort.field) return { sort: "", order: "" };
    return { sort: sort.field, order: sort.desc ? "desc" : "asc" };
}

/**
 * cycleSort is one header click, as a patch for `list.set`.
 *
 * Three states, not two: the natural direction, its reverse, then off. The
 * third exists because the engine's own order is the working order — newest
 * orders first, newest products first — and an operator who sorted by title to
 * find one thing needs it back. With a two-state toggle the only way back is
 * editing the URL by hand.
 *
 * `firstDesc` is where the per-column ergonomics live: money, counts and dates
 * read biggest-first on the first click, text reads A–Z. The wire contract
 * stays uniform (`order` defaults to `asc` for every field) because a per-field
 * default is a rule a client cannot discover without a table of exceptions.
 */
export function cycleSort(sort, field, firstDesc = false) {
    const natural = firstDesc ? "desc" : "asc";
    const reverse = firstDesc ? "asc" : "desc";
    if (sort.field !== field) return { sort: field, order: natural };
    if (sort.desc === firstDesc) return { sort: field, order: reverse };
    return { sort: "", order: "" };
}
