<script>
    /**
     * Booking a reverse pickup with Shiprocket: which of what went out is
     * coming back, and why, in Shiprocket's own words. It books the courier
     * and nothing else — what arrives is recorded with the order's Return
     * when it does, because only then can anybody say what goes back on the
     * shelf.
     */
    import { request } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";

    let { order, onbooked, oncancel } = $props();

    /* A short list of Shiprocket's documented return reasons, the ones a
       shop is asked about most; the engine accepts any on Shiprocket's list. */
    const REASONS = [
        "Size not as expected",
        "Does not fit",
        "Item is damaged",
        "Received wrong item",
        "Quality not as expected",
        "Not as described",
        "Changed my mind",
        "Item defective or doesn't work",
        "Other",
    ];

    let lines = $state(
        (order?.line_items ?? [])
            .filter((l) => (l.shipped_quantity ?? 0) > 0)
            .map((l) => ({ id: l.id, title: l.title, sku: l.sku, shipped: l.shipped_quantity, take: 0 })),
    );
    let reason = $state(REASONS[0]);
    let busy = $state(false);

    const units = $derived(lines.reduce((n, l) => n + Math.max(0, Math.min(Number(l.take) || 0, l.shipped)), 0));

    async function book(event) {
        event.preventDefault();
        busy = true;
        try {
            const row = await request("POST", `/api/admin/x/fulfill-shiprocket/orders/${order.id}/returns`, {
                body: {
                    reason,
                    lines: lines
                        .filter((l) => Number(l.take) > 0)
                        .map((l) => ({ order_line_id: l.id, quantity: Math.min(Number(l.take), l.shipped) })),
                },
            });
            toast.success(`Reverse pickup booked, waybill ${row.awb}`);
            onbooked?.(row);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }
</script>

<form class="sr-return" onsubmit={book} in:rise>
    <div class="sr-ndr-title">
        <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
        Reverse pickup
    </div>
    <div class="txt-hint txt-sm">
        A courier collects from {order?.address?.city || "the customer"} and brings it to your pickup address.
        Record what arrives with Return on this order.
    </div>
    {#each lines as line (line.id)}
        <label class="sr-return-line">
            <span class="sr-return-title">
                {line.title}
                <span class="txt-hint txt-sm txt-code">{line.sku}</span>
            </span>
            <input type="number" min="0" max={line.shipped} bind:value={line.take} aria-label="Units of {line.sku} coming back" />
            <span class="txt-hint txt-sm">of {line.shipped}</span>
        </label>
    {/each}
    <label class="sr-ndr-field">
        <span class="txt-hint txt-sm">Reason</span>
        <select bind:value={reason}>
            {#each REASONS as r (r)}
                <option value={r}>{r}</option>
            {/each}
        </select>
    </label>
    <div class="sr-actions">
        <button type="submit" class="btn sm" class:loading={busy} disabled={busy || units === 0}>
            <span class="txt">Book reverse pickup</span>
        </button>
        <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => oncancel?.()}>
            <span class="txt">Not now</span>
        </button>
    </div>
</form>
