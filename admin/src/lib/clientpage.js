/**
 * Paging a listing the engine deliberately sends whole.
 *
 * Two admin listings take no page parameter, and that is a decision in the
 * engine rather than an oversight: `GET /api/admin/tax-rates` answers
 * `ListMeta{Total: len(list), Limit: len(list)}` because the resolver reads
 * every rate in one pass and the order they come back in IS the order they are
 * applied in (core/taxes_http.go, core/taxes.go — no LIMIT in the SQL), and
 * `GET /api/admin/superusers` does the same for a table that holds the people
 * who administer the store (core/superusers_http.go). Sending `?page=2` to
 * either one changes nothing about the answer.
 *
 * A screen that must not grow an unreachable fifty-first row therefore pages
 * what it already holds. The meta returned here is the engine's own shape —
 * total, limit, offset, page, total_pages — so `<Pager>` cannot tell the
 * difference and the panel does not end up with two kinds of footer.
 *
 * This is NOT for a listing the engine pages. Slicing a window the server has
 * already windowed would draw "showing 1–25 of 50" over one page of a much
 * larger result, which is a lie the operator has no way to see through.
 */

/**
 * pageSlice cuts one page out of rows, with the meta the Pager reads.
 *
 *     const paged = $derived(pageSlice(visible, { page: list.page, limit: perPage }));
 *     ...
 *     {#each paged.rows as row (row.id)}
 *     <Pager meta={paged.meta} onpage={(n) => list.setPage(n)} />
 */
export function pageSlice(rows, options = {}) {
    const all = rows ?? [];
    const total = all.length;
    const limit = Math.max(1, Math.round(options.limit) || 50);
    const totalPages = Math.ceil(total / limit);
    /*
     * A page past the end is a stale URL — a bookmark from before six rates
     * were deleted, or a filter that has just cut the list to eight rows — and
     * the honest answer is the last page that exists rather than an empty table
     * under a footer claiming there are forty.
     */
    const page = Math.min(Math.max(Math.round(options.page) || 1, 1), Math.max(totalPages, 1));
    const offset = (page - 1) * limit;
    return {
        rows: all.slice(offset, offset + limit),
        meta: { total, limit, offset, page, total_pages: totalPages },
    };
}
