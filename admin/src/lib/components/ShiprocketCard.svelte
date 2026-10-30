<script>
    /**
     * What this order has booked through Shiprocket, and what can be done with
     * each booking: its label, manifest and invoice, its pickup, and calling it
     * off. A booking Shiprocket made that never reached the order — the order
     * was cancelled while it was being booked, say — is listed too, so it can
     * be cancelled rather than left live and billed.
     *
     * A component of its own because the order page is already very long and
     * this is one module's business.
     */
    import { can, request } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import ShiprocketNDR from "$lib/components/ShiprocketNDR.svelte";

    let { order = null, onchanged } = $props();

    let rows = $state([]);
    let working = $state("");
    let confirmOpen = $state(false);
    let cancelling = $state(null);
    const mayFulfill = $derived(can("orders.fulfill"));

    async function load(id) {
        try {
            rows = (await request("GET", `/api/admin/x/fulfill-shiprocket/orders/${id}/shipments`)) ?? [];
        } catch (err) {
            toast.error(err);
        }
    }

    // Again whenever the order changes, since shipping or cancelling a parcel
    // anywhere on the page can add or end a booking.
    $effect(() => {
        if (order?.id) load(order.id);
    });

    async function act(row, what, path, done) {
        working = `${row.id}:${what}`;
        try {
            const updated = await request("POST", `/api/admin/x/fulfill-shiprocket/shipments/${row.id}/${path}`);
            rows = rows.map((r) => (r.id === updated.id ? updated : r));
            toast.success(done);
            return updated;
        } catch (err) {
            toast.error(err);
        } finally {
            working = "";
        }
    }

    async function documentOf(row, kind) {
        const updated = await act(row, kind, `documents/${kind}`, `${kind[0].toUpperCase()}${kind.slice(1)} ready`);
        const url = updated?.[`${kind}_url`];
        if (url) window.open(url, "_blank", "noopener");
    }

    function askCancel(row) {
        cancelling = row;
        confirmOpen = true;
    }

    async function cancel() {
        const row = cancelling;
        if (!row) return;
        const updated = await act(row, "cancel", "cancel", "Cancelled on Shiprocket");
        if (updated) onchanged?.();
    }

    /* Shiprocket's failed-delivery codes: Undelivered, and an issue with the
       recipient. Its docs list no others that an NDR answer applies to. */
    const NDR_CODES = [21, 77];

    async function refresh(row) {
        await act(row, "track", "track", "Tracking refreshed");
        onchanged?.();
    }

    const docs = [
        { kind: "label", label: "Label", icon: "ri-printer-line" },
        { kind: "manifest", label: "Manifest", icon: "ri-file-list-3-line" },
        { kind: "invoice", label: "Invoice", icon: "ri-bill-line" },
    ];
</script>

{#if rows.length}
    <section class="order-card sr-card" in:rise>
        <h6 class="order-card-title m-b-10">Shiprocket</h6>
        <div class="list">
            {#each rows as row (row.id)}
                <div class="list-item sr-row" class:cancelled={!!row.cancelled_at}>
                    <div class="sr-row-head">
                        <span class="label">{row.courier_name || "Courier not assigned"}</span>
                        {#if row.awb}
                            <span class="txt-code txt-sm">{row.awb}</span>
                        {/if}
                        <span class="txt-hint txt-sm">Shiprocket order {row.channel_order_id}</span>
                        {#if row.status && !row.cancelled_at}
                            <span class="label sr-status" class:label-warning={NDR_CODES.includes(row.status_code)}>{row.status}</span>
                        {/if}
                        {#if row.cancelled_at}
                            <span class="label">Cancelled</span>
                        {:else if !row.fulfillment_id}
                            <span class="label label-warning" title="Booked at Shiprocket but not recorded as a shipment of this order">Not on this order</span>
                        {/if}
                        <div class="flex-fill"></div>
                        <span class="txt-hint txt-sm">{formatDate(row.created_at)}</span>
                    </div>
                    {#if !row.cancelled_at}
                        <div class="txt-hint txt-sm">
                            {#if row.pickup_requested}
                                Pickup requested{row.pickup_scheduled ? ` for ${row.pickup_scheduled}` : ""}.
                            {:else if row.awb}
                                No pickup requested yet.
                            {:else}
                                Booking did not finish: no waybill yet. Ship the order again to carry on.
                            {/if}
                            {#if row.etd && row.status_code !== 7}
                                Shiprocket expects delivery by {row.etd}.
                            {/if}
                        </div>
                        {#if NDR_CODES.includes(row.status_code) && mayFulfill}
                            <ShiprocketNDR {row} onanswered={(updated) => (rows = rows.map((r) => (r.id === updated.id ? updated : r)))} />
                        {/if}
                        {#if row.scans?.length}
                            <details class="sr-scans">
                                <summary class="txt-sm">
                                    <i class="ri-arrow-right-s-line sr-scans-chevron" aria-hidden="true"></i>
                                    Tracking: {row.scans.length} update{row.scans.length === 1 ? "" : "s"}, latest {row.scans[0].date}
                                </summary>
                                <ol class="sr-timeline">
                                    {#each row.scans as s, i (i)}
                                        <li class:latest={i === 0}>
                                            <span class="sr-dot" aria-hidden="true"></span>
                                            <div class="sr-scan-text">
                                                <div>{s.activity || s["sr-status-label"]}</div>
                                                <div class="txt-hint txt-sm">{s.date}{s.location ? ` · ${s.location}` : ""}</div>
                                            </div>
                                        </li>
                                    {/each}
                                </ol>
                            </details>
                        {/if}
                        {#if row.awb && mayFulfill}
                            <div class="sr-actions">
                                {#each docs as d (d.kind)}
                                    {#if row[`${d.kind}_url`]}
                                        <a class="btn sm transparent secondary" href={row[`${d.kind}_url`]} target="_blank" rel="noreferrer">
                                            <i class={d.icon} aria-hidden="true"></i>
                                            <span class="txt">{d.label}</span>
                                        </a>
                                    {:else}
                                        <button
                                            type="button"
                                            class="btn sm transparent secondary"
                                            class:loading={working === `${row.id}:${d.kind}`}
                                            disabled={!!working}
                                            onclick={() => documentOf(row, d.kind)}
                                        >
                                            <i class={d.icon} aria-hidden="true"></i>
                                            <span class="txt">{d.label}</span>
                                        </button>
                                    {/if}
                                {/each}
                                <button
                                    type="button"
                                    class="btn sm transparent secondary"
                                    class:loading={working === `${row.id}:track`}
                                    disabled={!!working}
                                    onclick={() => refresh(row)}
                                >
                                    <i class="ri-refresh-line" aria-hidden="true"></i>
                                    <span class="txt">Refresh tracking</span>
                                </button>
                                {#if !row.pickup_requested}
                                    <button
                                        type="button"
                                        class="btn sm transparent secondary"
                                        class:loading={working === `${row.id}:pickup`}
                                        disabled={!!working}
                                        onclick={() => act(row, "pickup", "pickup", "Pickup requested")}
                                    >
                                        <i class="ri-truck-line" aria-hidden="true"></i>
                                        <span class="txt">Request pickup</span>
                                    </button>
                                {/if}
                                <button
                                    type="button"
                                    class="btn sm transparent danger m-l-auto"
                                    class:loading={working === `${row.id}:cancel`}
                                    disabled={!!working}
                                    onclick={() => askCancel(row)}
                                >
                                    <span class="txt">Cancel</span>
                                </button>
                            </div>
                        {:else if mayFulfill && !row.awb}
                            <div class="sr-actions">
                                <button type="button" class="btn sm transparent danger m-l-auto" disabled={!!working} onclick={() => askCancel(row)}>
                                    <span class="txt">Cancel</span>
                                </button>
                            </div>
                        {/if}
                    {/if}
                </div>
            {/each}
        </div>
    </section>
{/if}

<Confirm
    bind:open={confirmOpen}
    title="Cancel this Shiprocket booking?"
    message={cancelling
        ? `Shiprocket order ${cancelling.channel_order_id}${cancelling.awb ? ` and waybill ${cancelling.awb}` : ""} will be cancelled, and what it carried goes back to being owed on this order. Shiprocket only allows this before the parcel is out for pickup.`
        : ""}
    confirmLabel="Cancel booking"
    danger
    onconfirm={cancel}
/>
