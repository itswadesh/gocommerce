<script>
    /**
     * Reports: what the store sold. A component rather than a page, because
     * the owner's "how much did we sell" now has its answer on the dashboard
     * as well as at /reports.
     *
     * Everything on this screen is one of two API calls, and every figure in
     * them is added up by PostgreSQL over the whole window — not by this file
     * over whatever page of orders it happened to fetch. That distinction is
     * the entire reason the screen exists: the dashboard used to sum the eight
     * most recent orders and call the answer revenue.
     *
     * A sale is an order whose stock has left the shelf — confirmed, partly
     * shipped, shipped or delivered. Cash on delivery confirms at checkout, so
     * an unpaid COD order is a sale that has not been collected yet; that is
     * what the chart's stacking answers, and what the "not counted" line under
     * the cards explains.
     */
    import { base } from "$app/paths";
    import { api, apiErrorFrom, can, getToken, query } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatMoney, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import SalesChart from "$lib/components/SalesChart.svelte";
    import Select from "$lib/components/Select.svelte";

    /* embedded: rendered inside the dashboard rather than as a page of its
       own — no page chrome, no NoAccess (the dashboard decides what an
       operator may see), a section heading in place of the title. */
    /* tiles: the four money tiles above the chart. The dashboard draws its
       own tile row and asks for the report through onreport instead, so the
       figure is fetched once and shown once. */
    let { embedded = false, tiles = true, onreport = null } = $props();

    /* The starting size of the best-seller table, not the only one: `limit` is
       a listState key below, so a ten-row top list can be opened out to a
       hundred and the choice rides in the URL with the window and the grain.
       liststate only serialises keys it was declared with, so a size that is
       not in the defaults above cannot reach the request at all. */
    const PER_PAGE = embedded ? 5 : 10;

    /*
     * The operator's own zone, so "today" means their today rather than UTC's.
     * The engine cuts both the buckets and the window's own edges in it, which
     * is what keeps the first and last bar honest.
     */
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

    /* The window, the grain, the currency and the best-seller page live in the
       URL: a report nobody can send to their accountant is half a report. */
    const list = listState({
        from: "",
        to: "",
        group_by: "day",
        sort: "revenue",
        currency: "",
        page: 1,
        limit: PER_PAGE,
    });

    /* Both reports are orders.read — nav.js gates the link on it, and the two
       endpoints below sum orders. Without it the address used to render the
       whole screen and answer with a pair of 403 toasts, which reads as a
       broken panel rather than as a permission. */
    const readable = $derived(can("orders.read"));

    let report = $state(null);
    let top = $state([]);
    let topMeta = $state(null);
    let loading = $state(true);

    const grain = $derived(list.params.group_by);
    const perPage = $derived(list.params.limit);
    const currencies = $derived(report?.currencies ?? []);
    /* The chosen block, or the first one. A store that has only ever sold in
       one currency never sees the tab strip at all. */
    const block = $derived(
        currencies.find((c) => c.currency === list.params.currency) ?? currencies[0] ?? null,
    );
    const totals = $derived(block?.totals ?? null);

    $effect(() => {
        // Any parameter changing reloads: the window, the grain, the currency,
        // the ranking, the page.
        list.params;
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put two 403 toasts
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        const p = list.params;
        try {
            const [sales, best] = await Promise.all([
                api.get(
                    "/api/admin/reports/sales" +
                        query({ from: p.from, to: p.to, group_by: p.group_by, tz }),
                ),
                api.get(
                    "/api/admin/reports/top-products" +
                        query({
                            from: p.from,
                            to: p.to,
                            tz,
                            currency: p.currency,
                            sort: p.sort,
                            limit: p.limit,
                            page: p.page,
                        }),
                ),
            ]);
            report = sales;
            onreport?.(sales);
            top = best.data ?? [];
            topMeta = best.meta ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* ------------------------------------------------------------- the window */

    /*
     * Presets are computed as LOCAL civil dates and sent as YYYY-MM-DD, with
     * `to` set to the day AFTER the last day wanted. `to` is exclusive
     * everywhere in this API and this screen must not be the one place it is
     * not — the footer restates the resolved window in words so the
     * exclusivity is never a trap for whoever reads the number.
     */
    function civil(date) {
        const pad = (n) => String(n).padStart(2, "0");
        return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    }

    function daysAgo(n) {
        const d = new Date();
        d.setHours(0, 0, 0, 0);
        d.setDate(d.getDate() - n);
        return d;
    }

    function monthStart(offset) {
        const d = new Date();
        return new Date(d.getFullYear(), d.getMonth() + offset, 1);
    }

    const PRESETS = {
        "7d": () => ({ from: civil(daysAgo(6)), to: civil(daysAgo(-1)), group_by: "day" }),
        "90d": () => ({ from: civil(daysAgo(89)), to: civil(daysAgo(-1)), group_by: "week" }),
        month: () => ({ from: civil(monthStart(0)), to: civil(daysAgo(-1)), group_by: "day" }),
        lastMonth: () => ({
            from: civil(monthStart(-1)),
            to: civil(monthStart(0)),
            group_by: "day",
        }),
        year: () => ({ from: civil(monthStart(-11)), to: civil(daysAgo(-1)), group_by: "month" }),
    };

    /* Which preset the URL currently describes — so a shared link comes back
       with the right control selected instead of reading as "custom". */
    const preset = $derived.by(() => {
        const p = list.params;
        if (!p.from && !p.to) return "default";
        for (const [name, build] of Object.entries(PRESETS)) {
            const want = build();
            if (want.from === p.from && want.to === p.to) return name;
        }
        return "custom";
    });

    const RANGE_OPTIONS = [
        { value: "default", label: "Last 30 days" },
        { value: "7d", label: "Last 7 days" },
        { value: "90d", label: "Last 90 days" },
        { value: "month", label: "This month" },
        { value: "lastMonth", label: "Last month" },
        { value: "year", label: "Last 12 months" },
        { value: "custom", label: "Custom…" },
    ];

    function chooseRange(value) {
        if (value === "default") {
            list.set({ from: "", to: "", group_by: "day" });
            return;
        }
        if (value === "custom") {
            // Seed the pickers with the window already on screen rather than
            // blanking it: the operator is narrowing what they can see.
            list.set({ from: list.params.from || civil(daysAgo(29)), to: list.params.to || civil(daysAgo(-1)) });
            return;
        }
        list.set(PRESETS[value]());
    }

    /* The last day the window actually covers, for the footer. `to` is
       exclusive, so the day shown is the one before it. */
    const windowWords = $derived.by(() => {
        if (!report) return "";
        const fmt = (iso, add) => {
            const d = new Date(iso);
            if (add) d.setDate(d.getDate() + add);
            try {
                return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: report.time_zone }).format(d);
            } catch {
                return new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(d);
            }
        };
        return `${fmt(report.from)} to ${fmt(report.to, -1)}`;
    });

    function bucketLabel(bucket) {
        const options =
            grain === "month"
                ? { year: "numeric", month: "long" }
                : { month: "short", day: "numeric", year: "numeric" };
        try {
            return new Intl.DateTimeFormat(undefined, {
                ...options,
                timeZone: report?.time_zone,
            }).format(new Date(bucket.start));
        } catch {
            return new Intl.DateTimeFormat(undefined, options).format(new Date(bucket.start));
        }
    }

    /*
     * Four figures, in the dashboard's own recipe. Collected is the one a cash-
     * on-delivery store actually opens this screen for: what has been charged is
     * not what has arrived, and the difference is the unpaid COD book.
     */
    const cards = $derived([
        {
            label: "Net sales",
            value: totals && formatMoney(totals.net),
            hint: "Items less discounts, before tax",
        },
        {
            label: "Orders",
            value: totals && String(totals.orders),
            hint: totals ? `${totals.units} ${pluralize(totals.units, "unit")} sold` : "",
        },
        {
            label: "Average order",
            value: totals && formatMoney(totals.average_order),
            hint: "Charged, including tax and delivery",
        },
        {
            label: "Collected",
            value: totals && formatMoney(totals.paid.net_collected),
            hint: totals ? `of ${formatMoney(totals.total)} charged` : "",
        },
    ]);

    /* ------------------------------------------------------------- the export */

    /*
     * A deliberate copy of the download helper on the Settings → Data screen,
     * comment and all: a link cannot carry the Authorization header, so the
     * file has to come back through fetch. Copied rather than lifted into
     * $lib/api.js so shipping a Reports screen does not put a working Settings
     * screen in its blast radius — a third caller is what earns the shared
     * helper.
     */
    async function download(path, filename) {
        try {
            const response = await fetch(path, {
                headers: { Authorization: "Bearer " + getToken() },
            });
            if (!response.ok) {
                toast.error(await apiErrorFrom(response));
                return;
            }
            const blob = await response.blob();
            const url = URL.createObjectURL(blob);
            const a = document.createElement("a");
            a.href = url;
            a.download = filename;
            a.click();
            URL.revokeObjectURL(url);
            toast.success("Export downloaded");
        } catch (err) {
            toast.error(err);
        }
    }

    function exportOrders() {
        // The window the ENGINE resolved, sent back as instants rather than as
        // the civil dates the pickers hold. Two reasons: the export route
        // resolves a bare date in UTC while the report resolves it in the
        // operator's zone, so civil dates would hand back a CSV off by an edge
        // day; and the default window has no dates in the URL at all, so
        // sending none would export the whole order book under a button that
        // says "last 30 days". Both bounds mean what they mean here — from is
        // inclusive, to is exclusive — so the file is exactly the rows behind
        // the chart.
        //
        // It also closes the sub-gap the Settings screen leaves open: its
        // export button sends a bare URL while the handler accepts from and to.
        if (!report) return;
        download(
            "/api/admin/export/admin-orders" + query({ from: report.from, to: report.to }),
            "orders.csv",
        );
    }
</script>

<!-- The controls and the body are snippets so the page and the embedded
     section render the same thing with different chrome around it. -->
{#snippet controls()}
            <div class="field">
                <Select
                    ariaLabel="Date range"
                    value={preset}
                    options={RANGE_OPTIONS}
                    onchange={chooseRange}
                />
            </div>

            {#if preset === "custom"}
                <div class="field">
                    <input
                        type="date"
                        aria-label="From"
                        value={list.params.from}
                        onchange={(e) => list.set({ from: e.currentTarget.value })}
                    />
                </div>
                <div class="field">
                    <input
                        type="date"
                        aria-label="To (exclusive)"
                        value={list.params.to}
                        onchange={(e) => list.set({ to: e.currentTarget.value })}
                    />
                </div>
            {/if}

            <div class="field">
                <Select
                    ariaLabel="Group by"
                    value={grain}
                    options={[
                        { value: "day", label: "By day" },
                        { value: "week", label: "By week" },
                        { value: "month", label: "By month" },
                    ]}
                    onchange={(v) => list.set({ group_by: v })}
                />
            </div>

            {#if can("data.export")}
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={!report}
                    onclick={exportOrders}
                >
                    <i class="ri-download-2-line" aria-hidden="true"></i>
                    <span class="txt">Export CSV</span>
                </button>
            {/if}
{/snippet}

{#snippet body()}

    <!-- Two across on a phone, four from 1024px: these cards carry money,
         and a formatted amount does not fit a quarter of 390px. The tile is
         the same object as the dashboard's — uppercase muted label, big
         tabular number, small muted subline, and never a title bar.
         DESIGN.md §7, but on `bg-card` rather than the `bg-background` its
         example prints, so the tiles lift off the page in dark. §2. -->
    {#if tiles}
    <div class="tw:grid tw:grid-cols-2 tw:gap-4 tw:lg:grid-cols-4 {embedded ? 'tw:mb-3' : 'tw:mb-6'}">
        {#each cards as card (card.label)}
            <!-- A figure is a fact, not somewhere to go: no link, no hover.
                 On the dashboard the tile is the dashboard's own compact
                 one, so the two rows of figures read as one set. -->
            <div
                class="tw:flex tw:flex-col tw:justify-between tw:rounded-xl tw:border tw:bg-card {embedded
                    ? 'tw:min-h-[84px] tw:p-4'
                    : 'tw:min-h-[110px] tw:p-4 tw:sm:min-h-[130px] tw:sm:p-5'}"
            >
                <span class="tw:text-xs tw:font-medium tw:tracking-wide tw:text-muted-foreground tw:uppercase">
                    {card.label}
                </span>
                <div>
                    {#if loading && !totals}
                        <div class="tw:h-8 tw:w-24 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                    {:else}
                        <!-- 2xl until there is room: two tiles across a
                             390px phone leave about 125px of inner width,
                             and a five-figure amount does not fit that at
                             3xl. -->
                        <div class="tw:font-bold tw:tracking-tight tw:tabular-nums {embedded ? 'tw:text-xl tw:sm:text-2xl' : 'tw:text-2xl tw:sm:text-3xl'}">
                            {card.value ?? "—"}
                        </div>
                    {/if}
                    <p class="tw:mt-1 tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                </div>
            </div>
        {/each}
    </div>

    {#if totals && (totals.excluded.cancelled.orders || totals.excluded.pending.orders)}
        <!-- What stops an operator asking why this disagrees with the Orders
             list: it disagrees on purpose, and by exactly this much. -->
        <p class="tw:text-xs tw:text-muted-foreground {embedded ? 'tw:mb-3' : 'tw:mb-6'}">
            Not counted: {totals.excluded.cancelled.orders} cancelled, and
            {totals.excluded.pending.orders} still in checkout worth
            {formatMoney(totals.excluded.pending.total)}.
        </p>
    {/if}
    {/if}

    {#if currencies.length > 1}
        <div class="tabs-header m-b-base">
            {#each currencies as c (c.currency)}
                <button
                    type="button"
                    class="tab-item"
                    class:active={c.currency === block?.currency}
                    onclick={() => list.set({ currency: c.currency })}
                >
                    {c.currency}
                </button>
            {/each}
        </div>
    {/if}

    <SalesChart
        buckets={block?.buckets ?? []}
        timeZone={report?.time_zone ?? tz}
        {grain}
        {loading}
    />

    {#if !embedded}
    <h2 class="tw:mt-2 tw:mb-3 tw:text-sm tw:font-semibold">By {grain}</h2>

    <div class="page-table-wrapper tw:rounded-xl tw:border">
        <table class="table responsive-table">
            <thead class="sticky">
                <tr>
                    <th class="col-field-name-id">Period</th>
                    <th class="col-field-type-number min-width">Orders</th>
                    <th class="col-field-type-number min-width">Units</th>
                    <th class="col-field-type-number min-width">Net</th>
                    <th class="col-field-type-number min-width">Tax</th>
                    <th class="col-field-type-number min-width">Delivery</th>
                    <th class="col-field-type-number min-width">Discount</th>
                    <th class="col-field-type-number min-width">Collected</th>
                    <th class="col-field-type-number min-width">Total</th>
                </tr>
            </thead>
            <tbody>
                {#each block?.buckets ?? [] as bucket (bucket.start)}
                    <tr>
                        <td class="col-field-name-id" data-name="Period">
                            {bucketLabel(bucket)}
                            {#if bucket.partial}
                                <span class="label">part period</span>
                            {/if}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Orders">
                            {bucket.orders}
                        </td>
                        <td class="col-field-type-number min-width txt-hint" data-name="Units">
                            {bucket.units}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Net">
                            {formatMoney(bucket.net)}
                        </td>
                        <td class="col-field-type-number min-width txt-hint" data-name="Tax">
                            {formatMoney(bucket.tax)}
                        </td>
                        <td class="col-field-type-number min-width txt-hint" data-name="Delivery">
                            {formatMoney(bucket.shipping)}
                        </td>
                        <td class="col-field-type-number min-width txt-hint" data-name="Discount">
                            {formatMoney(bucket.discount)}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Collected">
                            {formatMoney(bucket.paid.net_collected)}
                        </td>
                        <td class="col-field-type-number min-width txt-bold" data-name="Total">
                            {formatMoney(bucket.total)}
                        </td>
                    </tr>
                {/each}

                {#if totals}
                    <tr class="txt-bold">
                        <td class="col-field-name-id" data-name="Period">Window</td>
                        <td class="col-field-type-number min-width" data-name="Orders">
                            {totals.orders}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Units">
                            {totals.units}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Net">
                            {formatMoney(totals.net)}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Tax">
                            {formatMoney(totals.tax)}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Delivery">
                            {formatMoney(totals.shipping)}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Discount">
                            {formatMoney(totals.discount)}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Collected">
                            {formatMoney(totals.paid.net_collected)}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Total">
                            {formatMoney(totals.total)}
                        </td>
                    </tr>
                {/if}

                {#if loading && !block}
                    <tr><td colspan="9"><span class="skeleton-loader"></span></td></tr>
                {/if}

                {#if !loading && totals && !totals.orders}
                    <tr>
                        <td colspan="9" class="txt-center txt-hint p-base">
                            Nothing sold in this window.
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>
    </div>
    {/if}

    <h2 class="tw:mb-3 tw:flex tw:flex-wrap tw:items-center tw:gap-2 tw:text-sm tw:font-semibold {embedded ? 'tw:mt-4' : 'tw:mt-8'}">
        Best sellers
        <button
            type="button"
            class="btn sm pill"
            class:secondary={list.params.sort !== "revenue"}
            onclick={() => list.set({ sort: "revenue" })}
        >
            Revenue
        </button>
        <button
            type="button"
            class="btn sm pill"
            class:secondary={list.params.sort !== "units"}
            onclick={() => list.set({ sort: "units" })}
        >
            Units
        </button>
    </h2>

    <p class="tw:mb-3 tw:text-xs tw:text-muted-foreground">
        Line value excluding tax, before order-level discounts — a discount is recorded once
        per order and never split across its lines, so these do not add up to net.
    </p>

    <div class="page-table-wrapper tw:rounded-xl tw:border">
        <table class="table responsive-table">
            <thead class="sticky">
                <tr>
                    <th class="col-field-name-id">Product</th>
                    <th class="col-field-type-number min-width">Units</th>
                    <th class="col-field-type-number min-width">Orders</th>
                    <th class="col-field-type-number min-width">Revenue</th>
                </tr>
            </thead>
            <tbody>
                {#each top as row (row.sku + "/" + (row.variant_id ?? row.product_id ?? ""))}
                    <tr>
                        <td class="col-field-name-id" data-name="Product">
                            <div class="row-name">
                                {#if row.product_id}
                                    <a
                                        href="{base}/products/{row.product_id}"
                                        class="txt-bold txt-ellipsis"
                                    >
                                        {row.title}
                                    </a>
                                {:else}
                                    <!-- The line is a snapshot, so it still
                                         reads after the product it came from
                                         was deleted — there is simply
                                         nowhere to send anyone. -->
                                    <span class="txt-bold txt-ellipsis">{row.title}</span>
                                {/if}
                                <span class="txt-hint txt-sm row-handle txt-code">
                                    {row.sku}{row.variant_label ? " · " + row.variant_label : ""}
                                </span>
                            </div>
                            {#if !row.product_id}
                                <span class="txt-hint txt-sm">product deleted</span>
                            {/if}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Units">
                            {row.units}
                        </td>
                        <td class="col-field-type-number min-width txt-hint" data-name="Orders">
                            {row.orders}
                        </td>
                        <td class="col-field-type-number min-width txt-bold" data-name="Revenue">
                            {formatMoney(row.revenue)}
                        </td>
                    </tr>
                {/each}

                {#if loading && !top.length}
                    <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                {/if}

                {#if !loading && !top.length}
                    <tr>
                        <td colspan="4" class="txt-center txt-hint p-base">
                            No sales in this window, so there is nothing to rank.
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>
    </div>

{/snippet}

{#if !readable}
    {#if !embedded}
        <!-- The sidebar already hides the link; this is the direct URL. -->
        <NoAccess right="orders.read" what="reports" />
    {/if}
{:else if embedded}
    <section class="reports-embedded">
        <div class="reports-embedded-head">
            <h2 class="tw:text-sm tw:font-semibold">Sales</h2>
            <div class="reports-controls">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh sales"
                    disabled={loading}
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
                {@render controls()}
            </div>
        </div>
        {@render body()}
        <div class="reports-embedded-foot tw:text-xs tw:text-muted-foreground">
            <span class="txt txt-hint">
                Confirmed, partly shipped, shipped and delivered orders placed {windowWords}
                {#if report}· times in {report.time_zone}{/if}
            </span>
            <div class="flex-fill"></div>
            <a
                href="{base}/reports"
                class="tw:text-xs tw:text-muted-foreground tw:no-underline tw:transition-colors tw:hover:text-foreground"
            >
                full report <span aria-hidden="true">→</span>
            </a>
        </div>
    </section>
{:else}
<div class="page page-reports shopify-skin">
    <!-- No `full-height` here, unlike every list screen. That class makes
         `.page-content` a flex column, which is right when the page is one
         table that should fill the viewport and scroll inside itself. This page
         is a document: a chart and two tables stacked. As flex items they all
         shrank to `.page-table-wrapper`'s 130px floor, so a 32-row table was
         being read through a 130px porthole with its own scrollbar nested
         inside the page's. Plain `.page-content` already scrolls; letting the
         sections take their natural height gives the screen one scrollbar. -->
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Reports</div>
            </nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    disabled={loading}
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            <div class="page-header-primary-btns">
                {@render controls()}
            </div>
        </header>

        {@render body()}
        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager
                meta={topMeta}
                {loading}
                noun="product"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <div class="flex-fill"></div>
            <span class="txt txt-hint">
                Confirmed, partly shipped, shipped and delivered orders placed {windowWords}
                {#if report}· times in {report.time_zone}{/if}
            </span>
        </footer>
    </div>
</div>
{/if}

<style>
    /* On the dashboard the section sits in the page's own column, between
       the count tiles and the recent orders, with the controls where the
       page header would have put them. */
    .reports-embedded {
        display: flex;
        flex-direction: column;
        min-width: 0;
    }
    .reports-embedded-head {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 8px 12px;
        margin-bottom: 8px;
    }
    .reports-controls {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px;
    }
    /* PocketBase's .field is a block; here each is one control in a row. */
    .reports-controls :global(.field) {
        margin: 0;
        width: auto;
        min-width: 150px;
        flex: 0 0 auto;
    }
    .reports-embedded-foot {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px 16px;
        padding-top: 8px;
    }
</style>
