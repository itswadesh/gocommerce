<script>
    /**
     * One request for approval. An approver or admin approves it — which
     * places the order there and then, at the prices in the request — or
     * turns it down with a reason the buyer reads. The buyer who asked can
     * withdraw it while it waits.
     *
     * Approving can fail after the click: a line sold out since, or the credit
     * used up by another order. The request goes back to waiting with the
     * reason, which this page shows, so the approver can try again or turn it
     * down.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { trade, tradeApi, explain, isApprover, loadMe, refreshBadges, word, APPROVAL_WORDS, sentence } from "$lib/trade.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import PromptDialog from "$lib/components/PromptDialog.svelte";
    import PortalPage from "../../PortalPage.svelte";

    const id = $derived(page.params.id);

    let a = $state(null);
    let loading = $state(true);
    let failure = $state(null);
    let actionError = $state("");
    let approveOpen = $state(false);
    let rejectOpen = $state(false);
    let withdrawOpen = $state(false);
    let busy = $state(false);
    let placed = $state(null);

    $effect(() => {
        id;
        load();
    });

    async function load() {
        loading = true;
        failure = null;
        try {
            a = await tradeApi.get(`/x/b2b/approvals/${encodeURIComponent(id)}`);
        } catch (err) {
            if (!err.handled) failure = err;
        } finally {
            loading = false;
        }
    }

    const mine = $derived(a?.requested_by === trade.account?.id);
    const waiting = $derived(a?.status === "pending");
    const canDecide = $derived(waiting && isApprover());
    const status = $derived(a ? word(APPROVAL_WORDS, a.status) : null);

    async function approve() {
        actionError = "";
        try {
            const result = await tradeApi.post(`/x/b2b/approvals/${a.id}/approve`, {});
            placed = result.order;
            a = result.approval ?? a;
            refreshBadges();
            loadMe();
        } catch (err) {
            if (!err.handled) actionError = explain(err);
            await load();
        }
    }

    async function reject(reason) {
        busy = true;
        actionError = "";
        try {
            a = await tradeApi.post(`/x/b2b/approvals/${a.id}/reject`, reason ? { reason } : {});
            rejectOpen = false;
            refreshBadges();
        } catch (err) {
            if (!err.handled) actionError = explain(err);
        } finally {
            busy = false;
        }
    }

    async function withdraw() {
        actionError = "";
        try {
            a = await tradeApi.post(`/x/b2b/approvals/${a.id}/cancel`, {});
        } catch (err) {
            if (!err.handled) actionError = explain(err);
        }
    }
</script>

<PortalPage title={a ? (mine ? "Your request" : `Request from ${a.requested_by_email || "a former buyer"}`) : "Request"} back={{ href: "/portal/approvals", label: "Approvals" }}>
    {#snippet actions()}
        {#if canDecide}
            <button type="button" class="btn secondary portal-press" onclick={() => (rejectOpen = true)}>
                <i class="ri-close-circle-line" aria-hidden="true"></i>
                <span class="txt">Turn down</span>
            </button>
            <button type="button" class="btn success portal-press" onclick={() => (approveOpen = true)}>
                <i class="ri-check-line" aria-hidden="true"></i>
                <span class="txt">Approve and order</span>
            </button>
        {:else if waiting && mine}
            <button type="button" class="btn secondary portal-press" onclick={() => (withdrawOpen = true)}>
                <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                <span class="txt">Withdraw</span>
            </button>
        {/if}
    {/snippet}

    {#if failure}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card portal-notice">
            <i class="ri-file-search-line" aria-hidden="true"></i>
            <h2>{failure.status === 404 ? "This request isn't one you can see" : "We couldn't open this request"}</h2>
            <p class="txt-hint">{failure.status === 404 ? "Buyers see the requests they made themselves." : explain(failure)}</p>
            <a href="{base}/portal/approvals" class="b2b-more">All requests <span aria-hidden="true">→</span></a>
        </div>
    {:else if loading && !a}
        <span class="skeleton-loader"></span>
        <span class="skeleton-loader"></span>
    {:else if a}
        {#if placed || (a.status === "approved" && a.order_id)}
            <section class="portal-outcome portal-outcome-order" role="status">
                <span class="portal-outcome-icon" aria-hidden="true"><i class="ri-checkbox-circle-fill"></i></span>
                <div class="portal-outcome-body">
                    <h2 class="portal-outcome-title">Approved — order {placed?.number ?? a.order_number} is placed</h2>
                    <p>Approved {formatDate(a.decided_at)} at the prices in the request.</p>
                    <div class="portal-row-actions">
                        <a class="btn secondary portal-press" href="{base}/portal/orders/{placed?.id ?? a.order_id}"
                            ><span class="txt">See the order</span></a
                        >
                    </div>
                </div>
            </section>
        {:else if waiting}
            <section class="portal-outcome portal-outcome-approval" role="status">
                <span class="portal-outcome-icon" aria-hidden="true"><i class="ri-hourglass-2-fill"></i></span>
                <div class="portal-outcome-body">
                    <h2 class="portal-outcome-title">Waiting for approval — not ordered yet</h2>
                    <p>
                        {#if canDecide}
                            It's over {trade.me.company.name}'s approval limit of {formatMoney(trade.me.company.approval_threshold)},
                            so it needs you. Approving places the order.
                        {:else}
                            An approver at {trade.me.company.name} decides it. Nothing is ordered until they do, and you'll
                            get an email either way.
                        {/if}
                    </p>
                    {#if a.last_error}
                        <p class="txt-danger m-0">The last try to place it failed: {sentence(a.last_error)}</p>
                    {/if}
                </div>
            </section>
        {:else if a.status === "rejected"}
            <div class="alert danger m-b-base portal-notice" role="status">
                <p>
                    <strong>Turned down {formatDate(a.decided_at)}.</strong>
                    {a.reason ? `“${a.reason}”` : "No reason was given."} Nothing was ordered.
                </p>
            </div>
        {:else if a.status === "cancelled"}
            <div class="alert m-b-base portal-notice" role="status">
                <p><strong>Withdrawn.</strong> Nothing was ordered.</p>
            </div>
        {:else if a.status === "placing"}
            <div class="alert info m-b-base portal-notice" role="status">
                <p><strong>Being placed now.</strong> Refresh in a moment to see the order.</p>
            </div>
        {/if}

        {#key actionError}
            {#if actionError}
                <div class="alert danger m-b-base portal-refusal" role="alert"><p>{actionError}</p></div>
            {/if}
        {/key}

        <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
            <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                <span class="label {status.tone}">{status.label}</span>
                <span class="txt-hint txt-sm">{a.kind === "quote" ? "Accepting a quote" : "From a basket"}</span>
            </div>
            <dl class="b2b-facts">
                <div>
                    <dt>Asked by</dt>
                    <dd>{mine ? "You" : a.requested_by_email || "A former buyer"}</dd>
                </div>
                <div>
                    <dt>Asked</dt>
                    <dd>{formatDate(a.created_at)}</dd>
                </div>
                <div>
                    <dt>PO number</dt>
                    <dd>{#if a.po_number}<span class="txt-code">{a.po_number}</span>{:else}—{/if}</dd>
                </div>
                <div>
                    <dt>Payment</dt>
                    <dd>{a.payment_method === "on_account" ? "On account" : a.payment_method}</dd>
                </div>
                {#if a.quote_id}
                    <div>
                        <dt>Quote</dt>
                        <dd><a href="{base}/portal/quotes/{a.quote_id}">See the quote</a></dd>
                    </div>
                {/if}
            </dl>
        </section>

        <h2 class="section-title">
            <i class="ri-list-check-3" aria-hidden="true"></i>
            What's in it
        </h2>
        <div class="page-table-wrapper tw:rounded-xl tw:border m-b-sm">
            <table class="table responsive-table">
                <thead>
                    <tr>
                        <th class="col-field-name-id">Product code</th>
                        <th class="col-field-type-number min-width">Quantity</th>
                        <th class="col-field-type-number min-width">Price</th>
                        <th class="col-field-type-number min-width">Total</th>
                    </tr>
                </thead>
                <tbody>
                    {#each a.lines as l (l.variant_id)}
                        <tr>
                            <td class="col-field-name-id" data-name="Product code"><span class="txt-code txt-bold">{l.sku}</span></td>
                            <td class="col-field-type-number min-width" data-name="Quantity">{l.quantity}</td>
                            <td class="col-field-type-number min-width" data-name="Price">{formatMoney(l.unit_price)}</td>
                            <td class="col-field-type-number min-width" data-name="Total">
                                {formatMoney({ amount_minor: l.unit_price.amount_minor * l.quantity, currency: l.unit_price.currency })}
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
        <dl class="portal-sums m-b-base">
            <div><dt>Lines</dt><dd>{formatMoney(a.subtotal)}</dd></div>
            <div class="portal-sums-total"><dt>Total</dt><dd>{formatMoney(a.total)}</dd></div>
        </dl>
        <p class="txt-hint txt-sm portal-right">Delivery and tax are worked out again when it's placed.</p>
    {/if}
</PortalPage>

<Confirm
    bind:open={approveOpen}
    title="Approve and place this order?"
    message={a ? `It's ordered straight away for ${trade.me.company.name}, at ${formatMoney(a.total)}${a.payment_method === "on_account" ? ", on account" : ""}.` : ""}
    confirmLabel="Approve and order"
    onconfirm={approve}
/>

<Confirm
    bind:open={withdrawOpen}
    title="Withdraw this request?"
    message="Nobody will be asked to approve it, and nothing is ordered. Your basket is as you left it."
    confirmLabel="Withdraw"
    danger
    onconfirm={withdraw}
/>

<PromptDialog
    open={rejectOpen}
    title="Turn down this request?"
    message="Nothing is ordered. The buyer is emailed, with your reason."
    label="Reason"
    placeholder="Over budget this month — split it in two"
    help="The buyer reads this."
    confirmLabel="Turn down"
    danger
    {busy}
    onclose={() => (rejectOpen = false)}
    onconfirm={reject}
/>
