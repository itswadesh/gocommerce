<script>
    /**
     * The before/after of one recorded action.
     *
     * `changes` is what `writeAudit` stored: `before`, `after`, or both, each a
     * flat-ish map of the fields the service actually touched. It is not a dump
     * of the record — a price change carries `price_minor` and nothing else —
     * so this renders every key it is given rather than trying to decide which
     * ones matter.
     *
     * The two maps are unioned and shown side by side, because the question an
     * operator brings to this drawer is "what changed", and two separate JSON
     * blobs make them do the diff by eye. A key present on one side only is
     * exactly what a create or a delete looks like, and the empty cell says so
     * with a dash rather than by leaving a hole.
     *
     * Values are rendered as JSON, not prettified. A price crosses as minor
     * units and a metadata blob as an object, and inventing a formatter here
     * would be this component deciding what a field means — which is the thing
     * that makes a viewer lie about money.
     */
    let { changes = null } = $props();

    /** Every field either side mentions, in the order `after` lists them and
     *  then whatever only `before` had — so a create reads top to bottom. */
    const fields = $derived.by(() => {
        const after = changes?.after ?? {};
        const before = changes?.before ?? {};
        const seen = new Set();
        const out = [];
        for (const key of Object.keys(after)) {
            seen.add(key);
            out.push(key);
        }
        for (const key of Object.keys(before)) {
            if (!seen.has(key)) out.push(key);
        }
        return out;
    });

    const hasBefore = $derived(!!changes?.before && Object.keys(changes.before).length > 0);
    const hasAfter = $derived(!!changes?.after && Object.keys(changes.after).length > 0);

    /** `undefined` is "this side never mentioned the field" and renders as a
     *  dash; a stored `null` is a value and renders as `null`. */
    function show(map, key) {
        if (!map || !(key in map)) return null;
        const value = map[key];
        // Clearing a field IS the change on plenty of rows — a tracking number
        // removed, a note emptied — and rendering it as a blank cell makes the
        // one thing that happened the one thing that is invisible.
        if (value === "") return '""';
        if (typeof value === "string") return value;
        return JSON.stringify(value);
    }

    /** A field name reads better with spaces than with underscores, and the
     *  raw name stays available in the title for anyone matching it to a
     *  column. */
    const fieldName = (key) => key.replace(/_/g, " ");
</script>

<section class="order-card">
    <h6 class="order-card-title m-b-10">What changed</h6>

    {#if !fields.length}
        <!-- A real state, not a failure: some acts record that they happened
             and have no field to show for it — settling a refund the gateway
             never confirmed, or withdrawing a return. -->
        <p class="txt-hint">
            This action recorded no field values — the summary above is the whole of it.
        </p>
    {:else}
        <div class="page-table-wrapper">
            <table class="table">
                <thead>
                    <tr>
                        <th>Field</th>
                        {#if hasBefore}<th>Before</th>{/if}
                        {#if hasAfter}<th>After</th>{/if}
                    </tr>
                </thead>
                <tbody>
                    {#each fields as key (key)}
                        <tr>
                            <td class="txt-hint" title={key}>{fieldName(key)}</td>
                            {#if hasBefore}
                                <td class="audit-value">
                                    {#if show(changes.before, key) === null}
                                        <span class="txt-hint">—</span>
                                    {:else}
                                        <code>{show(changes.before, key)}</code>
                                    {/if}
                                </td>
                            {/if}
                            {#if hasAfter}
                                <td class="audit-value">
                                    {#if show(changes.after, key) === null}
                                        <span class="txt-hint">—</span>
                                    {:else}
                                        <code>{show(changes.after, key)}</code>
                                    {/if}
                                </td>
                            {/if}
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
    {/if}
</section>

<style>
    /*
     * Scoped here rather than appended to gocommerce.css, which several agents
     * were editing at once. Two rules, both about the same thing: a recorded
     * value can be a whole metadata object, and a cell that will not wrap
     * pushes the column beside it off the drawer.
     */
    .audit-value {
        max-width: 320px;
        vertical-align: top;
    }

    .audit-value code {
        display: inline-block;
        max-width: 100%;
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        font-size: var(--smFontSize);
    }
</style>
