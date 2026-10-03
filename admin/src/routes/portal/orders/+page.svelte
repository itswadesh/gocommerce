<script>
    /**
     * Orders: the company's orders the buyer may see — everybody's to an admin
     * or approver, their own to a buyer (GET /x/b2b/orders) — searchable by
     * order or PO number, with the late ones a click away.
     *
     * Repeat puts an order's lines into the basket at today's prices; it
     * never places anything, and what could not go in is listed right here.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { rowKey } from "$lib/rowkey.js";
    import { query } from "$lib/api.js";
    import { trade, tradeApi, explain, isApprover, orderWords, repeatOrder } from "$lib/trade.svelte.js";
    import Pager from "$lib/components/Pager.svelte";
    import PortalPage from "../PortalPage.svelte";
    import RejectedLines from "../RejectedLines.svelte";

    const list = listState({ q: "", overdue: false, page: 1, limit: 25 });

    let orders = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let failure = $state("");
    let draft = $state(list.params.q);
    let repeating = $state(0);
    let repeated = $state(null);
    let reqId = 0;

    $effect(() => {
        list.params;
        load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        failure = "";
        try {
            const p = list.params;
            const r = await tradeApi.list(
                "/x/b2b/orders" + query({ q: p.q, overdue: p.overdue ? "true" : "", page: p.page, limit: p.limit }),
            );
            if (mine !== reqId) return;
            orders = r.data;
            meta = r.meta;
        } catch (err) {
            if (mine === reqId && !err.handled) failure = explain(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function search(event) {
        event.preventDefault();
        list.set({ q: draft.trim() });
    }

    async function repeat(o) {
        repeating = o.order_id;
        try {
            const fill = await repeatOrder(o.order_id);
            repeated = { number: o.number, rejected: fill.rejected ?? [] };
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        } finally {
            repeating = 0;
        }
    }

    const open = (o) => goto(`${base}/portal/orders/${o.order_id}`);
    const canOrder = $derived(trade.me.company.status !== "closed");
</script>

<PortalPage title="Orders">
    <p class="field-help m-b-base portal-intro">
        {isApprover()
            ? `Every order placed for ${trade.me.company.name}.`
            : "The orders you've placed."} Repeat one to put its lines in your basket at today's prices.
    </p>

    {#if repeated}
        <div class="alert {repeated.rejected.length ? 'warning' : 'success'} m-b-base portal-notice portal-result" role="status">
            <p>
                <strong>{repeated.number}'s lines are in your basket</strong>{repeated.rejected.length
                    ? `, except ${repeated.rejected.length === 1 ? "one" : repeated.rejected.length}:`
                    : "."}
                <a href="{base}/portal/basket" class="portal-inline-link">Go to your basket →</a>
            </p>
            <RejectedLines rejected={repeated.rejected} />
        </div>
    {/if}

    <div class="b2b-section-head portal-filters">
        <form class="field portal-search" onsubmit={search} role="search">
            <input
                type="search"
                placeholder="Order or PO number"
                aria-label="Search orders by order or PO number"
                autocomplete="off"
                bind:value={draft}
            />
        </form>
        <label class="portal-toggle">
            <input type="checkbox" checked={list.params.overdue} onchange={(e) => list.set({ overdue: e.currentTarget.checked })} />
            <span>Overdue only</span>
        </label>
    </div>

    {#if failure}
        <div class="alert danger m-b-base portal-notice" role="alert">
            <p>{failure}</p>
            <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
        </div>
    {/if}

    <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && orders.length > 0}>
        <table class="table responsive-table">
            <thead>
                <tr>
                    <th class="col-field-name-id">Order</th>
                    <th>PO number</th>
                    <th class="col-field-type-number min-width">Total</th>
                    <th class="min-width">Progress</th>
                    <th class="min-width">Payment</th>
                    <th class="min-width" aria-label="Repeat"></th>
                </tr>
            </thead>
            <tbody>
                {#if loading && !orders.length}
                    {#each Array(4) as _, i (i)}
                        <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                    {/each}
                {/if}
                {#each orders as o (o.order_id)}
                    {@const w = orderWords(o)}
                    <tr class="handle" tabindex="0" onclick={() => open(o)} onkeydown={(e) => rowKey(e, () => open(o))}>
                        <td class="col-field-name-id" data-name="Order">
                            <div class="row-name row-name-stacked">
                                <a href="{base}/portal/orders/{o.order_id}" class="txt-bold txt-code" onclick={(e) => e.stopPropagation()}
                                    >{o.number}</a
                                >
                                <span class="txt-hint txt-sm">
                                    {formatDate(o.created_at, { withTime: false })}{isApprover() && o.placed_by ? ` · ${o.placed_by}` : ""}
                                </span>
                            </div>
                        </td>
                        <td data-name="PO number">
                            {#if o.po_number}<span class="txt-code">{o.po_number}</span>{:else}<span class="txt-hint">—</span>{/if}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Total">{formatMoney(o.total)}</td>
                        <td class="min-width" data-name="Progress"><span class="label {w.tone}">{w.status}</span></td>
                        <td class="min-width" data-name="Payment">
                            <span class="label {w.payment.tone}">{w.payment.label}</span>
                            {#if o.due_at && o.payment_status !== "paid"}
                                <div class="txt-hint txt-sm">due {formatDate(o.due_at, { withTime: false })}</div>
                            {/if}
                        </td>
                        <td class="min-width b2b-actions" data-name="">
                            {#if canOrder}
                                <button
                                    type="button"
                                    class="btn sm secondary portal-press"
                                    class:loading={repeating === o.order_id}
                                    disabled={!!repeating}
                                    aria-label="Repeat order {o.number}"
                                    onclick={(e) => {
                                        e.stopPropagation();
                                        repeat(o);
                                    }}
                                >
                                    <i class="ri-repeat-line" aria-hidden="true"></i>
                                    <span class="txt">Repeat</span>
                                </button>
                            {/if}
                        </td>
                    </tr>
                {/each}
                {#if !loading && !orders.length && !failure}
                    <tr>
                        <td colspan="6" class="txt-hint txt-center p-base">
                            {#if list.params.q}
                                No order matches “{list.params.q}”.
                            {:else if list.params.overdue}
                                Nothing is overdue.
                            {:else}
                                No orders yet. <a href="{base}/portal/quick-order">Start one with a quick order.</a>
                            {/if}
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>
    </div>

    <footer class="page-footer tw:text-xs tw:text-muted-foreground">
        <Pager {meta} {loading} noun="order" perPage={list.params.limit} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
        <div class="flex-fill"></div>
    </footer>
</PortalPage>
