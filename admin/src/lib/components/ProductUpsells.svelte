<script>
    /**
     * The product editor's two upsell cards (D133).
     *
     * "Complete the set" is the order bump: other products' variants a shopper
     * ticks beside the buy button, each at an optional offer price the engine
     * honours only while this product is in the same basket. "You may also
     * like" is the products suggested with it, at their own prices.
     *
     * Both are saved by the page's one Save button, like the option draft: the
     * page reads `dirty` and calls `save()`, and Discard calls `reset()`. Two
     * saves on one screen is the mistake the editor already made once.
     */
    import { flip } from "svelte/animate";
    import { api, request } from "$lib/api.js";
    import { formatMoney } from "$lib/format.js";
    import { rise, flipDuration } from "$lib/motion.js";
    import { addOnRows, addOnsBody, offerProblem, moved, rowFromVariant } from "$lib/upsells.js";
    import VariantSearch from "$lib/components/VariantSearch.svelte";
    import TargetPicker from "$lib/components/TargetPicker.svelte";

    let { productId, currency, disabled = false, dirty = $bindable(false) } = $props();

    let rows = $state([]);
    let savedRows = $state("[]");
    let suggested = $state([]);
    let savedSuggested = $state("[]");
    let names = $state(new Map());
    let missing = $state(new Set());
    let loaded = $state(false);

    $effect(() => {
        dirty =
            loaded &&
            (JSON.stringify(rows) !== savedRows || JSON.stringify(suggested) !== savedSuggested);
    });

    $effect(() => {
        if (productId) load(productId);
    });

    async function load(id) {
        loaded = false;
        const [addOns, cross] = await Promise.all([
            api.get(`/api/admin/products/${id}/add-ons`),
            api.get(`/api/admin/products/${id}/cross-sells`),
        ]);
        takeAddOns(addOns);
        takeSuggested(cross);
        loaded = true;
    }

    function takeAddOns(addOns) {
        rows = addOnRows(addOns, currency);
        savedRows = JSON.stringify(rows);
    }

    function takeSuggested(cross) {
        suggested = [...(cross?.product_ids ?? [])];
        savedSuggested = JSON.stringify(suggested);
        names = new Map((cross?.products ?? []).map((p) => [p.id, p.title]));
        // A draft is drawn like a missing one: saved, but not shown to shoppers.
        missing = new Set((cross?.products ?? []).filter((p) => p.status !== "active").map((p) => p.id));
    }

    /** Throws before anything is written when an offer box cannot be read. */
    export async function save() {
        if (!dirty) return;
        const body = addOnsBody(rows, currency);
        const addOns =
            JSON.stringify(rows) !== savedRows
                ? await request("PUT", `/api/admin/products/${productId}/add-ons`, { body })
                : null;
        const cross =
            JSON.stringify(suggested) !== savedSuggested
                ? await request("PUT", `/api/admin/products/${productId}/cross-sells`, {
                      body: { product_ids: [...suggested] },
                  })
                : null;
        if (addOns) takeAddOns(addOns);
        if (cross) takeSuggested(cross);
    }

    /** The first offer box that cannot be saved, as a sentence, or "". */
    export function invalid() {
        try {
            addOnsBody(rows, currency);
            return "";
        } catch (err) {
            return err.message;
        }
    }

    export function reset() {
        rows = JSON.parse(savedRows);
        suggested = JSON.parse(savedSuggested);
    }

    function pick(variant) {
        if (rows.some((r) => r.variant_id === variant.id)) return;
        if (variant.product?.id === productId) return;
        rows = [...rows, rowFromVariant(variant)];
    }

    function remove(i) {
        rows = rows.filter((_, j) => j !== i);
    }
</script>

<section class="card upsells-card">
    <h6 class="section-title">
        <i class="ri-checkbox-multiple-line" aria-hidden="true"></i>
        Complete the set
    </h6>
    <p class="txt-hint txt-sm m-b-sm">
        Products a shopper can tick beside the buy button and add with this one. An offer price
        holds only while this product is in the basket, one for each of it; leave it empty to sell
        the add-on at its own price.
    </p>

    {#if rows.length}
        <ul class="upsell-rows">
            {#each rows as row, i (row.variant_id)}
                {@const problem = offerProblem(row, currency)}
                <li class="upsell-row" animate:flip={{ duration: flipDuration() }} transition:rise>
                    <div class="row-thumb">
                        {#if row.image}
                            <img src={row.image} alt="" loading="lazy" />
                        {:else}
                            <i class="ri-image-line" aria-hidden="true"></i>
                        {/if}
                    </div>
                    <div class="upsell-name">
                        <span class="txt-bold txt-ellipsis">{row.title}</span>
                        <span class="txt-hint txt-sm">
                            {row.label ? row.label + " · " : ""}{formatMoney(row.price)}
                            {#if !row.live}· <span class="txt-danger">not on sale</span>{/if}
                        </span>
                    </div>
                    <div class="field upsell-offer" class:error={problem.error}>
                        <label for="offer-{row.variant_id}" class="sr-only">Offer price for {row.title}</label>
                        <input
                            id="offer-{row.variant_id}"
                            type="text"
                            inputmode="decimal"
                            placeholder="Offer price"
                            {disabled}
                            bind:value={row.offer}
                        />
                    </div>
                    <div class="inline-flex gap-0">
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            disabled={disabled || i === 0}
                            aria-label="Move {row.title} up"
                            onclick={() => (rows = moved(rows, i, -1))}
                        >
                            <i class="ri-arrow-up-line" aria-hidden="true"></i>
                        </button>
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            disabled={disabled || i === rows.length - 1}
                            aria-label="Move {row.title} down"
                            onclick={() => (rows = moved(rows, i, 1))}
                        >
                            <i class="ri-arrow-down-line" aria-hidden="true"></i>
                        </button>
                        <button
                            type="button"
                            class="btn circle sm transparent secondary row-delete"
                            {disabled}
                            aria-label="Remove {row.title}"
                            onclick={() => remove(i)}
                        >
                            <i class="ri-close-line" aria-hidden="true"></i>
                        </button>
                    </div>
                    {#if problem.message}
                        <div class="field-help upsell-help" class:error={problem.error} transition:rise>
                            {problem.message}
                        </div>
                    {/if}
                </li>
            {/each}
        </ul>
    {/if}
    {#if !disabled && rows.length < 10}
        <VariantSearch
            id="add-on-search"
            placeholder="Search a product to offer with this one"
            onpick={pick}
        />
    {/if}
    <div class="field-help">Up to ten. Prices are in {currency}.</div>
</section>

<section class="card">
    <h6 class="section-title">
        <i class="ri-heart-add-line" aria-hidden="true"></i>
        You may also like
    </h6>
    <p class="txt-hint txt-sm m-b-sm">
        Products suggested on this product's page and in a basket that holds it, each with a quick
        Add to cart. With none chosen, the page shows its related products instead.
    </p>
    <label for="cross-sells" class="sr-only">Suggested products</label>
    <TargetPicker kind="products" id="cross-sells" bind:value={suggested} {names} {missing} {disabled} />
</section>

<style>
    .upsell-rows {
        list-style: none;
        margin: 0 0 var(--smSpacing);
        padding: 0;
        display: grid;
        gap: 8px;
    }
    .upsell-row {
        display: grid;
        grid-template-columns: auto minmax(0, 1fr) 8.5rem auto;
        align-items: center;
        gap: 10px;
        padding: 6px 0;
        border-bottom: 1px solid var(--borderColor);
    }
    .upsell-name {
        display: flex;
        flex-direction: column;
        min-width: 0;
    }
    .upsell-offer {
        margin: 0;
    }
    .upsell-offer input {
        transition: box-shadow 0.18s ease;
    }
    .upsell-help {
        grid-column: 2 / -1;
        margin-top: -4px;
    }
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip: rect(0 0 0 0);
        white-space: nowrap;
    }
    @media (max-width: 480px) {
        .upsell-row {
            grid-template-columns: auto minmax(0, 1fr) auto;
        }
        .upsell-offer {
            grid-column: 2 / -1;
            grid-row: 2;
        }
        .upsell-help {
            grid-row: 3;
        }
    }
</style>
