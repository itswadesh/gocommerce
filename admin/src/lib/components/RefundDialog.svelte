<script>
    /**
     * Sending money back off an order.
     *
     * A popup rather than the shared Confirm: that component's props are
     * exactly open/title/message/confirmLabel/danger/onconfirm, and it renders a
     * heading and a hint. Widening it would put a form into a component ten
     * destructive actions share, so this follows the pattern the orders page
     * already uses for an action that needs a field — the ship dialog.
     *
     * It is a component rather than another block in the orders page because
     * that file is already 1,900 lines and several changes land in it at once.
     *
     * The amount is parsed with the order's own currency, never with a bare
     * parseFloat: the engine speaks minor units plus a code, and a reader that
     * formats has to read back in the same terms.
     */
    import { untrack } from "svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import { formatMoney, fromMinor, toMinor } from "$lib/format.js";

    let {
        open = false,
        order = null,
        busy = false,
        /** What the money would go back through, as a person would say it. */
        methodName = "",
        onclose,
        onrefund,
    } = $props();

    let amount = $state("");
    let reason = $state("");

    /* Refilled when it opens, and only then. `order` is read untracked because
       the drawer re-reads the order after every action: without that, a reload
       behind an open dialog would silently reset a figure somebody had typed. */
    $effect(() => {
        if (open) {
            const o = untrack(() => order);
            amount = fromMinor(remainingOf(o), o?.currency);
            reason = "";
        }
    });

    function remainingOf(o) {
        return (o?.total?.amount_minor ?? 0) - (o?.refunded?.amount_minor ?? 0);
    }

    const currency = $derived(order?.currency);
    const refunded = $derived(order?.refunded?.amount_minor ?? 0);
    const remaining = $derived(remainingOf(order));
    /* null when what was typed is not a number at all, which is why every guard
       below compares against a number rather than asking isNaN of a string. */
    const minor = $derived(currency ? toMinor(amount, currency) : null);

    const error = $derived(
        !amount.trim()
            ? ""
            : minor === null
              ? "That is not an amount."
              : minor <= 0
                ? "Enter an amount above zero."
                : minor > remaining
                  ? `Only ${formatMoney({ amount_minor: remaining, currency })} is still refundable.`
                  : "",
    );

    /* The last thing read before money leaves should be how much. */
    const label = $derived(
        minor === null || minor <= 0
            ? "Refund"
            : `Refund ${formatMoney({ amount_minor: minor, currency })}`,
    );

    function submit(event) {
        event?.preventDefault();
        if (error || minor === null || minor <= 0) return;
        onrefund?.({ amount_minor: minor, reason: reason.trim() });
    }
</script>

<Drawer {open} size="popup sm" title="Refund this order" onclose={() => onclose?.()}>
    <form id="refund-form" onsubmit={submit}>
        <div class="field-help m-b-sm">
            The money goes back through {methodName || "the method that took it"}. Cash on delivery
            cannot refund and will say so. This moves money only — if the goods came back, record a
            return, which is what puts them on the shelf.
        </div>

        <div class="field required" class:error={!!error}>
            <label for="refund-amount">Amount ({currency})</label>
            <!-- Text with a decimal keypad, never type="number": the panel parses
                 what a person typed in their own locale, and a number input
                 hands back whatever the browser made of a comma. -->
            <input
                id="refund-amount"
                type="text"
                inputmode="decimal"
                autocomplete="off"
                bind:value={amount}
            />
        </div>
        {#if error}
            <div class="field-help error">{error}</div>
        {:else}
            <div class="field-help">
                {formatMoney({ amount_minor: remaining, currency })} of {formatMoney(order?.total)}
                is still refundable.{#if refunded}
                    {" "}{formatMoney(order?.refunded)} has already gone back.{/if}
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="refund-reason">Reason</label>
            <input
                id="refund-reason"
                type="text"
                bind:value={reason}
                placeholder="Damaged in transit"
            />
        </div>
        <div class="field-help">
            Recorded on the order and passed to whatever tells the customer. Optional.
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="refund-form"
            class="btn danger expanded"
            class:loading={busy}
            disabled={busy || !!error || minor === null || minor <= 0}
        >
            <span class="txt">{label}</span>
        </button>
    {/snippet}
</Drawer>
