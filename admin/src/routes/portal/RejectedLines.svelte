<script>
    /**
     * Lines that did not go into the basket, each with why, in the buyer's
     * words. A repeat order and a quick order both answer with this list.
     */
    import { rejectedWords } from "$lib/trade.svelte.js";

    let { rejected = [] } = $props();
</script>

{#if rejected.length}
    <ul class="portal-rejected" aria-label="Lines that weren't added">
        {#each rejected as r, i (i)}
            <li class="portal-rejected-row">
                <span class="portal-rejected-what">
                    <span class="txt-code txt-bold">{r.sku || `Item ${r.variant_id ?? ""}`}</span>
                    <span class="txt-hint txt-sm">× {r.quantity}</span>
                </span>
                <span class="portal-rejected-why txt-sm">{rejectedWords(r)}</span>
            </li>
        {/each}
    </ul>
{/if}
