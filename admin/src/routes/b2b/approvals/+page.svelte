<script>
    /**
     * Approvals: orders a buyer could not place on their own.
     *
     * A buyer whose basket is over their company's approval threshold files it
     * here, and one of that company's own approvers decides it on the
     * storefront. Read-only for the store, and on purpose: whether Acme's
     * junior buyer may spend Acme's money is Acme's decision. The store looks
     * to know what is coming, and to see a request that has sat for a week.
     *
     * The rows carry a company id and no name, so the names are read once per
     * company on the page and kept.
     */
    import { base } from "$app/paths";
    import { api, query } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney, relativeTime } from "$lib/format.js";
    import { APPROVAL_STATUSES, approvalStatusClass, approvalStatusLabel } from "$lib/b2b.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ status: "", company_id: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const readable = $derived(can("companies.read"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let approvals = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let names = $state({});
    let reqId = 0;

    const STATUS_OPTIONS = [
        { value: "", label: "Every request" },
        ...APPROVAL_STATUSES.map((s) => ({ value: s.value, label: s.hint ? `${s.label} — ${s.hint}` : s.label, short: s.label })),
    ];

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
                "/api/admin/x/b2b/approvals" +
                    query({ status: p.status, company_id: p.company_id, page: p.page, limit: p.limit }),
            );
            if (mine !== reqId) return;
            approvals = result.data ?? [];
            meta = result.meta ?? null;
            const wanted = new Set(approvals.map((a) => a.company_id));
            if (p.company_id) wanted.add(Number(p.company_id));
            nameCompanies([...wanted]);
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function nameCompanies(ids) {
        const unknown = ids.filter((cid) => !(cid in names));
        if (!unknown.length) return;
        const found = {};
        await Promise.all(
            unknown.map(async (cid) => {
                try {
                    found[cid] = (await api.get(`/api/admin/x/b2b/companies/${cid}`))?.name ?? null;
                } catch {
                    found[cid] = null;
                }
            }),
        );
        names = { ...names, ...found };
    }

    const companyName = (cid) => names[cid] ?? `Company ${cid}`;

    /* The first two lines by SKU and a count of the rest: enough to recognise
       a basket, short enough to stay one line in the table. */
    function basket(a) {
        const shown = a.lines.slice(0, 2).map((l) => `${l.quantity} × ${l.sku}`);
        const rest = a.lines.length - shown.length;
        return rest > 0 ? `${shown.join(", ")} and ${rest} more` : shown.join(", ");
    }
</script>

<svelte:head><title>Approvals · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="approval requests" />
{:else}
    <div class="page page-approvals b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Orders</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Approvals</div>
                </nav>
                <div class="flex-fill"></div>
                {#if !missing}
                    <div class="page-header-primary-btns">
                        <div class="field b2b-filter">
                            <Select
                                ariaLabel="Status"
                                value={list.params.status}
                                options={STATUS_OPTIONS}
                                onchange={(v) => list.set({ status: v })}
                            />
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Approvals are served by ext/b2b, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    A buyer's order over their company's approval threshold waits here until one of the
                    company's own approvers decides it on the storefront. The store does not approve them
                    — this is where to see what is coming, and what has been waiting too long.
                </p>

                {#if list.params.company_id}
                    <div class="m-b-base b2b-notice">
                        <span class="label">
                            {companyName(Number(list.params.company_id))}
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                aria-label="Show every company's requests"
                                title="Show every company's requests"
                                onclick={() => list.set({ company_id: "" })}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </span>
                    </div>
                {/if}

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && approvals.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Request</th>
                                <th>Company</th>
                                <th>Basket</th>
                                <th class="col-field-type-number min-width">Total</th>
                                <th class="min-width">Status</th>
                                <th class="min-width">Order</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !approvals.length}
                                {#each Array(4) as _, i (i)}
                                    <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each approvals as a (a.id)}
                                <tr>
                                    <td class="col-field-name-id" data-name="Request">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold">{a.requested_by_email || "a former buyer"}</span>
                                            <span class="txt-hint txt-sm" title={formatDate(a.created_at)}>
                                                {a.kind === "quote" ? "Accepting a quote" : "From a cart"} ·
                                                {relativeTime(a.created_at)}
                                                {#if a.po_number}· PO <span class="txt-code">{a.po_number}</span>{/if}
                                            </span>
                                        </div>
                                    </td>
                                    <td data-name="Company">
                                        <a href="{base}/b2b/companies/{a.company_id}">{companyName(a.company_id)}</a>
                                    </td>
                                    <td class="txt-sm" data-name="Basket">
                                        <span class="txt-code">{basket(a)}</span>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Total">{formatMoney(a.total)}</td>
                                    <td class="min-width" data-name="Status">
                                        <span class="label {approvalStatusClass(a.status)}">{approvalStatusLabel(a.status)}</span>
                                        {#if a.reason}
                                            <div class="txt-hint txt-sm b2b-reason">“{a.reason}”</div>
                                        {/if}
                                        {#if a.last_error}
                                            <div class="txt-danger txt-sm b2b-reason">Placing failed: {a.last_error}</div>
                                        {/if}
                                    </td>
                                    <td class="min-width" data-name="Order">
                                        {#if a.order_id}
                                            <a href="{base}/orders/{a.order_id}" class="txt-code">{a.order_number}</a>
                                        {:else}
                                            <span class="txt-hint">—</span>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !approvals.length}
                                <tr>
                                    <td colspan="6" class="txt-hint txt-center p-base">
                                        {#if list.params.status === "pending"}
                                            Nothing is waiting for an approver.
                                        {:else if list.params.status || list.params.company_id}
                                            No request matches that.
                                        {:else}
                                            No buyer has needed approval yet. Requests appear once a company
                                            has an approval threshold and a buyer goes over it.
                                        {/if}
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
                        noun="request"
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
