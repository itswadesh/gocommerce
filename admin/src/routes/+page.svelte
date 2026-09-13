<script>
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import {
        formatMoney,
        relativeTime,
        orderStatusLabel,
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

    /*
     * The one place colour appears. DESIGN.md §2 allows four: emerald for done,
     * red for failed, amber for in progress, muted grey for idle — as a small
     * dot beside the word, never as a fill behind it.
     */
    function dotFor(status) {
        switch (status) {
            case "delivered":
                return "tw:bg-emerald-500";
            case "cancelled":
                return "tw:bg-red-500";
            case "shipped":
            case "partial":
                return "tw:bg-amber-500";
            default:
                return "tw:bg-muted-foreground";
        }
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

<!-- `.page` stays: the shell renders each route straight into its flex row,
     so this element is the one that gets sized beside the sidebar, and the
     Playwright pass uses it as the proof a screen actually rendered. The new
     design lives inside it rather than in place of it. -->
<div class="page page-dashboard">
    <div class="tw:flex tw:min-h-full tw:w-full tw:min-w-0 tw:flex-col tw:bg-background tw:font-sans tw:text-foreground">
        <div class="tw:mx-auto tw:flex tw:w-full tw:max-w-5xl tw:flex-col tw:gap-6 tw:p-4 tw:sm:p-6">
        <!-- The page title is the only large type on the screen. Everything
             else lives at 12-14px, and hierarchy comes from weight and border
             rather than size. DESIGN.md §3. -->
        <header class="tw:flex tw:items-center tw:justify-between tw:gap-4">
            <h1 class="tw:text-2xl tw:font-semibold tw:tracking-tight tw:sm:text-3xl">Dashboard</h1>
            <!-- The panel’s own button, untouched: controls keep the vocabulary
                 they already have, and only the surfaces around them change. -->
            <button type="button" class="btn secondary" class:loading disabled={loading} onclick={load}>
                <i class="ri-refresh-line" aria-hidden="true"></i>
                <span class="txt">Refresh</span>
            </button>
        </header>

        {#if cards.length}
            <!-- One column on a phone, two on a tablet, four on a desktop. The
                 tile is the signature of this style: uppercase muted label, big
                 number, small muted subline, and never a title bar. -->
            <div class="tw:grid tw:grid-cols-1 tw:gap-4 tw:sm:grid-cols-2 tw:lg:grid-cols-4">
                {#each cards as card (card.label)}
                    <a
                        href="{base}{card.href}"
                        class="tw:flex tw:min-h-[120px] tw:flex-col tw:text-foreground tw:no-underline tw:justify-between tw:rounded-xl tw:border tw:bg-background tw:p-5 tw:transition-colors tw:hover:bg-accent tw:focus-visible:ring-2 tw:focus-visible:ring-ring tw:focus-visible:ring-offset-2 tw:focus-visible:ring-offset-background tw:focus-visible:outline-none tw:sm:min-h-[140px]"
                    >
                        <span class="tw:text-xs tw:font-medium tw:tracking-wide tw:text-muted-foreground tw:uppercase">
                            {card.label}
                        </span>
                        <div>
                            {#if loading}
                                <div class="tw:h-8 tw:w-16 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                            {:else}
                                <div class="tw:text-3xl tw:font-bold tw:tracking-tight tw:tabular-nums">{card.value}</div>
                            {/if}
                            <p class="tw:mt-1 tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                        </div>
                    </a>
                {/each}
            </div>
        {/if}

        {#if mayOrders}
            <section class="tw:rounded-xl tw:border tw:bg-background">
                <div class="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2 tw:border-b tw:px-4 tw:py-4 tw:sm:px-5">
                    <h2 class="tw:text-sm tw:font-semibold">
                        Recent orders
                        {#if revenue}
                            <span class="tw:font-normal tw:text-muted-foreground">
                                · {formatMoney(revenue)} net in the last 30 days
                            </span>
                        {/if}
                    </h2>
                    <!-- Secondary navigation is a text link with an arrow, not a
                         button. DESIGN.md §7. -->
                    <div class="tw:flex tw:items-center tw:gap-4">
                        <a
                            href="{base}/reports"
                            class="tw:text-xs tw:text-muted-foreground tw:no-underline tw:transition-colors tw:hover:text-foreground"
                        >
                            reports
                        </a>
                        <a
                            href="{base}/orders"
                            class="tw:text-xs tw:text-muted-foreground tw:no-underline tw:transition-colors tw:hover:text-foreground"
                        >
                            all orders <span aria-hidden="true">→</span>
                        </a>
                    </div>
                </div>

                <div class="tw:divide-y">
                    {#each recent as order (order.id)}
                        <!-- A row is a link rather than a clickable <tr>: it
                             middle-clicks into a second tab, takes focus, and
                             answers Enter without a key handler of its own. -->
                        <a
                            href="{base}/orders/{order.id}"
                            class="tw:flex tw:flex-col tw:gap-2 tw:px-4 tw:py-3 tw:text-foreground tw:no-underline tw:transition-colors tw:hover:bg-muted/50 tw:focus-visible:ring-2 tw:focus-visible:ring-ring tw:focus-visible:ring-inset tw:focus-visible:outline-none tw:sm:flex-row tw:sm:items-center tw:sm:gap-4 tw:sm:px-5"
                        >
                            <div class="tw:min-w-0 tw:flex-1">
                                <div class="tw:flex tw:items-center tw:gap-2">
                                    <span
                                        class="tw:size-1.5 tw:shrink-0 tw:rounded-full {dotFor(order.status)}"
                                        aria-hidden="true"
                                    ></span>
                                    <span class="tw:truncate tw:font-mono tw:text-sm tw:font-semibold">{order.number}</span>
                                </div>
                                <p class="tw:truncate tw:pl-3.5 tw:text-xs tw:text-muted-foreground">
                                    {order.name || order.email}
                                </p>
                            </div>

                            <div class="tw:flex tw:items-center tw:justify-between tw:gap-3 tw:pl-3.5 tw:sm:justify-end tw:sm:pl-0 tw:sm:gap-4">
                                <!-- Status is never the dot alone; the word is
                                     always beside it. DESIGN.md §11. -->
                                <span
                                    class="tw:inline-flex tw:items-center tw:gap-1 tw:rounded-full tw:border tw:px-2 tw:py-0.5 tw:text-xs tw:whitespace-nowrap"
                                >
                                    <span class="tw:size-1.5 tw:rounded-full {dotFor(order.status)}" aria-hidden="true"></span>
                                    {orderStatusLabel(order.status)}
                                </span>
                                <span class="tw:text-sm tw:font-medium tw:tabular-nums">{formatMoney(order.total)}</span>
                                <span class="tw:hidden tw:text-xs tw:whitespace-nowrap tw:text-muted-foreground tw:sm:inline">
                                    {relativeTime(order.created_at)}
                                </span>
                            </div>
                        </a>
                    {/each}

                    {#if loading && !recent.length}
                        {#each Array(4) as _, i (i)}
                            <div class="tw:flex tw:items-center tw:gap-4 tw:px-4 tw:py-3 tw:sm:px-5">
                                <div class="tw:h-4 tw:flex-1 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                <div class="tw:h-4 tw:w-20 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                            </div>
                        {/each}
                    {/if}

                    {#if !loading && !recent.length}
                        <p class="tw:px-4 tw:py-8 tw:text-center tw:text-sm tw:text-muted-foreground tw:sm:px-5">
                            No orders yet. They will appear here as soon as somebody buys something.
                        </p>
                    {/if}
                </div>
            </section>
        {/if}

        {#if mayStock}
            <section class="tw:rounded-xl tw:border tw:bg-background">
                <div class="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2 tw:border-b tw:px-4 tw:py-4 tw:sm:px-5">
                    <h2 class="tw:text-sm tw:font-semibold">Running low</h2>
                    <a
                        href="{base}/inventory"
                        class="tw:text-xs tw:text-muted-foreground tw:no-underline tw:transition-colors tw:hover:text-foreground"
                    >
                        inventory <span aria-hidden="true">→</span>
                    </a>
                </div>

                <div class="tw:divide-y">
                    {#each lowStock as variant (variant.id)}
                        <div
                            class="tw:flex tw:flex-col tw:gap-2 tw:px-4 tw:py-3 tw:sm:flex-row tw:sm:items-center tw:sm:gap-4 tw:sm:px-5"
                        >
                            <div class="tw:min-w-0 tw:flex-1">
                                <div class="tw:truncate tw:font-mono tw:text-sm tw:font-semibold">{variant.sku}</div>
                                <p class="tw:truncate tw:text-xs tw:text-muted-foreground">{variant.label || "—"}</p>
                            </div>
                            <!-- The three numbers keep their labels on a phone,
                                 where a bare row of digits says nothing. -->
                            <div class="tw:flex tw:items-center tw:gap-4 tw:text-xs tw:text-muted-foreground">
                                <span>on hand <b class="tw:font-medium tw:text-foreground tw:tabular-nums">{variant.stock_on_hand}</b></span>
                                <span>reserved <b class="tw:font-medium tw:text-foreground tw:tabular-nums">{variant.stock_reserved}</b></span>
                                <span>
                                    available
                                    <b class="tw:font-semibold tw:tabular-nums {variant.available <= 0 ? 'tw:text-destructive' : 'tw:text-foreground'}">
                                        {variant.available}
                                    </b>
                                </span>
                            </div>
                        </div>
                    {/each}

                    {#if loading && !lowStock.length}
                        <div class="tw:flex tw:items-center tw:gap-4 tw:px-4 tw:py-3 tw:sm:px-5">
                            <div class="tw:h-4 tw:flex-1 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                        </div>
                    {/if}

                    {#if !loading && !lowStock.length}
                        <p class="tw:px-4 tw:py-8 tw:text-center tw:text-sm tw:text-muted-foreground tw:sm:px-5">
                            Nothing is running out — every tracked variant has more than five available
                            across the whole store. This is deliberately the store's view; the inventory
                            screen can ask one location.
                        </p>
                    {/if}
                </div>
            </section>
        {/if}

        {#if !mayOrders && !mayCatalog && !mayStock}
            <p class="tw:py-8 tw:text-center tw:text-sm tw:text-muted-foreground">
                Your role does not carry any of the figures this page shows. The screens it does reach
                are in the navigation.
            </p>
        {/if}

        <footer class="tw:flex tw:items-center tw:justify-between tw:gap-4 tw:border-t tw:pt-4 tw:text-xs tw:text-muted-foreground">
            <span>Live counts, read straight from the API</span>
            <ThemeToggle />
        </footer>
    </div>
</div>
</div>
