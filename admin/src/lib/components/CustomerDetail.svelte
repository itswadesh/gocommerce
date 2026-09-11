<script>
    /**
     * One customer, composed rather than fetched.
     *
     * There is no `GET /api/admin/customers/{email}` and there is deliberately
     * no customer table behind it — D22 makes guest checkout permanent, so a
     * customer here is the orders that share an email and nothing else. That is
     * why this reads `GET /api/admin/orders?email=`: the same public endpoint
     * the Orders screen uses, filtered exactly the way the row's own link
     * filters it. Nothing on this panel knows anything the API does not serve.
     *
     * The row it is opened from already carries the lifetime figures, so those
     * render immediately and the order list fills in behind them. What the
     * orders add is the half the aggregate cannot say: which of them were
     * cancelled, which are still waiting to be paid, and whether any of them
     * were priced in a currency the lifetime total is not in.
     *
     * It borrows the order drawer's card classes wholesale. The shape is the
     * same shape — figures in a rail, a list beside it — and a second set of
     * rules describing it would be the same rules under another name.
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import {
        formatDate,
        formatMoney,
        orderStatusClass,
        orderStatusLabel,
        paymentLabel,
        pluralize,
    } from "$lib/format.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let { open = false, customer = null, onclose } = $props();

    /* A page of their orders, not all of them. Somebody with four hundred
       orders is a wholesale account, and the link to the full filtered list is
       right there under the table. */
    const PER_PAGE = 50;

    let orders = $state([]);
    let meta = $state(null);
    let loading = $state(false);
    let failed = $state(null);

    /* The drawer is reused for every row, so a slow answer for the customer
       who was open a moment ago must not land in the one open now. */
    let reqId = 0;

    const mayReadOrders = $derived(can("orders.read"));

    $effect(() => {
        const email = open ? customer?.email : null;
        load(email);
    });

    async function load(email) {
        const mine = ++reqId;
        orders = [];
        meta = null;
        failed = null;
        if (!email || !mayReadOrders) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const res = await api.get(
                "/api/admin/orders" + query({ email, limit: PER_PAGE }),
            );
            if (mine !== reqId) return;
            orders = res.data ?? [];
            meta = res.meta ?? null;
        } catch (err) {
            if (mine !== reqId) return;
            // Kept rather than toasted: the drawer has somewhere to say it, and
            // the lifetime figures beside it are still true.
            failed = err;
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    /*
     * Why the two columns on the list disagree, said with this customer's own
     * numbers rather than as a footnote nobody reads.
     *
     * `orders` on the row excludes cancelled ones and `spent` counts only what
     * was actually paid, less refunds — so a cash-on-delivery shopper with three
     * orders in the post reads "3 orders / £0.00", which looks like a bug and is
     * not one. These are what makes it legible.
     */
    const cancelled = $derived(orders.filter((o) => o.status === "cancelled").length);
    const unpaid = $derived(
        orders.filter((o) => o.status !== "cancelled" && o.payment_status !== "paid").length,
    );
    const refunded = $derived(orders.filter((o) => (o.refunded?.amount_minor ?? 0) > 0).length);

    /** Whether the page holds every order there is, or a page of them. */
    const partial = $derived(!!meta && (meta.total ?? 0) > orders.length);

    /*
     * Lifetime spend is stamped with the store's configured currency whatever
     * the orders were actually priced in — see customers.go, where the sum is
     * over `total_minor` across a group that may hold several. Saying so here is
     * the only place the panel can: the figure itself cannot carry the caveat.
     */
    const foreign = $derived([
        ...new Set(
            orders
                .map((o) => o.currency)
                .filter((c) => c && c !== (customer?.spent?.currency ?? "")),
        ),
    ]);

    const ordersHref = $derived(
        customer ? `${base}/orders?email=${encodeURIComponent(customer.email)}` : `${base}/orders`,
    );

    /** The address as a parcel would carry it, or nothing at all. */
    const hasAddress = $derived(
        !!customer?.address &&
            !!(
                customer.address.line1 ||
                customer.address.city ||
                customer.address.postal_code ||
                customer.address.country
            ),
    );
</script>

<Drawer
    {open}
    size="lg"
    title={customer ? customer.name || customer.email : "Customer"}
    {onclose}
>
    {#if customer}
        <div class="order-detail">
            <div class="order-status">
                <span class="label">
                    {customer.orders}
                    {pluralize(customer.orders, "order")}
                </span>
                {#if cancelled}
                    <span class="label danger">
                        {cancelled} cancelled
                    </span>
                {/if}
                {#if unpaid}
                    <span class="label warning">
                        {unpaid} awaiting payment
                    </span>
                {/if}
                {#if refunded}
                    <span class="label warning">
                        {refunded} with a refund
                    </span>
                {/if}
                <div class="flex-fill"></div>
                <span class="txt-hint txt-sm">
                    Customer since {formatDate(customer.first_order_at, { withTime: false })}
                </span>
            </div>

            <div class="order-grid">
                <div class="order-main">
                    <section class="order-card">
                        <div class="order-card-head">
                            <h6 class="order-card-title">Orders</h6>
                            <!-- A real link, so it opens in a tab and says
                                 where it goes on hover. The list screen reads
                                 the same `email=` filter off its own URL. -->
                            <a href={ordersHref} class="btn sm transparent secondary">
                                <span class="txt">Open in Orders</span>
                            </a>
                        </div>

                        {#if !mayReadOrders}
                            <p class="txt-hint m-0">
                                Your role does not carry the right to read orders, so what this
                                person bought cannot be shown. The figures beside this come with
                                the customer list itself.
                            </p>
                        {:else if loading && !orders.length}
                            {#each Array(3) as _, i (i)}
                                <div class="m-b-5"><span class="skeleton-loader"></span></div>
                            {/each}
                        {:else if failed}
                            <p class="txt-hint m-0">Their orders could not be read just now.</p>
                        {:else if !orders.length}
                            <p class="txt-hint m-0">No orders under this address.</p>
                        {:else}
                            <table class="table">
                                <thead>
                                    <tr>
                                        <th>Order</th>
                                        <th>Status</th>
                                        <th>Payment</th>
                                        <th class="txt-right">Total</th>
                                        <th class="txt-right">Placed</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {#each orders as row (row.id)}
                                        {@const pay = paymentLabel(row)}
                                        <tr>
                                            <td>
                                                <a
                                                    class="txt-code"
                                                    href="{base}/orders?q={encodeURIComponent(
                                                        row.number,
                                                    )}"
                                                >
                                                    {row.number}
                                                </a>
                                            </td>
                                            <td>
                                                <span class="label {orderStatusClass(row.status)}">
                                                    {orderStatusLabel(row.status)}
                                                </span>
                                            </td>
                                            <td>
                                                <span class="label {pay.cls}">{pay.text}</span>
                                            </td>
                                            <td class="txt-right">{formatMoney(row.total)}</td>
                                            <td class="txt-right txt-hint">
                                                {formatDate(row.created_at, { withTime: false })}
                                            </td>
                                        </tr>
                                    {/each}
                                </tbody>
                            </table>

                            {#if partial}
                                <p class="txt-hint txt-sm m-t-5 m-b-0">
                                    The most recent {orders.length} of {meta.total}. The rest are
                                    on the Orders screen under the same filter.
                                </p>
                            {/if}
                        {/if}
                    </section>
                </div>

                <aside class="order-rail">
                    <section class="order-card">
                        <h6 class="order-card-title">Lifetime</h6>
                        <div class="order-total">{formatMoney(customer.spent)}</div>
                        <div class="order-paid" class:is-paid={customer.spent?.amount_minor > 0}>
                            paid, less anything refunded
                        </div>

                        <div class="order-lines">
                            <div class="order-line">
                                <span class="txt-hint">Orders</span>
                                <span>{customer.orders}</span>
                            </div>
                            {#if cancelled}
                                <div class="order-line">
                                    <span class="txt-hint">Cancelled</span>
                                    <span>{cancelled}</span>
                                </div>
                            {/if}
                            <div class="order-line">
                                <span class="txt-hint">First order</span>
                                <span>{formatDate(customer.first_order_at, { withTime: false })}</span>
                            </div>
                            <div class="order-line">
                                <span class="txt-hint">Last order</span>
                                <span>{formatDate(customer.last_order_at, { withTime: false })}</span>
                            </div>
                        </div>

                        <!--
                            The two figures above have different denominators,
                            and nothing on the list said so. This is where it is
                            said, in this customer's own numbers.
                        -->
                        <p class="txt-hint txt-sm m-t-sm m-b-0">
                            Orders counts everything placed that was not cancelled. Spent counts
                            only what was actually paid, less refunds{#if unpaid}, which is why
                                {unpaid}
                                {pluralize(unpaid, "order")}
                                {unpaid === 1 ? "is" : "are"} not in it yet{/if}.
                        </p>

                        {#if foreign.length}
                            <p class="txt-hint txt-sm m-t-5 m-b-0">
                                Some of these orders were priced in {foreign.join(", ")}. Lifetime
                                spend is totalled in {customer.spent?.currency} whatever the order
                                was taken in, so treat it as an order of magnitude rather than a
                                ledger.
                            </p>
                        {/if}
                    </section>

                    <section class="order-card">
                        <h6 class="order-card-title">Contact</h6>
                        <div class="order-customer-name">{customer.name || "—"}</div>
                        <!-- Links, not text: chasing an order means writing to
                             somebody or ringing them. -->
                        <a class="order-contact" href="mailto:{customer.email}">{customer.email}</a>
                        {#if customer.phone}
                            <a class="order-contact" href="tel:{customer.phone}">{customer.phone}</a>
                        {:else}
                            <span class="order-contact txt-hint">No phone number</span>
                        {/if}

                        {#if hasAddress}
                            <address class="order-address">
                                {#if customer.address.name}
                                    {customer.address.name}<br />
                                {/if}
                                {customer.address.line1}{customer.address.line2
                                    ? ", " + customer.address.line2
                                    : ""}<br />
                                {customer.address.city}{customer.address.state
                                    ? " " + customer.address.state
                                    : ""}
                                {customer.address.postal_code}<br />
                                {customer.address.country}
                            </address>
                            <p class="txt-hint txt-sm m-t-5 m-b-0">
                                Where their most recent order went. Correcting it means correcting
                                that order — there is no customer record to edit.
                            </p>
                        {:else}
                            <p class="order-address m-b-0">No address on their orders.</p>
                        {/if}
                    </section>
                </aside>
            </div>
        </div>
    {/if}
</Drawer>
