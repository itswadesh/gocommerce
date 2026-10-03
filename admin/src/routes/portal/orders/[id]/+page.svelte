<script>
    /**
     * One order: what was in it, where it went, how far it has got and where
     * it stands on the account (GET /x/b2b/orders/{id}). Repeat puts its lines
     * in the basket at today's prices.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, explain, orderWords, repeatOrder, isApprover } from "$lib/trade.svelte.js";
    import PortalPage from "../../PortalPage.svelte";
    import RejectedLines from "../../RejectedLines.svelte";

    const id = $derived(page.params.id);

    let entry = $state(null);
    let loading = $state(true);
    let failure = $state(null);
    let repeating = $state(false);
    let repeated = $state(null);

    $effect(() => {
        id;
        load();
    });

    async function load() {
        loading = true;
        failure = null;
        try {
            entry = await tradeApi.get(`/x/b2b/orders/${encodeURIComponent(id)}`);
        } catch (err) {
            if (!err.handled) failure = err;
        } finally {
            loading = false;
        }
    }

    async function repeat() {
        repeating = true;
        try {
            const fill = await repeatOrder(entry.order_id);
            repeated = fill.rejected ?? [];
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        } finally {
            repeating = false;
        }
    }

    const o = $derived(entry?.order);
    const w = $derived(entry ? orderWords(entry) : null);
    const place = (a) => [a?.line1, a?.line2, a?.city, a?.state, a?.postal_code, a?.country].filter(Boolean).join(", ");
</script>

<PortalPage title={entry ? `Order ${entry.number}` : "Order"} back={{ href: "/portal/orders", label: "Orders" }}>
    {#snippet actions()}
        {#if entry && trade.me.company.status !== "closed"}
            <button type="button" class="btn portal-press" class:loading={repeating} disabled={repeating} onclick={repeat}>
                <i class="ri-repeat-line" aria-hidden="true"></i>
                <span class="txt">Repeat this order</span>
            </button>
        {/if}
    {/snippet}

    {#if failure}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card portal-notice">
            <i class="ri-file-search-line" aria-hidden="true"></i>
            {#if failure.status === 404}
                <h2>This order isn't one you can see</h2>
                <p class="txt-hint">
                    {isApprover() ? "It isn't one of your company's orders." : "Buyers see the orders they placed themselves."}
                </p>
            {:else}
                <h2>We couldn't open this order</h2>
                <p class="txt-hint">{explain(failure)}</p>
                <button type="button" class="btn secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
            {/if}
            <a href="{base}/portal/orders" class="b2b-more">All orders <span aria-hidden="true">→</span></a>
        </div>
    {:else if loading && !entry}
        <span class="skeleton-loader"></span>
        <span class="skeleton-loader"></span>
        <span class="skeleton-loader"></span>
    {:else if entry}
        {#if repeated}
            <div class="alert {repeated.length ? 'warning' : 'success'} m-b-base portal-notice portal-result" role="status">
                <p>
                    <strong>Its lines are in your basket</strong>{repeated.length
                        ? `, except ${repeated.length === 1 ? "one" : repeated.length}:`
                        : ", at today's prices."}
                    <a href="{base}/portal/basket" class="portal-inline-link">Go to your basket →</a>
                </p>
                <RejectedLines rejected={repeated} />
            </div>
        {/if}

        <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
            <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                <span class="label {w.tone}">{w.status}</span>
                <span class="label {w.payment.tone}">{w.payment.label}</span>
            </div>
            <dl class="b2b-facts">
                <div>
                    <dt>Placed</dt>
                    <dd>{formatDate(entry.created_at)}</dd>
                </div>
                <div>
                    <dt>By</dt>
                    <dd>{entry.placed_by || "—"}</dd>
                </div>
                <div>
                    <dt>PO number</dt>
                    <dd>{#if entry.po_number}<span class="txt-code">{entry.po_number}</span>{:else}—{/if}</dd>
                </div>
                <div>
                    <dt>{entry.on_account ? "Due" : "Payment"}</dt>
                    <dd>
                        {#if entry.on_account}
                            {entry.payment_status === "paid" ? "Paid" : formatDate(entry.due_at, { withTime: false })}
                            {#if entry.overdue}<span class="txt-danger"> — overdue</span>{/if}
                        {:else}
                            {w.payment.label}
                        {/if}
                    </dd>
                </div>
                {#if entry.approval_id}
                    <div>
                        <dt>Approved from</dt>
                        <dd><a href="{base}/portal/approvals/{entry.approval_id}">Request {entry.approval_id}</a></dd>
                    </div>
                {/if}
                {#if entry.quote_id}
                    <div>
                        <dt>From quote</dt>
                        <dd><a href="{base}/portal/quotes/{entry.quote_id}">Quote {entry.quote_id}</a></dd>
                    </div>
                {/if}
            </dl>
        </section>

        <h2 class="section-title">
            <i class="ri-list-check-3" aria-hidden="true"></i>
            What was ordered
        </h2>
        <div class="page-table-wrapper tw:rounded-xl tw:border m-b-sm">
            <table class="table responsive-table">
                <thead>
                    <tr>
                        <th class="col-field-name-id">Item</th>
                        <th class="col-field-type-number min-width">Quantity</th>
                        <th class="col-field-type-number min-width">Shipped</th>
                        <th class="col-field-type-number min-width">Price</th>
                        <th class="col-field-type-number min-width">Total</th>
                    </tr>
                </thead>
                <tbody>
                    {#each o.line_items as l (l.id)}
                        <tr>
                            <td class="col-field-name-id" data-name="Item">
                                <div class="row-name row-name-stacked">
                                    <span class="txt-bold">{l.title}</span>
                                    <span class="txt-hint txt-sm">
                                        <span class="txt-code">{l.sku}</span>{l.variant_label ? ` · ${l.variant_label}` : ""}
                                    </span>
                                </div>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Quantity">{l.quantity}</td>
                            <td class="col-field-type-number min-width" data-name="Shipped">{l.shipped_quantity ?? 0}</td>
                            <td class="col-field-type-number min-width" data-name="Price">{formatMoney(l.unit_price)}</td>
                            <td class="col-field-type-number min-width" data-name="Total">{formatMoney(l.total)}</td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
        <dl class="portal-sums m-b-base">
            <div><dt>Lines</dt><dd>{formatMoney(o.subtotal)}</dd></div>
            {#if o.discount?.amount_minor}
                <div><dt>Discount</dt><dd>−{formatMoney(o.discount)}</dd></div>
            {/if}
            <div><dt>Delivery{o.shipping_method ? ` (${o.shipping_method})` : ""}</dt><dd>{formatMoney(o.shipping)}</dd></div>
            <div><dt>Tax{o.tax_inclusive ? " (included)" : ""}</dt><dd>{formatMoney(o.tax)}</dd></div>
            <div class="portal-sums-total"><dt>Total</dt><dd>{formatMoney(o.total)}</dd></div>
            {#if o.refunded?.amount_minor}
                <div><dt>Refunded</dt><dd>{formatMoney(o.refunded)}</dd></div>
            {/if}
        </dl>

        <div class="portal-columns">
            <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5">
                <h3 class="b2b-eyebrow">Delivered to</h3>
                <p class="m-0">
                    {#if o.name}<strong>{o.name}</strong><br />{/if}
                    {place(o.address)}
                    {#if o.phone}<br /><span class="txt-hint">{o.phone}</span>{/if}
                </p>
            </section>
            <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5">
                <h3 class="b2b-eyebrow">Shipments</h3>
                {#if o.fulfillments?.length}
                    <ul class="portal-plain-list">
                        {#each o.fulfillments as f (f.id)}
                            <li>
                                {f.carrier_name || f.carrier || "Shipment"} · {formatDate(f.created_at, { withTime: false })}
                                {#if f.tracking_url}
                                    · <a href={f.tracking_url} target="_blank" rel="noreferrer">Track {f.tracking || "it"}</a>
                                {:else if f.tracking}
                                    · <span class="txt-code">{f.tracking}</span>
                                {/if}
                            </li>
                        {/each}
                    </ul>
                {:else}
                    <p class="txt-hint m-0">Nothing has shipped yet.</p>
                {/if}
            </section>
        </div>
    {/if}
</PortalPage>
