<script>
    /**
     * A company's statement: its account over a period, as the store would send
     * it — the balance it opened on, every order placed on account and every
     * payment, the balance it closed on, and what is owed by how late it is.
     *
     * The engine works it out, not this screen: the closing balance is the
     * figure the credit check uses, and a payment is dated when the store
     * recorded it. One whose record did not survive is dated when ext/b2b first
     * noticed it, and the row says so rather than presenting a guess as a date.
     *
     * `to` is exclusive in the API, as everywhere in it, so the period travels
     * in the URL that way (`sfrom`, `sto`) and the screen shows the last day it
     * covers. The CSV is fetched with the session's token, because a plain link
     * cannot carry one.
     */
    import { base } from "$app/paths";
    import { api, query } from "$lib/api.js";
    import { downloadFile, safeFilename } from "$lib/download.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import Select from "$lib/components/Select.svelte";

    let { companyId, companyCode = "" } = $props();

    const list = listState({ sfrom: "", sto: "" });

    let statement = $state(null);
    let loading = $state(true);
    let downloading = $state(false);
    let reqId = 0;

    $effect(() => {
        companyId;
        list.params;
        load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const st = await api.get(
                `/api/admin/x/b2b/companies/${companyId}/statement` + query({ from: list.params.sfrom, to: list.params.sto }),
            );
            if (mine !== reqId) return;
            statement = st;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function download() {
        if (downloading || !statement) return;
        downloading = true;
        try {
            await downloadFile(
                `/api/admin/x/b2b/companies/${companyId}/statement` +
                    query({ from: statement.from, to: statement.to, format: "csv" }),
                safeFilename(`statement-${companyCode || companyId}-${statement.from}-to-${statement.aging.as_of}`, "csv"),
                "text/csv",
            );
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

    const PRESETS = {
        month: () => ({ sfrom: civil(monthStart(0)), sto: civil(tomorrow()) }),
        lastMonth: () => ({ sfrom: civil(monthStart(-1)), sto: civil(monthStart(0)) }),
        quarter: () => ({ sfrom: civil(monthStart(-2)), sto: civil(tomorrow()) }),
        year: () => ({ sfrom: civil(new Date(new Date().getFullYear(), 0, 1)), sto: civil(tomorrow()) }),
    };
    const PERIOD_OPTIONS = [
        { value: "month", label: "This month" },
        { value: "lastMonth", label: "Last month" },
        { value: "quarter", label: "Last three months" },
        { value: "year", label: "This year" },
        { value: "custom", label: "Custom…" },
    ];

    let custom = $state(false);
    const period = $derived.by(() => {
        const p = list.params;
        if (!p.sfrom && !p.sto && !custom) return "month";
        for (const [name, build] of Object.entries(PRESETS)) {
            const want = build();
            if (want.sfrom === p.sfrom && want.sto === p.sto && !custom) return name;
        }
        return "custom";
    });

    function choosePeriod(value) {
        if (value === "custom") {
            custom = true;
            if (statement) list.set({ sfrom: statement.from, sto: statement.to });
            return;
        }
        custom = false;
        list.set(value === "month" ? { sfrom: "", sto: "" } : PRESETS[value]());
    }

    /* The two boxes show the first and the last day; the URL keeps the API's
       exclusive end. */
    const fromDay = $derived(list.params.sfrom || statement?.from || "");
    const lastDay = $derived(list.params.sto ? shift(list.params.sto, -1) : statement?.aging?.as_of || "");

    function setFrom(value) {
        if (value) list.set({ sfrom: value, sto: list.params.sto || statement?.to || "" });
    }
    function setLast(value) {
        if (value) list.set({ sfrom: list.params.sfrom || statement?.from || "", sto: shift(value, 1) });
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
        { key: "current_minor", label: "Not yet due" },
        { key: "days_1_30_minor", label: "1–30 days late" },
        { key: "days_31_60_minor", label: "31–60 days late" },
        { key: "days_61_90_minor", label: "61–90 days late" },
        { key: "days_over_90_minor", label: "Over 90 days late" },
    ];

    const KIND = {
        order: "Order placed",
        payment: "Payment",
        payment_reversed: "Payment taken back",
        cancellation: "Order cancelled",
    };

    /** A civil date as words, read in the store's own zone so the day cannot move. */
    function dayWords(day) {
        if (!day) return "—";
        const [y, m, d] = day.split("-").map(Number);
        return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(
            new Date(Date.UTC(y, m - 1, d)),
        );
    }
</script>

<div class="b2b-section-head">
    <div class="field b2b-filter">
        <Select ariaLabel="Statement period" value={period} options={PERIOD_OPTIONS} onchange={choosePeriod} />
    </div>
    {#if period === "custom"}
        <div class="field b2b-statement-day">
            <input
                type="date"
                aria-label="First day"
                value={fromDay}
                max={lastDay || undefined}
                onchange={(e) => setFrom(e.currentTarget.value)}
            />
        </div>
        <div class="field b2b-statement-day">
            <input
                type="date"
                aria-label="Last day"
                value={lastDay}
                min={fromDay || undefined}
                onchange={(e) => setLast(e.currentTarget.value)}
            />
        </div>
    {/if}
    <div class="flex-fill"></div>
    <button
        type="button"
        class="btn secondary"
        class:loading={downloading}
        disabled={downloading || !statement}
        onclick={download}
    >
        <i class="ri-download-2-line" aria-hidden="true"></i>
        <span class="txt">Download CSV</span>
    </button>
</div>

{#if loading && !statement}
    <span class="skeleton-loader"></span>
    <span class="skeleton-loader"></span>
{:else if statement}
    <p class="field-help m-b-sm" aria-live="polite">
        {dayWords(statement.from)} to {dayWords(statement.aging.as_of)}, in the store's time zone
        ({statement.time_zone}).
    </p>

    <div class="b2b-statement" class:faded={loading}>
        <div class="tw:grid tw:grid-cols-2 tw:gap-3 tw:lg:grid-cols-4 m-b-sm">
            <div class="b2b-tile">
                <span class="b2b-tile-label">Opening balance</span>
                <span class="b2b-tile-value">{money(statement.opening_balance_minor)}</span>
                <span class="b2b-tile-hint">Owed on {dayWords(statement.from)}</span>
            </div>
            <div class="b2b-tile">
                <span class="b2b-tile-label">Charged</span>
                <span class="b2b-tile-value">{money(totals.debit)}</span>
                <span class="b2b-tile-hint">Orders on account in the period</span>
            </div>
            <div class="b2b-tile">
                <span class="b2b-tile-label">Paid or cancelled</span>
                <span class="b2b-tile-value">{money(totals.credit)}</span>
                <span class="b2b-tile-hint">Taken off what is owed</span>
            </div>
            <div class="b2b-tile">
                <span class="b2b-tile-label">Closing balance</span>
                <span class="b2b-tile-value">{money(statement.closing_balance_minor)}</span>
                <span class="b2b-tile-hint">Owed at the end of {dayWords(statement.aging.as_of)}</span>
            </div>
        </div>

        <div class="b2b-aging m-b-base" role="list" aria-label="What is owed, by how late it is">
            {#each AGING as bucket (bucket.key)}
                <div class="b2b-aging-cell" role="listitem" class:is-bad={bucket.key !== "current_minor" && statement.aging[bucket.key] > 0}>
                    <span class="b2b-tile-label">{bucket.label}</span>
                    <span class="b2b-aging-value">{money(statement.aging[bucket.key])}</span>
                </div>
            {/each}
        </div>

        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table">
                <thead>
                    <tr>
                        <th class="min-width">Date</th>
                        <th class="col-field-name-id">Entry</th>
                        <th class="min-width">PO number</th>
                        <th class="min-width">Due</th>
                        <th class="col-field-type-number min-width">Debit</th>
                        <th class="col-field-type-number min-width">Credit</th>
                        <th class="col-field-type-number min-width">Balance</th>
                    </tr>
                </thead>
                <tbody>
                    {#each statement.entries as entry, i (`${entry.order_id}-${entry.kind}-${entry.at}-${i}`)}
                        <tr>
                            <td class="min-width txt-sm" data-name="Date">
                                <span>{dayWords(entry.date)}</span>
                                {#if entry.date_source === "noticed"}
                                    <span
                                        class="label label-warning b2b-noticed"
                                        title="The store kept no record of when this happened, so it is dated when it was first seen. It happened on or before this day."
                                        >Noticed</span
                                    >
                                {/if}
                            </td>
                            <td class="col-field-name-id" data-name="Entry">
                                <div class="row-name row-name-stacked">
                                    <span class:txt-bold={entry.kind === "order"}>{KIND[entry.kind] ?? entry.kind}</span>
                                    <a href="{base}/orders/{entry.order_id}" class="txt-hint txt-sm txt-code">{entry.order_number}</a>
                                </div>
                            </td>
                            <td class="min-width" data-name="PO number">
                                {#if entry.po_number}<span class="txt-code">{entry.po_number}</span>{:else}<span class="txt-hint">—</span>{/if}
                            </td>
                            <td class="min-width txt-sm" data-name="Due">
                                {#if entry.kind === "order"}{formatDate(entry.due_at, { withTime: false })}{:else}<span class="txt-hint">—</span>{/if}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Debit">
                                {#if entry.debit_minor}{money(entry.debit_minor)}{:else}<span class="txt-hint">—</span>{/if}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Credit">
                                {#if entry.credit_minor}
                                    {money(entry.credit_minor)}
                                {:else if entry.kind !== "order" && !entry.debit_minor}
                                    <span class="txt-hint" title="It was already settled, so this moved nothing.">—</span>
                                {:else}
                                    <span class="txt-hint">—</span>
                                {/if}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Balance">{money(entry.balance_minor)}</td>
                        </tr>
                    {/each}
                    {#if !statement.entries.length}
                        <tr>
                            <td colspan="7" class="txt-hint txt-center p-base">
                                Nothing was ordered on account or paid in this period.
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>
    </div>
{/if}
