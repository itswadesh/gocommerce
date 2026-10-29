<script>
    /**
     * The product editor's Highlights card (D134): up to eight short lines
     * the storefront draws as a ticked list beside the buy box, a free gift
     * note shown on the product page, and the badge on the product's picture
     * (D157). Plain text only; the storefront never reads any as markup.
     *
     * The page owns the values and saves them with the rest of the product's
     * metadata, so this card has no Save of its own.
     */
    import { flip } from "svelte/animate";
    import { rise, flipDuration } from "$lib/motion.js";
    import { MAX_BADGE, MAX_HIGHLIGHTS, MAX_LINE, lineLength, lineProblem, removedRow, typedRows } from "$lib/highlights.js";

    let { rows = $bindable(), freeGift = $bindable(), badge = $bindable(), disabled = false } = $props();

    // Keyed by position: a highlight has no identity of its own, and a key
    // made from its text would remount the input on every keystroke.
    const keyed = $derived(rows.map((text, i) => ({ text, i })));
    const giftProblem = $derived(lineProblem(freeGift));
    const badgeProblem = $derived(lineProblem(badge, MAX_BADGE));
</script>

<section class="card hl-card">
    <h6 class="section-title">
        <i class="ri-list-check-3" aria-hidden="true"></i>
        Highlights
    </h6>
    <p class="txt-hint txt-sm m-b-sm">
        Short lines shown with a tick beside the price, above the description — what a shopper should
        know at a glance. Up to {MAX_HIGHLIGHTS}, {MAX_LINE} characters each.
    </p>
    <ul class="hl-rows">
        {#each keyed as row (row.i)}
            {@const problem = lineProblem(row.text)}
            <li class="hl-row" animate:flip={{ duration: flipDuration }} in:rise>
                <i class="ri-check-line hl-tick" aria-hidden="true"></i>
                <input
                    type="text"
                    class="hl-input"
                    class:invalid={problem}
                    value={row.text}
                    maxlength={MAX_LINE + 20}
                    placeholder={row.i === 0 ? "e.g. Shockproof raised corners" : "Another highlight"}
                    aria-label="Highlight {row.i + 1}"
                    aria-invalid={problem ? "true" : undefined}
                    {disabled}
                    oninput={(e) => (rows = typedRows(rows, row.i, e.currentTarget.value))}
                />
                {#if row.text.trim() !== ""}
                    <button
                        type="button"
                        class="btn circle sm transparent secondary hl-remove"
                        aria-label="Remove highlight {row.i + 1}"
                        {disabled}
                        onclick={() => (rows = removedRow(rows, row.i))}
                    >
                        <i class="ri-close-line" aria-hidden="true"></i>
                    </button>
                {:else}
                    <span class="hl-spacer" aria-hidden="true"></span>
                {/if}
                {#if problem}
                    <div class="field-help error hl-help" transition:rise>{problem}</div>
                {/if}
            </li>
        {/each}
    </ul>

    <div class="field m-t-sm">
        <label for="free-gift">Free gift note</label>
        <input
            id="free-gift"
            type="text"
            bind:value={freeGift}
            maxlength={MAX_LINE + 20}
            placeholder="e.g. Free screen guard with every case"
            aria-invalid={giftProblem ? "true" : undefined}
            aria-describedby="free-gift-help"
            {disabled}
        />
        <div class="field-help" id="free-gift-help" class:error={giftProblem}>
            {giftProblem || "Shown as a badge on the product page. Leave it empty when nothing comes free."}
        </div>
    </div>

    <div class="field m-t-sm">
        <label for="product-badge">Badge</label>
        <input
            id="product-badge"
            type="text"
            bind:value={badge}
            maxlength={MAX_BADGE + 20}
            placeholder="e.g. Oversized fit"
            aria-invalid={badgeProblem ? "true" : undefined}
            aria-describedby="product-badge-help"
            {disabled}
        />
        <div class="field-help hl-help-count" id="product-badge-help" class:error={badgeProblem}>
            <span>
                {badgeProblem || "A short label on the product's picture, such as New or Oversized fit. Leave it empty for none."}
            </span>
            {#if !badgeProblem}
                <span class="hl-count">{lineLength(badge)} of {MAX_BADGE}</span>
            {/if}
        </div>
    </div>
</section>

<style>
    .hl-rows {
        list-style: none;
        margin: 0;
        padding: 0;
        display: grid;
        gap: 6px;
    }
    .hl-row {
        display: grid;
        grid-template-columns: auto minmax(0, 1fr) 32px;
        align-items: center;
        gap: 8px;
    }
    .hl-tick {
        color: var(--successColor);
        font-size: 1.1rem;
    }
    .hl-input {
        transition:
            border-color var(--activeAnimationSpeed) ease-out,
            box-shadow var(--activeAnimationSpeed) ease-out;
    }
    .hl-input.invalid {
        border-color: var(--dangerColor);
    }
    .hl-remove {
        transition: transform var(--activeAnimationSpeed) ease-out;
    }
    .hl-remove:active {
        transform: scale(0.92);
    }
    .hl-help {
        grid-column: 2 / -1;
        margin-top: 0;
    }
    .hl-help-count {
        display: flex;
        gap: 12px;
        justify-content: space-between;
    }
    .hl-count {
        flex-shrink: 0;
        font-variant-numeric: tabular-nums;
    }
    /* 16px text so iOS Safari does not zoom on focus. */
    @media (max-width: 599px) {
        .hl-card input[type="text"] {
            min-height: 44px;
            font-size: 16px;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .hl-input,
        .hl-remove {
            transition: none;
        }
        .hl-remove:active {
            transform: none;
        }
    }
</style>
