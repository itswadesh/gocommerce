<script module>
    /*
     * What each entry is called on screen, and what it is drawn with.
     *
     * Two tables because an entry can arrive through either half of the merge:
     * most things an order does announce an event, and a few — a note, a
     * shipment corrected, a refund settled by hand — are recorded only as
     * something somebody did. A name in neither table still renders, under its
     * own raw name, so a verb added to the engine later appears here as a plain
     * line rather than disappearing.
     */
    const EVENTS = {
        "order.created": { icon: "ri-shopping-bag-3-line", label: "Order placed" },
        "order.paid": { icon: "ri-money-dollar-circle-line", label: "Payment recorded" },
        "order.unpaid": { icon: "ri-arrow-go-back-line", label: "Payment taken back" },
        "order.shipped": { icon: "ri-truck-line", label: "Shipped" },
        "order.unshipped": { icon: "ri-arrow-go-back-line", label: "Shipment removed" },
        "order.delivered": { icon: "ri-checkbox-circle-line", label: "Delivered" },
        "order.undelivered": { icon: "ri-arrow-go-back-line", label: "Delivery undone" },
        "order.cancelled": { icon: "ri-close-circle-line", label: "Cancelled" },
        "order.refunded": { icon: "ri-refund-2-line", label: "Money refunded" },
        "order.returned": { icon: "ri-inbox-unarchive-line", label: "Goods returned" },
        "order.unreturned": { icon: "ri-arrow-go-back-line", label: "Return withdrawn" },
    };

    const ACTIONS = {
        "order.note": { icon: "ri-sticky-note-line", label: "Note written" },
        "order.refund_settle": { icon: "ri-refund-2-line", label: "Refund settled by hand" },
        "order.shipment_update": { icon: "ri-truck-line", label: "Tracking corrected" },
        "order.shipment_delete": { icon: "ri-truck-line", label: "Shipment removed" },
        "order.mark_failed": { icon: "ri-error-warning-line", label: "Payment failed" },
    };

    /* Status and payment status are already said by the phrase above them, and
       a note's own text is rendered instead of the column it lives in. */
    const IMPLIED = new Set(["status", "payment_status", "metadata"]);

    /* Column names an operator did not choose. Only the ones that do not read
       as English on their own: everything else loses its underscore and is
       already the word somebody would say. */
    const FIELD_NAMES = {
        payment_provider: "payment method",
        payment_reference: "payment reference",
        label_url: "label",
        tracking: "tracking number",
    };
    const fieldName = (f) => FIELD_NAMES[f] ?? f.replace(/_/g, " ");
</script>

<script>
    /**
     * One order's history: what happened, who did it, and — for the things that
     * told the customer something — whether that actually went out.
     *
     * It is one list from two sources, merged by the engine rather than here: a
     * transition an operator caused writes its event and its audit record in
     * the same transaction, and deciding in the browser which of the two rows
     * was the same moment would be a heuristic over timestamps.
     *
     * Nothing here is clickable. A history is a report, and a row that reads as
     * pressable and does nothing is worse than a plain one.
     */
    import { formatDate, relativeTime, summarizeEdit } from "$lib/format.js";

    let { entries = [], loading = false, currency = "USD" } = $props();

    function look(entry) {
        if (entry.name && EVENTS[entry.name]) return EVENTS[entry.name];
        // order.edited is deliberately not in that table: it is two different
        // things under one name, and the change block is what tells them apart.
        if (entry.name === "order.edited") {
            return {
                icon: "ri-pencil-line",
                label: entry.change ? "Items changed" : "Details corrected",
            };
        }
        if (entry.action && ACTIONS[entry.action]) return ACTIONS[entry.action];
        return {
            icon: "ri-information-line",
            label: entry.summary || entry.name || entry.action || "Something happened",
        };
    }

    /** The one line under the phrase: what this entry actually says. */
    function detail(entry) {
        if (entry.action === "order.note") {
            const note = entry.after?.metadata?.notes;
            return typeof note === "string" && note ? note : "Note cleared";
        }
        if (entry.change) return summarizeEdit(entry.change, currency).join(" · ");
        if (entry.reason) return entry.reason;
        if (entry.tracking) return `Tracking ${entry.tracking}`;
        // Which fields a correction moved. The values are not shown — an
        // address is four lines and a history is a list — but knowing that the
        // email was the thing that changed is most of the answer.
        const fields = Object.keys(entry.after ?? {})
            .filter((f) => !IMPLIED.has(f))
            .map(fieldName);
        if (fields.length) return `Changed ${fields.sort().join(", ")}`;
        return "";
    }

    /** Who did it, in the words the trail actually has. */
    function who(entry) {
        if (entry.actor_email) return entry.actor_email;
        if (entry.actor_label) return entry.actor_label;
        if (entry.actor_kind === "token") return "an admin token";
        if (entry.actor_kind === "system") return "the store itself";
        return "";
    }
</script>

<section class="order-card">
    <h6 class="order-card-title m-b-10">History</h6>

    <div class="list order-history">
        {#each entries as entry, i (entry.at + (entry.action || "") + (entry.name || "") + i)}
            {@const shape = look(entry)}
            {@const text = detail(entry)}
            {@const actor = who(entry)}
            <div class="list-item">
                <i class={shape.icon} aria-hidden="true"></i>
                <div class="order-item-text">
                    {shape.label}
                    {#if text || actor}
                        <div class="txt-hint txt-sm">
                            {text}{#if text && actor}&nbsp;·&nbsp;{/if}{#if actor}by {actor}{/if}
                        </div>
                    {/if}
                </div>
                <div class="flex-fill"></div>

                <!--
                    Delivery, and only where there is something to deliver: an
                    act that announced nothing has no queue state to report, and
                    a missing published_at on one of those would read as a
                    notification that never went out.

                    Silent in the normal case. `last_error` is simply absent for
                    a reader without store.operate, so the tooltip is omitted
                    rather than empty.
                -->
                {#if entry.name}
                    {#if entry.dead}
                        <span class="label danger" title={entry.last_error || undefined}>
                            not delivered
                        </span>
                    {:else if !entry.published_at}
                        <span class="label" title={entry.last_error || undefined}>queued</span>
                    {/if}
                {/if}

                <span class="txt-hint txt-sm order-history-when" title={formatDate(entry.at)}>
                    {relativeTime(entry.at)}
                </span>
            </div>
        {/each}

        {#if loading && !entries.length}
            {#each Array(3) as _, i (i)}
                <div class="list-item"><span class="skeleton-loader"></span></div>
            {/each}
        {/if}

        {#if !loading && !entries.length}
            <p class="txt-hint txt-sm">Nothing recorded yet.</p>
        {/if}
    </div>
</section>
