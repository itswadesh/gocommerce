<script>
    /**
     * Home, in Litekart's order: who you are and the one thing you came to
     * do, four figures, the orders, the sales, and what is running out.
     *
     * The figures are the store's — revenue over the last thirty days from
     * the engine's own report, orders and products all time, customers all
     * time — and each is a link to the screen behind it. The Sales panel is
     * the Reports screen embedded without its own tile row, so the revenue
     * figure is fetched once and shown once.
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { getRecord } from "$lib/session.svelte.js";
    import { formatMoney, relativeTime, orderStatusLabel } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import ReportsPanel from "$lib/components/ReportsPanel.svelte";

    let loading = $state(true);
    let recent = $state([]);
    let lowStock = $state([]);
    let counts = $state({ orders: 0, products: 0, activeProducts: 0, pending: 0, unpaid: 0, customers: 0 });
    let revenue = $state(null);

    /*
     * What this operator may actually read.
     *
     * Each read is asked for only by somebody who may have it, and the
     * section it fills is drawn only when there was a read to fill it. This
     * is a shell rather than a screen with a right of its own, so there is
     * no NoAccess here: a role that can read nothing still gets the panel
     * and its navigation, which is the honest answer.
     */
    const mayOrders = $derived(can("orders.read"));
    const mayCatalog = $derived(can("catalog.read"));
    const mayStock = $derived(can("inventory.read"));
    const mayCustomers = $derived(can("customers.read"));
    const mayWrite = $derived(can("catalog.write"));

    const who = $derived.by(() => {
        const r = getRecord();
        return r?.name || r?.email || "";
    });

    $effect(() => {
        mayOrders;
        mayCatalog;
        mayStock;
        mayCustomers;
        load();
    });

    async function load() {
        loading = true;
        try {
            // Small reads rather than one bespoke dashboard endpoint: the
            // panel uses the same API everything else does. The sales figures
            // are the Reports panel's own reads, below.
            const [orders, products, active, pending, unpaid, stock, customers] = await Promise.all([
                mayOrders ? api.get("/api/admin/orders" + query({ limit: 10 })) : null,
                mayCatalog ? api.get("/api/admin/products" + query({ limit: 1 })) : null,
                mayCatalog ? api.get("/api/admin/products" + query({ status: "active", limit: 1 })) : null,
                mayOrders ? api.get("/api/admin/orders" + query({ status: "confirmed", limit: 1 })) : null,
                mayOrders ? api.get("/api/admin/orders" + query({ payment_status: "pending", limit: 1 })) : null,
                mayStock ? api.get("/api/admin/inventory/low-stock" + query({ threshold: 5, limit: 5 })) : null,
                mayCustomers ? api.get("/api/admin/customers" + query({ limit: 1 })) : null,
            ]);

            recent = orders?.data ?? [];
            counts = {
                orders: orders?.meta.total ?? 0,
                products: products?.meta.total ?? 0,
                activeProducts: active?.meta.total ?? 0,
                pending: pending?.meta.total ?? 0,
                unpaid: unpaid?.meta.total ?? 0,
                customers: customers?.meta.total ?? 0,
            };
            lowStock = stock?.data ?? [];
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

    /* Four figures, Litekart's four: revenue, orders, products, customers.
       A card whose figure nobody here may read is not shown at zero — a zero
       is a fact about the store, and this would be a fact about the role. */
    const cards = $derived(
        [
            {
                label: "Revenue",
                value: revenue == null ? "—" : formatMoney(revenue),
                hint: "Net sales, last 30 days",
                href: "/reports",
                show: mayOrders,
            },
            {
                label: "Orders",
                value: counts.orders,
                hint: `${counts.pending} awaiting shipment · ${counts.unpaid} unpaid`,
                href: "/orders",
                show: mayOrders,
            },
            {
                label: "Products",
                value: counts.products,
                hint: `${counts.activeProducts} active`,
                href: "/products",
                show: mayCatalog,
            },
            {
                label: "Customers",
                value: counts.customers,
                hint: "All time",
                href: "/customers",
                show: mayCustomers,
            },
        ].filter((card) => card.show),
    );
</script>

<svelte:head><title>Home · GoCommerce</title></svelte:head>

<!-- `.page` stays: the shell renders each route straight into its flex row,
     so this element is the one that gets sized beside the sidebar, and the
     Playwright pass uses it as the proof a screen actually rendered. -->
<div class="page page-dashboard shopify-skin">
    <!-- This element is the page; everything card-shaped inside it is
         `bg-card`, so cards lift above the page in dark. DESIGN.md §2. -->
    <div class="tw:flex tw:min-h-full tw:w-full tw:min-w-0 tw:flex-col tw:bg-background tw:font-sans tw:text-foreground">
        <!-- Full bleed, with the gutters every other screen has (20px on a
             phone, PocketBase's 30px above it), so the title and the first
             card line up with the tables on either side of a sidebar click. -->
        <div class="tw:flex tw:w-full tw:min-w-0 tw:flex-col tw:gap-4 tw:p-5 tw:sm:p-[30px]">
        <!-- Who you are and the one thing most owners open the panel to do.
             The title is the only large type on the screen. DESIGN.md §3. -->
        <header class="tw:flex tw:flex-wrap tw:items-start tw:justify-between tw:gap-4">
            <div>
                <h1 class="tw:text-2xl tw:font-semibold tw:tracking-tight tw:sm:text-3xl">Home</h1>
                {#if who}
                    <p class="tw:mt-0.5 tw:text-sm tw:text-muted-foreground">Welcome back, {who}.</p>
                {/if}
            </div>
            <div class="tw:flex tw:items-center tw:gap-2">
                <button type="button" class="btn secondary" class:loading disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                    <span class="txt">Refresh</span>
                </button>
                {#if mayWrite}
                    <a href="{base}/products?new=1" class="btn">
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Add product</span>
                    </a>
                {/if}
            </div>
        </header>

        {#if cards.length}
            <!-- One column on a phone, two on a tablet, four on a desktop. The
                 tile: uppercase muted label, big number, small muted subline,
                 never a title bar. -->
            <div class="tw:grid tw:grid-cols-1 tw:gap-4 tw:sm:grid-cols-2 tw:lg:grid-cols-4">
                {#each cards as card (card.label)}
                    <a
                        href="{base}{card.href}"
                        class="tw:flex tw:min-h-[84px] tw:flex-col tw:text-foreground tw:no-underline tw:justify-between tw:rounded-xl tw:border tw:bg-card tw:p-4 tw:transition-colors tw:hover:bg-accent tw:focus-visible:ring-2 tw:focus-visible:ring-ring tw:focus-visible:ring-offset-2 tw:focus-visible:ring-offset-background tw:focus-visible:outline-none"
                    >
                        <span class="tw:text-xs tw:font-medium tw:tracking-wide tw:text-muted-foreground tw:uppercase">
                            {card.label}
                        </span>
                        <div>
                            {#if loading}
                                <div class="tw:h-8 tw:w-16 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                            {:else}
                                <div class="tw:text-2xl tw:font-bold tw:tracking-tight tw:tabular-nums">{card.value}</div>
                            {/if}
                            <p class="tw:mt-1 tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                        </div>
                    </a>
                {/each}
            </div>
        {/if}

        {#if mayOrders}
            <!-- The latest orders, two columns wide from 1024px as Litekart
                 draws them: ten orders in the height of five. -->
            <section class="tw:rounded-xl tw:border tw:bg-card">
                <div class="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2 tw:border-b tw:px-4 tw:py-3 tw:sm:px-5">
                    <h2 class="tw:text-sm tw:font-semibold">Orders</h2>
                    <!-- Secondary navigation is a text link with an arrow, not a
                         button. DESIGN.md §7. -->
                    <a
                        href="{base}/orders"
                        class="tw:text-xs tw:text-muted-foreground tw:no-underline tw:transition-colors tw:hover:text-foreground"
                    >
                        view all <span aria-hidden="true">→</span>
                    </a>
                </div>

                <div class="tw:grid tw:grid-cols-1 tw:lg:grid-cols-2 recent-grid">
                    {#each recent as order (order.id)}
                        <!-- A row is a link rather than a clickable <tr>: it
                             middle-clicks into a second tab, takes focus, and
                             answers Enter without a key handler of its own. -->
                        <a
                            href="{base}/orders/{order.id}"
                            class="recent-row tw:flex tw:items-center tw:gap-3 tw:px-4 tw:py-2 tw:text-foreground tw:no-underline tw:transition-colors tw:hover:bg-muted/50 tw:focus-visible:ring-2 tw:focus-visible:ring-ring tw:focus-visible:ring-inset tw:focus-visible:outline-none tw:sm:px-5"
                        >
                            <div class="tw:min-w-0 tw:flex-1">
                                <div class="tw:flex tw:items-center tw:gap-2">
                                    <span class="tw:size-1.5 tw:shrink-0 tw:rounded-full {dotFor(order.status)}" aria-hidden="true"></span>
                                    <span class="tw:truncate tw:font-mono tw:text-sm tw:font-semibold">{order.number}</span>
                                    <span class="tw:truncate tw:text-xs tw:text-muted-foreground">{order.name || order.email}</span>
                                </div>
                                <p class="tw:truncate tw:pl-3.5 tw:text-xs tw:text-muted-foreground">{relativeTime(order.created_at)}</p>
                            </div>
                            <span class="tw:shrink-0 tw:text-sm tw:font-medium tw:tabular-nums">{formatMoney(order.total)}</span>
                            <!-- Status is never the dot alone; the word is always
                                 beside it. DESIGN.md §11. -->
                            <span class="tw:inline-flex tw:w-28 tw:shrink-0 tw:items-center tw:justify-center tw:gap-1 tw:rounded-full tw:border tw:px-2 tw:py-0.5 tw:text-xs tw:whitespace-nowrap">
                                <span class="tw:size-1.5 tw:rounded-full {dotFor(order.status)}" aria-hidden="true"></span>
                                {orderStatusLabel(order.status)}
                            </span>
                        </a>
                    {/each}

                    {#if loading && !recent.length}
                        {#each Array(6) as _, i (i)}
                            <div class="recent-row tw:flex tw:items-center tw:gap-4 tw:px-4 tw:py-3 tw:sm:px-5">
                                <div class="tw:h-4 tw:flex-1 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                <div class="tw:h-4 tw:w-20 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                            </div>
                        {/each}
                    {/if}

                    {#if !loading && !recent.length}
                        <p class="tw:px-4 tw:py-8 tw:text-center tw:text-sm tw:text-muted-foreground tw:sm:px-5 tw:lg:col-span-2">
                            No orders yet. They will appear here as soon as somebody buys something.
                        </p>
                    {/if}
                </div>
            </section>

            <!-- What the store sold: the Reports screen embedded, without its
                 own tile row — the revenue tile above is that figure. -->
            <ReportsPanel embedded tiles={false} onreport={(r) => (revenue = r?.currencies?.[0]?.totals?.net ?? null)} />
        {/if}

        {#if mayStock}
            <section class="tw:rounded-xl tw:border tw:bg-card">
                <div class="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2 tw:border-b tw:px-4 tw:py-3 tw:sm:px-5">
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
                        <div class="tw:flex tw:flex-col tw:gap-2 tw:px-4 tw:py-2 tw:sm:flex-row tw:sm:items-center tw:sm:gap-4 tw:sm:px-5">
                            <div class="tw:min-w-0 tw:flex-1">
                                <div class="tw:truncate tw:font-mono tw:text-sm tw:font-semibold">{variant.sku}</div>
                                <p class="tw:truncate tw:text-xs tw:text-muted-foreground">{variant.label || "—"}</p>
                            </div>
                            <!-- The three numbers keep their labels on a phone,
                                 where a bare row of digits says nothing, and
                                 take fixed tracks above it so the digits line up
                                 in columns down the list. -->
                            <div class="tw:flex tw:items-center tw:gap-4 tw:text-xs tw:text-muted-foreground">
                                <span class="tw:shrink-0 tw:sm:w-24 tw:sm:text-right">
                                    on hand <b class="tw:font-medium tw:text-foreground tw:tabular-nums">{variant.stock_on_hand}</b>
                                </span>
                                <span class="tw:shrink-0 tw:sm:w-24 tw:sm:text-right">
                                    reserved <b class="tw:font-medium tw:text-foreground tw:tabular-nums">{variant.stock_reserved}</b>
                                </span>
                                <span class="tw:shrink-0 tw:sm:w-24 tw:sm:text-right">
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
                        <p class="tw:px-4 tw:py-6 tw:text-center tw:text-sm tw:text-muted-foreground tw:sm:px-5">
                            Nothing is running out — every tracked variant has more than five available
                            across the whole store.
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
        </footer>
    </div>
</div>
</div>

<style>
    /* Hairlines between the order rows, in both columns: a divide-y would
       draw one line per row of the grid and miss the column boundary. */
    .recent-row {
        border-bottom: 1px solid var(--surfaceAlt2Color);
    }
    @media (min-width: 1024px) {
        .recent-row:nth-child(odd) {
            border-right: 1px solid var(--surfaceAlt2Color);
        }
    }
</style>
