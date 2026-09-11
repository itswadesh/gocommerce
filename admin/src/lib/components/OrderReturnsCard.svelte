<script>
    /**
     * What has come back off an order, and what the store did with it.
     *
     * A component rather than another block in the orders page, for the reason
     * the ship and refund dialogs are: that file is long and several changes
     * land in it at once.
     *
     * The figure on the right is what the goods were worth, and it is not a
     * refund. Nothing on this card moves money — the return and the refund are
     * separate records on purpose — so the amount is titled rather than styled
     * as a total, and the only button here withdraws a record made in error.
     */
    import { formatMoney, formatDate } from "$lib/format.js";

    let { order = null, onwithdraw } = $props();

    const returns = $derived(order?.returns ?? []);

    /* Which returns actually moved stock, in the words an operator would use.
       A return that put nothing back is the ordinary damaged-goods case, and
       the row has to say so — otherwise the only way to tell is to open the
       return and count. */
    function shelfNote(r) {
        if (!r.units) return "";
        if (r.status === "withdrawn") {
            return r.restocked_units
                ? `${r.restocked_units} taken back off the shelf`
                : "nothing was restocked";
        }
        if (!r.restocked_units) return "nothing restocked";
        if (r.restocked_units === r.units) return `${r.restocked_units} back on sale`;
        return `${r.restocked_units} of ${r.units} back on sale`;
    }
</script>

{#if returns.length}
    <section class="order-card">
        <h6 class="order-card-title m-b-10">Returns</h6>
        <div class="list">
            {#each returns as r (r.id)}
                <div class="list-item return-row">
                    {#each r.lines as l (l.id)}
                        <span class="label">{l.quantity} × {l.sku}</span>
                    {/each}
                    <div class="flex-fill"></div>
                    <!-- A withdrawn return keeps its row rather than
                         disappearing: it is what the order says happened, and
                         the units it moved and moved back are part of that. -->
                    {#if r.status === "withdrawn"}
                        <span class="label">withdrawn</span>
                    {/if}
                    <span
                        class="txt-money txt-sm"
                        title="What the goods were worth. Refunding is a separate action — this moved no money."
                    >
                        {formatMoney(r.refundable)}
                    </span>
                    <span class="txt-hint txt-sm">{formatDate(r.created_at)}</span>
                    {#if r.status === "received"}
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            title="Withdraw this return"
                            aria-label="Withdraw this return"
                            onclick={() => onwithdraw?.(r)}
                        >
                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                        </button>
                    {/if}
                    <!-- Why it came back, under what came back — the row above
                         answers "what", this answers "why", and an operator
                         reading a list of returns asks them in that order. -->
                    <div class="return-note">
                        {#if r.reason}
                            <span>{r.reason}</span>
                        {:else}
                            <span class="txt-hint txt-sm">No reason given</span>
                        {/if}
                        <!-- A withdrawn return can outlive its lines: editing an
                             order deletes an order_line, and the return line
                             that named it goes with it. The row is still the
                             record that something came back. -->
                        {#if shelfNote(r)}
                            <span class="txt-hint txt-sm">· {shelfNote(r)}</span>
                        {/if}
                    </div>
                </div>
            {/each}
        </div>
    </section>
{/if}
