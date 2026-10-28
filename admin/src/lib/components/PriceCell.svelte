<script>
    /**
     * A product row's prices, typed in place on the products list.
     *
     * One price box and one MRP box set every variant; the expander has the
     * same two for each variant. A box saves when it is left or Enter is
     * pressed. The row changes at once and says it is saving; if the engine
     * refuses, it goes back to what it was and says why. Each variant is its
     * own PATCH — the route the product editor already uses — so nothing here
     * is a second write path.
     */
    import { api } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { commonValue, mixed, needsPrice, readMRP, readPrice, withAmount } from "$lib/listprices.js";

    let { product, currency, onupdate } = $props();

    let open = $state(false);
    let phase = $state("idle"); // idle | saving | saved
    let error = $state("");
    let price = $state("");
    let mrp = $state("");
    let timer;

    // What the boxes show follows the row: after a save, after a reload.
    $effect(() => {
        price = commonValue(product.variants, "price", currency);
        mrp = commonValue(product.variants, "compare_at_price", currency);
    });

    const key = { price: "price_minor", compare_at_price: "compare_at_price_minor" };

    /** save writes one amount to the given variants, showing it before the engine answers. */
    async function save(variantIds, field, minor) {
        const before = product.variants;
        const ids = new Set(variantIds);
        const touched = before.filter((v) => ids.has(v.id) && (v[field]?.amount_minor ?? null) !== minor);
        if (!touched.length) return;
        error = "";
        onupdate(before.map((v) => (ids.has(v.id) ? withAmount(v, field, minor, currency) : v)));
        phase = "saving";
        clearTimeout(timer);
        try {
            await Promise.all(touched.map((v) => api.patch(`/api/admin/variants/${v.id}`, { [key[field]]: minor })));
            phase = "saved";
            timer = setTimeout(() => (phase = "idle"), 1400);
        } catch (err) {
            onupdate(before);
            phase = "idle";
            error = err?.message || "Not saved.";
            toast.error(err);
        }
    }

    const all = () => product.variants.map((v) => v.id);

    function commitPrice(text, ids) {
        const r = readPrice(text, currency);
        if (r.empty) return;
        if (r.error) return (error = r.error);
        save(ids, "price", r.minor);
    }

    function commitMRP(text, ids) {
        const prices = product.variants.filter((v) => ids.includes(v.id)).map((v) => v.price?.amount_minor ?? 0);
        const r = readMRP(text, currency, Math.max(...prices));
        if (r.error) return (error = r.error);
        save(ids, "compare_at_price", r.minor);
    }

    const many = $derived(product.variants.length > 1);
    const unpriced = $derived(needsPrice(product));
    const uid = `pc-${product.id}`;
</script>

<!-- The row opens the product on click; typing a price must not. -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="price-cell" data-phase={phase} onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
    <div class="price-row">
        <label class="sr-only" for="{uid}-price">Price of {product.title}</label>
        <input
            id="{uid}-price"
            class="price-input"
            class:invalid={!!error}
            inputmode="decimal"
            autocomplete="off"
            placeholder={mixed(product.variants, "price") && !unpriced ? "Mixed" : "Price"}
            bind:value={price}
            oninput={() => (error = "")}
            onchange={(e) => commitPrice(e.currentTarget.value, all())}
            onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
        />
        <label class="sr-only" for="{uid}-mrp">MRP of {product.title}</label>
        <input
            id="{uid}-mrp"
            class="price-input"
            inputmode="decimal"
            autocomplete="off"
            placeholder={mixed(product.variants, "compare_at_price") ? "Mixed" : "MRP"}
            bind:value={mrp}
            oninput={() => (error = "")}
            onchange={(e) => commitMRP(e.currentTarget.value, all())}
            onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
        />
        <span class="state" aria-live="polite">
            {#if phase === "saving"}
                <i class="ri-loader-4-line spin" aria-hidden="true"></i><span class="sr-only">Saving</span>
            {:else if phase === "saved"}
                <i class="ri-check-line ok" aria-hidden="true"></i><span class="sr-only">Saved</span>
            {/if}
        </span>
        {#if many}
            <button
                type="button"
                class="btn circle sm transparent secondary expand"
                aria-expanded={open}
                aria-controls="{uid}-variants"
                aria-label="{open ? 'Hide' : 'Show'} each variant's price"
                title="Each variant's price"
                onclick={() => (open = !open)}
            >
                <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
            </button>
        {/if}
    </div>
    {#if unpriced}<span class="needs"><span class="dot" aria-hidden="true"></span>Needs a price</span>{/if}
    {#if error}<span class="err" role="alert">{error}</span>{/if}

    {#if open}
        <ul class="variants" id="{uid}-variants">
            {#each product.variants as v (v.id)}
                <li>
                    <span class="vlabel" title={v.sku}>{v.label || v.sku}</span>
                    <input
                        class="price-input"
                        aria-label="Price of {v.label || v.sku}"
                        inputmode="decimal"
                        autocomplete="off"
                        placeholder="Price"
                        value={v.price?.amount_minor ? commonValue([v], "price", currency) : ""}
                        onchange={(e) => commitPrice(e.currentTarget.value, [v.id])}
                        onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
                    />
                    <input
                        class="price-input"
                        aria-label="MRP of {v.label || v.sku}"
                        inputmode="decimal"
                        autocomplete="off"
                        placeholder="MRP"
                        value={commonValue([v], "compare_at_price", currency)}
                        onchange={(e) => commitMRP(e.currentTarget.value, [v.id])}
                        onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
                    />
                </li>
            {/each}
        </ul>
    {/if}
</div>

<style>
    .price-cell {
        display: flex;
        flex-direction: column;
        align-items: flex-end;
        gap: 4px;
        cursor: default;
    }
    .price-row {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .price-input {
        width: 88px;
        height: 32px;
        padding: 0 8px;
        text-align: right;
        font-variant-numeric: tabular-nums;
        border: 1px solid var(--baseAlt2Color, rgba(127, 127, 127, 0.35));
        border-radius: 8px;
        background: var(--baseColor, transparent);
        color: inherit;
        transition:
            border-color 150ms ease,
            box-shadow 150ms ease,
            background-color 200ms ease;
    }
    .price-input:hover {
        border-color: var(--baseAlt4Color, rgba(127, 127, 127, 0.6));
    }
    .price-input:focus-visible {
        outline: none;
        border-color: currentColor;
        box-shadow: 0 0 0 2px var(--baseAlt2Color, rgba(127, 127, 127, 0.3));
    }
    .price-input.invalid {
        border-color: rgb(239 68 68);
    }
    [data-phase="saving"] .price-row .price-input {
        background-color: var(--baseAlt1Color, rgba(127, 127, 127, 0.08));
    }
    .state {
        width: 16px;
        display: inline-grid;
        place-items: center;
    }
    .ok {
        color: rgb(16 185 129);
        animation: pop 200ms ease-out;
    }
    .spin {
        display: inline-block;
        animation: spin 900ms linear infinite;
    }
    .expand i {
        transition: transform 200ms ease;
    }
    .expand[aria-expanded="true"] i {
        transform: rotate(180deg);
    }
    .needs {
        font-size: 12px;
        color: rgb(180 83 9);
        display: inline-flex;
        align-items: center;
        gap: 4px;
    }
    .dot {
        width: 6px;
        height: 6px;
        border-radius: 999px;
        background: rgb(245 158 11);
    }
    .err {
        font-size: 12px;
        color: rgb(220 38 38);
        max-width: 220px;
        text-align: right;
    }
    .variants {
        list-style: none;
        margin: 4px 0 0;
        padding: 0;
        display: grid;
        gap: 4px;
        animation: open 180ms ease-out;
    }
    .variants li {
        display: flex;
        align-items: center;
        justify-content: flex-end;
        gap: 6px;
    }
    .vlabel {
        font-size: 12px;
        max-width: 120px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip: rect(0 0 0 0);
        white-space: nowrap;
    }
    @keyframes spin {
        to {
            transform: rotate(360deg);
        }
    }
    @keyframes pop {
        from {
            transform: scale(0.6);
            opacity: 0;
        }
    }
    @keyframes open {
        from {
            opacity: 0;
            transform: translateY(-4px);
        }
    }
    @keyframes pulse {
        50% {
            opacity: 0.4;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .ok,
        .variants {
            animation: fade 150ms ease-out;
        }
        .expand i {
            transition: none;
        }
        .spin {
            animation: pulse 1.2s ease-in-out infinite;
        }
    }
    @keyframes fade {
        from {
            opacity: 0;
        }
    }
</style>
