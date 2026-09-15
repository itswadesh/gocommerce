<script>
    /**
     * Payouts: what each payment method took, and what went back out.
     *
     * The sales report answers "how much did we sell" and never asks which
     * gateway carried it. An operator reconciling a bank statement does ask:
     * the money lands one payout per gateway. What this is not, and the page
     * says so at the foot, is a settlement statement — the engine knows what
     * it charged and refunded, not what a gateway withheld in fees or when
     * it actually paid out.
     */
    import { api, can, query } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const readable = $derived(can("orders.read"));

    /* The operator's own zone, so "today" means their today rather than
       UTC's — the engine cuts the window's edges in it. */
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

    /* The window and the chosen currency ride in the URL: a reconciliation
       nobody can send to their bookkeeper is half a reconciliation. */
    const list = listState({ from: "", to: "", currency: "" });

    let report = $state(null);
    let loading = $state(true);

    const currencies = $derived(report?.currencies ?? []);
    const block = $derived(
        currencies.find((c) => c.currency === list.params.currency) ?? currencies[0] ?? null,
    );

    $effect(() => {
        list.params;
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            const p = list.params;
            report = await api.get("/api/admin/reports/payouts" + query({ from: p.from, to: p.to, tz }));
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* Presets computed as LOCAL civil dates and sent as YYYY-MM-DD, with
       `to` the day AFTER the last one wanted: `to` is exclusive everywhere
       in this API and this screen must not be the one place it is not. */
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
        "7d": () => ({ from: civil(daysAgo(6)), to: civil(daysAgo(-1)) }),
        month: () => ({ from: civil(monthStart(0)), to: civil(daysAgo(-1)) }),
        lastMonth: () => ({ from: civil(monthStart(-1)), to: civil(monthStart(0)) }),
        "90d": () => ({ from: civil(daysAgo(89)), to: civil(daysAgo(-1)) }),
        year: () => ({ from: civil(monthStart(-11)), to: civil(daysAgo(-1)) }),
    };
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
        { value: "month", label: "This month" },
        { value: "lastMonth", label: "Last month" },
        { value: "90d", label: "Last 90 days" },
        { value: "year", label: "Last 12 months" },
        { value: "custom", label: "Custom…" },
    ];
    function chooseRange(value) {
        if (value === "default") return list.set({ from: "", to: "" });
        if (value === "custom") {
            return list.set({
                from: list.params.from || civil(daysAgo(29)),
                to: list.params.to || civil(daysAgo(-1)),
            });
        }
        list.set(PRESETS[value]());
    }

    /* The last day the window covers, for the foot. `to` is exclusive, so
       the day shown is the one before it. */
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
</script>

<svelte:head><title>Payouts · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="orders.read" what="payouts" />
{:else}
    <div class="page page-payouts shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Orders</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Payouts</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                <div class="page-header-primary-btns">
                    <div class="field">
                        <Select ariaLabel="Date range" value={preset} options={RANGE_OPTIONS} onchange={chooseRange} />
                    </div>
                    {#if preset === "custom"}
                        <div class="field">
                            <input type="date" aria-label="From" value={list.params.from} onchange={(e) => list.set({ from: e.currentTarget.value })} />
                        </div>
                        <div class="field">
                            <input type="date" aria-label="To (exclusive)" value={list.params.to} onchange={(e) => list.set({ to: e.currentTarget.value })} />
                        </div>
                    {/if}
                </div>
            </header>

            <p class="txt-hint m-b-base">
                What each payment method charged over the window, what went back out through it, and
                what it is still owed. A sale here means the same as on Reports: an order whose stock
                has left the shelf.
            </p>

            {#if currencies.length > 1}
                <div class="tabs-header m-b-base">
                    {#each currencies as c (c.currency)}
                        <button type="button" class="tab-item" class:active={c.currency === block?.currency} onclick={() => list.set({ currency: c.currency })}>
                            {c.currency}
                        </button>
                    {/each}
                </div>
            {/if}

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Method</th>
                            <th class="col-field-type-number min-width">Orders</th>
                            <th class="col-field-type-number min-width">Collected</th>
                            <th class="col-field-type-number min-width">Refunded</th>
                            <th class="col-field-type-number min-width">Net</th>
                            <th class="col-field-type-number min-width">Owed</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each block?.methods ?? [] as method (method.code)}
                            <tr>
                                <td class="col-field-name-id" data-name="Method">
                                    <div class="row-name row-name-stacked">
                                        <span class="txt-bold">{method.name}</span>
                                        <span class="txt-hint txt-sm txt-code">{method.code}</span>
                                        {#if !method.installed}
                                            <!-- A method that took money and has since left the
                                                 binary still gets its row; this is what stops an
                                                 operator hunting for a screen to open. -->
                                            <span class="txt-hint txt-sm">no longer in this build</span>
                                        {/if}
                                    </div>
                                </td>
                                <td class="col-field-type-number min-width" data-name="Orders">{method.orders}</td>
                                <td class="col-field-type-number min-width" data-name="Collected">{formatMoney(method.collected)}</td>
                                <td class="col-field-type-number min-width txt-hint" data-name="Refunded">
                                    {method.refunded.amount_minor ? formatMoney(method.refunded) : "—"}
                                </td>
                                <td class="col-field-type-number min-width txt-bold" data-name="Net">{formatMoney(method.net)}</td>
                                <td class="col-field-type-number min-width" data-name="Owed">
                                    {#if method.outstanding_orders}
                                        <span title="{method.outstanding_orders} sold and not yet paid">{formatMoney(method.outstanding)}</span>
                                    {:else}
                                        <span class="txt-hint">—</span>
                                    {/if}
                                </td>
                            </tr>
                        {/each}

                        {#if block && block.methods.length}
                            <tr class="txt-bold">
                                <td class="col-field-name-id" data-name="Method">All methods</td>
                                <td class="col-field-type-number min-width" data-name="Orders">{block.totals.orders}</td>
                                <td class="col-field-type-number min-width" data-name="Collected">{formatMoney(block.totals.collected)}</td>
                                <td class="col-field-type-number min-width" data-name="Refunded">
                                    {block.totals.refunded.amount_minor ? formatMoney(block.totals.refunded) : "—"}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Net">{formatMoney(block.totals.net)}</td>
                                <td class="col-field-type-number min-width" data-name="Owed">
                                    {block.totals.outstanding.amount_minor ? formatMoney(block.totals.outstanding) : "—"}
                                </td>
                            </tr>
                        {/if}

                        {#if loading && !report}
                            <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                        {:else if block && !block.methods.length}
                            <tr><td colspan="6" class="txt-center txt-hint p-base">Nothing was sold in this window, so no method took anything.</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <!-- The gap between this and a bank statement, named rather than
                 left for somebody to discover while reconciling. -->
            <p class="tw:mt-4 tw:text-xs tw:text-muted-foreground tw:max-w-[80ch]">
                These are the store's own records, counted by the day each order was placed. They are
                not settlement statements: a gateway pays out on its own schedule, withholds its fees
                first, and groups orders into transfers of its choosing, so the difference between
                Net here and what reaches the bank is fees and settlement lag. The gateway's own
                dashboard is the record of what it actually sent.
            </p>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">
                    Confirmed, partly shipped, shipped and delivered orders placed {windowWords}
                    {#if report}· times in {report.time_zone}{/if}
                </span>
            </footer>
        </div>
    </div>
{/if}
