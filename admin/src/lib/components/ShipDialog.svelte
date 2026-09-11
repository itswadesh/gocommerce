<script>
    /**
     * Booking a parcel against an order.
     *
     * A popup rather than a drawer: it belongs to the Ship button just pressed,
     * not to the page behind it.
     *
     * It is a component rather than another block in the orders page for the
     * same reason the returns card is: that file is already 1,800 lines and
     * several changes land in it at once.
     *
     * The quantities live here because they are the dialog's own business. The
     * tracking number and the carrier do not: the carrier lookup is shared with
     * the correction dialog on the page, and two implementations of "which
     * carrier issues numbers of this shape" would disagree the day one of them
     * is updated.
     */
    import { untrack } from "svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";

    let {
        open = false,
        order = null,
        busy = false,
        /** Every carrier, suggestions first — the page owns the list. */
        carrierChoices = [],
        /** The carriers this tracking number could belong to, best first. */
        carrierOptions = [],
        /**
         * Who can book the parcel, from `settings.fulfillment_providers` — the
         * one list the engine composes and every client reads.
         *
         * The panel used to post the literal "manual" on every shipment, so a
         * store that installed a carrier module could not book through it from
         * here at all and the module was dead weight. The control appears only
         * when there is a choice to make: on a stock build this list is one
         * entry long, and a select with one option is furniture.
         */
        providerChoices = [],
        tracking = $bindable(""),
        carrier = $bindable(""),
        provider = $bindable(""),
        onlookup,
        onpick,
        onclose,
        onship,
    } = $props();

    /**
     * What is going in the box, prefilled with everything the order still owes
     * — so shipping the whole of what is left is the button the operator was
     * already pressing.
     */
    let lines = $state([]);

    /* Refilled when it opens, and only then. `order` is read untracked because
       the drawer re-reads the order after every action: without that, a reload
       behind an open dialog would silently reset figures somebody had typed. */
    $effect(() => {
        if (open) lines = untrack(() => plan(order));
    });

    function plan(o) {
        return (o?.line_items ?? [])
            .map((l) => ({
                id: l.id,
                sku: l.sku,
                title: l.title,
                variant_label: l.variant_label,
                ordered: l.quantity,
                left: l.quantity - (l.shipped_quantity ?? 0),
            }))
            .filter((l) => l.left > 0)
            .map((l) => ({ ...l, take: l.left }));
    }

    const take = (l) => Math.max(0, Math.min(Number(l.take) || 0, l.left));
    const units = $derived(lines.reduce((n, l) => n + take(l), 0));
    const owed = $derived(lines.reduce((n, l) => n + l.left, 0));
    const whole = $derived(lines.length > 0 && lines.every((l) => take(l) === l.left));

    /* The table is a choice, so it appears only when there is one to make: more
       than one line, or a single line of which part has already gone. */
    const choosable = $derived(lines.length > 1 || lines.some((l) => l.left < l.ordered));

    /* Whether the shipment is being recorded by hand or booked by a module,
       which is what the help text under the form is actually about. `manual` is
       the engine's own provider; anything else came from a module. */
    const manual = $derived(!provider || provider === "manual");

    function submit(event) {
        event?.preventDefault();
        onship?.({
            tracking: tracking.trim(),
            carrier,
            provider,
            /* Only when something is being held back. An untouched dialog sends
               the body this panel has always sent, which is the one the engine
               resolves under the row lock — so a colleague shipping a unit while
               this dialog is open cannot turn an unchanged submit into a 400
               against a stale screen. */
            lines: whole
                ? null
                : lines
                      .filter((l) => take(l) > 0)
                      .map((l) => ({ order_line_id: l.id, quantity: take(l) })),
        });
    }
</script>

<Drawer
    {open}
    size={lines.length > 1 ? "popup" : "popup sm"}
    title={order?.status === "partial" ? "Ship the rest" : "Ship this order"}
    onclose={() => onclose?.()}
>
    <form id="ship-form" onsubmit={submit}>
        {#if providerChoices.length > 1}
            <div class="field">
                <label for="ship-provider">Booked through</label>
                <Select id="ship-provider" bind:value={provider} options={providerChoices} />
            </div>
        {/if}
        <div class="field-help m-b-sm">
            {#if manual}
                The manual provider records what you type — the parcel is booked wherever you
                book parcels, and this is the store's note of it.
            {:else}
                This provider books the shipment itself and fills in the tracking number, the
                carrier and any label it issues. What you type below is only used if it cannot.
            {/if}
        </div>

        {#if choosable}
            <table class="table">
                <thead>
                    <tr>
                        <th></th>
                        <th class="txt-right">Left</th>
                        <th class="txt-right">In this parcel</th>
                    </tr>
                </thead>
                <tbody>
                    {#each lines as line (line.id)}
                        <tr>
                            <td>
                                <div>{line.title}</div>
                                <div class="txt-hint txt-sm txt-code">
                                    {line.sku}{line.variant_label ? " · " + line.variant_label : ""}
                                </div>
                            </td>
                            <td class="txt-right txt-hint txt-sm">{line.left} of {line.ordered}</td>
                            <td class="txt-right">
                                <input
                                    type="number"
                                    class="order-qty"
                                    min="0"
                                    max={line.left}
                                    aria-label="Units of {line.sku} in this parcel"
                                    bind:value={line.take}
                                />
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
            <div class="field-help">
                Everything still owed is filled in. Reduce a figure to hold that line back — the
                order stays open and can ship again.
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="tracking">Tracking number</label>
            <input
                id="tracking"
                type="text"
                bind:value={tracking}
                oninput={() => onlookup?.(tracking)}
                placeholder="Optional"
            />
        </div>

        <!-- The same question the correction dialog asks, asked the same way.
             This is where a shipment is first recorded, so leaving the carrier
             out here meant the only way to name one was to fix it afterwards. -->
        <div class="field m-t-sm">
            <label for="ship-carrier">Carrier</label>
            <Select
                id="ship-carrier"
                bind:value={carrier}
                onchange={() => onpick?.()}
                options={carrierChoices}
            />
        </div>
        <div class="field-help">
            {#if carrierOptions.length === 1}
                That number is {carrierOptions[0].name}'s.
            {:else if carrierOptions.length > 1}
                Several carriers issue numbers of that shape. Pick the right one if the first
                guess is wrong.
            {:else if tracking.trim()}
                No carrier uses numbers of that shape. Pick one if you know who has it.
            {:else}
                The carrier is worked out from the number.
            {/if}
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="ship-form"
            class="btn expanded"
            class:loading={busy}
            disabled={busy || units === 0}
        >
            <span class="txt">{whole ? "Mark shipped" : `Ship ${units} of ${owed}`}</span>
        </button>
    {/snippet}
</Drawer>
