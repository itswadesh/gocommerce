<script>
    /**
     * What has gone back off an order, under the payment state it explains.
     *
     * A component rather than another block in the orders page, for the reason
     * the ship dialog is one: that file is long and several changes land in it.
     *
     * There are no edit or delete buttons here, deliberately. A tracking number
     * is something somebody typed off a screen and can retype; a refund is a
     * movement of money, and the only honest correction to one is another one.
     */
    import { formatMoney, formatDate, relativeTime } from "$lib/format.js";

    let { order = null } = $props();

    /* The same fifteen minutes the engine's doctor warns past and its settle
       route refuses before, so the screen and the engine agree about when a
       refund has stopped being in flight and started being stranded. */
    const STALE_MS = 15 * 60 * 1000;
    const isStale = (at) => Date.now() - new Date(at).getTime() > STALE_MS;

    const refunds = $derived(order?.refunds ?? []);
    const refunded = $derived(order?.refunded?.amount_minor ?? 0);
</script>

{#if refunds.length}
    <div class="list m-t-sm">
        {#each refunds as r (r.id)}
            <div class="list-item">
                <span class="label">{formatMoney(r.amount)}</span>
                {#if r.reason}
                    <span class="txt-hint txt-sm">{r.reason}</span>
                {/if}
                {#if r.provider_reference}
                    <span class="txt-code txt-sm">{r.provider_reference}</span>
                {/if}
                {#if r.status === "pending"}
                    {#if isStale(r.created_at)}
                        <!-- The provider was asked and never answered, so this
                             amount is blocked until somebody says what happened
                             to it. The engine's doctor says the same thing. -->
                        <span class="txt-warning txt-sm">
                            in flight since {relativeTime(r.created_at)} — check the gateway
                        </span>
                    {:else}
                        <span class="txt-hint txt-sm">in flight</span>
                    {/if}
                {:else if r.status === "failed"}
                    <span class="txt-danger txt-sm">{r.error || "declined"}</span>
                {/if}
                <div class="flex-fill"></div>
                {#if r.by}
                    <span class="txt-hint txt-sm">{r.by}</span>
                {/if}
                <span class="txt-hint txt-sm">{formatDate(r.created_at)}</span>
            </div>
        {/each}
    </div>
{:else if refunded > 0}
    <!-- Impossible after the migration that backfilled the ledger, and cheap
         insurance: an order that says money went back must never say nothing
         about it. -->
    <div class="txt-hint txt-sm m-t-sm">
        refunded before this store recorded refunds one at a time
    </div>
{/if}
