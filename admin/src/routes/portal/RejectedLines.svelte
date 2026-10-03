<script>
    /**
     * Lines that did not go into the basket, each with why, in the buyer's
     * words. A repeat order, a quick order and an uploaded file all answer
     * with this list; a file's lines also carry their row, counted as the
     * spreadsheet counts them, so the buyer can find the one to fix.
     */
    import { rejectedWords } from "$lib/trade.svelte.js";

    let { rejected = [] } = $props();
</script>

{#if rejected.length}
    <ul class="portal-rejected" aria-label="Lines that weren't added">
        {#each rejected as r, i (i)}
            <li class="portal-rejected-row">
                <span class="portal-rejected-what">
                    {#if r.row}<span class="label portal-row-no">Row {r.row}</span>{/if}
                    <span class="txt-code txt-bold">{r.sku || (r.variant_id ? `Item ${r.variant_id}` : "No product code")}</span>
                    <!-- A row whose quantity could not be read has none to show. -->
                    {#if r.quantity}<span class="txt-hint txt-sm">× {r.quantity}</span>{/if}
                </span>
                <span class="portal-rejected-why txt-sm">{rejectedWords(r)}</span>
            </li>
        {/each}
    </ul>
{/if}
