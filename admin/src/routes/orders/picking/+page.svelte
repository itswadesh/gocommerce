<script>
    /**
     * The picking list: one sheet for a whole run of orders.
     *
     * /orders/{id}/print closed half the gap — an order finally had a document
     * — and left the half a warehouse actually feels. Ten orders meant ten
     * tabs, opened and printed one at a time, and the picker still walked the
     * aisles ten times because nothing told them that four of those orders want
     * the same mug.
     *
     * So this is two documents in one run, in the order the work happens:
     *
     *   The pick — every SKU across the whole run, summed, with the shelf it
     *   comes off and the orders that want it. One walk of the warehouse.
     *
     *   The pack — one block per order, each starting on a fresh sheet, so what
     *   comes out of the printer is a stack that can be laid on the bench beside
     *   the boxes. Same columns as the single-order slip, on purpose: a packer
     *   who knows one reads the other.
     *
     * It takes either a selection (`?ids=3,7,9`, which is what the orders
     * screen's bulk bar sends) or the orders screen's own filters — `status`,
     * `payment_status`, `q`, `email`, `from`, `to` — so "everything confirmed
     * and unpaid from this morning" is a link. `?auto=1` opens the print dialog
     * on arrival, exactly as the single-order sheet does.
     *
     * A selection is fetched one order at a time because there is no batch read
     * and this screen does not invent one — the same answer the bulk actions on
     * the orders list already give. It is bounded: the selection can only be as
     * large as the pages an operator ticked.
     */
    import { base } from "$app/paths";
    import { page as route } from "$app/state";
    import { api, can, query } from "$lib/api.js";
    import {
        formatDate,
        formatMoney,
        orderStatusLabel,
        paymentLabel,
        pluralize,
    } from "$lib/format.js";
    import { pickLines, countUnits } from "$lib/picking.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PrintSheet from "$lib/components/PrintSheet.svelte";

    /* The cap on a filtered run. A warehouse printing more than this in one
       press has a different problem than a missing button, and the count under
       the heading says plainly when the run was cut — a picking list that
       silently stopped at 100 is how a parcel goes missing. */
    const MAX_ORDERS = 100;
    /* Requests in flight at once when the run is a selection. Enough to be
       quick, few enough that fifty orders do not arrive as fifty simultaneous
       connections. */
    const BATCH = 6;

    const readable = $derived(can("orders.read"));
    const auto = $derived(route.url.searchParams.get("auto") === "1");

    const ids = $derived(
        (route.url.searchParams.get("ids") || "")
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean),
    );

    /* The orders screen's own parameter names, so its address bar is this
       screen's address bar with the path swapped. */
    const filters = $derived({
        status: route.url.searchParams.get("status") || "",
        payment_status: route.url.searchParams.get("payment_status") || "",
        q: route.url.searchParams.get("q") || "",
        email: route.url.searchParams.get("email") || "",
        from: route.url.searchParams.get("from") || "",
        to: route.url.searchParams.get("to") || "",
    });

    let orders = $state([]);
    let loading = $state(true);
    let truncated = $state(0);
    /* Selected orders that could not be read back. Counted rather than
       swallowed: a sheet quietly one parcel short is the failure this whole
       screen exists to prevent. */
    let missing = $state(0);
    let locations = $state([]);
    let locationsAsked = false;

    /* A second run started before the first settled would otherwise leave the
       sheet showing whichever reply happened to land last. */
    let reqId = 0;

    $effect(() => {
        route.url.search;
        if (readable) load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        truncated = 0;
        missing = 0;
        try {
            const rows = ids.length ? await loadSelection() : await loadFiltered();
            if (mine !== reqId) return;
            orders = rows;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function loadSelection() {
        const out = [];
        let lost = 0;
        for (let i = 0; i < ids.length; i += BATCH) {
            const slice = ids.slice(i, i + BATCH);
            // allSettled, not all: one order cancelled and gone since the
            // checkbox was ticked must not cost the other nineteen their sheet.
            // What it cost is said out loud instead — see `missing` below.
            const batch = await Promise.allSettled(
                slice.map((id) => api.get(`/api/admin/orders/${id}`)),
            );
            for (const settled of batch) {
                if (settled.status === "fulfilled") out.push(settled.value);
                else lost += 1;
            }
        }
        missing = lost;
        // Oldest first, which is the order a queue is worked in — not the order
        // the checkboxes happened to be ticked.
        return out.sort((a, b) => a.id - b.id);
    }

    async function loadFiltered() {
        const result = await api.get(
            "/api/admin/orders" + query({ ...filters, limit: MAX_ORDERS, sort: "created_at", order: "asc" }),
        );
        const rows = result.data ?? [];
        truncated = Math.max(0, (result.meta?.total ?? rows.length) - rows.length);
        return rows;
    }

    /* Which shelf each line comes off. Best effort, exactly as on the
       single-order slip: locations.read is its own right, and a sheet without a
       From column is still a usable sheet. */
    const needsLocations = $derived(
        orders.some((o) => (o.line_items ?? []).some((l) => l.location_id != null)),
    );

    $effect(() => {
        if (!needsLocations || locationsAsked) return;
        locationsAsked = true;
        api.get("/api/admin/locations")
            .then((res) => (locations = res.data ?? []))
            .catch(() => {
                locations = [];
            });
    });

    function locationName(id) {
        return locations.find((l) => l.id === id)?.name || `#${id}`;
    }

    /* The pick: the whole run collapsed to what has to come off the shelves.
       It lives in $lib/picking.js with a test rather than inline here, because
       it is the one piece of arithmetic on this sheet that a warehouse acts on
       without checking — a wrong total is units that never go in the box. */
    const pick = $derived(pickLines(orders));
    const units = $derived(countUnits(orders));

    /* What this run is, in a sentence — so a sheet found on a bench says where
       it came from and a reader can tell a selection from a filter. */
    const describes = $derived.by(() => {
        if (ids.length) return `${ids.length} selected ${pluralize(ids.length, "order")}`;
        const said = [];
        if (filters.status) said.push(orderStatusLabel(filters.status).toLowerCase());
        if (filters.payment_status) said.push(filters.payment_status);
        if (filters.q) said.push(`matching “${filters.q}”`);
        if (filters.email) said.push(filters.email);
        if (filters.from || filters.to) {
            const spoken = (iso, shift) => {
                const d = new Date(iso + "T00:00:00");
                if (shift) d.setDate(d.getDate() + shift);
                return formatDate(d.toISOString(), { withTime: false });
            };
            if (filters.from && filters.to) {
                said.push(`placed ${spoken(filters.from)} to ${spoken(filters.to, -1)}`);
            } else if (filters.from) {
                said.push(`placed from ${spoken(filters.from)}`);
            } else {
                said.push(`placed up to ${spoken(filters.to, -1)}`);
            }
        }
        return said.length ? `Orders ${said.join(", ")}` : "Every order";
    });

    /* One order's own count, from the same function the run's total uses — two
       sums that must agree should not be two pieces of arithmetic. */
    const orderUnits = (order) => countUnits([order]);

    /* The shop's own note goes on the sheet: "leave with the neighbour" is
       useless to the person packing if it stays in the panel. Coerced the way
       the order screen coerces it — jsonb takes anything, and older rows
       predate the engine's check. */
    function noteOf(order) {
        return typeof order?.metadata?.notes === "string" ? order.metadata.notes : "";
    }
</script>

<svelte:head><title>Picking list · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="orders.read" what="orders" />
{:else}
    <div class="page">
        <div class="page-content">
            <!-- Hidden in print by the block in gocommerce.css, so what comes
                 out of the printer is the run and nothing else. -->
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/orders">Orders</a>
                    <div>Picking list</div>
                </nav>
                <div class="page-header-primary-btns">
                    <button
                        type="button"
                        class="btn sm"
                        disabled={!orders.length}
                        onclick={() => window.print()}
                    >
                        <i class="ri-printer-line" aria-hidden="true"></i>
                        <span class="txt">Print</span>
                    </button>
                </div>
            </header>

            {#if truncated > 0}
                <div class="alert warning m-b-sm">
                    <p>
                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                        This run covers the first {orders.length} orders and {truncated} more match.
                        Narrow the filter on the orders screen and print the rest as a second run —
                        a picking list that quietly stopped short is how a parcel goes missing.
                    </p>
                </div>
            {/if}

            {#if missing > 0}
                <div class="alert warning m-b-sm">
                    <p>
                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                        {missing} of the {ids.length} selected orders could not be read and are
                        not on this sheet — they may have been deleted since the selection was
                        made. Everything below is complete; what is absent is absent entirely.
                    </p>
                </div>
            {/if}

            {#if loading && !orders.length}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if !orders.length}
                <div class="block txt-center txt-hint p-base">
                    Nothing to pick. {ids.length
                        ? "None of the selected orders could be read."
                        : "No order matches that filter."}
                    <div class="m-t-sm">
                        <a class="btn sm secondary" href="{base}/orders">
                            <span class="txt">Back to orders</span>
                        </a>
                    </div>
                </div>
            {:else}
                <PrintSheet title="Picking list · {orders.length} {pluralize(orders.length, 'order')}" {auto}>
                    <div class="pick-head">
                        <div>
                            <h4 class="pick-title">Picking list</h4>
                            <div class="txt-hint txt-sm">{describes}</div>
                            <div class="txt-hint txt-sm">Printed {formatDate(new Date().toISOString())}</div>
                        </div>
                        <div class="pick-chips">
                            <span class="label">{orders.length} {pluralize(orders.length, 'order')}</span>
                            <span class="label">{units} {pluralize(units, 'unit')}</span>
                            <span class="label">{pick.length} {pluralize(pick.length, 'line')}</span>
                        </div>
                    </div>

                    <!-- The half that makes this a picking list rather than a
                         stack of slips: one walk of the warehouse, not one per
                         order. -->
                    <h6 class="order-card-title">Pick</h6>
                    <table class="table pick-table">
                        <thead>
                            <tr>
                                <th class="pick-tick">✓</th>
                                <th>SKU</th>
                                <th>Item</th>
                                {#if needsLocations}<th>From</th>{/if}
                                <th class="txt-right">Qty</th>
                                <th>For</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each pick as row (row.key)}
                                <tr>
                                    <!-- A box to pencil into. The sheet is
                                         worked with a hand, not a mouse. -->
                                    <td class="pick-tick"><span class="pick-box"></span></td>
                                    <td class="txt-code txt-sm">{row.sku}</td>
                                    <td>
                                        {row.title}
                                        {#if row.variantLabel}
                                            <div class="txt-hint txt-sm">{row.variantLabel}</div>
                                        {/if}
                                    </td>
                                    {#if needsLocations}
                                        <td class="txt-sm">
                                            {row.locationID != null ? locationName(row.locationID) : "—"}
                                        </td>
                                    {/if}
                                    <!-- The column being read, so it is the one
                                         set in bold — the single-order slip
                                         makes the same choice. -->
                                    <td class="txt-right txt-bold">{row.quantity}</td>
                                    <td class="txt-hint txt-sm pick-for">{row.numbers.join(", ")}</td>
                                </tr>
                            {/each}
                        </tbody>
                    </table>

                    <!-- One sheet per order from here on, so the stack can be
                         laid out beside the boxes. -->
                    {#each orders as order (order.id)}
                        <section class="pick-order">
                            <div class="pick-head">
                                <div>
                                    <h5 class="pick-title">
                                        Order <span class="txt-code">{order.number}</span>
                                    </h5>
                                    <div class="txt-hint txt-sm">
                                        Placed {formatDate(order.created_at)}
                                    </div>
                                </div>
                                <div class="pick-chips">
                                    <span class="label">{orderStatusLabel(order.status)}</span>
                                    <span class="label">{paymentLabel(order).text}</span>
                                </div>
                            </div>

                            <div class="pick-parties">
                                <div>
                                    <h6 class="order-card-title">Deliver to</h6>
                                    <div class="txt-bold">{order.name || order.email || "—"}</div>
                                    {#if order.address}
                                        <address class="order-address">
                                            {order.address.line1}{order.address.line2
                                                ? ", " + order.address.line2
                                                : ""}<br />
                                            {order.address.city}{order.address.state
                                                ? " " + order.address.state
                                                : ""}
                                            {order.address.postal_code}<br />
                                            {order.address.country}
                                        </address>
                                    {/if}
                                    {#if order.phone}<div class="txt-sm">{order.phone}</div>{/if}
                                    {#if order.email}<div class="txt-sm">{order.email}</div>{/if}
                                </div>
                                <div>
                                    <h6 class="order-card-title">This parcel</h6>
                                    <div class="txt-sm">
                                        {order.line_items?.length ?? 0} line(s), {orderUnits(order)} unit(s)
                                    </div>
                                    {#if order.fulfillments?.length}
                                        {#each order.fulfillments as f (f.id)}
                                            <div class="txt-sm">
                                                {f.carrier_name || "No carrier"}
                                                {#if f.tracking}
                                                    · <span class="txt-code">{f.tracking}</span>
                                                {/if}
                                            </div>
                                        {/each}
                                    {:else}
                                        <div class="txt-hint txt-sm">Not yet shipped</div>
                                    {/if}
                                    <div class="txt-sm">
                                        Total <span class="txt-bold">{formatMoney(order.total)}</span>
                                    </div>
                                </div>
                            </div>

                            <table class="table pick-table">
                                <thead>
                                    <tr>
                                        <th class="pick-tick">✓</th>
                                        <th>SKU</th>
                                        <th>Item</th>
                                        {#if needsLocations}<th>From</th>{/if}
                                        <th class="txt-right">Qty</th>
                                        <th class="txt-right">Unit</th>
                                        <th class="txt-right">Total</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each order.line_items ?? [] as line (line.id)}
                                        <tr>
                                            <td class="pick-tick"><span class="pick-box"></span></td>
                                            <td class="txt-code txt-sm">{line.sku}</td>
                                            <td>
                                                {line.title}
                                                {#if line.variant_label}
                                                    <div class="txt-hint txt-sm">
                                                        {line.variant_label}
                                                    </div>
                                                {/if}
                                            </td>
                                            {#if needsLocations}
                                                <td class="txt-sm">
                                                    {line.location_id != null
                                                        ? locationName(line.location_id)
                                                        : "—"}
                                                </td>
                                            {/if}
                                            <td class="txt-right txt-bold">{line.quantity}</td>
                                            <td class="txt-right">{formatMoney(line.unit_price)}</td>
                                            <td class="txt-right">{formatMoney(line.total)}</td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>

                            {#if noteOf(order)}
                                <div class="pick-note">
                                    <h6 class="order-card-title">Note</h6>
                                    <p>{noteOf(order)}</p>
                                </div>
                            {/if}
                        </section>
                    {/each}
                </PrintSheet>
            {/if}
        </div>
    </div>
{/if}

<style>
    /*
     * Scoped here rather than appended to gocommerce.css, which several agents
     * are editing at once. Everything inside the sheet is PocketBase's
     * vocabulary — .table, .label, .order-card-title, .order-address — and
     * these rules are only about where blocks sit on paper, which no existing
     * class says.
     */
    .pick-head {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: var(--smSpacing);
        margin-bottom: var(--smSpacing);
    }
    .pick-title {
        margin: 0 0 4px;
    }
    .pick-chips {
        display: flex;
        flex-direction: column;
        align-items: flex-end;
        gap: 4px;
    }
    .pick-parties {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: var(--spacing);
        margin-bottom: var(--smSpacing);
    }
    .pick-table {
        margin-bottom: var(--spacing);
    }
    .pick-for {
        max-width: 220px;
        overflow-wrap: anywhere;
    }
    /* The tick column: a box to pencil into as each line comes off the shelf.
       A real border rather than a ☐ glyph, which renders as a different size in
       every font a print driver might substitute. */
    .pick-tick {
        width: 24px;
    }
    .pick-box {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 1px solid var(--surfaceTxtHintColor);
        border-radius: 2px;
    }
    /* One order per sheet, and the Pick table gets a sheet of its own because
       the first order breaks too — that is the page the picker carries round
       the warehouse while the rest stay on the bench. `break-before` rather
       than `break-after`, so the last order does not push a blank page out of
       the printer; `break-inside: avoid` keeps a short order off two sheets. */
    .pick-order {
        break-before: page;
        break-inside: avoid;
        padding-top: var(--smSpacing);
    }
    .pick-note {
        margin-bottom: var(--spacing);
    }
    .pick-note p {
        margin: 0;
        white-space: pre-wrap;
    }
    /* On screen the page breaks are invisible, so the blocks need a line
       between them to read as separate sheets. */
    @media screen {
        .pick-order {
            border-top: 1px solid var(--surfaceAlt3Color);
            margin-top: var(--spacing);
        }
    }
</style>
