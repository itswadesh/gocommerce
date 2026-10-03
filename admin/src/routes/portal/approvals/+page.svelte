<script>
    /**
     * Approvals: orders a buyer could not place on their own because they
     * were over the company's approval limit. Approvers and admins see every
     * request and decide them; a buyer sees their own and can withdraw one
     * that is still waiting.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { rowKey } from "$lib/rowkey.js";
    import { query } from "$lib/api.js";
    import { trade, tradeApi, explain, isApprover, word, APPROVAL_WORDS, refreshBadges } from "$lib/trade.svelte.js";
    import Pager from "$lib/components/Pager.svelte";
    import PortalPage from "../PortalPage.svelte";

    const list = listState({ status: "pending", page: 1, limit: 25 });

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let failure = $state("");
    let reqId = 0;

    const TABS = [
        { value: "pending", label: "Waiting" },
        { value: "approved", label: "Approved" },
        { value: "rejected", label: "Turned down" },
        { value: "all", label: "All" },
    ];

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
                "/x/b2b/approvals" + query({ status: p.status === "all" ? "" : p.status, page: p.page, limit: p.limit }),
            );
            if (mine !== reqId) return;
            rows = r.data;
            meta = r.meta;
            if (p.status === "pending") refreshBadges();
        } catch (err) {
            if (mine === reqId && !err.handled) failure = explain(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function basket(a) {
        const shown = a.lines.slice(0, 2).map((l) => `${l.quantity} × ${l.sku}`);
        const rest = a.lines.length - shown.length;
        return rest > 0 ? `${shown.join(", ")} and ${rest} more` : shown.join(", ");
    }

    const open = (a) => goto(`${base}/portal/approvals/${a.id}`);
</script>

<PortalPage title="Approvals">
    <p class="field-help m-b-base portal-intro">
        {#if isApprover()}
            Orders from {trade.me.company.name}'s buyers that are over the approval limit of
            {formatMoney(trade.me.company.approval_threshold)}. Approving one places the order; nothing is ordered until
            you do.
        {:else}
            Your orders that were over {trade.me.company.name}'s approval limit. Nothing is ordered until an approver
            says yes.
        {/if}
    </p>

    <div class="portal-tabs m-b-base" role="tablist" aria-label="Which requests">
        {#each TABS as t (t.value)}
            <button
                type="button"
                role="tab"
                class="portal-tab"
                class:active={list.params.status === t.value}
                aria-selected={list.params.status === t.value}
                onclick={() => list.set({ status: t.value })}
            >
                {t.label}
                {#if t.value === "pending" && trade.waiting > 0 && isApprover()}
                    <span class="portal-nav-badge">{trade.waiting}</span>
                {/if}
            </button>
        {/each}
    </div>

    {#if failure}
        <div class="alert danger m-b-base portal-notice" role="alert">
            <p>{failure}</p>
            <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
        </div>
    {/if}

    <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && rows.length > 0}>
        <table class="table responsive-table">
            <thead>
                <tr>
                    <th class="col-field-name-id">Request</th>
                    <th>Lines</th>
                    <th class="col-field-type-number min-width">Total</th>
                    <th class="min-width">Status</th>
                </tr>
            </thead>
            <tbody>
                {#if loading && !rows.length}
                    {#each Array(3) as _, i (i)}
                        <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                    {/each}
                {/if}
                {#each rows as a (a.id)}
                    {@const s = word(APPROVAL_WORDS, a.status)}
                    <tr class="handle" tabindex="0" onclick={() => open(a)} onkeydown={(e) => rowKey(e, () => open(a))}>
                        <td class="col-field-name-id" data-name="Request">
                            <div class="row-name row-name-stacked">
                                <a href="{base}/portal/approvals/{a.id}" class="txt-bold" onclick={(e) => e.stopPropagation()}>
                                    {a.requested_by === trade.account?.id ? "Your request" : a.requested_by_email || "A former buyer"}
                                </a>
                                <span class="txt-hint txt-sm">
                                    {a.kind === "quote" ? "Accepting a quote" : "From a basket"} · {formatDate(a.created_at)}
                                    {#if a.po_number}· PO <span class="txt-code">{a.po_number}</span>{/if}
                                </span>
                            </div>
                        </td>
                        <td class="txt-sm" data-name="Lines"><span class="txt-code">{basket(a)}</span></td>
                        <td class="col-field-type-number min-width" data-name="Total">{formatMoney(a.total)}</td>
                        <td class="min-width" data-name="Status">
                            <span class="label {s.tone}">{s.label}</span>
                            {#if a.last_error && a.status === "pending"}
                                <div class="txt-danger txt-sm b2b-reason">Last try failed: {a.last_error}</div>
                            {/if}
                        </td>
                    </tr>
                {/each}
                {#if !loading && !rows.length && !failure}
                    <tr>
                        <td colspan="4" class="txt-hint txt-center p-base">
                            {#if list.params.status === "pending"}
                                {isApprover() ? "Nothing is waiting for a decision." : "None of your orders is waiting for approval."}
                            {:else}
                                No requests here.
                            {/if}
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>
    </div>

    <footer class="page-footer tw:text-xs tw:text-muted-foreground">
        <Pager {meta} {loading} noun="request" perPage={list.params.limit} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
        <div class="flex-fill"></div>
    </footer>
</PortalPage>
