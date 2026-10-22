<script>
    /**
     * Cart recovery: is the sequence bringing baskets back, and how much. Laid
     * out as kitcommerce-admin's report is — six figures, the funnel, recovered
     * revenue over time, and the split by type — with each chart's table kept
     * under it for anybody who cannot see the bars.
     *
     * One request — `GET …/analytics` — answers the whole screen, and every
     * figure in it is counted by PostgreSQL over the window: the summary, a
     * funnel in which each stage is a subset of the one before, the trend by
     * period of abandonment, and the split between carts and checkouts. The
     * screen draws them and adds nothing up.
     *
     * Periods are cut in the reader's own zone (the browser's, sent as `tz`),
     * so a basket left at 23:30 is counted on the day it was left here. The
     * chart is the Reports screen's — a row of CSS bars, no charting library —
     * and, as there, the figure is hidden from assistive technology because the
     * table under it carries every number in the same order.
     */
    import { base } from "$app/paths";
    import { can, recovery } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatMoney, pluralize } from "$lib/format.js";
    import {
        RANGES,
        fillTrend,
        funnelRows,
        kindLabel,
        periodLabel,
        rangeLabel,
        rangeWindow,
        rateText,
        shareText,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

    /* A report somebody can send: the window, the kind and the grain are in
       the address. Thirty days by default, which is what "how is it doing"
       usually means; the range key is stored rather than two dates, so a
       bookmark of "last 30 days" stays the last thirty days. */
    const list = listState({ range: "30d", from: "", to: "", kind: "", group_by: "day" });

    const readable = $derived(can("abandonment.read"));
    /* Trusted only where the store could have been asked: the probe is made for
       an operator holding this right or orders.read (modules.svelte.js), and
       for anybody else the rights answer below is the true one. */
    const missing = $derived(
        modulesKnown() && !hasModule("cart-recovery") && (readable || can("orders.read")),
    );

    let data = $state(null);
    let loading = $state(true);
    let reqId = 0;

    const win = $derived(rangeWindow(list.params.range, list.params.from, list.params.to));
    const grain = $derived(list.params.group_by);

    $effect(() => {
        list.params;
        load();
    });

    async function load() {
        if (!readable || !hasModule("cart-recovery")) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        const p = list.params;
        const w = rangeWindow(p.range, p.from, p.to);
        try {
            const next = await recovery.analytics({
                from: w.from,
                to: w.to,
                kind: p.kind,
                group_by: p.group_by,
                tz,
            });
            if (mine !== reqId) return;
            data = next;
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

    const summary = $derived(data?.summary ?? null);
    const currency = $derived(summary?.potential?.currency ?? "USD");

    const count = (n) => new Intl.NumberFormat().format(n ?? 0);
    /* The noun follows the Type switch: with Checkouts chosen, "12 carts" would
       be the wrong word for what is counted. */
    const noun = $derived(list.params.kind === "checkout" ? "checkout" : "cart");
    const tracked = $derived(data?.tracked !== false);

    /* "Messages sent", not kitcommerce-admin's "delivered": the engine knows a
       message left, not that it arrived. */
    const cards = $derived([
        {
            label: "Potential lost revenue",
            icon: "ri-cash-line",
            value: summary && formatMoney(summary.potential),
            hint: summary ? `${count(summary.abandoned)} abandoned` : "",
        },
        {
            label: "Recovered revenue",
            icon: "ri-wallet-3-line",
            success: true,
            value: summary && formatMoney(summary.recovered_revenue),
            hint: summary ? `${formatMoney(summary.recovered_after_message_revenue)} after a message` : "",
        },
        {
            label: "Recovery rate",
            icon: "ri-percent-line",
            value: summary && rateText(summary.recovery_rate_bp),
            hint: "Recovered ÷ abandoned",
        },
        {
            label: "Messages sent",
            icon: "ri-mail-check-line",
            value: summary && count(summary.messages_sent),
            hint: summary ? `to ${count(summary.contacted)} ${pluralize(summary.contacted, noun)}` : "",
        },
        /* Carts whose link was opened, not clicks; and nothing at all when the
           store cannot count them, rather than a zero nobody measured. A link
           copied by hand counts too, so this is not a share of the contacted. */
        {
            label: "Clicked through",
            icon: "ri-cursor-line",
            value: summary && (tracked ? count(summary.clicked) : "Not counted"),
            hint: tracked ? `${noun === "checkout" ? "Checkouts" : "Carts"} whose recovery link was opened` : "No public address is set",
        },
        {
            label: "Recovered orders",
            icon: "ri-checkbox-circle-line",
            success: true,
            value: summary && count(summary.recovered),
            hint: summary ? `${count(summary.recovered_after_message)} after a message` : "",
        },
    ]);

    /* The period's recovered revenue and orders, for the chart card's headline. */
    const recoveredTotal = $derived(summary ? formatMoney(summary.recovered_revenue) : "");

    const KINDS = [
        { value: "", label: "All" },
        { value: "checkout", label: "Checkouts" },
        { value: "cart", label: "Carts" },
    ];
    const GRAINS = [
        ["day", "Daily"],
        ["week", "Weekly"],
        ["month", "Monthly"],
    ];

    const funnel = $derived(funnelRows(data?.funnel));

    /* The chart's series, with the quiet periods put back so neighbouring bars
       are neighbouring periods. The table below lists only periods with
       something in them. */
    const series = $derived(
        data ? fillTrend(data.trend, grain, currency, win.fromCivil, win.toCivil) : [],
    );
    const peak = $derived(
        series.reduce(
            (top, p) => Math.max(top, p.potential?.amount_minor ?? 0, p.recovered_revenue?.amount_minor ?? 0),
            0,
        ),
    );

    /** A column is as tall as the larger of the two figures it carries. */
    function column(p) {
        const left = p.potential?.amount_minor ?? 0;
        const back = p.recovered_revenue?.amount_minor ?? 0;
        const top = Math.max(left, back);
        return {
            h: peak ? (top / peak) * 100 : 0,
            seg: top ? (back / top) * 100 : 0,
        };
    }

    function tooltip(p) {
        return [
            periodLabel(p.period, grain, true),
            `${p.abandoned} abandoned · ${formatMoney(p.potential)}`,
            `${p.recovered} recovered · ${formatMoney(p.recovered_revenue)}`,
        ].join("\n");
    }

    const breakdown = $derived(data?.breakdown ?? []);
    const windowWords = $derived.by(() => {
        if (!win.fromCivil) return "since recovery was set up";
        const f = (c) => periodLabel(c, "day", true);
        return win.fromCivil === win.toCivil ? `on ${f(win.fromCivil)}` : `from ${f(win.fromCivil)} to ${f(win.toCivil)}`;
    });
</script>

<svelte:head><title>{titled("Cart recovery")}</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="the recovery figures" />
{:else}
    <div class="page page-recovery-analytics shopify-skin">
        <!-- A document, not one table filling the window, so no full-height:
             the sections take their own height and the page has one scrollbar,
             as on Reports. -->
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <div class="recovery-title-block">
                    <h1 class="recovery-h1">Cart recovery</h1>
                    <p class="txt-hint txt-sm">{rangeLabel(list.params.range, list.params.from, list.params.to)}, in {tz}</p>
                </div>
                <div class="flex-fill"></div>
                {#if !missing}
                    <div class="page-header-primary-btns">
                        <a class="btn sm secondary" href="{base}/dash/abandoned">
                            <i class="ri-inbox-line" aria-hidden="true"></i>
                            <span class="txt">Abandoned checkouts</span>
                        </a>
                        <a class="btn sm secondary" href="{base}/dash/marketing/automations">
                            <i class="ri-flow-chart" aria-hidden="true"></i>
                            <span class="txt">Automations</span>
                        </a>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="The recovery figures are ext/cart-recovery's, and this binary does not have it."
                />
            {:else}
                <div class="recovery-filters recovery-report-controls" role="group" aria-label="Filters">
                    <div class="recovery-seg" role="group" aria-label="Type">
                        {#each KINDS as k (k.value)}
                            <button
                                type="button"
                                class="recovery-seg-btn"
                                aria-pressed={list.params.kind === k.value}
                                onclick={() => list.set({ kind: k.value })}
                            >
                                {k.label}
                            </button>
                        {/each}
                    </div>
                    <div class="field recovery-select">
                        <Select ariaLabel="Window" value={list.params.range} options={RANGES} onchange={chooseRange} />
                    </div>
                    {#if list.params.range === "custom"}
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
                                aria-label="To, inclusive"
                                value={list.params.to}
                                onchange={(e) => list.set({ to: e.currentTarget.value })}
                            />
                        </div>
                    {/if}
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

                <div class="recovery-tiles tw:mb-4 tw:grid tw:grid-cols-2 tw:gap-3 tw:md:grid-cols-3 tw:xl:grid-cols-6">
                    {#each cards as card (card.label)}
                        <div class="recovery-tile tw:flex tw:min-h-24 tw:flex-col tw:justify-between tw:rounded-lg tw:border tw:bg-card tw:p-3 tw:sm:p-4">
                            <span class="recovery-tile-label" class:is-success={card.success}>
                                <i class={card.icon} aria-hidden="true"></i>
                                {card.label}
                            </span>
                            <div>
                                {#if loading && !summary}
                                    <div class="tw:h-7 tw:w-20 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                {:else}
                                    {#key card.value}
                                        <div class="recovery-figure tw:text-xl tw:font-semibold tw:tracking-tight tw:tabular-nums tw:sm:text-2xl">
                                            {card.value ?? "—"}
                                        </div>
                                    {/key}
                                    <p class="tw:mt-1 tw:truncate tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                                {/if}
                            </div>
                        </div>
                    {/each}
                </div>

                {#if loading && !data}
                    <span class="skeleton-loader" style="height: 180px"></span>
                {:else if data && !summary.abandoned}
                    <div class="recovery-empty-block recovery-report-card">
                        <i class="ri-bar-chart-2-line" aria-hidden="true"></i>
                        <p><strong>No abandoned {noun}s in this period</strong></p>
                        <p class="txt-hint">
                            Figures appear here once a {noun} sits idle past its automation's threshold.
                            <a class="recovery-arrow-link" href="{base}/dash/marketing/automations">
                                See the automations <span aria-hidden="true">→</span>
                            </a>
                        </p>
                    </div>
                {:else if data}
                    <section class="recovery-report-card">
                        <h2 class="recovery-report-title">Recovery funnel</h2>
                        <p class="txt-hint txt-sm recovery-card-desc">Bars against all abandoned; arrows step to step</p>
                        <div class="recovery-funnel-bars" aria-hidden="true">
                            {#each funnel as stage, i (stage.key)}
                                <!-- A share of nothing is not a figure: after an empty
                                     stage the arrow stays and the percentage does not. -->
                                {#if i > 0}
                                    <div class="recovery-funnel-step txt-hint txt-sm">
                                        ↓{stage.prevCount && stage.count <= stage.prevCount
                                            ? ` ${shareText(stage.count, stage.prevCount)} of the step before`
                                            : ""}
                                    </div>
                                {/if}
                                <div class="recovery-funnel-row">
                                    <span class="recovery-funnel-label">{stage.label}</span>
                                    <span class="recovery-funnel-track">
                                        <span
                                            class="recovery-funnel-fill"
                                            class:is-last={i === funnel.length - 1}
                                            style="--w: {stage.ofFirst}"
                                        ></span>
                                    </span>
                                    <span class="recovery-funnel-count">{count(stage.count)}</span>
                                </div>
                            {/each}
                        </div>
                        <table class="tw:sr-only">
                            <caption>The recovery funnel</caption>
                            <thead>
                                <tr><th>Stage</th><th>Carts</th><th>Of the stage before</th><th>Of abandoned</th></tr>
                            </thead>
                            <tbody>
                                {#each funnel as stage (stage.key)}
                                    <tr>
                                        <td>{stage.label}</td>
                                        <td>{stage.count}</td>
                                        <td>{stage.prevCount === null ? "—" : shareText(stage.count, stage.prevCount)}</td>
                                        <td>{shareText(stage.count, funnel[0]?.count)}</td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                        <p class="tw:mt-3 tw:text-xs tw:text-muted-foreground">
                            Each stage is a part of the one above it. "Recovered orders" here are those bought after a
                            recovery message; a shopper who came back on their own is counted in the figures above and
                            not in the funnel the messages are judged by.
                        </p>
                    </section>

                    <section class="recovery-report-card">
                        <div class="recovery-card-head">
                            <div>
                                <h2 class="recovery-report-title">Recovered revenue</h2>
                                <p class="txt-hint txt-sm recovery-card-desc">In {data.tz}</p>
                            </div>
                            <div class="recovery-seg" role="group" aria-label="Group by">
                                {#each GRAINS as [value, label] (value)}
                                    <button
                                        type="button"
                                        class="recovery-seg-btn"
                                        aria-pressed={grain === value}
                                        onclick={() => list.set({ group_by: value })}
                                    >
                                        {label}
                                    </button>
                                {/each}
                            </div>
                        </div>
                        <div class="recovery-report-headline">
                            {#key recoveredTotal}
                                <span class="recovery-figure tw:text-2xl tw:font-semibold tw:tabular-nums">{recoveredTotal}</span>
                            {/key}
                            <span class="txt-hint txt-sm">from {count(summary.recovered)} recovered {pluralize(summary.recovered, "order")}</span>
                        </div>

                        <div class="report-legend">
                            <span><i class="report-key recovery-key-back" aria-hidden="true"></i> Recovered revenue</span>
                            <span><i class="report-key" aria-hidden="true"></i> Value abandoned</span>
                        </div>
                        <!-- The column's height is a scale rather than a height, so a
                             change of window or type moves the bars on the
                             compositor instead of re-laying the row out each frame. -->
                        <figure class="report-chart recovery-chart" aria-hidden="true">
                            {#each series as p (p.period)}
                                {@const c = column(p)}
                                <div class="report-bar recovery-bar" style="--k: {c.h / 100}" title={tooltip(p)}>
                                    <span class="report-seg recovery-seg-back" style="--seg: {c.seg}%"></span>
                                </div>
                            {/each}
                        </figure>
                        {#if series.length}
                            <div class="report-axis">
                                <span>{periodLabel(series[0].period, grain)}</span>
                                <span>{periodLabel(series[series.length - 1].period, grain)}</span>
                            </div>
                        {/if}

                        <div class="page-table-wrapper tw:mt-3 tw:rounded-lg tw:border">
                            <table class="table responsive-table">
                                <caption class="tw:sr-only">Abandoned and recovered, by {grain}</caption>
                                <thead class="sticky">
                                    <tr>
                                        <th class="col-field-name-id">Period</th>
                                        <th class="col-field-type-number min-width">Abandoned</th>
                                        <th class="col-field-type-number min-width">Value abandoned</th>
                                        <th class="col-field-type-number min-width">Recovered</th>
                                        <th class="col-field-type-number min-width">Recovered revenue</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each data.trend as p (p.period)}
                                        <tr>
                                            <td class="col-field-name-id" data-name="Period">{periodLabel(p.period, grain, true)}</td>
                                            <td class="col-field-type-number min-width" data-name="Abandoned">{p.abandoned}</td>
                                            <td class="col-field-type-number min-width txt-hint" data-name="Value abandoned">
                                                {formatMoney(p.potential)}
                                            </td>
                                            <td class="col-field-type-number min-width" data-name="Recovered">{p.recovered}</td>
                                            <td class="col-field-type-number min-width txt-bold" data-name="Recovered revenue">
                                                {formatMoney(p.recovered_revenue)}
                                            </td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                        <p class="tw:mt-2 tw:text-xs tw:text-muted-foreground">
                            By the {grain} a {noun} was abandoned. Periods with nothing abandoned are drawn as empty columns
                            and left out of the table.
                        </p>
                    </section>

                    <section class="recovery-report-card">
                        <h2 class="recovery-report-title">By type</h2>
                        <div class="page-table-wrapper tw:mt-2 tw:rounded-lg tw:border">
                            <table class="table responsive-table">
                                <thead class="sticky">
                                    <tr>
                                        <th class="col-field-name-id">Type</th>
                                        <th class="col-field-type-number min-width">Abandoned</th>
                                        <th class="col-field-type-number min-width">Recovered</th>
                                        <th class="col-field-type-number min-width">Rate</th>
                                        <th class="col-field-type-number min-width">Revenue</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each breakdown as b (b.key)}
                                        <tr>
                                            <td class="col-field-name-id" data-name="Type">
                                                {kindLabel(b.key)}
                                                <div class="txt-hint txt-sm">
                                                    {b.key === "checkout" ? "Reached checkout" : "Left before checkout"}
                                                </div>
                                            </td>
                                            <td class="col-field-type-number min-width" data-name="Abandoned">{b.abandoned}</td>
                                            <td class="col-field-type-number min-width" data-name="Recovered">{b.recovered}</td>
                                            <td class="col-field-type-number min-width" data-name="Rate">
                                                <span class="recovery-rate">
                                                    <span class="recovery-rate-track" aria-hidden="true">
                                                        <span class="recovery-funnel-fill" style="--w: {b.recovery_rate_bp / 10000}"></span>
                                                    </span>
                                                    {rateText(b.recovery_rate_bp)}
                                                </span>
                                            </td>
                                            <td class="col-field-type-number min-width txt-bold" data-name="Revenue">
                                                {formatMoney(b.recovered_revenue)}
                                            </td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                    </section>
                {/if}

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <span class="txt txt-hint">
                        {list.params.kind ? `${kindLabel(list.params.kind)}s` : "Carts and checkouts"} abandoned {windowWords} ·
                        times in {tz}
                    </span>
                    <div class="flex-fill"></div>
                    <a class="recovery-arrow-link" href="{base}/dash/abandoned">
                        The carts <span aria-hidden="true">→</span>
                    </a>
                </footer>
            {/if}
        </div>
    </div>
{/if}
