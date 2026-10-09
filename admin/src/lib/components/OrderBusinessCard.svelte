<script>
    /**
     * An order placed for a company, said in words on the order's own screen.
     *
     * ext/b2b writes `metadata.b2b` in the order's transaction, and the order
     * screen used to show it as one line of JSON in the generic Metadata card.
     * The ledger has more than the metadata does — when the order is due and
     * whether that has passed — so this reads the ledger row, and falls back
     * to the metadata for the few seconds after a crash before the reconciler
     * has written the row.
     */
    import { base } from "$app/paths";
    import { api, can } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";

    let { orderId, meta = {} } = $props();

    let row = $state(null);
    let loading = $state(true);

    $effect(() => {
        orderId;
        load();
    });

    async function load() {
        loading = true;
        try {
            row = await api.get(`/api/admin/x/b2b/orders/${orderId}`);
        } catch {
            // No ledger row yet: what the order itself recorded is still true,
            // and it is enough to name the company and the PO.
            row = null;
        } finally {
            loading = false;
        }
    }

    const companyId = $derived(row?.company_id ?? meta.company_id ?? null);
    const companyName = $derived(row?.company_name || meta.company || `Company ${companyId}`);
    const po = $derived(row?.po_number ?? meta.po_number ?? "");
    const quoteId = $derived(row?.quote_id ?? meta.quote_id ?? null);
    const approvalId = $derived(row?.approval_id ?? meta.approval_id ?? null);
</script>

<section class="order-card b2b-order-card" aria-busy={loading}>
    <h6 class="order-card-title">Business order</h6>
    <div class="order-lines b2b-order-lines">
        <div class="order-line">
            <span class="txt-hint">Company</span>
            {#if companyId}
                <a href="{base}/dash/b2b/companies/{companyId}" class="txt-ellipsis">{companyName}</a>
            {:else}
                <span>—</span>
            {/if}
        </div>
        <div class="order-line">
            <span class="txt-hint">PO number</span>
            {#if po}<span class="txt-code">{po}</span>{:else}<span class="txt-hint">None given</span>{/if}
        </div>
        {#if row?.placed_by}
            <div class="order-line">
                <span class="txt-hint">Placed by</span>
                <span class="txt-ellipsis">{row.placed_by}</span>
            </div>
        {/if}
        {#if row}
            <div class="order-line">
                <span class="txt-hint">Payment</span>
                <span>{row.on_account ? "On account" : "Paid at checkout"}</span>
            </div>
            {#if row.on_account && row.due_at}
                <div class="order-line">
                    <span class="txt-hint">Due</span>
                    <span class="inline-flex gap-5">
                        {formatDate(row.due_at, { withTime: false })}
                        {#if row.overdue}<span class="label label-danger">Overdue</span>{/if}
                    </span>
                </div>
            {/if}
        {:else if !loading && meta.net_days != null}
            <div class="order-line">
                <span class="txt-hint">Terms</span>
                <span>Net {meta.net_days}</span>
            </div>
        {/if}
        {#if quoteId}
            <div class="order-line">
                <span class="txt-hint">From quote</span>
                {#if can("quotes.read")}
                    <a href="{base}/dash/b2b/quotes/{quoteId}" class="txt-code">Q-{String(quoteId).padStart(6, "0")}</a>
                {:else}
                    <span class="txt-code">Q-{String(quoteId).padStart(6, "0")}</span>
                {/if}
            </div>
        {/if}
        {#if approvalId}
            <div class="order-line">
                <span class="txt-hint">Approved request</span>
                <a href="{base}/dash/b2b/approvals?company_id={companyId}">#{approvalId}</a>
            </div>
        {/if}
    </div>
</section>
