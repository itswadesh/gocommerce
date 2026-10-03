<script>
    /**
     * Receivables: every company's orders on account, and which of them are
     * late.
     *
     * An order on account is due its company's net days after it was placed,
     * and it is overdue once that day passes while it is still unpaid. That
     * judgement is the engine's — the row says `overdue` — so this screen does
     * no date arithmetic of its own and cannot disagree with the credit check
     * that will refuse the company's next order.
     *
     * A company usually pays several invoices with one transfer, so the rows
     * can be ticked and marked paid together, through the same mark-paid the
     * order screen uses, one order at a time. A reference for the payment
     * still belongs on each order: one string cannot be the reconciliation
     * reference for ten different invoices.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { runBulk } from "$lib/bulk.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { selection } from "$lib/selection.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney, paymentStatusClass } from "$lib/format.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ q: "", overdue: false, page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const readable = $derived(can("companies.read"));
    /* Marking an order paid is an order write, whoever's order it is. */
    const writable = $derived(can("orders.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let orders = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);
    let reqId = 0;

    $effect(() => {
        list.params;
        if (readable && hasModule("b2b")) load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = await api.get(
                "/api/admin/x/b2b/receivables" +
                    query({ q: p.q, overdue: p.overdue ? "true" : "", page: p.page, limit: p.limit }),
            );
            if (mine !== reqId) return;
            // `id` is what the shared selection keys on; a ledger row calls it
            // order_id.
            orders = (result.data ?? []).map((o) => ({ ...o, id: o.order_id }));
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    /*
     * A selection survives a page change and is cleared by a filter change,
     * as on Orders: an action must not land on rows nobody can see.
     */
    const sel = selection();
    $effect(() => {
        list.params.q;
        list.params.overdue;
        sel.clear();
    });
    const picked = $derived(sel.pick(orders));
    /* The order screen's own rule: a declined payment can be taken again,
       while a paid, refunded or cancelled order is refused by the engine. */
    const payable = $derived(
        picked.filter(
            (o) => o.payment_status !== "paid" && o.payment_status !== "refunded" && o.status !== "cancelled",
        ),
    );
    const payableTotal = $derived.by(() => {
        if (!payable.length) return null;
        return { amount_minor: payable.reduce((n, o) => n + o.total.amount_minor, 0), currency: payable[0].total.currency };
    });
    let paying = $state(false);

    async function markPaid() {
        if (!payable.length || paying) return;
        paying = true;
        try {
            await runBulk(payable, (o) => api.post(`/api/admin/orders/${o.order_id}/mark-paid`, {}), {
                describe: "Marked paid",
                noun: "order",
                label: (o) => o.number,
            });
        } finally {
            paying = false;
        }
        sel.clear();
        await load();
    }

    function search(event) {
        event.preventDefault();
        list.set({ q: draft.trim() });
    }

    const open = (o) => goto(`${base}/orders/${o.order_id}`);
</script>

<svelte:head><title>Receivables · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="receivables" />
{:else}
    <div class="page page-receivables b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Orders</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Receivables</div>
                </nav>
                <div class="flex-fill"></div>
                {#if !missing}
                    <div class="page-header-primary-btns">
                        <div class="field b2b-filter">
                            <Select
                                ariaLabel="Which orders"
                                value={list.params.overdue ? "overdue" : ""}
                                options={[
                                    { value: "", label: "Everything on account" },
                                    { value: "overdue", label: "Overdue only" },
                                ]}
                                onchange={(v) => list.set({ overdue: v === "overdue" })}
                            />
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Receivables are served by ext/b2b, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    Every company's orders placed on account, newest first. Each is due its company's
                    payment terms after it was placed, and is overdue once that day passes unpaid — an
                    overdue order counts against the company's credit until it is marked paid. Tick the
                    orders one transfer paid to mark them paid together.
                </p>

                <form class="fields m-b-base" onsubmit={search} role="search">
                    <div class="field">
                        <label for="receivable-search">Search</label>
                        <input
                            id="receivable-search"
                            type="search"
                            placeholder="PO number, order number or email"
                            autocomplete="off"
                            bind:value={draft}
                            oninput={(e) => list.set({ q: e.currentTarget.value.trim() })}
                        />
                    </div>
                </form>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && orders.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                {#if writable}
                                    <th class="col-bulk-select min-width">
                                        <div class="field">
                                            <input
                                                id="select-all-receivables"
                                                type="checkbox"
                                                checked={sel.allSelected(orders)}
                                                onchange={() => sel.toggleAll(orders)}
                                            />
                                            <label
                                                for="select-all-receivables"
                                                aria-label="Select every order on this page"
                                            ></label>
                                        </div>
                                    </th>
                                {/if}
                                <th class="col-field-name-id">Order</th>
                                <th>Company</th>
                                <th class="min-width">PO number</th>
                                <th class="col-field-type-number min-width">Total</th>
                                <th class="min-width">Payment</th>
                                <th class="min-width">Due</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !orders.length}
                                {#each Array(4) as _, i (i)}
                                    <tr><td colspan="7"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each orders as order (order.order_id)}
                                <tr
                                    class="handle"
                                    class:active={sel.has(order.id)}
                                    tabindex="0"
                                    onclick={() => open(order)}
                                    onkeydown={(e) => rowKey(e, () => open(order))}
                                >
                                    {#if writable}
                                        <td class="col-bulk-select min-width" onclick={(e) => e.stopPropagation()}>
                                            <div class="field">
                                                <input
                                                    id="select-receivable-{order.order_id}"
                                                    type="checkbox"
                                                    checked={sel.has(order.id)}
                                                    onchange={() => sel.toggle(order.id)}
                                                />
                                                <label
                                                    for="select-receivable-{order.order_id}"
                                                    aria-label="Select order {order.number}"
                                                ></label>
                                            </div>
                                        </td>
                                    {/if}
                                    <td class="col-field-name-id" data-name="Order">
                                        <div class="row-name row-name-stacked">
                                            <a
                                                href="{base}/orders/{order.order_id}"
                                                class="txt-bold txt-code"
                                                onclick={(e) => e.stopPropagation()}>{order.number}</a
                                            >
                                            <span class="txt-hint txt-sm">
                                                {formatDate(order.created_at, { withTime: false })}
                                                {#if order.placed_by}· {order.placed_by}{/if}
                                            </span>
                                        </div>
                                    </td>
                                    <td data-name="Company">
                                        <a href="{base}/b2b/companies/{order.company_id}" onclick={(e) => e.stopPropagation()}
                                            >{order.company_name}</a
                                        >
                                    </td>
                                    <td class="min-width" data-name="PO number">
                                        {#if order.po_number}<span class="txt-code">{order.po_number}</span>{:else}<span class="txt-hint">—</span>{/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Total">{formatMoney(order.total)}</td>
                                    <td class="min-width" data-name="Payment">
                                        <span class="label label-{paymentStatusClass(order.payment_status)}">{order.payment_status}</span>
                                    </td>
                                    <td class="min-width" data-name="Due">
                                        {#if order.due_at}
                                            <div class="inline-flex gap-5">
                                                <span class="txt-sm">{formatDate(order.due_at, { withTime: false })}</span>
                                                {#if order.overdue}<span class="label label-danger">Overdue</span>{/if}
                                            </div>
                                        {:else}
                                            <span class="txt-hint">—</span>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !orders.length}
                                <tr>
                                    <td colspan="7" class="txt-hint txt-center p-base">
                                        {#if list.params.q}
                                            No order on account matches “{list.params.q}”.
                                        {:else if list.params.overdue}
                                            Nothing is overdue.
                                        {:else}
                                            No company has ordered on account yet.
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                {#if writable}
                    <BulkBar count={sel.count} noun="order" onclear={() => sel.clear()}>
                        <!-- The count on the button is what will actually be
                             marked: a selection usually mixes paid and unpaid. -->
                        <button
                            type="button"
                            class="btn sm secondary"
                            class:loading={paying}
                            disabled={paying || !payable.length}
                            title={payable.length === sel.count
                                ? `Record payment of ${formatMoney(payableTotal)} against these orders`
                                : `${payable.length} of ${sel.count} can be marked paid`}
                            onclick={markPaid}
                        >
                            <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                            <span class="txt">
                                Mark paid ({payable.length}){#if payableTotal}&nbsp;· {formatMoney(payableTotal)}{/if}
                            </span>
                        </button>
                    </BulkBar>
                {/if}

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <Pager
                        {meta}
                        {loading}
                        noun="order"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>
{/if}
