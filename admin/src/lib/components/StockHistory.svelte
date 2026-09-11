<script>
    /**
     * The stock ledger for one variant or one location: what moved, when, which
     * way, and who did it.
     *
     * A component rather than markup in either drawer, because both drawers
     * render it — the inventory drawer asks "how did this SKU get to four?" and
     * the locations drawer asks "what has moved at this shelf?", and those are
     * the same table read from two ends.
     *
     * Nothing here is clickable. A history is a report: a row that reads as
     * pressable and does nothing is worse than a plain row, which is why
     * `.stock-history` deliberately leaves out `.stock-locations`' picker
     * rules.
     */
    import { api, query } from "$lib/api.js";
    import { formatDate, relativeTime } from "$lib/format.js";

    let { scope = "variant", id, limit = 25, compact = false, showSku = false } = $props();

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let error = $state("");
    let page = $state(1);
    let filter = $state("all");

    /*
     * The kinds, grouped the way an operator thinks about them rather than one
     * chip per kind: eleven chips is a database schema on screen.
     */
    const GROUPS = {
        all: [],
        counts: ["stock_take", "opening"],
        adjustments: ["adjust", "import"],
        transfers: ["transfer_in", "transfer_out"],
        orders: ["reserve", "commit", "release", "restock", "sell"],
    };

    const LABELS = {
        opening: "Opening balance",
        adjust: "Adjustment",
        stock_take: "Stock take",
        import: "Import",
        transfer_out: "Moved out",
        transfer_in: "Moved in",
        reserve: "Reserved",
        commit: "Sold",
        release: "Released",
        restock: "Restocked",
        sell: "Sold",
    };

    const base = $derived(
        `/api/admin/${scope === "variant" ? "variants" : "locations"}/${id}/movements`,
    );
    const hasMore = $derived(!!meta && rows.length < meta.total);

    $effect(() => {
        // Re-runs when the parent changes which record is open, and when the
        // operator moves to a later page or picks a different filter.
        id;
        page;
        filter;
        load();
    });

    async function load() {
        if (!id) return;
        loading = true;
        error = "";
        try {
            const result = await api.get(
                base +
                    query({
                        kind: GROUPS[filter].join(",") || undefined,
                        limit,
                        page,
                    }),
            );
            rows = page === 1 ? (result.data ?? []) : [...rows, ...(result.data ?? [])];
            meta = result.meta;
        } catch (err) {
            error = err.message;
        } finally {
            loading = false;
        }
    }

    function pick(next) {
        if (filter === next) return;
        page = 1;
        rows = [];
        filter = next;
    }

    /* The second line under the movement's name: where the other end of a
       transfer was, or which order took the units. */
    function context(m) {
        if (m.counterpart_code) {
            return m.kind === "transfer_out" ? `to ${m.counterpart_code}` : `from ${m.counterpart_code}`;
        }
        if (m.order_number) return m.order_number;
        return "";
    }

    function signed(n) {
        return n > 0 ? `+${n}` : String(n);
    }
</script>

{#if !compact}
    <div class="inline-flex gap-5 m-b-sm flex-wrap">
        {#each Object.keys(GROUPS) as g (g)}
            <button
                type="button"
                class="btn sm pill"
                class:warning={filter === g}
                class:secondary={filter !== g}
                class:transparent={filter !== g}
                onclick={() => pick(g)}
            >
                {g === "all" ? "All" : g[0].toUpperCase() + g.slice(1)}
            </button>
        {/each}
    </div>
{/if}

{#if error}
    <div class="field-help error">{error}</div>
{/if}

<div class="page-table-wrapper">
    <table class="table responsive-table stock-history">
        <thead>
            <tr>
                <th>When</th>
                <th>What</th>
                {#if showSku}<th>SKU</th>{/if}
                {#if scope === "variant"}<th>Where</th>{/if}
                <th class="txt-right">Change</th>
                <th class="txt-right">Balance</th>
                <th>Who</th>
            </tr>
        </thead>
        <tbody>
            {#each rows as m (m.id)}
                <tr>
                    <td data-name="When" class="txt-hint" title={formatDate(m.created_at)}>
                        {relativeTime(m.created_at)}
                    </td>
                    <td data-name="What">
                        <span class="label">{LABELS[m.kind] ?? m.kind}</span>
                        {#if context(m)}
                            <div class="txt-hint txt-sm txt-code">{context(m)}</div>
                        {/if}
                        {#if m.reason}<div class="txt-hint txt-sm">{m.reason}</div>{/if}
                    </td>
                    {#if showSku}
                        <td data-name="SKU"><span class="txt-code">{m.sku}</span></td>
                    {/if}
                    {#if scope === "variant"}
                        <td data-name="Where" class="txt-hint txt-code">{m.location_code}</td>
                    {/if}
                    <td data-name="Change" class="txt-right delta">
                        <!-- A stock take that confirmed the count moved nothing,
                             and saying so is the point of the row. -->
                        {#if m.on_hand_delta === 0 && m.reserved_delta === 0}
                            <span class="txt-hint">counted, no change</span>
                        {:else}
                            {#if m.on_hand_delta !== 0}
                                <span class="txt-bold {m.on_hand_delta < 0 ? 'txt-danger' : 'txt-success'}">
                                    {signed(m.on_hand_delta)}
                                </span>
                            {/if}
                            <!-- Both counters, honestly: a commit takes units out
                                 of the reservation and off the shelf at once. -->
                            {#if m.reserved_delta !== 0}
                                <div class="txt-hint txt-sm">
                                    {signed(m.reserved_delta)} reserved
                                </div>
                            {/if}
                        {/if}
                    </td>
                    <td data-name="Balance" class="txt-right delta">
                        {m.on_hand_after}
                        {#if m.reserved_after !== 0}
                            <div class="txt-hint txt-sm">{m.reserved_after} reserved</div>
                        {/if}
                    </td>
                    <td data-name="Who" class="txt-hint">
                        {#if m.actor_email}
                            {m.actor_email}
                        {:else}
                            <span class="label">{m.source}</span>
                        {/if}
                    </td>
                </tr>
            {/each}

            {#if loading && !rows.length}
                {#each Array(3) as _, i (i)}
                    <tr>
                        <td colspan="7"><span class="skeleton-loader"></span></td>
                    </tr>
                {/each}
            {/if}

            {#if !loading && !rows.length}
                <tr>
                    <td colspan="7" class="txt-center txt-hint p-base">
                        Nothing has moved here yet.
                    </td>
                </tr>
            {/if}
        </tbody>
    </table>

    <!--
        Paged in place rather than through the shared Pager: this table lives
        inside a drawer, and a drawer that writes the page number into the
        address bar leaves a stale `?page=` behind when it closes and fights the
        screen underneath it.
    -->
    {#if hasMore}
        <button
            type="button"
            class="btn expanded block load-more-btn"
            class:loading
            disabled={loading}
            onclick={() => (page += 1)}
        >
            <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
            <span class="txt">Load more</span>
        </button>
    {/if}
</div>
