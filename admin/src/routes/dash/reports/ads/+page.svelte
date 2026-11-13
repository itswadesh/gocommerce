<script>
    /**
     * Ads performance (ext/ads, D168): which sources, mediums and campaigns
     * bring orders, what those orders earned after product cost, and against
     * the spend entered here, whether the ads paid for themselves.
     *
     * Every figure is the engine's; the screen adds nothing up. Each currency
     * is a block of its own, because minor units of two currencies do not add.
     * A row opens onto the orders behind it. The AI summary is shown beside
     * the figures it was written from, so every claim in it can be checked.
     */
    import { base } from "$app/paths";
    import { slide, fade } from "svelte/transition";
    import { ads, can } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatDate, formatMoney, pluralize, toMinor } from "$lib/format.js";
    import { RANGES, rangeLabel, rangeWindow } from "$lib/recovery.js";
    import { NOT_RECORDED, decodeExport, ratioText, reportCSV, rowLabel, summaryProblem } from "$lib/ads.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";
    import { ensureSettings, settings } from "$lib/settings.svelte.js";

    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    /* The engine reads at most two years, so "All time" is not offered. */
    const WINDOWS = RANGES.filter((r) => r.value !== "all");
    const list = listState({ range: "30d", from: "", to: "", touch: "last" });

    const readable = $derived(can("ads.read"));
    const writable = $derived(can("ads.write"));
    const missing = $derived(modulesKnown() && !hasModule("ads") && readable);

    /* Transitions shrink to nothing for a reader who asked for less motion;
       the change itself still shows. */
    const still = typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    const ms = (n) => (still ? 0 : n);

    let report = $state(null);
    let breakdown = $state(null);
    let advice = $state(null);
    let spend = $state([]);
    let loading = $state(true);
    let reqId = 0;
    const win = $derived(rangeWindow(list.params.range, list.params.from, list.params.to));

    $effect(() => {
        list.params;
        load();
    });

    function params() {
        const w = rangeWindow(list.params.range, list.params.from, list.params.to);
        return { from: w.fromCivil, to: w.toCivil, tz, touch: list.params.touch };
    }

    async function load() {
        if (!readable || !hasModule("ads")) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        open = null;
        try {
            const p = params();
            const [r, s, b, a] = await Promise.all([ads.report(p), ads.spend({ from: p.from, to: p.to }), ads.breakdown(p), ads.advice(p)]);
            if (mine !== reqId) return;
            advice = a;
            report = r;
            spend = s ?? [];
            breakdown = b;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function chooseRange(value) {
        if (value === "custom") {
            const w = rangeWindow(list.params.range === "custom" ? "30d" : list.params.range);
            list.set({ range: "custom", from: list.params.from || w.fromCivil, to: list.params.to || w.toCivil });
            return;
        }
        list.set({ range: value, from: "", to: "" });
    }

    const count = (n) => new Intl.NumberFormat().format(n ?? 0);
    const blocks = $derived(report?.currencies ?? []);

    function tiles(t) {
        return [
            { label: "Ad spend", icon: "ri-coins-line", value: t.spend ? formatMoney(t.spend) : "Not entered" },
            { label: "Sales", icon: "ri-shopping-bag-3-line", value: formatMoney(t.revenue), hint: `${count(t.orders)} ${pluralize(t.orders, "order")}` },
            {
                label: "Gross profit",
                icon: "ri-funds-line",
                value: formatMoney(t.gross_profit),
                hint: t.uncosted_units ? `${count(t.uncosted_units)} ${pluralize(t.uncosted_units, "unit")} without a cost` : "After product cost",
                warn: t.uncosted_units > 0,
            },
            { label: "ROAS", icon: "ri-line-chart-line", value: ratioText(t.roas), hint: "Sales ÷ spend" },
            { label: "Profit per spend", icon: "ri-scales-3-line", value: ratioText(t.poas), hint: "Gross profit ÷ spend" },
            {
                label: "Customers",
                icon: "ri-user-heart-line",
                value: count(t.new_customers),
                hint: `new · ${count(t.returning_customers)} returning`,
            },
        ];
    }

    // ------------------------------------------------------------ drill-down

    let open = $state(null);
    let orders = $state([]);
    let ordersLoading = $state(false);

    const keyOf = (currency, r) => `${currency}|${r.source}|${r.medium}|${r.campaign}`;

    async function toggle(currency, r) {
        const k = keyOf(currency, r);
        if (open === k) {
            open = null;
            return;
        }
        open = k;
        orders = [];
        if (!r.orders) return;
        ordersLoading = true;
        try {
            const got = await ads.orders({ ...params(), currency, source: r.source, medium: r.medium, campaign: r.campaign });
            if (open === k) orders = got ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            ordersLoading = false;
        }
    }

    function downloadCSV() {
        const url = URL.createObjectURL(new Blob([reportCSV(report)], { type: "text/csv" }));
        const a = document.createElement("a");
        a.href = url;
        a.download = `ads-${report.from}-to-${report.to}.csv`;
        a.click();
        URL.revokeObjectURL(url);
    }

    // ----------------------------------------------------------- breakdowns

    const CUTS = [
        ["weekdays", "Day of week"],
        ["hours", "Hour"],
        ["regions", "State"],
        ["campaigns", "Campaign"],
    ];
    const DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
    let cut = $state("weekdays");
    const cutName = $derived(CUTS.find((c) => c[0] === cut)[1]);
    function cutLabel(key) {
        if (cut === "weekdays") return DAYS[Number(key) - 1] ?? key;
        if (cut === "hours") return `${String(key).padStart(2, "0")}:00`;
        return key;
    }
    /* The bar is each line's share of the busiest line's orders, drawn with
       scaleX so it animates on the compositor. */
    const most = (slices) => Math.max(1, ...slices.map((x) => x.orders));

    // ---------------------------------------------------------------- pacing

    let pacing = $state(null);
    let budget = $state({ source: "", amount: "" });
    let savingBudget = $state(false);
    const PACE = { over: "Over budget", under: "Under pace", on_pace: "On pace", none: "No budget" };
    const PACE_KIND = { over: "wasting", on_pace: "working", under: "watch", none: "watch" };

    function loadPacing() {
        ads.pacing({ tz }).then((p) => (pacing = p)).catch((err) => toast.error(err));
    }
    $effect(() => {
        if (readable && hasModule("ads")) loadPacing();
    });

    async function saveBudget(e, source, amountText) {
        e?.preventDefault();
        const currency = storeCurrency;
        const amount = amountText === 0 ? 0 : toMinor(amountText, currency);
        if (amount === null || amount < 0) {
            toast.error("Enter the month's budget, such as 30000.");
            return;
        }
        savingBudget = true;
        try {
            await ads.saveBudget({ source, amount_minor: amount, currency });
            toast.success(amount ? "Budget saved." : "Budget removed.");
            budget = { source: "", amount: "" };
            loadPacing();
        } catch (err) {
            toast.error(err);
        } finally {
            savingBudget = false;
        }
    }

    // --------------------------------------------------------------- advisor

    const FINDING = {
        no_orders: ["Spends, sells nothing", "wasting"],
        loss: ["Loses money", "wasting"],
        quiet_hours: ["Hours that never sell", "watch"],
        losing_region: ["State that loses money", "watch"],
        attribution: ["Orders not traced", "watch"],
    };
    let reviewing = $state(false);

    async function review() {
        reviewing = true;
        try {
            advice = await ads.review(params());
            if (advice.source === "ai") toast.success("AI review ready.");
        } catch (err) {
            toast.error(err);
        } finally {
            reviewing = false;
        }
    }

    // ------------------------------------------------------------ connectors

    let connectors = $state([]);
    let syncing = $state("");

    function loadConnectors() {
        ads.connectors().then((c) => (connectors = c ?? [])).catch(() => {});
    }
    $effect(() => {
        if (readable && hasModule("ads")) loadConnectors();
    });

    async function syncNow(c) {
        syncing = c.platform;
        try {
            const run = await ads.sync(c.platform);
            if (run.ok) toast.success(run.message);
            else toast.error(run.message);
            loadConnectors();
            loadPacing();
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            syncing = "";
        }
    }

    // --------------------------------------------------------------- summary

    let summary = $state(null);
    let summarising = $state(false);
    let problem = $state(null);
    let weekly = $state(false);
    let savingWeekly = $state(false);

    $effect(() => {
        if (readable && hasModule("ads")) {
            ads.summary().then((s) => (summary = s?.id ? s : null)).catch(() => {});
            ads.settings().then((s) => (weekly = !!s?.weekly_summary)).catch(() => {});
        }
    });

    async function summarise() {
        summarising = true;
        problem = null;
        try {
            summary = await ads.summarise(params());
        } catch (err) {
            problem = summaryProblem(err);
        } finally {
            summarising = false;
        }
    }

    async function setWeekly(on, box) {
        savingWeekly = true;
        try {
            weekly = !!(await ads.saveSettings({ weekly_summary: on }))?.weekly_summary;
            toast.success(on ? "A summary of each week will be made on Monday." : "Weekly summaries are off.");
        } catch (err) {
            toast.error(err);
            box.checked = weekly;
        } finally {
            savingWeekly = false;
        }
    }

    const KIND = { working: "Working", wasting: "Wasting money", watch: "Check" };
    const figureOf = (row) => summary?.figures?.find((f) => f.row === row);

    // ----------------------------------------------------------------- spend

    let entry = $state({ source: "", medium: "", campaign: "", from: "", to: "", amount: "" });
    let importing = $state({ format: "google", source: "google", medium: "cpc" });
    let file = $state(null);
    let busy = $state(false);
    /* The store's own currency: spend with no currency named is saved in it
       by the engine, so the form names the same one. */
    $effect(() => {
        ensureSettings();
    });
    const storeCurrency = $derived(settings.currency);

    async function addSpend(e) {
        e.preventDefault();
        const currency = storeCurrency;
        const amount = toMinor(entry.amount, currency);
        if (amount === null || amount < 0) {
            toast.error("Enter the amount spent, such as 4000 or 4000.50.");
            return;
        }
        busy = true;
        try {
            const { source, medium, campaign, from } = entry;
            await ads.addSpend({ source, medium, campaign, from, to: entry.to || from, amount_minor: amount, currency });
            toast.success("Spend saved.");
            entry = { ...entry, campaign: "", amount: "" };
            loadPacing();
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    function chooseFormat(format) {
        importing = format === "meta"
            ? { ...importing, format, source: "facebook", medium: "paid" }
            : { ...importing, format, source: "google", medium: "cpc" };
    }

    async function importSpend(e) {
        e.preventDefault();
        if (!file) {
            toast.error("Choose the file you downloaded.");
            return;
        }
        const form = e.currentTarget;
        busy = true;
        try {
            const csv = decodeExport(await file.arrayBuffer());
            // No currency: the engine uses the file's, else the store's.
            const { format, source, medium } = importing;
            const res = await ads.importSpend({ format, source, medium, csv });
            toast.success(
                `Imported ${count(res.days)} ${pluralize(res.days, "day")} for ${count(res.campaigns)} ${pluralize(res.campaigns, "campaign")}: ` +
                    res.totals.map(formatMoney).join(", "),
            );
            file = null;
            form.reset();
            loadPacing();
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    async function removeSpend(s) {
        try {
            await ads.deleteSpend(s.id);
            loadPacing();
            await load();
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>{titled("Ads performance")}</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="ads.read" what="the ads report" />
{:else}
    <div class="page page-ads shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <div class="recovery-title-block">
                    <h1 class="recovery-h1">Ads performance</h1>
                    <p class="txt-hint txt-sm">{rangeLabel(list.params.range, list.params.from, list.params.to)}, in {tz}</p>
                </div>
                <div class="flex-fill"></div>
                {#if !missing && report}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn sm secondary ads-press" onclick={downloadCSV}>
                            <i class="ri-download-2-line" aria-hidden="true"></i>
                            <span class="txt">Download CSV</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="ads" what="The ads report is ext/ads's, and this binary does not have it." />
            {:else}
                <div class="recovery-filters recovery-report-controls" role="group" aria-label="Filters">
                    <div class="field recovery-select">
                        <Select ariaLabel="Window" value={list.params.range} options={WINDOWS} onchange={chooseRange} />
                    </div>
                    {#if list.params.range === "custom"}
                        <div class="field">
                            <input type="date" aria-label="From" value={list.params.from} onchange={(e) => list.set({ from: e.currentTarget.value })} />
                        </div>
                        <div class="field">
                            <input type="date" aria-label="To, inclusive" value={list.params.to} onchange={(e) => list.set({ to: e.currentTarget.value })} />
                        </div>
                    {/if}
                    <div class="recovery-seg" role="group" aria-label="Credit each order to">
                        {#each [["last", "Last visit"], ["first", "First visit"]] as [value, label] (value)}
                            <button type="button" class="recovery-seg-btn" aria-pressed={list.params.touch === value} onclick={() => list.set({ touch: value })}>
                                {label}
                            </button>
                        {/each}
                    </div>
                    <button
                        type="button"
                        class="btn circle sm transparent secondary recovery-refresh"
                        class:is-spinning={loading}
                        title={loading ? "Refreshing" : "Refresh"}
                        aria-label={loading ? "Refreshing" : "Refresh"}
                        disabled={loading}
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if loading && !report}
                    <span class="skeleton-loader" style="height: 180px"></span>
                {:else if report && !blocks.length}
                    <div class="recovery-empty-block recovery-report-card">
                        <i class="ri-megaphone-line" aria-hidden="true"></i>
                        <p><strong>No orders and no ad spend in this period</strong></p>
                        <p class="txt-hint">
                            Orders appear here by where the shopper came from, once your storefront records it. Enter what your
                            ads cost below to see whether they pay.
                        </p>
                    </div>
                {/if}

                {#each blocks as block (block.currency)}
                    <section class="recovery-report-card ads-block" aria-label="In {block.currency}">
                        {#if blocks.length > 1}
                            <h2 class="recovery-report-title">In {block.currency}</h2>
                        {/if}
                        <div class="tw:mb-4 tw:grid tw:grid-cols-2 tw:gap-3 tw:md:grid-cols-3 tw:xl:grid-cols-6">
                            {#each tiles(block.totals) as t (t.label)}
                                <div class="recovery-tile tw:flex tw:min-h-24 tw:flex-col tw:justify-between tw:rounded-lg tw:border tw:bg-card tw:p-3 tw:sm:p-4">
                                    <span class="recovery-tile-label"><i class={t.icon} aria-hidden="true"></i>{t.label}</span>
                                    <div>
                                        {#key t.value}
                                            <div class="recovery-figure tw:text-xl tw:font-semibold tw:tracking-tight tw:tabular-nums tw:sm:text-2xl">{t.value}</div>
                                        {/key}
                                        {#if t.hint}
                                            <p class="tw:mt-1 tw:truncate tw:text-xs tw:text-muted-foreground" class:ads-warn={t.warn}>{t.hint}</p>
                                        {/if}
                                    </div>
                                </div>
                            {/each}
                        </div>

                        <div class="page-table-wrapper tw:rounded-lg tw:border">
                            <table class="table responsive-table ads-table">
                                <caption class="tw:sr-only">Orders and spend by source, medium and campaign, in {block.currency}</caption>
                                <thead class="sticky">
                                    <tr>
                                        <th>Source · campaign</th>
                                        <th class="col-field-type-number">Orders</th>
                                        <th class="col-field-type-number">Sales</th>
                                        <th class="col-field-type-number">Net</th>
                                        <th class="col-field-type-number">Gross profit</th>
                                        <th class="col-field-type-number">Spend</th>
                                        <th class="col-field-type-number">ROAS</th>
                                        <th class="col-field-type-number">Profit / spend</th>
                                        <th class="col-field-type-number">Cost / order</th>
                                        <th class="col-field-type-number">New · returning</th>
                                        <th class="col-field-type-number">Returned</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each block.rows as r (keyOf(block.currency, r))}
                                        {@const label = rowLabel(r)}
                                        {@const k = keyOf(block.currency, r)}
                                        <tr class="ads-row" class:is-open={open === k}>
                                            <td data-name="Source · campaign">
                                                <button type="button" class="ads-row-btn" aria-expanded={open === k} onclick={() => toggle(block.currency, r)}>
                                                    <i class="ri-arrow-right-s-line ads-chevron" aria-hidden="true"></i>
                                                    <span>
                                                        <span class="ads-where" class:txt-hint={r.source === NOT_RECORDED}>{label.where}</span>
                                                        <span class="ads-campaign">{label.campaign}</span>
                                                    </span>
                                                </button>
                                            </td>
                                            <td class="col-field-type-number" data-name="Orders">{count(r.orders)}</td>
                                            <td class="col-field-type-number" data-name="Sales">{formatMoney(r.revenue)}</td>
                                            <td class="col-field-type-number" data-name="Net">{formatMoney(r.net)}</td>
                                            <td class="col-field-type-number" data-name="Gross profit">
                                                <span class:ads-loss={r.gross_profit.amount_minor < 0}>{formatMoney(r.gross_profit)}</span>
                                                {#if r.uncosted_units}
                                                    <span class="ads-warn txt-sm" title="Units sold with no cost recorded; the profit does not take them off">
                                                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                                                        {count(r.uncosted_units)} without cost
                                                    </span>
                                                {/if}
                                            </td>
                                            <td class="col-field-type-number" data-name="Spend">{r.spend ? formatMoney(r.spend) : "—"}</td>
                                            <td class="col-field-type-number" data-name="ROAS">{ratioText(r.roas)}</td>
                                            <td class="col-field-type-number" data-name="Profit / spend">
                                                <span class:ads-loss={r.poas !== null && r.poas < 1}>{ratioText(r.poas)}</span>
                                            </td>
                                            <td class="col-field-type-number" data-name="Cost / order">{r.cost_per_order ? formatMoney(r.cost_per_order) : "—"}</td>
                                            <td class="col-field-type-number" data-name="New · returning">{count(r.new_customers)} · {count(r.returning_customers)}</td>
                                            <td class="col-field-type-number" data-name="Returned">{count(r.returns)}</td>
                                        </tr>
                                        {#if open === k}
                                            <tr class="ads-drill">
                                                <td colspan="11">
                                                    <div transition:slide={{ duration: ms(200) }}>
                                                        {#if !r.orders}
                                                            <p class="txt-hint txt-sm">No orders came from this — only spend.</p>
                                                        {:else if ordersLoading}
                                                            <span class="skeleton-loader" style="height: 48px"></span>
                                                        {:else}
                                                            <ul class="ads-orders">
                                                                {#each orders as o (o.id)}
                                                                    <li>
                                                                        <a class="ads-order-link" href="{base}/dash/orders/{o.id}">{o.number}</a>
                                                                        <span class="txt-hint">{formatDate(o.created_at)}</span>
                                                                        <span>{formatMoney(o.total)}</span>
                                                                        <span class:ads-loss={o.gross_profit.amount_minor < 0}>profit {formatMoney(o.gross_profit)}</span>
                                                                        <span class="label">{o.customer}</span>
                                                                        {#if o.refunded.amount_minor}<span class="label">refunded {formatMoney(o.refunded)}</span>{/if}
                                                                        {#if o.returned}<span class="label">returned</span>{/if}
                                                                    </li>
                                                                {/each}
                                                            </ul>
                                                        {/if}
                                                    </div>
                                                </td>
                                            </tr>
                                        {/if}
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                    </section>
                {/each}

                {#if breakdown?.currencies?.length}
                    <section class="recovery-report-card" aria-labelledby="ads-cut-title">
                        <h2 class="recovery-report-title" id="ads-cut-title">When and where orders come from</h2>
                        <p class="txt-hint txt-sm recovery-card-desc">
                            The same orders by the day and hour they were placed in {breakdown.time_zone}, the state they were
                            shipped to, and campaign name across every source.
                        </p>
                        <div class="recovery-seg ads-cuts" role="group" aria-label="Break down by">
                            {#each CUTS as [value, label] (value)}
                                <button type="button" class="recovery-seg-btn" aria-pressed={cut === value} onclick={() => (cut = value)}>{label}</button>
                            {/each}
                        </div>
                        {#each breakdown.currencies as b (b.currency)}
                            {@const slices = b[cut] ?? []}
                            {@const top = most(slices)}
                            {#if breakdown.currencies.length > 1}<h3 class="ads-form-title">In {b.currency}</h3>{/if}
                            {#if !slices.length}
                                <p class="txt-hint txt-sm">{cut === "campaigns" ? "No order or spend in this period names a campaign." : "No orders in this period."}</p>
                            {:else}
                                <div class="page-table-wrapper tw:mt-2 tw:rounded-lg tw:border">
                                    <table class="table responsive-table ads-table">
                                        <caption class="tw:sr-only">Orders by {cutName.toLowerCase()}, in {b.currency}</caption>
                                        <thead class="sticky">
                                            <tr>
                                                <th>{cutName}</th>
                                                <th class="col-field-type-number">Orders</th>
                                                <th class="col-field-type-number">Net</th>
                                                <th class="col-field-type-number">Gross profit</th>
                                                {#if cut === "campaigns"}
                                                    <th class="col-field-type-number">Spend</th>
                                                    <th class="col-field-type-number">ROAS</th>
                                                    <th class="col-field-type-number">Profit / spend</th>
                                                {/if}
                                            </tr>
                                        </thead>
                                        <tbody>
                                            {#each slices as x (cut + x.key)}
                                                <tr class:ads-quiet={!x.orders}>
                                                    <td data-name={cutName}>
                                                        <span class="ads-where">{cutLabel(x.key)}</span>
                                                        <span class="ads-bar" aria-hidden="true"><span style="transform: scaleX({x.orders / top})"></span></span>
                                                    </td>
                                                    <td class="col-field-type-number" data-name="Orders">{count(x.orders)}</td>
                                                    <td class="col-field-type-number" data-name="Net">{formatMoney(x.net)}</td>
                                                    <td class="col-field-type-number" data-name="Gross profit">
                                                        <span class:ads-loss={x.gross_profit.amount_minor < 0}>{formatMoney(x.gross_profit)}</span>
                                                    </td>
                                                    {#if cut === "campaigns"}
                                                        <td class="col-field-type-number" data-name="Spend">{x.spend ? formatMoney(x.spend) : "—"}</td>
                                                        <td class="col-field-type-number" data-name="ROAS">{ratioText(x.roas ?? null)}</td>
                                                        <td class="col-field-type-number" data-name="Profit / spend">
                                                            <span class:ads-loss={x.poas != null && x.poas < 1}>{ratioText(x.poas ?? null)}</span>
                                                        </td>
                                                    {/if}
                                                </tr>
                                            {/each}
                                        </tbody>
                                    </table>
                                </div>
                            {/if}
                        {/each}
                    </section>
                {/if}

                <section class="recovery-report-card" aria-labelledby="ads-pace-title">
                    <h2 class="recovery-report-title" id="ads-pace-title">Budget pacing</h2>
                    <p class="txt-hint txt-sm recovery-card-desc">
                        {#if pacing}This month's spend to day {pacing.day} of {pacing.days}, against the monthly budget you set for each source.{/if}
                        Spend counts once it is entered or imported, so a day not yet in reads as under pace.
                    </p>
                    {#if pacing?.sources?.length}
                        <ul class="ads-pace">
                            {#each pacing.sources as p (p.source + p.spent.currency)}
                                <li class="ads-pace-row" transition:fade={{ duration: ms(160) }}>
                                    <div class="ads-pace-head">
                                        <strong>{p.source}</strong>
                                        <span class="ads-kind is-{PACE_KIND[p.status]}">{PACE[p.status]}</span>
                                        <span class="flex-fill"></span>
                                        <span class="tw:tabular-nums">{formatMoney(p.spent)}{p.budget ? ` of ${formatMoney(p.budget)}` : ""}</span>
                                        {#if writable && p.budget}
                                            <button type="button" class="btn circle sm transparent secondary ads-press" aria-label="Remove the budget for {p.source}" title="Remove budget" disabled={savingBudget} onclick={(e) => saveBudget(e, p.source, 0)}>
                                                <i class="ri-close-line" aria-hidden="true"></i>
                                            </button>
                                        {/if}
                                    </div>
                                    {#if p.budget}
                                        <span class="ads-bar is-wide" role="img" aria-label="{formatMoney(p.spent)} spent of {formatMoney(p.budget)}">
                                            <span class:is-over={p.status === "over"} style="transform: scaleX({Math.min(1, p.spent.amount_minor / p.budget.amount_minor)})"></span>
                                        </span>
                                        <p class="txt-hint txt-sm">By today: {formatMoney(p.expected)} · at this rate the month ends at {formatMoney(p.projected)}</p>
                                    {/if}
                                </li>
                            {/each}
                        </ul>
                    {:else if pacing}
                        <p class="txt-hint txt-sm">No budget set and no spend this month.</p>
                    {/if}
                    {#if writable}
                        <form class="ads-form ads-budget-form" onsubmit={(e) => saveBudget(e, budget.source, budget.amount)}>
                            <div class="ads-grid">
                                <label class="field"><span class="txt-sm">utm_source</span><input required placeholder="google" bind:value={budget.source} /></label>
                                <label class="field"><span class="txt-sm">Monthly budget ({storeCurrency})</span><input inputmode="decimal" required bind:value={budget.amount} /></label>
                            </div>
                            <button type="submit" class="btn sm ads-press" class:btn-loading={savingBudget} disabled={savingBudget}>Set budget</button>
                        </form>
                    {/if}
                </section>

                {#if advice}
                    <section class="recovery-report-card" aria-labelledby="ads-advice-title">
                        <div class="recovery-card-head">
                            <div>
                                <h2 class="recovery-report-title" id="ads-advice-title">
                                    <i class="ri-lightbulb-flash-line" aria-hidden="true"></i> Ads advisor
                                </h2>
                                <p class="txt-hint txt-sm recovery-card-desc">
                                    Where this period's ads look wasted, with the figure each point rests on. Suggestions only:
                                    nothing is changed in your ad accounts.
                                </p>
                            </div>
                            {#if writable && advice.findings.length}
                                <button type="button" class="btn sm secondary ads-press" class:btn-loading={reviewing} disabled={reviewing} onclick={review}>
                                    <i class="ri-sparkling-2-line" aria-hidden="true"></i>
                                    <span class="txt">{reviewing ? "Reviewing…" : "Review with AI"}</span>
                                </button>
                            {/if}
                        </div>
                        {#if advice.note}
                            <p class="ads-problem txt-sm" role="status" in:fade={{ duration: ms(160) }}>{advice.note}</p>
                        {/if}
                        {#if !advice.findings.length}
                            <p class="txt-hint txt-sm">Nothing stands out as wasted in this period.</p>
                        {:else}
                            {#key advice}
                                <ul class="ads-points" in:fade={{ duration: ms(220) }}>
                                    {#each advice.findings as f, i (i)}
                                        <li class="ads-point">
                                            <div>
                                                <span class="ads-kind is-{FINDING[f.kind]?.[1] ?? 'watch'}">{FINDING[f.kind]?.[0] ?? f.kind}</span>
                                                <p>{f.text}</p>
                                                <p class="ads-action"><i class="ri-arrow-right-line" aria-hidden="true"></i> {f.action}</p>
                                            </div>
                                            <p class="ads-figures">{f.figure}</p>
                                        </li>
                                    {/each}
                                </ul>
                                <p class="txt-hint txt-sm tw:mt-2">{advice.source === "ai" ? "Written by AI from these figures." : "Worked out from the report's own figures; no AI used."}</p>
                            {/key}
                        {/if}
                    </section>
                {/if}

                <section class="recovery-report-card ads-summary" aria-labelledby="ads-summary-title">
                    <div class="recovery-card-head">
                        <div>
                            <h2 class="recovery-report-title" id="ads-summary-title">
                                <i class="ri-sparkling-2-line" aria-hidden="true"></i> AI summary
                            </h2>
                            <p class="txt-hint txt-sm recovery-card-desc">
                                Written only from the figures in this report, shown beside each point. Each summary uses AI credit.
                            </p>
                        </div>
                        {#if writable}
                            <button type="button" class="btn sm ads-press" class:btn-loading={summarising} disabled={summarising || !report} onclick={summarise}>
                                <span class="txt">{summarising ? "Summarising…" : "Summarise this period"}</span>
                            </button>
                        {/if}
                    </div>

                    {#if problem}
                        <div class="ads-problem" role="alert" in:fade={{ duration: ms(160) }}>
                            <strong>{problem.title}</strong>
                            <p class="txt-sm">{problem.text}</p>
                            {#if problem.action === "settings"}
                                <a class="btn sm secondary ads-press" href="{base}/dash/plugins?open=ai-gateway">AI gateway settings</a>
                            {:else if problem.action === "credit"}
                                <a class="btn sm secondary ads-press" href="{base}/dash/ai">AI credit</a>
                            {/if}
                        </div>
                    {/if}

                    {#if summary}
                        {#key summary.id}
                            <div in:fade={{ duration: ms(220) }}>
                                <p class="ads-headline">{summary.headline}</p>
                                <p class="txt-hint txt-sm">
                                    {summary.kind === "weekly" ? "Weekly summary" : "Summary"}, {summary.from} to {summary.to},
                                    by {summary.touch} visit · {formatDate(summary.created_at)}
                                </p>
                                <ul class="ads-points">
                                    {#each summary.points as p, i (i)}
                                        {@const f = figureOf(p.row)}
                                        <li class="ads-point">
                                            <div>
                                                <span class="ads-kind is-{p.kind}">{KIND[p.kind]}</span>
                                                <p>{p.text}</p>
                                            </div>
                                            {#if f}
                                                <dl class="ads-figures" aria-label="The figures for {f.campaign || f.source}">
                                                    <div><dt>Row</dt><dd>{f.source}{f.medium ? ` / ${f.medium}` : ""} · {f.campaign || "(no campaign)"}</dd></div>
                                                    <div><dt>Spend</dt><dd>{f.spend}</dd></div>
                                                    <div><dt>Orders</dt><dd>{f.orders}</dd></div>
                                                    <div><dt>Net sales</dt><dd>{f.net}</dd></div>
                                                    <div><dt>Gross profit</dt><dd>{f.gross_profit}</dd></div>
                                                    <div><dt>ROAS</dt><dd>{f.roas}</dd></div>
                                                    {#each f.products ?? [] as prod (prod.title)}
                                                        {#if prod.out_of_stock}<div><dt>Out of stock</dt><dd>{prod.title}</dd></div>{/if}
                                                    {/each}
                                                </dl>
                                            {/if}
                                        </li>
                                    {/each}
                                </ul>
                            </div>
                        {/key}
                    {:else if !problem}
                        <p class="txt-hint txt-sm">No summary yet.</p>
                    {/if}

                    {#if writable}
                        <label class="ads-switch">
                            <input type="checkbox" checked={weekly} disabled={savingWeekly} onchange={(e) => setWeekly(e.currentTarget.checked, e.currentTarget)} />
                            <span>Make a summary of each week, every Monday</span>
                        </label>
                    {/if}
                </section>

                <section class="recovery-report-card" aria-labelledby="ads-connect-title">
                    <h2 class="recovery-report-title" id="ads-connect-title">Spend from Google Ads and Meta</h2>
                    <p class="txt-hint txt-sm recovery-card-desc">
                        Once connected, each day's spend per campaign is read from the platform every day, as an import would
                        bring it in. Read-only: nothing is changed in your ad accounts. These connections have not yet been tried
                        against a real ad account, so compare the first days with the platform's own figures.
                    </p>
                    <ul class="ads-pace">
                        {#each connectors as c (c.platform)}
                            <li class="ads-pace-row">
                                <div class="ads-pace-head">
                                    <strong>{c.title}</strong>
                                    {#if !c.enabled || c.missing.length}
                                        <span class="ads-kind is-watch">Not connected</span>
                                    {:else if c.last && !c.last.ok}
                                        <span class="ads-kind is-wasting">Last read failed</span>
                                    {:else}
                                        <span class="ads-kind is-working">Connected</span>
                                    {/if}
                                    <span class="flex-fill"></span>
                                    {#if writable && c.enabled && !c.missing.length}
                                        <button type="button" class="btn sm secondary ads-press" class:btn-loading={syncing === c.platform} disabled={!!syncing} onclick={() => syncNow(c)}>Read spend now</button>
                                    {/if}
                                    <a class="btn sm transparent secondary ads-press" href="{base}/dash/plugins?open={c.plugin}">Settings</a>
                                </div>
                                <p class="txt-hint txt-sm">
                                    {#if !c.enabled}
                                        Switch on its plugin and fill in its settings to read spend automatically. Until then it does nothing.
                                    {:else if c.missing.length}
                                        Still needed: {c.missing.join(", ")}. Until then it does nothing.
                                    {:else if c.last}
                                        {formatDate(c.last.at)}: {c.last.message}
                                    {:else}
                                        Not read yet: the first read runs within the hour.
                                    {/if}
                                </p>
                            </li>
                        {/each}
                    </ul>
                </section>

                <section class="recovery-report-card" aria-labelledby="ads-spend-title">
                    <h2 class="recovery-report-title" id="ads-spend-title">Ad spend</h2>
                    <p class="txt-hint txt-sm recovery-card-desc">
                        Spend meets orders by source, medium and campaign: use what your ads put in utm_source, utm_medium and
                        utm_campaign. An ad whose link carries no utm_campaign (Google's auto-tagging alone) is counted under
                        "(no campaign)" and cannot meet its spend. A day entered again replaces the old figure.
                    </p>

                    {#if writable}
                        <div class="ads-forms">
                            <form class="ads-form" onsubmit={importSpend}>
                                <h3 class="ads-form-title">Import a report</h3>
                                <div class="recovery-seg" role="group" aria-label="Platform">
                                    {#each [["google", "Google Ads"], ["meta", "Meta Ads Manager"]] as [value, label] (value)}
                                        <button type="button" class="recovery-seg-btn" aria-pressed={importing.format === value} onclick={() => chooseFormat(value)}>{label}</button>
                                    {/each}
                                </div>
                                <p class="txt-hint txt-sm">
                                    {#if importing.format === "google"}
                                        A campaign report downloaded as CSV with the Day segment (Segment › Time › Day).
                                    {:else}
                                        Ads Manager's table, exported as CSV, at the campaign level; break it down by Day for daily figures.
                                    {/if}
                                </p>
                                <div class="ads-grid">
                                    <label class="field"><span class="txt-sm">utm_source</span><input required bind:value={importing.source} /></label>
                                    <label class="field"><span class="txt-sm">utm_medium</span><input bind:value={importing.medium} /></label>
                                    <label class="field"><span class="txt-sm">File</span>
                                        <input type="file" accept=".csv,.tsv,text/csv,text/plain" required onchange={(e) => (file = e.currentTarget.files?.[0] ?? null)} />
                                    </label>
                                </div>
                                <button type="submit" class="btn sm ads-press" class:btn-loading={busy} disabled={busy}>Import</button>
                            </form>

                            <form class="ads-form" onsubmit={addSpend}>
                                <h3 class="ads-form-title">Enter by hand</h3>
                                <div class="ads-grid">
                                    <label class="field"><span class="txt-sm">utm_source</span><input required placeholder="google" bind:value={entry.source} /></label>
                                    <label class="field"><span class="txt-sm">utm_medium</span><input placeholder="cpc" bind:value={entry.medium} /></label>
                                    <label class="field"><span class="txt-sm">Campaign</span><input required bind:value={entry.campaign} /></label>
                                    <label class="field"><span class="txt-sm">From</span><input type="date" required bind:value={entry.from} /></label>
                                    <label class="field"><span class="txt-sm">To</span><input type="date" bind:value={entry.to} /></label>
                                    <label class="field"><span class="txt-sm">Amount ({storeCurrency})</span><input inputmode="decimal" required bind:value={entry.amount} /></label>
                                </div>
                                <button type="submit" class="btn sm ads-press" class:btn-loading={busy} disabled={busy}>Save spend</button>
                            </form>
                        </div>
                    {/if}

                    {#if spend.length}
                        <div class="page-table-wrapper tw:mt-3 tw:rounded-lg tw:border">
                            <table class="table responsive-table">
                                <caption class="tw:sr-only">Spend by day</caption>
                                <thead class="sticky">
                                    <tr><th>Day</th><th>Source / medium</th><th>Campaign</th><th class="col-field-type-number">Spend</th><th>From</th><th></th></tr>
                                </thead>
                                <tbody>
                                    {#each spend as s (s.id)}
                                        <tr out:fade={{ duration: ms(160) }}>
                                            <td data-name="Day">{s.day}</td>
                                            <td data-name="Source / medium">{s.source}{s.medium ? ` / ${s.medium}` : ""}</td>
                                            <td data-name="Campaign">{s.campaign}</td>
                                            <td class="col-field-type-number" data-name="Spend">{formatMoney(s.amount)}</td>
                                            <td data-name="From" class="txt-hint">{s.origin === "manual" ? "Typed in" : s.origin === "google" ? "Google Ads" : "Meta"}</td>
                                            <td class="col-actions">
                                                {#if writable}
                                                    <button type="button" class="btn circle sm transparent secondary ads-press" aria-label="Delete {s.day} {s.campaign}" title="Delete" onclick={() => removeSpend(s)}>
                                                        <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                                    </button>
                                                {/if}
                                            </td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                    {:else if report}
                        <p class="txt-hint txt-sm tw:mt-3">No spend entered for this period.</p>
                    {/if}
                </section>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <span class="txt txt-hint">
                        Sales placed {win.fromCivil} to {win.toCivil}, credited to the {list.params.touch} visit. Gross profit is
                        net sales less tax and today's product cost; shipping costs are not known.
                    </span>
                </footer>
            {/if}
        </div>
    </div>
{/if}
