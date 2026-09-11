<script>
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, can, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import {
        formatMoney,
        relativeTime,
        orderStatusClass,
        orderStatusLabel,
        paymentStatusClass,
        stockClass,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    // The reader's own zone, so "the last 30 days" ends at their midnight
    // rather than UTC's.
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

    let loading = $state(true);
    let recent = $state([]);
    let lowStock = $state([]);
    let counts = $state({ orders: 0, products: 0, pending: 0, unpaid: 0 });
    let revenue = $state(null);

    /*
     * What this operator may actually read.
     *
     * The screen used to fire all six reads regardless and hand the whole page
     * to Promise.all, so an operator whose role does not carry orders.read got
     * one toast, no dashboard, and no way to tell which of the six was the
     * problem. Each read is now asked for only by somebody who may have it, and
     * the section it fills is drawn only when there was a read to fill it.
     *
     * This is a shell rather than a screen with a right of its own, so there is
     * no NoAccess here: a role that can read nothing still gets the panel and
     * its navigation, which is the honest answer.
     */
    const mayOrders = $derived(can("orders.read"));
    const mayCatalog = $derived(can("catalog.read"));
    const mayStock = $derived(can("inventory.read"));

    $effect(() => {
        // Reading the three is what re-runs this if the role is re-cut.
        mayOrders;
        mayCatalog;
        mayStock;
        load();
    });

    async function load() {
        loading = true;
        try {
            // Six small reads rather than one bespoke dashboard endpoint: the
            // panel uses the same API everything else does, and an endpoint
            // that exists only for this screen would be one more thing to keep
            // in step with it.
            const [orders, products, pending, unpaid, stock, sales] = await Promise.all([
                mayOrders ? api.get("/api/admin/orders" + query({ limit: 8 })) : null,
                mayCatalog ? api.get("/api/admin/products" + query({ limit: 1 })) : null,
                mayOrders
                    ? api.get("/api/admin/orders" + query({ status: "confirmed", limit: 1 }))
                    : null,
                mayOrders
                    ? api.get("/api/admin/orders" + query({ payment_status: "pending", limit: 1 }))
                    : null,
                mayStock
                    ? api.get("/api/admin/inventory/low-stock" + query({ threshold: 5, limit: 6 }))
                    : null,
                // Revenue is the engine's own arithmetic over a whole window,
                // not a sum over the eight rows above it. The figure beside
                // "Recent orders" used to describe those eight and was read as
                // the store's revenue — on a store with nine orders it was
                // simply wrong, and it counted a refunded order at full value.
                mayOrders
                    ? api.get("/api/admin/reports/sales" + query({ group_by: "month", tz }))
                    : null,
            ]);

            recent = orders?.data ?? [];
            counts = {
                orders: orders?.meta.total ?? 0,
                products: products?.meta.total ?? 0,
                pending: pending?.meta.total ?? 0,
                unpaid: unpaid?.meta.total ?? 0,
            };
            lowStock = stock?.data ?? [];

            // The first currency block, which is the store's own: the report
            // never sums across currencies, and a store holding two gets the
            // whole picture on /reports rather than a wrong single number here.
            revenue = sales?.currencies[0]?.totals.net ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* The anchor in the first cell already navigates, so the row handler stands
       aside for it — otherwise the browser follows the link and this follows it
       again over the top. */
    function openOrder(event, order) {
        if (event.target.closest("a")) return;
        goto(`${base}/orders/${order.id}`);
    }

    /* A card whose figure nobody here may read is not shown at zero — a zero
       is a fact about the store, and this would be a fact about the role. */
    const cards = $derived(
        [
            {
                icon: "ri-shopping-bag-3-line",
                label: "Orders",
                value: counts.orders,
                hint: "All time",
                href: "/orders",
                show: mayOrders,
            },
            {
                icon: "ri-truck-line",
                label: "Awaiting shipment",
                value: counts.pending,
                hint: "Confirmed, not yet shipped",
                href: "/orders?status=confirmed",
                show: mayOrders,
            },
            {
                icon: "ri-money-dollar-circle-line",
                label: "Awaiting payment",
                value: counts.unpaid,
                hint: "Cash on delivery, or unpaid",
                href: "/orders?payment_status=pending",
                show: mayOrders,
            },
            {
                icon: "ri-price-tag-3-line",
                label: "Products",
                value: counts.products,
                hint: "Every status",
                href: "/products",
                show: mayCatalog,
            },
        ].filter((card) => card.show),
    );
</script>

<svelte:head><title>Dashboard · GoCommerce</title></svelte:head>

<div class="page page-dashboard">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Dashboard</div></nav>
            <div class="page-header-primary-btns">
                <button type="button" class="btn secondary" class:loading disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                    <span class="txt">Refresh</span>
                </button>
            </div>
        </header>

        <div class="grid m-b-base">
            {#each cards as card (card.label)}
                <div class="col-3">
                    <a class="stat-card" href="{base}{card.href}">
                        <span class="stat-label">
                            <i class={card.icon} aria-hidden="true"></i>
                            {card.label}
                        </span>
                        {#if loading}
                            <span class="skeleton-loader" style="height: 26px"></span>
                        {:else}
                            <span class="stat-value">{card.value}</span>
                        {/if}
                        <span class="stat-hint">{card.hint}</span>
                    </a>
                </div>
            {/each}
        </div>

        {#if mayOrders}
        <h6 class="section-title">
            <i class="ri-history-line" aria-hidden="true"></i>
            Recent orders
            {#if revenue}
                <span class="txt-hint txt-sm">· {formatMoney(revenue)} net in the last 30 days</span>
            {/if}
            <a href="{base}/reports" class="btn sm transparent secondary">
                <span class="txt">Reports</span>
            </a>
            <a href="{base}/orders" class="btn sm transparent secondary">
                <span class="txt">All orders</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
        </h6>

        <table class="table responsive-table">
            <thead>
                <tr>
                    <th class="col-field-name-id">Order</th>
                    <th>Customer</th>
                    <th class="col-field-type-select">Status</th>
                    <th class="col-field-type-select">Payment</th>
                    <th class="col-field-type-number min-width">Total</th>
                    <th class="col-field-type-date min-width">Placed</th>
                </tr>
            </thead>
            <tbody>
                {#each recent as order (order.id)}
                    <!-- The order itself, now that an order has an address.
                         This pointed at `/orders?id=…`, which nothing read — so
                         the dashboard's most-clicked row landed on an
                         unfiltered order list, exactly as a bare link would. -->
                    <tr
                        class="handle"
                        tabindex="0"
                        onclick={(e) => openOrder(e, order)}
                        onkeydown={(e) => rowKey(e, () => openOrder(e, order))}
                    >
                        <td class="col-field-name-id txt-code txt-sm" data-name="Order">
                            <!-- A link as well as a row: the point of the row is
                                 to be opened, often in a second tab beside the
                                 dashboard it was spotted on. -->
                            <a href="{base}/orders/{order.id}">{order.number}</a>
                        </td>
                        <!-- The ellipsis goes on a span, never the cell: overflow
                             on a <td> takes it out of the table's border model
                             and the row's rules stop meeting. -->
                        <td class="col-field-type-text" data-name="Customer">
                            <span class="txt-ellipsis">{order.name || order.email}</span>
                        </td>
                        <td class="col-field-type-select" data-name="Status">
                            <span class="label {orderStatusClass(order.status)}">
                                {orderStatusLabel(order.status)}
                            </span>
                        </td>
                        <td class="col-field-type-select" data-name="Payment">
                            <span class="label {paymentStatusClass(order.payment_status)}">
                                {order.payment_status}
                            </span>
                        </td>
                        <td class="col-field-type-number min-width" data-name="Total">
                            {formatMoney(order.total)}
                        </td>
                        <td class="col-field-type-date min-width txt-hint txt-sm" data-name="Placed">
                            {relativeTime(order.created_at)}
                        </td>
                    </tr>
                {/each}

                {#if loading && !recent.length}
                    {#each Array(4) as _, i (i)}
                        <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                    {/each}
                {/if}

                {#if !loading && !recent.length}
                    <tr>
                        <td colspan="6" class="txt-center txt-hint p-base">
                            No orders yet. They will appear here as soon as somebody buys something.
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>

        {/if}

        {#if mayStock}
        <h6 class="section-title">
            <i class="ri-alert-line" aria-hidden="true"></i>
            Running low
            <a href="{base}/inventory" class="btn sm transparent secondary">
                <span class="txt">Inventory</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
        </h6>

        <table class="table responsive-table">
            <thead>
                <tr>
                    <th class="col-field-name-id">SKU</th>
                    <th>Variant</th>
                    <th class="col-field-type-number min-width">On hand</th>
                    <th class="col-field-type-number min-width">Reserved</th>
                    <th class="col-field-type-number min-width">Available</th>
                </tr>
            </thead>
            <tbody>
                {#each lowStock as variant (variant.id)}
                    <tr>
                        <td class="col-field-name-id txt-code txt-sm" data-name="SKU">{variant.sku}</td>
                        <td class="txt-hint" data-name="Variant">{variant.label || "—"}</td>
                        <td class="col-field-type-number min-width" data-name="On hand">
                            {variant.stock_on_hand}
                        </td>
                        <td class="col-field-type-number min-width txt-hint" data-name="Reserved">
                            {variant.stock_reserved}
                        </td>
                        <td
                            class="col-field-type-number min-width txt-bold {stockClass(variant.available)}"
                            data-name="Available"
                        >
                            {variant.available}
                        </td>
                    </tr>
                {/each}

                {#if loading && !lowStock.length}
                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                {/if}

                {#if !loading && !lowStock.length}
                    <tr>
                        <td colspan="5" class="txt-center txt-hint p-base">
                            Nothing is running out — every tracked variant has more than five
                            available across the whole store. This card is deliberately the
                            store's view; the inventory screen can ask one location.
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>

        {/if}

        {#if !mayOrders && !mayCatalog && !mayStock}
            <p class="txt-hint txt-center p-base">
                Your role does not carry any of the figures this page shows. The screens it does
                reach are in the navigation.
            </p>
        {/if}

        <footer class="page-footer">
            <span class="txt">Live counts, read straight from the API</span>
            <ThemeToggle />
        </footer>
    </div>
</div>
