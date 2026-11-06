<script>
    /**
     * The shop counter (D211). A scanner types like a keyboard and presses Enter, so
     * this is one box that keeps the focus, and big rows. Totals and prices are the
     * engine's; a sale goes through core's order path. Choosing UPI is not proof of
     * money, so the screen asks separately whether it arrived.
     */
    import { can, retailAssistant } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatMoney } from "$lib/format.js";
    import { basketAdd, basketSet, newKey } from "$lib/retail.js";
    import { t } from "$lib/retail-lang.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { titled } from "$lib/brand.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import RetailLanguage from "$lib/components/RetailLanguage.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";

    const missing = $derived(modulesKnown() && !hasModule("retailassistant"));
    const allowed = $derived(can("retailassistant.counter"));
    const canLink = $derived(can("catalog.write"));
    const currency = $derived(settings.currency);

    let enabled = $state(true);
    let code = $state("");
    let field = $state(null);
    let basket = $state([]);
    let discount = $state("");
    let unknown = $state(null);
    let choices = $state([]);
    let total = $state(null);
    let paying = $state(false);
    let mode = $state("cash");
    let seen = $state(false);
    let reference = $state("");
    let receipt = $state(null);
    let busy = $state(false);
    let key = newKey();

    $effect(() => {
        if (allowed && hasModule("retailassistant")) {
            retailAssistant.status().then((s) => (enabled = s.enabled), (err) => toast.error(err));
        }
    });

    // The next scan is always coming, a new customer's included, and it goes wherever
    // the focus is. A disabled box cannot take the focus, so this waits for busy too.
    $effect(() => {
        if (field && !paying && !unknown && !busy) field.focus();
    });

    const refocus = () => queueMicrotask(() => field?.focus());
    const subtotal = $derived(basket.reduce((sum, l) => sum + l.price.amount_minor * l.quantity, 0));

    function add(item) {
        basket = basketAdd(basket, item);
        total = null;
        paying = false;
        choices = [];
        refocus();
    }

    async function scan(e) {
        e.preventDefault();
        const wanted = code.trim();
        code = "";
        if (!wanted) return;
        receipt = null;
        try {
            const found = await retailAssistant.counterLookup(wanted);
            if (found.length === 1) add(found[0]);
            else if (found.length > 1) choices = found;
            else unknown = wanted;
        } catch (err) {
            toast.error(err);
        }
    }

    function setQuantity(line, quantity) {
        basket = basketSet(basket, line.variant_id, quantity);
        total = null;
        paying = false;
        refocus();
    }

    /** What is unknown is taught to the catalogue, and the sale goes on. */
    async function pickForUnknown(row) {
        const barcode = unknown;
        unknown = null;
        const item = { variant_id: row.id, product_id: row.product?.id, sku: row.sku, barcode, title: row.product?.title ?? row.sku, price: row.price, available: row.track_inventory ? row.available : null };
        add(item);
        if (!canLink) return;
        try {
            await retailAssistant.counterLinkBarcode({ variant_id: row.id, barcode });
            toast.success(t("Barcode saved on this product"));
        } catch (err) {
            toast.error(err);
        }
    }

    const body = () => ({
        lines: basket.map((l) => ({ variant_id: l.variant_id, quantity: l.quantity })),
        discount_bps: Math.round(Number(discount || 0) * 100),
        mode,
        reference: reference.trim(),
        money_seen: seen,
    });

    async function charge() {
        busy = true;
        try {
            total = await retailAssistant.counterPreview(body());
            mode = "cash";
            seen = false;
            reference = "";
            paying = true;
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    async function finish() {
        busy = true;
        try {
            const done = await retailAssistant.counterSale(body(), key);
            receipt = done;
            for (const w of done.warnings) toast.error(new Error(w));
            basket = [];
            discount = "";
            total = null;
            paying = false;
            key = newKey();
        } catch (err) {
            // A press that failed left nothing sold, so the same key may try again.
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    const MODES = [
        { value: "cash", label: "Cash" },
        { value: "upi", label: "UPI" },
        { value: "credit", label: "On credit" },
    ];
</script>

<svelte:head><title>{titled(t("Shop counter"))}</title></svelte:head>

<div class="page page-retail shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">{t("Retail assistant")}</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{t("Shop counter")}</h1>
            </nav>
            <RetailLanguage />
        </header>

        {#if missing}
            <ModuleMissing module="retailassistant" what="The counter is part of ext/retailassistant, and this binary does not include it." />
        {:else if !allowed}
            <NoAccess right="retailassistant.counter" what="the shop counter" />
        {:else if !enabled}
            <EmptyState icon="ri-barcode-line" title={t("The retail assistant is off")} text={t("Switch it on under Supplier bills to use the counter.")} />
        {:else}
            <form class="scan" onsubmit={scan}>
                <label class="tw:sr-only" for="counter-scan">{t("Scan a barcode")}</label>
                <i class="ri-barcode-line" aria-hidden="true"></i>
                <input id="counter-scan" bind:this={field} bind:value={code} type="text" autocomplete="off" inputmode="search" placeholder={t("Scan, or type a SKU")} disabled={busy || paying} />
            </form>

            {#if unknown}
                <div class="box" in:rise>
                    <p class="tw:mb-2"><strong>{t("{code} is not on any product.", { code: unknown })}</strong> {t("Find the product. The barcode is then saved on it.")}</p>
                    <VariantSearch id="counter-unknown" onpick={pickForUnknown} placeholder={t("Search a product name")} />
                    <button type="button" class="btn transparent secondary tw:mt-2" onclick={() => { unknown = null; refocus(); }}><span class="txt">{t("Cancel")}</span></button>
                </div>
            {/if}

            {#if choices.length}
                <div class="box" in:rise>
                    <p class="tw:mb-2"><strong>{t("This code is on more than one product. Which is it?")}</strong></p>
                    {#each choices as c (c.variant_id)}
                        <button type="button" class="btn secondary transparent choice" onclick={() => add(c)}>
                            <span class="txt">{c.title} <span class="txt-code">{c.sku}</span> · {formatMoney(c.price)}</span>
                        </button>
                    {/each}
                </div>
            {/if}

            {#if receipt}
                <div class="box done" in:rise role="status">
                    <p class="done-total">{formatMoney(receipt.total)}</p>
                    <p>
                        <strong>{t("Sale {number}", { number: receipt.number })}</strong>
                        {#if receipt.payment_status === "paid"}<span class="label success">{t("Paid")}</span>{:else}<span class="label warning">{t("Not paid yet")}</span>{/if}
                    </p>
                    <p class="tw:mt-2"><a href={`/dash/orders/${receipt.order_id}`}>{t("Open this sale, to return it")}</a></p>
                </div>
            {/if}

            <ul class="basket">
                {#each basket as line (line.variant_id)}
                    <li in:rise>
                        <div class="line-name">
                            <strong>{line.title}</strong>
                            <span class="txt-hint txt-sm">{line.sku} · {formatMoney(line.price)}</span>
                            {#if line.available != null && line.quantity > line.available}
                                <span class="label warning">{t("Only {n} in stock", { n: line.available })}</span>
                            {/if}
                        </div>
                        <div class="line-qty">
                            <button type="button" class="btn secondary qty" aria-label={t("One less {name}", { name: line.title })} disabled={busy || paying} onclick={() => setQuantity(line, line.quantity - 1)}>−</button>
                            <span class="qty-n" aria-live="polite">{line.quantity}</span>
                            <button type="button" class="btn secondary qty" aria-label={t("One more {name}", { name: line.title })} disabled={busy || paying} onclick={() => setQuantity(line, line.quantity + 1)}>+</button>
                        </div>
                    </li>
                {:else}
                    {#if !receipt}
                        <li class="empty"><EmptyState icon="ri-barcode-line" title={t("Scan the first item")} text={t("A Bluetooth or USB barcode scanner works here. Scanning the same item again adds one more.")} /></li>
                    {/if}
                {/each}
            </ul>

            {#if basket.length}
                <div class="bar" in:rise>
                    {#if !paying}
                        <div class="field discount">
                            <label for="counter-discount">{t("Discount %")}</label>
                            <input id="counter-discount" type="number" min="0" max="100" step="0.5" inputmode="decimal" bind:value={discount} oninput={() => (total = null)} />
                        </div>
                        <div class="sum">
                            <span class="txt-hint txt-sm">{t("Before discount and tax")}</span>
                            <strong>{formatMoney({ amount_minor: subtotal, currency })}</strong>
                        </div>
                        <button type="button" class="btn big" class:loading={busy} disabled={busy} onclick={charge}><span class="txt">{t("Charge")}</span></button>
                    {:else}
                        <div class="pay" in:rise>
                            <p class="pay-total">{formatMoney(total.total)}</p>
                            {#if total.tax?.amount_minor}<p class="txt-hint txt-sm">{t("Includes tax of {amount}", { amount: formatMoney(total.tax) })}</p>{/if}
                            <div class="modes" role="group" aria-label={t("How is this being paid?")}>
                                {#each MODES as m (m.value)}
                                    <button type="button" class="btn big" class:transparent={mode !== m.value} class:secondary={mode !== m.value} aria-pressed={mode === m.value} onclick={() => { mode = m.value; seen = false; }}><span class="txt">{t(m.label)}</span></button>
                                {/each}
                            </div>
                            {#if mode === "upi"}
                                <label class="check" in:rise>
                                    <input type="checkbox" bind:checked={seen} />
                                    <span>{t("I saw this money arrive")}</span>
                                </label>
                                <div class="field" in:rise>
                                    <label for="counter-ref">{t("UTR or reference, if you have it")}</label>
                                    <input id="counter-ref" type="text" autocomplete="off" bind:value={reference} />
                                </div>
                                {#if !seen}<p class="txt-hint txt-sm">{t("Without that, the sale is recorded as not paid yet.")}</p>{/if}
                            {/if}
                            {#if mode === "credit"}
                                <p class="txt-hint txt-sm" in:rise>{t("The sale is recorded as not paid yet.")}</p>
                            {/if}
                            <div class="pay-actions">
                                <button type="button" class="btn transparent secondary" disabled={busy} onclick={() => { paying = false; refocus(); }}><span class="txt">{t("Back")}</span></button>
                                <button type="button" class="btn big" class:loading={busy} disabled={busy} onclick={finish}><span class="txt">{t("Finish sale")}</span></button>
                            </div>
                        </div>
                    {/if}
                </div>
            {/if}
        {/if}
    </div>
</div>

<style>
    .scan {
        position: relative;
        margin: 8px 0 12px;
    }
    .scan i {
        position: absolute;
        left: 14px;
        top: 50%;
        transform: translateY(-50%);
        font-size: 22px;
        opacity: 0.6;
    }
    .scan input {
        width: 100%;
        min-height: 56px;
        padding: 0 14px 0 46px;
        font-size: 18px;
        border: 2px solid var(--color-border, #d4d4d4);
        border-radius: 10px;
        background: var(--color-background, #fff);
        transition: border-color 150ms;
    }
    .scan input:focus {
        outline: none;
        border-color: currentColor;
    }
    .box {
        margin-bottom: 12px;
        padding: 14px;
        border: 1px solid var(--color-border, #e5e5e5);
        border-radius: 10px;
    }
    .done-total {
        font-size: 30px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        margin: 0 0 4px;
    }
    .choice {
        display: block;
        width: 100%;
        min-height: 48px;
        margin-top: 6px;
        text-align: left;
        height: auto;
        white-space: normal;
    }
    .basket {
        list-style: none;
        margin: 0 0 12px;
        padding: 0;
    }
    .basket li:not(.empty) {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 8px 12px;
        padding: 10px 0;
        border-top: 1px solid var(--color-border, #e5e5e5);
    }
    .line-name {
        display: flex;
        flex-direction: column;
        gap: 2px;
        min-width: 0;
        flex: 1 1 11rem;
        overflow-wrap: anywhere;
    }
    .line-qty {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .qty {
        width: 48px;
        height: 48px;
        padding: 0;
        font-size: 22px;
        justify-content: center;
    }
    .qty-n {
        min-width: 2.2ch;
        text-align: center;
        font-size: 20px;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
    }
    .bar {
        position: sticky;
        bottom: 0;
        display: flex;
        flex-wrap: wrap;
        align-items: flex-end;
        gap: 12px;
        padding: 12px 0;
        background: var(--color-background, #fff);
        border-top: 2px solid var(--color-border, #e5e5e5);
    }
    .discount {
        flex: 0 1 7rem;
        margin: 0;
    }
    .sum {
        display: flex;
        flex-direction: column;
        flex: 1 1 7rem;
        font-variant-numeric: tabular-nums;
    }
    .sum strong {
        font-size: 20px;
    }
    .big {
        min-height: 52px;
        font-size: 17px;
    }
    .pay {
        width: 100%;
        display: grid;
        gap: 10px;
    }
    .pay-total {
        font-size: 34px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        margin: 0;
    }
    .modes {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(5.5rem, 1fr));
        gap: 8px;
    }
    .modes .btn {
        justify-content: center;
    }
    .pay-actions {
        display: flex;
        flex-wrap: wrap;
        justify-content: space-between;
        gap: 8px;
    }
    .pay-actions .big {
        flex: 1 1 10rem;
        justify-content: center;
    }
    .check {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        min-height: 44px;
        cursor: pointer;
    }
    @media (prefers-reduced-motion: reduce) {
        .scan input {
            transition: none;
        }
    }
</style>
