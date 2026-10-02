<script>
    /**
     * Receivables: every company's orders, and which of them are late.
     *
     * An order on account is due its company's net days after it was placed,
     * and it is overdue once that day passes while it is still unpaid. That
     * judgement is the engine's — the row says `overdue` — so this screen does
     * no date arithmetic of its own and cannot disagree with the credit check
     * that will refuse the company's next order.
     *
     * Money arriving is recorded on the order itself (Mark paid), which is why
     * every row opens it.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney, paymentStatusClass } from "$lib/format.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ overdue: false, page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const readable = $derived(can("companies.read"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let orders = $state([]);
    let meta = $state(null);
    let loading = $state(true);
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
                    query({ overdue: p.overdue ? "true" : "", page: p.page, limit: p.limit }),
            );
            if (mine !== reqId) return;
            orders = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
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
                                    { value: "", label: "Every company order" },
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
                    Orders placed for a company, newest first. One placed on account is due its
                    company's payment terms after it was placed, and is overdue once that day passes
                    unpaid — an overdue order counts against the company's credit until it is marked
                    paid on the order.
                </p>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && orders.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
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
                                    <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each orders as order (order.order_id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => open(order)}
                                    onkeydown={(e) => rowKey(e, () => open(order))}
                                >
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
                                        {#if !order.on_account}<div class="txt-hint txt-sm">paid at checkout</div>{/if}
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
                                    <td colspan="6" class="txt-hint txt-center p-base">
                                        {list.params.overdue
                                            ? "Nothing is overdue."
                                            : "No company has ordered yet."}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

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
