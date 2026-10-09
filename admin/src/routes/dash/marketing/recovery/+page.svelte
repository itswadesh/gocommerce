<script>
    /**
     * Recovery analytics: is the sequence bringing baskets back, and how much.
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
        rangeWindow,
        rateText,
        shareText,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

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

    const cards = $derived([
        {
            label: "Potential lost revenue",
            value: summary && formatMoney(summary.potential),
            hint: summary ? `${summary.abandoned} ${pluralize(summary.abandoned, "basket")} left behind` : "",
        },
        {
            label: "Recovered revenue",
            value: summary && formatMoney(summary.recovered_revenue),
            hint: summary ? `${formatMoney(summary.recovered_after_message_revenue)} after an email` : "",
        },
        {
            label: "Recovery rate",
            value: summary && rateText(summary.recovery_rate_bp),
            hint: summary ? `${summary.recovered} of ${summary.abandoned} recovered` : "",
        },
        {
            label: "Emails sent",
            value: summary && String(summary.messages_sent),
            hint: summary ? `To ${summary.contacted} ${pluralize(summary.contacted, "basket")}` : "",
        },
        {
            label: "Link clicks",
            value: summary && String(summary.clicked),
            hint: "Baskets whose recovery link was opened",
        },
        {
            label: "Recovered orders",
            value: summary && String(summary.recovered),
            hint: summary ? `${summary.recovered_after_message} after an email` : "",
        },
    ]);

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

<svelte:head><title>Recovery analytics · GoCommerce</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="the recovery figures" />
{:else}
    <div class="page page-recovery-analytics shopify-skin">
        <!-- A document, not one table filling the window, so no full-height:
             the sections take their own height and the page has one scrollbar,
             as on Reports. -->
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Marketing</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Recovery analytics</div>
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
                {#if !missing}
                    <div class="page-header-primary-btns recovery-controls">
                        <div class="field">
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
                        <div class="field">
                            <Select
                                ariaLabel="Type"
                                value={list.params.kind}
                                options={[
                                    { value: "", label: "Checkouts and carts" },
                                    { value: "checkout", label: "Checkouts" },
                                    { value: "cart", label: "Carts" },
                                ]}
                                onchange={(v) => list.set({ kind: v })}
                            />
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="The recovery figures are ext/cart-recovery's, and this binary does not have it."
                />
            {:else}
                <div class="recovery-tiles tw:mb-6 tw:grid tw:grid-cols-2 tw:gap-3 tw:md:grid-cols-3 tw:xl:grid-cols-6">
                    {#each cards as card (card.label)}
                        <div class="recovery-tile tw:flex tw:min-h-[110px] tw:flex-col tw:justify-between tw:rounded-xl tw:border tw:bg-card tw:p-4">
                            <span class="tw:text-xs tw:font-medium tw:tracking-wide tw:text-muted-foreground tw:uppercase">
                                {card.label}
                            </span>
                            <div>
                                {#if loading && !summary}
                                    <div class="tw:h-8 tw:w-24 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                {:else}
                                    {#key card.value}
                                        <div class="recovery-figure tw:text-2xl tw:font-bold tw:tracking-tight tw:tabular-nums tw:sm:text-3xl">
                                            {card.value ?? "—"}
                                        </div>
                                    {/key}
                                {/if}
                                <p class="tw:mt-1 tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                            </div>
                        </div>
                    {/each}
                </div>

                {#if loading && !data}
                    <span class="skeleton-loader" style="height: 180px"></span>
                {:else if data && !summary.abandoned}
                    <div class="recovery-empty-block tw:rounded-xl tw:border tw:bg-card" >
                        <i class="ri-bar-chart-2-line" aria-hidden="true"></i>
                        <p><strong>Nothing was abandoned {windowWords}.</strong></p>
                        <p class="txt-hint">
                            Figures appear here once a basket sits idle past its automation's threshold.
                            <a class="recovery-arrow-link" href="{base}/dash/marketing/automations">
                                See the automations <span aria-hidden="true">→</span>
                            </a>
                        </p>
                    </div>
                {:else if data}
                    <h2 class="tw:mb-3 tw:text-sm tw:font-semibold">Funnel</h2>
                    <div class="recovery-funnel tw:rounded-xl tw:border tw:bg-card" aria-hidden="true">
                        {#each funnel as stage (stage.key)}
                            <div class="recovery-funnel-row">
                                <span class="recovery-funnel-label">{stage.label}</span>
                                <span class="recovery-funnel-track">
                                    <span class="recovery-funnel-fill" style="--w: {stage.ofFirst}"></span>
                                </span>
                                <span class="recovery-funnel-count">{stage.count}</span>
                            </div>
                        {/each}
                    </div>

                    <div class="page-table-wrapper tw:mt-3 tw:rounded-xl tw:border">
                        <table class="table responsive-table">
                            <caption class="tw:sr-only">The recovery funnel</caption>
                            <thead class="sticky">
                                <tr>
                                    <th class="col-field-name-id">Stage</th>
                                    <th class="col-field-type-number min-width">Baskets</th>
                                    <th class="col-field-type-number min-width">Of the stage before</th>
                                    <th class="col-field-type-number min-width">Of abandoned</th>
                                </tr>
                            </thead>
                            <tbody>
                                {#each funnel as stage (stage.key)}
                                    <tr>
                                        <td class="col-field-name-id" data-name="Stage">{stage.label}</td>
                                        <td class="col-field-type-number min-width txt-bold" data-name="Baskets">{stage.count}</td>
                                        <td class="col-field-type-number min-width" data-name="Of the stage before">
                                            {stage.prevCount === null ? "—" : shareText(stage.count, stage.prevCount)}
                                        </td>
                                        <td class="col-field-type-number min-width txt-hint" data-name="Of abandoned">
                                            {shareText(stage.count, funnel[0]?.count)}
                                        </td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                    <p class="tw:mt-2 tw:text-xs tw:text-muted-foreground">
                        Each stage is a part of the one above it. "Recovered orders" here are those bought after a
                        recovery email; a shopper who came back on their own is counted in the figures above and
                        not in the funnel the emails are judged by.
                    </p>

                    <h2 class="tw:mt-8 tw:mb-3 tw:flex tw:flex-wrap tw:items-center tw:gap-2 tw:text-sm tw:font-semibold">
                        Recovered revenue over time
                        <span class="recovery-segmented" role="group" aria-label="Group by">
                            {#each [["day", "Daily"], ["week", "Weekly"], ["month", "Monthly"]] as [value, label] (value)}
                                <button
                                    type="button"
                                    class="btn sm pill"
                                    class:secondary={grain !== value}
                                    aria-pressed={grain === value}
                                    onclick={() => list.set({ group_by: value })}
                                >
                                    {label}
                                </button>
                            {/each}
                        </span>
                    </h2>

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

                    <div class="page-table-wrapper tw:mt-3 tw:rounded-xl tw:border">
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
                        By the {grain} a basket was abandoned, in {data.tz}. Periods with nothing abandoned are drawn
                        as empty columns and left out of the table.
                    </p>

                    <h2 class="tw:mt-8 tw:mb-3 tw:text-sm tw:font-semibold">Carts and checkouts</h2>
                    <div class="page-table-wrapper tw:rounded-xl tw:border">
                        <table class="table responsive-table">
                            <thead class="sticky">
                                <tr>
                                    <th class="col-field-name-id">Type</th>
                                    <th class="col-field-type-number min-width">Abandoned</th>
                                    <th class="col-field-type-number min-width">Recovered</th>
                                    <th class="col-field-type-number min-width">Recovered revenue</th>
                                    <th class="col-field-type-number min-width">Recovery rate</th>
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
                                        <td class="col-field-type-number min-width txt-bold" data-name="Recovered revenue">
                                            {formatMoney(b.recovered_revenue)}
                                        </td>
                                        <td class="col-field-type-number min-width" data-name="Recovery rate">
                                            <span class="recovery-rate">
                                                <span class="recovery-rate-track" aria-hidden="true">
                                                    <span class="recovery-funnel-fill" style="--w: {b.recovery_rate_bp / 10000}"></span>
                                                </span>
                                                {rateText(b.recovery_rate_bp)}
                                            </span>
                                        </td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {/if}

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <span class="txt txt-hint">
                        Baskets abandoned {windowWords}{list.params.kind ? `, ${kindLabel(list.params.kind).toLowerCase()}s only` : ""} ·
                        times in {tz}
                    </span>
                    <div class="flex-fill"></div>
                    <a class="recovery-arrow-link" href="{base}/dash/abandoned">
                        The baskets <span aria-hidden="true">→</span>
                    </a>
                </footer>
            {/if}
        </div>
    </div>
{/if}
