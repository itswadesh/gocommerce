<script>
    /**
     * The packing slip: the sheet that goes in the box.
     *
     * Nothing in this panel could be printed. Ctrl+P on an order produced the
     * orders table with a drawer laid over it, because the order had no
     * document of its own — and ext/invoices, the nearest thing that exists, is
     * a money document for the customer rather than a pick-and-pack sheet for
     * the person filling the box.
     *
     * So it is a route rather than a print mode, which is the contract
     * PrintSheet already states: a warehouse wants ten orders open in ten tabs
     * and printed in a row, and a mode can only ever print the one on screen.
     * `@media print` in gocommerce.css hides the shell around it.
     *
     * What a packer needs and an invoice does not carry: the SKU, the count,
     * the variant label and the shelf the units came off. Prices are here too,
     * quietly — a slip with no money on it is refused at some borders and is
     * awkward for a return — but the quantity column is the one set in bold,
     * because that is the column being read.
     *
     * `?auto=1` opens the print dialog on arrival. The button on the order
     * links without it, so the sheet can be checked before it is printed; a
     * picking run that wants paper immediately adds the parameter.
     */
    import { base } from "$app/paths";
    import { page as route } from "$app/state";
    import { api, can } from "$lib/api.js";
    import { formatDate, formatMoney, orderStatusLabel, paymentLabel } from "$lib/format.js";
    import { settings } from "$lib/settings.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PrintSheet from "$lib/components/PrintSheet.svelte";

    const orderId = $derived(route.params.id);
    const readable = $derived(can("orders.read"));
    const auto = $derived(route.url.searchParams.get("auto") === "1");

    let order = $state(null);
    let loading = $state(true);
    let locations = $state([]);
    let locationsAsked = false;

    $effect(() => {
        orderId;
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            order = await api.get(`/api/admin/orders/${orderId}`);
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* Which shelf each line came off. Best effort, exactly as on the order
       itself: `locations.read` is its own right, and a slip without a location
       column is still a usable slip. */
    const needsLocations = $derived((order?.line_items ?? []).some((l) => l.location_id != null));

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

    const units = $derived(
        (order?.line_items ?? []).reduce((n, l) => n + (l.quantity ?? 0), 0),
    );

    const payChip = $derived(paymentLabel(order));

    const methodName = $derived(
        settings.paymentMethods.find((m) => m.code === order?.payment_provider)?.name ??
            order?.payment_provider ??
            "—",
    );

    /* The shop's own note goes on the slip: "leave with the neighbour" is
       written there and is useless to whoever is packing if it stays in the
       panel. Coerced for the reason the order screen coerces it — jsonb takes
       anything, and older rows predate the engine's check. */
    const note = $derived(
        typeof order?.metadata?.notes === "string" ? order.metadata.notes : "",
    );
</script>

<svelte:head>
    <title>{order ? `Packing slip ${order.number}` : "Packing slip"}</title>
</svelte:head>

{#if !readable}
    <NoAccess right="orders.read" what="orders" />
{:else}
    <div class="page">
        <div class="page-content">
            <!-- Hidden in print by the block in gocommerce.css, so what comes
                 out of the printer is the sheet and nothing else. -->
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/orders">Orders</a>
                    <a href="{base}/orders/{orderId}">{order?.number || "…"}</a>
                    <div>Packing slip</div>
                </nav>
                <div class="page-header-primary-btns">
                    <button
                        type="button"
                        class="btn sm"
                        disabled={!order}
                        onclick={() => window.print()}
                    >
                        <i class="ri-printer-line" aria-hidden="true"></i>
                        <span class="txt">Print</span>
                    </button>
                </div>
            </header>

            {#if loading && !order}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if order}
                <PrintSheet title="Packing slip {order.number}" {auto}>
                    <div class="slip-head">
                        <div>
                            <h4 class="slip-title">Packing slip</h4>
                            <div class="txt-hint txt-sm">
                                Order <span class="txt-code">{order.number}</span>
                            </div>
                            <div class="txt-hint txt-sm">
                                Placed {formatDate(order.created_at)}
                            </div>
                        </div>
                        <div class="slip-chips">
                            <span class="label">{orderStatusLabel(order.status)}</span>
                            <span class="label">{payChip.text}</span>
                            <div class="txt-hint txt-sm">via {methodName}</div>
                        </div>
                    </div>

                    <div class="slip-parties">
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
                            {#if order.phone}
                                <div class="txt-sm">{order.phone}</div>
                            {/if}
                            {#if order.email}
                                <div class="txt-sm">{order.email}</div>
                            {/if}
                        </div>
                        <div>
                            <h6 class="order-card-title">This parcel</h6>
                            <div class="txt-sm">
                                {order.line_items?.length ?? 0} line(s), {units} unit(s)
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
                        </div>
                    </div>

                    <table class="table slip-table">
                        <thead>
                            <tr>
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
                                    <td class="txt-code txt-sm">{line.sku}</td>
                                    <td>
                                        {line.title}
                                        {#if line.variant_label}
                                            <div class="txt-hint txt-sm">{line.variant_label}</div>
                                        {/if}
                                    </td>
                                    {#if needsLocations}
                                        <td class="txt-sm">
                                            {line.location_id != null
                                                ? locationName(line.location_id)
                                                : "—"}
                                        </td>
                                    {/if}
                                    <!-- The column being read, so it is the one
                                         set in bold. -->
                                    <td class="txt-right txt-bold">{line.quantity}</td>
                                    <td class="txt-right">{formatMoney(line.unit_price)}</td>
                                    <td class="txt-right">{formatMoney(line.total)}</td>
                                </tr>
                            {/each}
                        </tbody>
                    </table>

                    <div class="slip-totals">
                        <div class="order-lines">
                            <div class="order-line">
                                <span class="txt-hint">Subtotal</span>
                                <span class="txt-money">{formatMoney(order.subtotal)}</span>
                            </div>
                            {#if order.discount?.amount_minor}
                                <div class="order-line">
                                    <span class="txt-hint">Discount</span>
                                    <span class="txt-money">−{formatMoney(order.discount)}</span>
                                </div>
                            {/if}
                            {#if order.tax?.amount_minor}
                                <div class="order-line">
                                    <span class="txt-hint">
                                        Tax{order.tax_inclusive ? " (included)" : ""}
                                    </span>
                                    <span class="txt-money">{formatMoney(order.tax)}</span>
                                </div>
                            {/if}
                            <div class="order-line">
                                <span class="txt-hint">Shipping</span>
                                <span class="txt-money">
                                    {order.shipping?.amount_minor
                                        ? formatMoney(order.shipping)
                                        : "Free"}
                                </span>
                            </div>
                            <div class="order-line slip-total">
                                <span class="txt-bold">Total</span>
                                <span class="txt-money txt-bold">{formatMoney(order.total)}</span>
                            </div>
                        </div>
                    </div>

                    {#if note}
                        <div class="slip-note">
                            <h6 class="order-card-title">Note</h6>
                            <p>{note}</p>
                        </div>
                    {/if}
                </PrintSheet>
            {/if}
        </div>
    </div>
{/if}

<style>
    /* The sheet's own layout. Everything inside it is PocketBase's vocabulary —
       .table, .label, .order-lines, .order-address — and these four rules are
       only about where the blocks sit on the paper, which no existing class
       says. */
    .slip-head {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: var(--smSpacing);
        margin-bottom: var(--spacing);
    }
    .slip-title {
        margin: 0 0 4px;
    }
    .slip-chips {
        display: flex;
        flex-direction: column;
        align-items: flex-end;
        gap: 4px;
    }
    .slip-parties {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: var(--spacing);
        margin-bottom: var(--spacing);
    }
    .slip-table {
        margin-bottom: var(--smSpacing);
    }
    /* The figures against the right edge of the sheet, where an invoice puts
       them and where an eye looking for a total goes first. */
    .slip-totals {
        display: flex;
        justify-content: flex-end;
    }
    .slip-totals .order-lines {
        min-width: 240px;
    }
    .slip-total {
        border-top: 1px solid var(--surfaceAlt3Color);
        padding-top: 6px;
        margin-top: 4px;
    }
    .slip-note {
        margin-top: var(--spacing);
    }
    .slip-note p {
        margin: 0;
        white-space: pre-wrap;
    }
</style>
