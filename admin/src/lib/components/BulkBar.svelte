<script>
    /**
     * The bar that appears when rows are selected.
     *
     * It uses `.bulkbar-wrapper`, the sticky in-page variant PocketBase ships
     * and nothing in this panel had ever used. The bare `.bulkbar` is
     * `position: fixed` and would sit over the `.page-footer` every list screen
     * renders — and now over the Pager in it.
     *
     * It takes no rights. A selection's legal actions differ per screen (orders
     * alone spans orders.write, orders.fulfill and orders.refund), so each
     * action is passed in already wrapped in the screen's own `{#if can(...)}`.
     */
    import { pluralize } from "$lib/format.js";

    let { count = 0, noun = "item", plural = "", onclear, children } = $props();
</script>

{#if count > 0}
    <div class="bulkbar-wrapper">
        <div class="bulkbar" role="group" aria-label="{count} selected">
            <span class="txt">
                {count}
                {pluralize(count, noun, plural)} selected
            </span>

            <div class="flex-fill"></div>

            {@render children?.()}

            <button
                type="button"
                class="btn circle sm transparent secondary"
                aria-label="Clear the selection"
                onclick={() => onclear?.()}
            >
                <i class="ri-close-line" aria-hidden="true"></i>
            </button>
        </div>
    </div>
{/if}
