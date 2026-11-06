<script>
    /**
     * What the shop's records say today (D206). Every figure comes from the engine,
     * and no model is near them. A claim the records cannot support, such as a
     * product being slow before any sales are recorded, is left out with the reason.
     */
    import { can, retailAssistant } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { titled } from "$lib/brand.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const missing = $derived(modulesKnown() && !hasModule("retailassistant"));
    const readable = $derived(can("retailassistant.read"));

    let enabled = $state(true);
    let data = $state(null);

    $effect(() => {
        if (readable && hasModule("retailassistant")) load();
    });

    async function load() {
        try {
            enabled = (await retailAssistant.status()).enabled;
            if (enabled) data = await retailAssistant.insights();
        } catch (err) {
            toast.error(err);
        }
    }

    const todo = $derived(
        data
            ? [
                  { n: data.ledger.bills_waiting, one: "document waiting for review", many: "documents waiting for review", href: "/dash/retail-assistant" },
                  { n: data.ledger.unverified_count, one: "payment not verified", many: "payments not verified", href: "/dash/retail-assistant/suppliers" },
                  { n: data.ledger.bank_lines_unmatched, one: "bank statement line to match", many: "bank statement lines to match", href: "/dash/retail-assistant/bank" },
                  { n: data.ledger.lines_not_booked_in, one: "bill line not booked into stock", many: "bill lines not booked into stock", href: "/dash/retail-assistant/suppliers" },
              ].filter((x) => x.n > 0)
            : [],
    );
</script>

<svelte:head><title>{titled("Shop insights")}</title></svelte:head>

<div class="page page-retail shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Retail assistant</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Shop insights</h1>
            </nav>
        </header>

        {#if missing}
            <ModuleMissing module="retailassistant" what="Shop insights come from ext/retailassistant, and this binary does not include it." />
        {:else if !readable}
            <NoAccess right="retailassistant.read" what="the store's supplier accounts" />
        {:else if !enabled}
            <EmptyState icon="ri-line-chart-line" title="The retail assistant is off" text="Switch it on under Supplier bills to see this." />
        {:else if data}
            <p class="txt-hint txt-sm tw:mb-4">Worked out from your records on {formatDate(data.as_of)}.</p>

            <dl class="facts">
                <div><dt>You owe suppliers</dt><dd>{formatMoney(data.ledger.outstanding)}</dd></div>
                <div><dt>Bought in the last 30 days</dt><dd>{formatMoney(data.ledger.purchases_30d)}</dd></div>
                <div><dt>Paid in the last 30 days</dt><dd>{formatMoney(data.ledger.paid_30d)}</dd></div>
                {#if data.ledger.unverified.amount_minor}
                    <div><dt>Payments not verified</dt><dd>{formatMoney(data.ledger.unverified)}</dd></div>
                {/if}
            </dl>

            {#if todo.length}
                <h2 class="tw:mt-6 tw:mb-2 tw:text-lg tw:font-semibold">To do</h2>
                <ul class="rows">
                    {#each todo as t (t.one)}
                        <li><a href={t.href}><strong>{t.n}</strong> {t.n === 1 ? t.one : t.many}</a></li>
                    {/each}
                </ul>
            {/if}

            <h2 class="tw:mt-6 tw:mb-2 tw:text-lg tw:font-semibold">Owed most</h2>
            <ul class="rows">
                {#each data.ledger.top_owed as s (s.id)}
                    <li class="row"><span>{s.name}</span><strong>{formatMoney(s.outstanding)}</strong></li>
                {:else}
                    <li class="txt-hint">You owe no supplier anything.</li>
                {/each}
            </ul>

            {#if data.stock}
                <h2 class="tw:mt-6 tw:mb-2 tw:text-lg tw:font-semibold">Stock</h2>
                <dl class="facts">
                    <div><dt>Sales in the last 90 days</dt><dd>{data.stock.sales_90d}</dd></div>
                    <div><dt>Stock at what it cost you</dt><dd>{formatMoney(data.stock.stock_value)}</dd></div>
                </dl>
                <p class="txt-hint txt-sm tw:mt-2">
                    Stock counts are only as good as your sales and goods records.
                    {#if data.stock.items_with_no_cost === 1}1 product has no cost entered, so it is not in that figure.{:else if data.stock.items_with_no_cost}{data.stock.items_with_no_cost} products have no cost entered, so they are not in that figure.{/if}
                </p>

                <h3 class="tw:mt-4 tw:mb-2 tw:font-semibold">Running low</h3>
                <ul class="rows">
                    {#each data.stock.low_stock as i (i.variant_id)}
                        <li class="row"><a href={`/dash/products/${i.product_id}`}>{i.title} <span class="txt-hint txt-sm">{i.sku}</span></a><strong>{i.available} left</strong></li>
                    {:else}
                        <li class="txt-hint">Nothing is running low.</li>
                    {/each}
                </ul>

                <h3 class="tw:mt-4 tw:mb-2 tw:font-semibold">Not sold in 90 days</h3>
                {#if data.stock.slow_withheld}
                    <p class="txt-hint txt-sm">{data.stock.slow_withheld}</p>
                {:else}
                    <ul class="rows">
                        {#each data.stock.slow_stock as i (i.variant_id)}
                            <li class="row"><a href={`/dash/products/${i.product_id}`}>{i.title} <span class="txt-hint txt-sm">{i.sku}</span></a><strong>{i.on_hand} on hand</strong></li>
                        {:else}
                            <li class="txt-hint">Everything in stock has sold in the last 90 days.</li>
                        {/each}
                    </ul>
                {/if}
            {/if}
        {/if}
    </div>
</div>

<style>
    .facts {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
        gap: 12px;
        margin: 0;
    }
    .facts dt {
        font-size: 13px;
        opacity: 0.7;
    }
    .facts dd {
        margin: 2px 0 0;
        font-size: 20px;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
    }
    .rows {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .rows li {
        padding: 10px 0;
        border-top: 1px solid var(--color-border, #e5e5e5);
        overflow-wrap: anywhere;
    }
    .row {
        display: flex;
        flex-wrap: wrap;
        justify-content: space-between;
        gap: 4px 12px;
    }
    .rows a {
        transition: opacity 150ms;
    }
    .rows a:hover {
        opacity: 0.7;
    }
    @media (prefers-reduced-motion: reduce) {
        .rows a {
            transition: none;
        }
    }
</style>
