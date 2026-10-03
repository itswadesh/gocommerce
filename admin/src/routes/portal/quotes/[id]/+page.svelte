<script>
    /**
     * One quote: what was asked, what the store answered, and — once it is
     * priced — accepting it or declining it.
     *
     * Accepting places an order at the quoted prices (D68), through the same
     * limits a basket meets: over a buyer's approval limit it becomes a request
     * for approval instead, and this page says which happened as plainly as
     * the basket does.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, explain, isApprover, loadMe, refreshBadges, termsWords, word, QUOTE_WORDS } from "$lib/trade.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import PortalPage from "../../PortalPage.svelte";
    import CheckoutDetails from "../../CheckoutDetails.svelte";

    const id = $derived(page.params.id);

    let q = $state(null);
    let loading = $state(true);
    let failure = $state(null);
    let accepting = $state(false);
    let placing = $state(false);
    let placeError = $state("");
    let outcome = $state(null);
    let declineOpen = $state(false);
    let details = $state({ po: "", addressId: null, address: null, name: "", phone: "", method: "", rateId: null, ready: false });

    $effect(() => {
        id;
        load();
    });

    async function load() {
        loading = true;
        failure = null;
        try {
            q = await tradeApi.get(`/x/b2b/quotes/${encodeURIComponent(id)}`);
        } catch (err) {
            if (!err.handled) failure = err;
        } finally {
            loading = false;
        }
    }

    const company = $derived(trade.me.company);
    const status = $derived(q ? word(QUOTE_WORDS, q.status) : null);
    const mine = $derived(q?.requested_by === trade.account?.id);
    const canAct = $derived(q?.status === "quoted" && (mine || isApprover()) && company.status !== "closed");
    const overThreshold = $derived(
        trade.me.role === "buyer" && !!company.approval_threshold && !!q?.total && q.total.amount_minor > company.approval_threshold.amount_minor,
    );

    async function accept(event) {
        event?.preventDefault();
        if (placing || !details.ready) return;
        placing = true;
        placeError = "";
        try {
            const result = await tradeApi.post(`/x/b2b/quotes/${q.id}/accept`, {
                payment_method: details.method,
                po_number: details.po.trim(),
                name: details.name,
                phone: details.phone,
                address: details.address,
            });
            if (result.approval) {
                outcome = { kind: "approval", approval: result.approval };
                refreshBadges();
            } else {
                outcome = { kind: "order", order: result.order };
                loadMe();
            }
            accepting = false;
            await load();
        } catch (err) {
            if (!err.handled) placeError = explain(err);
        } finally {
            placing = false;
        }
    }

    async function decline() {
        try {
            q = await tradeApi.post(`/x/b2b/quotes/${q.id}/decline`, {});
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        }
    }
</script>

<PortalPage title={q ? `Quote ${q.number}` : "Quote"} back={{ href: "/portal/quotes", label: "Quotes" }}>
    {#snippet actions()}
        {#if canAct && !accepting && !outcome}
            <button type="button" class="btn secondary portal-press" onclick={() => (declineOpen = true)}>
                <span class="txt">Decline</span>
            </button>
            <button type="button" class="btn portal-press" onclick={() => (accepting = true)}>
                <i class="ri-check-line" aria-hidden="true"></i>
                <span class="txt">Accept</span>
            </button>
        {/if}
    {/snippet}

    {#if failure}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card portal-notice">
            <i class="ri-file-search-line" aria-hidden="true"></i>
            <h2>{failure.status === 404 ? "This quote isn't one you can see" : "We couldn't open this quote"}</h2>
            <p class="txt-hint">{failure.status === 404 ? "Buyers see the quotes they asked for themselves." : explain(failure)}</p>
            <a href="{base}/portal/quotes" class="b2b-more">All quotes <span aria-hidden="true">→</span></a>
        </div>
    {:else if loading && !q}
        <span class="skeleton-loader"></span>
        <span class="skeleton-loader"></span>
    {:else if q}
        {#if outcome?.kind === "order"}
            <section class="portal-outcome portal-outcome-order" role="status">
                <span class="portal-outcome-icon" aria-hidden="true"><i class="ri-checkbox-circle-fill"></i></span>
                <div class="portal-outcome-body">
                    <h2 class="portal-outcome-title">Accepted — order {outcome.order.number} is placed</h2>
                    <p>
                        {formatMoney(outcome.order.total)} at the quoted prices.
                        {#if outcome.order.payment_provider === "on_account"}It's on {company.name}'s account — {termsWords(company).toLowerCase()}.{/if}
                    </p>
                    <div class="portal-row-actions">
                        <a class="btn secondary portal-press" href="{base}/portal/orders/{outcome.order.id}"><span class="txt">See the order</span></a>
                    </div>
                </div>
            </section>
        {:else if outcome?.kind === "approval"}
            <section class="portal-outcome portal-outcome-approval" role="status">
                <span class="portal-outcome-icon" aria-hidden="true"><i class="ri-hourglass-2-fill"></i></span>
                <div class="portal-outcome-body">
                    <h2 class="portal-outcome-title">Sent for approval — not ordered yet</h2>
                    <p>
                        At {formatMoney(outcome.approval.total)} it's over your approval limit, so an approver decides.
                        <strong>Nothing has been ordered.</strong> The quote is held for them.
                    </p>
                    <div class="portal-row-actions">
                        <a class="btn secondary portal-press" href="{base}/portal/approvals/{outcome.approval.id}"
                            ><span class="txt">See the request</span></a
                        >
                    </div>
                </div>
            </section>
        {:else if q.status === "requested"}
            <div class="alert info m-b-base portal-notice" role="status">
                <p><strong>Waiting for a price.</strong> {trade.store.name || "The store"} will email you when it's ready.</p>
            </div>
        {:else if q.status === "quoted"}
            <div class="alert success m-b-base portal-notice" role="status">
                <p>
                    <strong>Priced and ready to accept</strong>{q.expires_at ? ` until ${formatDate(q.expires_at, { withTime: false })}` : ""}.
                    {#if !canAct}Only the buyer who asked, an approver or an admin can accept it.{/if}
                </p>
            </div>
        {:else if q.status === "awaiting_approval"}
            <div class="alert warning m-b-base portal-notice" role="status">
                <p><strong>Waiting for approval.</strong> It was accepted over the approval limit, so an approver decides.</p>
            </div>
        {:else if q.status === "accepted"}
            <div class="alert success m-b-base portal-notice" role="status">
                <p>
                    <strong>Accepted {formatDate(q.accepted_at)}.</strong>
                    {#if q.order_id}It became order <a href="{base}/portal/orders/{q.order_id}" class="txt-code">{q.order_number}</a>.{/if}
                </p>
            </div>
        {:else if q.status === "expired"}
            <div class="alert warning m-b-base portal-notice" role="status">
                <p><strong>Expired {formatDate(q.expires_at, { withTime: false })}.</strong> Ask again for a fresh price.</p>
            </div>
        {:else}
            <div class="alert m-b-base portal-notice" role="status">
                <p><strong>{status.label}.</strong> It's closed.</p>
            </div>
        {/if}

        <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
            <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                <span class="label {status.tone}">{status.label}</span>
            </div>
            <dl class="b2b-facts">
                <div>
                    <dt>Asked by</dt>
                    <dd>{mine ? "You" : q.requested_by_email}</dd>
                </div>
                <div>
                    <dt>Asked</dt>
                    <dd>{formatDate(q.created_at)}</dd>
                </div>
                {#if q.quoted_at}
                    <div>
                        <dt>Priced</dt>
                        <dd>{formatDate(q.quoted_at)}</dd>
                    </div>
                {/if}
            </dl>
            {#if q.note}
                <h3 class="b2b-eyebrow">{mine ? "What you wrote" : "What they wrote"}</h3>
                <blockquote class="b2b-note">{q.note}</blockquote>
            {/if}
            {#if q.reply}
                <h3 class="b2b-eyebrow">{trade.store.name || "The store"}'s reply</h3>
                <blockquote class="b2b-note">{q.reply}</blockquote>
            {/if}
        </section>

        <h2 class="section-title">
            <i class="ri-list-check-3" aria-hidden="true"></i>
            Lines
        </h2>
        <div class="page-table-wrapper tw:rounded-xl tw:border m-b-sm">
            <table class="table responsive-table">
                <thead>
                    <tr>
                        <th class="col-field-name-id">Item</th>
                        <th class="col-field-type-number min-width">Quantity</th>
                        <th class="col-field-type-number min-width">Price</th>
                        <th class="col-field-type-number min-width">Total</th>
                    </tr>
                </thead>
                <tbody>
                    {#each q.lines as l (l.variant_id)}
                        <tr>
                            <td class="col-field-name-id" data-name="Item">
                                <div class="row-name row-name-stacked">
                                    <span class="txt-bold">{l.title || l.sku}</span>
                                    <span class="txt-hint txt-sm txt-code">{l.sku}</span>
                                </div>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Quantity">{l.quantity}</td>
                            <td class="col-field-type-number min-width" data-name="Price">
                                {#if l.unit_price}{formatMoney(l.unit_price)}{:else}<span class="txt-hint">Not priced yet</span>{/if}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Total">
                                {#if l.unit_price}
                                    {formatMoney({ amount_minor: l.unit_price.amount_minor * l.quantity, currency: l.unit_price.currency })}
                                {:else}
                                    <span class="txt-hint">—</span>
                                {/if}
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
        {#if q.total}
            <div class="b2b-total m-b-base">
                <span class="txt-hint">Total, before delivery and tax</span>
                <strong class="b2b-total-value">{formatMoney(q.total)}</strong>
            </div>
        {/if}

        {#if accepting && canAct}
            <section class="portal-checkout tw:rounded-xl tw:border tw:bg-card portal-notice" aria-labelledby="accept-heading">
                <h2 class="portal-card-title" id="accept-heading">Accept {q.number}</h2>
                <form onsubmit={accept} novalidate>
                    <CheckoutDetails bind:details idPrefix="quote" quoteDelivery={false} />
                    {#if overThreshold}
                        <div class="alert warning portal-notice m-b-sm" role="status">
                            <p>This is over your approval limit of {formatMoney(company.approval_threshold)}, so an approver decides before it's ordered.</p>
                        </div>
                    {/if}
                    {#key placeError}
                        {#if placeError}
                            <div class="alert danger portal-refusal m-b-sm" role="alert"><p>{placeError}</p></div>
                        {/if}
                    {/key}
                    <div class="portal-row-actions">
                        <button type="button" class="btn transparent secondary portal-press" onclick={() => (accepting = false)}>
                            <span class="txt">Not yet</span>
                        </button>
                        <button
                            type="submit"
                            class="btn portal-press"
                            class:warning={overThreshold}
                            class:loading={placing}
                            disabled={placing || !details.ready}
                        >
                            <span class="txt">{overThreshold ? "Send for approval" : "Accept and order"}</span>
                        </button>
                    </div>
                </form>
            </section>
        {/if}
    {/if}
</PortalPage>

<Confirm
    bind:open={declineOpen}
    title="Decline {q?.number ?? 'this quote'}?"
    message="It closes, and can't be accepted later. You can always ask for a new one."
    confirmLabel="Decline"
    danger
    onconfirm={decline}
/>
