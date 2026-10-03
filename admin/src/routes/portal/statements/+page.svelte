<script>
    /**
     * Statements: the company's account with the store over a period (D74,
     * GET /x/b2b/statement) — what it owed at the start, every order placed on
     * account and every payment, what it owes at the end, and how late that
     * is. Its admins and approvers only; the route refuses a plain buyer, and
     * so does this page before asking.
     *
     * The engine's period is exclusive at the end and in the store's own
     * calendar, so the address keeps `to` as the API takes it and the screen
     * shows the last day covered. A day the store has no record of — a payment
     * it only noticed later — is said as "on or before", which is what it is.
     *
     * The CSV is fetched with the buyer's token and saved from a blob: the
     * token is a header, not a cookie, so a plain link would be refused.
     */
    import { base } from "$app/paths";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, explain, isApprover } from "$lib/trade.svelte.js";
    import Select from "$lib/components/Select.svelte";
    import PortalPage from "../PortalPage.svelte";

    const list = listState({ from: "", to: "" });

    let statement = $state(null);
    let loading = $state(true);
    let failure = $state("");
    let downloading = $state(false);
    let reqId = 0;

    const allowed = $derived(isApprover());

    $effect(() => {
        list.params;
        if (allowed) load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        failure = "";
        try {
            const st = await tradeApi.get("/x/b2b/statement" + query({ from: list.params.from, to: list.params.to }));
            if (mine !== reqId) return;
            statement = st;
        } catch (err) {
            if (mine === reqId && !err.handled) failure = explain(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function download() {
        if (downloading || !statement) return;
        downloading = true;
        try {
            const name = `statement-${statement.company.code || "account"}-${statement.from}-to-${statement.aging.as_of}`
                .replace(/[^A-Za-z0-9._-]+/g, "-");
            await tradeApi.download(
                "/x/b2b/statement" + query({ from: statement.from, to: statement.to, format: "csv" }),
                `${name}.csv`,
            );
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        } finally {
            downloading = false;
        }
    }

    /* ------------------------------------------------------------ the period */

    const pad = (n) => String(n).padStart(2, "0");
    const civil = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    const monthStart = (offset) => {
        const d = new Date();
        return new Date(d.getFullYear(), d.getMonth() + offset, 1);
    };
    const tomorrow = () => {
        const d = new Date();
        return new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1);
    };
    /** Moves a YYYY-MM-DD by whole days, on the calendar rather than the clock. */
    function shift(day, days) {
        const [y, m, d] = day.split("-").map(Number);
        return civil(new Date(y, m - 1, d + days));
    }

    /* `to` is the day after the last one covered, as the engine takes it. */
    const PRESETS = {
        month: () => ({ from: "", to: "" }),
        lastMonth: () => ({ from: civil(monthStart(-1)), to: civil(monthStart(0)) }),
        quarter: () => ({ from: civil(monthStart(-2)), to: civil(tomorrow()) }),
        year: () => ({ from: civil(new Date(new Date().getFullYear(), 0, 1)), to: civil(tomorrow()) }),
    };
    const PERIOD_OPTIONS = [
        { value: "month", label: "This month so far" },
        { value: "lastMonth", label: "Last month" },
        { value: "quarter", label: "The last three months" },
        { value: "year", label: "This year so far" },
        { value: "custom", label: "Choose the days…" },
    ];

    let custom = $state(false);
    const period = $derived.by(() => {
        if (custom) return "custom";
        const p = list.params;
        for (const [name, build] of Object.entries(PRESETS)) {
            const want = build();
            if (want.from === p.from && want.to === p.to) return name;
        }
        return "custom";
    });

    function choosePeriod(value) {
        if (value === "custom") {
            custom = true;
            if (statement) list.set({ from: statement.from, to: statement.to });
            return;
        }
        custom = false;
        list.set(PRESETS[value]());
    }

    /* The boxes show the first and the last day; the address keeps the
       engine's exclusive end. */
    const fromDay = $derived(list.params.from || statement?.from || "");
    const lastDay = $derived(list.params.to ? shift(list.params.to, -1) : statement?.aging?.as_of || "");

    function setFrom(value) {
        if (value) list.set({ from: value, to: list.params.to || statement?.to || "" });
    }
    function setLast(value) {
        if (value) list.set({ from: list.params.from || statement?.from || "", to: shift(value, 1) });
    }

    /* ------------------------------------------------------------ the figures */

    const money = (minor) => formatMoney({ amount_minor: minor, currency: statement?.currency });
    const totals = $derived.by(() => {
        let debit = 0;
        let credit = 0;
        for (const e of statement?.entries ?? []) {
            debit += e.debit_minor;
            credit += e.credit_minor;
        }
        return { debit, credit };
    });
    const AGING = [
        { key: "current_minor", label: "Not due yet" },
        { key: "days_1_30_minor", label: "1–30 days late" },
        { key: "days_31_60_minor", label: "31–60 days late" },
        { key: "days_61_90_minor", label: "61–90 days late" },
        { key: "days_over_90_minor", label: "Over 90 days late" },
    ];
    const KIND = {
        order: "Order placed",
        payment: "Payment received",
        payment_reversed: "Payment taken back",
        cancellation: "Order cancelled",
    };

    /** A civil date as words, read as written so the store's day cannot move. */
    function dayWords(day) {
        if (!day) return "—";
        const [y, m, d] = day.split("-").map(Number);
        return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(
            new Date(Date.UTC(y, m - 1, d)),
        );
    }
    const late = $derived(
        statement ? AGING.slice(1).reduce((sum, b) => sum + statement.aging[b.key], 0) : 0,
    );
</script>

<PortalPage title="Statements">
    {#snippet actions()}
        {#if allowed}
            <button
                type="button"
                class="btn secondary portal-press"
                class:loading={downloading}
                disabled={downloading || !statement}
                onclick={download}
            >
                <i class="ri-download-2-line" aria-hidden="true"></i>
                <span class="txt">Download CSV</span>
            </button>
        {/if}
    {/snippet}

    {#if !allowed}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card">
            <i class="ri-bill-line" aria-hidden="true"></i>
            <h2>Statements are for account admins and approvers</h2>
            <p class="txt-hint">Ask one of {trade.me.company.name}'s admins for a copy.</p>
        </div>
    {:else}
        <p class="field-help m-b-base portal-intro">
            {trade.me.company.name}'s account with {trade.store.name || "the store"}: what was owed at the start, every
            order placed on account and every payment, and what is owed at the end.
        </p>

        <div class="b2b-section-head portal-filters">
            <div class="field b2b-filter portal-period">
                <Select ariaLabel="Which period" value={period} options={PERIOD_OPTIONS} onchange={choosePeriod} />
            </div>
            {#if period === "custom"}
                <div class="field b2b-statement-day portal-notice">
                    <input
                        type="date"
                        aria-label="First day"
                        value={fromDay}
                        max={lastDay || undefined}
                        onchange={(e) => setFrom(e.currentTarget.value)}
                    />
                </div>
                <div class="field b2b-statement-day portal-notice">
                    <input
                        type="date"
                        aria-label="Last day"
                        value={lastDay}
                        min={fromDay || undefined}
                        onchange={(e) => setLast(e.currentTarget.value)}
                    />
                </div>
            {/if}
        </div>

        {#if failure}
            <div class="alert danger m-b-base portal-notice" role="alert">
                <p>{failure}</p>
                <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
            </div>
        {/if}

        {#if loading && !statement}
            <span class="skeleton-loader"></span>
            <span class="skeleton-loader"></span>
            <span class="skeleton-loader"></span>
        {:else if statement}
            <p class="field-help m-b-sm" aria-live="polite">
                {dayWords(statement.from)} to {dayWords(statement.aging.as_of)}, by {trade.store.name || "the store"}'s
                calendar ({statement.time_zone}).
            </p>

            <div class="b2b-statement" class:faded={loading}>
                <div class="portal-tiles m-b-sm">
                    <div class="b2b-tile">
                        <span class="b2b-tile-label">Owed at the start</span>
                        <span class="b2b-tile-value">{money(statement.opening_balance_minor)}</span>
                        <span class="b2b-tile-hint">on {dayWords(statement.from)}</span>
                    </div>
                    <div class="b2b-tile">
                        <span class="b2b-tile-label">Ordered on account</span>
                        <span class="b2b-tile-value">{money(totals.debit)}</span>
                        <span class="b2b-tile-hint">in the period</span>
                    </div>
                    <div class="b2b-tile">
                        <span class="b2b-tile-label">Paid or cancelled</span>
                        <span class="b2b-tile-value">{money(totals.credit)}</span>
                        <span class="b2b-tile-hint">taken off what's owed</span>
                    </div>
                    <div class="b2b-tile" class:is-bad={late > 0}>
                        <span class="b2b-tile-label">Owed at the end</span>
                        <span class="b2b-tile-value">{money(statement.closing_balance_minor)}</span>
                        <span class="b2b-tile-hint">
                            {late > 0 ? `${money(late)} of it is late` : `at the end of ${dayWords(statement.aging.as_of)}`}
                        </span>
                    </div>
                </div>

                <div class="b2b-aging m-b-base" role="list" aria-label="What's owed, by how late it is">
                    {#each AGING as bucket (bucket.key)}
                        <div
                            class="b2b-aging-cell"
                            role="listitem"
                            class:is-bad={bucket.key !== "current_minor" && statement.aging[bucket.key] > 0}
                        >
                            <span class="b2b-tile-label">{bucket.label}</span>
                            <span class="b2b-aging-value">{money(statement.aging[bucket.key])}</span>
                        </div>
                    {/each}
                </div>

                <div class="page-table-wrapper tw:rounded-xl tw:border">
                    <table class="table responsive-table portal-statement-table">
                        <thead>
                            <tr>
                                <th class="min-width">Date</th>
                                <th class="col-field-name-id">What happened</th>
                                <th class="min-width">PO number</th>
                                <th class="min-width">Due</th>
                                <th class="col-field-type-number min-width">Charged</th>
                                <th class="col-field-type-number min-width">Paid</th>
                                <th class="col-field-type-number min-width">Owed after</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr class="portal-statement-edge">
                                <td class="min-width txt-sm" data-name="Date">{dayWords(statement.from)}</td>
                                <td class="col-field-name-id" data-name="What happened"><span class="txt-hint">Owed at the start</span></td>
                                <td class="min-width" data-name="PO number"><span class="txt-hint">—</span></td>
                                <td class="min-width" data-name="Due"><span class="txt-hint">—</span></td>
                                <td class="col-field-type-number min-width" data-name="Charged"><span class="txt-hint">—</span></td>
                                <td class="col-field-type-number min-width" data-name="Paid"><span class="txt-hint">—</span></td>
                                <td class="col-field-type-number min-width txt-bold" data-name="Owed after">{money(statement.opening_balance_minor)}</td>
                            </tr>
                            {#each statement.entries as entry, i (`${entry.order_id}-${entry.kind}-${entry.at}-${i}`)}
                                <tr>
                                    <td class="min-width txt-sm" data-name="Date">
                                        {#if entry.date_source === "noticed"}
                                            <span class="portal-noticed" title="The store has no record of the exact day. This is when it was first seen; it happened on or before then.">
                                                On or before {dayWords(entry.date)}
                                            </span>
                                        {:else}
                                            {dayWords(entry.date)}
                                        {/if}
                                    </td>
                                    <td class="col-field-name-id" data-name="What happened">
                                        <div class="row-name row-name-stacked">
                                            <span class:txt-bold={entry.kind === "order"}>{KIND[entry.kind] ?? entry.kind}</span>
                                            <a href="{base}/portal/orders/{entry.order_id}" class="txt-hint txt-sm txt-code">{entry.order_number}</a>
                                        </div>
                                    </td>
                                    <td class="min-width" data-name="PO number">
                                        {#if entry.po_number}<span class="txt-code">{entry.po_number}</span>{:else}<span class="txt-hint">—</span>{/if}
                                    </td>
                                    <td class="min-width txt-sm" data-name="Due">
                                        {#if entry.kind === "order" && entry.due_at}{formatDate(entry.due_at, { withTime: false })}{:else}<span class="txt-hint">—</span>{/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Charged">
                                        {#if entry.debit_minor}{money(entry.debit_minor)}{:else}<span class="txt-hint">—</span>{/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Paid">
                                        {#if entry.credit_minor}{money(entry.credit_minor)}{:else}<span class="txt-hint">—</span>{/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Owed after">{money(entry.balance_minor)}</td>
                                </tr>
                            {/each}
                            {#if !statement.entries.length}
                                <tr>
                                    <td colspan="7" class="txt-hint txt-center p-base">
                                        Nothing was ordered on account or paid in this period.
                                    </td>
                                </tr>
                            {/if}
                            <tr class="portal-statement-edge">
                                <td class="min-width txt-sm" data-name="Date">{dayWords(statement.aging.as_of)}</td>
                                <td class="col-field-name-id" data-name="What happened"><span class="txt-hint">Owed at the end</span></td>
                                <td class="min-width" data-name="PO number"><span class="txt-hint">—</span></td>
                                <td class="min-width" data-name="Due"><span class="txt-hint">—</span></td>
                                <td class="col-field-type-number min-width" data-name="Charged">{money(totals.debit)}</td>
                                <td class="col-field-type-number min-width" data-name="Paid">{money(totals.credit)}</td>
                                <td class="col-field-type-number min-width txt-bold" data-name="Owed after">{money(statement.closing_balance_minor)}</td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                {#if statement.entries.some((e) => e.date_source === "noticed")}
                    <p class="field-help m-t-sm">
                        <span class="portal-noticed">On or before</span> marks a payment {trade.store.name || "the store"} has no
                        record of the exact day for. It shows the day it was first seen, and the payment was made on or before it.
                    </p>
                {/if}
            </div>
        {/if}
    {/if}
</PortalPage>
