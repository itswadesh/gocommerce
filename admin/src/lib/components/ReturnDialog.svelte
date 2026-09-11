<script>
    /**
     * Recording goods that have come back.
     *
     * A popup rather than a page drawer: it belongs to the button just pressed,
     * which is the rule the ship and refund dialogs already follow. It is a
     * component rather than another block in the orders page because that file
     * is long and several changes land in it at once.
     *
     * Quantities start at zero. The operator has the parcel in front of them
     * and the panel must not guess what is in it, which also makes the number
     * box the inclusion control — there is no second per-line checkbox saying
     * "this line is part of the return".
     *
     * Back on sale starts ticked, which is the opposite of the engine's own
     * default, and deliberately: a client that forgets the field must
     * under-count stock, while a person looking at the goods usually means yes.
     */
    import { untrack } from "svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import { formatMoney } from "$lib/format.js";

    let { open = false, order = null, busy = false, onclose, onsave } = $props();

    let lines = $state([]);
    let reason = $state("");

    /* Refilled when it opens, and only then. `order` is read untracked because
       the drawer re-reads the order after every action: without that, a reload
       behind an open dialog would silently reset figures somebody had typed. */
    $effect(() => {
        if (open) {
            lines = untrack(() => plan(order));
            reason = "";
        }
    });

    function plan(o) {
        /* Only what has actually gone out can come back, which on a partly
           shipped order is less than what was sold — the same cap the engine
           applies, so the box does not offer a figure the save would refuse.
           Only on `partial`: a shipped or delivered order has all of it out by
           definition, and an order imported without its parcels would otherwise
           show nothing as returnable. */
        const partly = o?.status === "partial";
        return (o?.line_items ?? []).map((l) => {
            const out = partly ? (l.shipped_quantity ?? 0) : l.quantity;
            const back = l.returned_quantity ?? 0;
            return {
                line_id: l.id,
                sku: l.sku,
                title: l.title,
                variant_label: l.variant_label,
                unit_price: l.unit_price,
                sold: l.quantity,
                out,
                back,
                left: Math.max(0, out - back),
                quantity: 0,
                restock: true,
                /* A line whose variant has been deleted has no shelf to go
                   back on, which is what the engine records too. */
                restockable: l.variant_id != null,
            };
        });
    }

    const take = (l) => Math.max(0, Math.min(Number(l.quantity) || 0, l.left));
    const units = $derived(lines.reduce((n, l) => n + take(l), 0));
    const value = $derived(
        lines.reduce((n, l) => n + (l.unit_price?.amount_minor ?? 0) * take(l), 0),
    );
    const restocking = $derived(
        lines.reduce((n, l) => n + (l.restock && l.restockable ? take(l) : 0), 0),
    );

    const currency = $derived(order?.currency);
    const asMoney = (minor) => formatMoney({ amount_minor: minor, currency });

    function submit(event) {
        event?.preventDefault();
        if (units === 0) return;
        onsave?.({
            reason: reason.trim(),
            lines: lines
                .filter((l) => take(l) > 0)
                .map((l) => ({
                    line_id: l.line_id,
                    quantity: take(l),
                    restock: l.restock && l.restockable,
                })),
        });
    }
</script>

<Drawer {open} size="popup lg" title="Record a return" onclose={() => onclose?.()}>
    <form id="return-form" onsubmit={submit}>
        <div class="field-help m-b-sm">
            Only what has already gone out can come back. Units you send back on sale go to the
            shelf this order took them from.
        </div>

        <table class="table return-table">
            <thead>
                <tr>
                    <th></th>
                    <th class="txt-right">Coming back</th>
                    <!-- Left, not right: the cell under it is a checkbox,
                         and a heading that does not sit over its control reads
                         as belonging to the column beside it. -->
                    <th>Back on sale</th>
                    <th class="txt-right">Value</th>
                </tr>
            </thead>
            <tbody>
                {#each lines as line (line.line_id)}
                    <tr>
                        <td>
                            <div>{line.title}</div>
                            <div class="txt-hint txt-sm txt-code">
                                {line.sku}{line.variant_label ? " · " + line.variant_label : ""}
                            </div>
                            <div class="txt-hint txt-sm">
                                {line.sold} sold{line.out < line.sold
                                    ? ` · ${line.out} gone out`
                                    : ""}{line.back ? ` · ${line.back} already back` : ""}
                            </div>
                        </td>
                        <td class="txt-right">
                            {#if line.left === 0}
                                <span class="txt-hint txt-sm">all back</span>
                            {:else}
                                <input
                                    type="number"
                                    class="order-qty"
                                    min="0"
                                    max={line.left}
                                    aria-label="Units of {line.sku} coming back"
                                    bind:value={line.quantity}
                                />
                            {/if}
                        </td>
                        <td>
                            {#if !line.restockable}
                                <span class="txt-hint txt-sm">no longer in the catalog</span>
                            {:else if line.left === 0}
                                <span class="txt-hint txt-sm">—</span>
                            {:else}
                                <div class="field">
                                    <input
                                        type="checkbox"
                                        id="restock-{line.line_id}"
                                        bind:checked={line.restock}
                                    />
                                    <label for="restock-{line.line_id}">Restock</label>
                                </div>
                            {/if}
                        </td>
                        <td class="txt-right">
                            {asMoney((line.unit_price?.amount_minor ?? 0) * take(line))}
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>

        <div class="field m-t-sm">
            <label for="return-reason">Reason</label>
            <input
                id="return-reason"
                type="text"
                bind:value={reason}
                placeholder="Arrived damaged"
            />
        </div>
        <div class="field-help">
            The reason goes to the customer with the return, so it is what they said rather than a
            remark about them.
        </div>

        <div class="flex m-t-sm">
            <span class="txt-hint txt-sm">
                {units} item{units === 1 ? "" : "s"} · {restocking} going back on the shelf
            </span>
            <div class="flex-fill"></div>
            <span class="txt-money">{asMoney(value)}</span>
        </div>

        <div class="field-help m-t-sm">
            Recording a return does not move the money. Refunding is its own action, so it is
            recorded as one.
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="return-form"
            class="btn expanded"
            class:loading={busy}
            disabled={busy || units === 0}
        >
            <span class="txt">Record return</span>
        </button>
    {/snippet}
</Drawer>
