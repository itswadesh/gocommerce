/**
 * The row selection behind a BulkBar.
 *
 * It is the variant matrix's selection, written once: toggling one row,
 * toggling a group, toggling everything, and asking whether a group is whole —
 * four operations that eight screens would otherwise each get slightly
 * differently wrong.
 *
 * Two behaviours are decided here rather than per screen, because they are the
 * ones a screen would guess at:
 *
 *   A selection is keyed by id and SURVIVES a page change. Picking ten orders
 *   across two pages and acting on all ten is the point of having one.
 *
 *   A selection is CLEARED by a filter change, because the rows the operator
 *   was looking at are not on the screen any more and a bulk action on rows
 *   nobody can see is how a hundred products get archived by accident. The
 *   screen does the clearing, since only it knows which of its parameters are
 *   filters:
 *
 *       $effect(() => {
 *           list.params.q;
 *           list.params.status;
 *           sel.clear();
 *       });
 */

export function selection() {
    let ids = $state([]);

    const idsOf = (rows) => (rows ?? []).map((row) => row.id);

    return {
        get ids() {
            return ids;
        },

        get count() {
            return ids.length;
        },

        has(id) {
            return ids.includes(id);
        },

        toggle(id) {
            ids = ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id];
        },

        /**
         * toggleAll works over the rows on screen, not over the whole result.
         * "Select all" on a windowed list can only honestly mean the window —
         * the panel does not hold the other pages and the API has no
         * select-everything call.
         */
        toggleAll(rows) {
            const page = idsOf(rows);
            ids = this.allSelected(rows) ? ids.filter((id) => !page.includes(id)) : [
                ...new Set([...ids, ...page]),
            ];
        },

        /** toggleGroup is the same over a sub-list, for a grouped table. */
        toggleGroup(rows) {
            this.toggleAll(rows);
        },

        allSelected(rows) {
            const page = idsOf(rows);
            return page.length > 0 && page.every((id) => ids.includes(id));
        },

        someSelected(rows) {
            const page = idsOf(rows);
            return page.some((id) => ids.includes(id)) && !this.allSelected(rows);
        },

        /**
         * pick returns the selected rows, in the order the table draws them.
         *
         * A bulk action wants the rows and not the ids: it has to read each
         * one's state to decide whether the action is legal for it, and to name
         * it in a failure. Ids selected on a page that is no longer loaded are
         * dropped here rather than sent as requests nothing can report on.
         */
        pick(rows) {
            return (rows ?? []).filter((row) => ids.includes(row.id));
        },

        clear() {
            ids = [];
        },
    };
}
