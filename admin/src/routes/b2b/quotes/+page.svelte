<script>
    /**
     * Quotes: business customers asking what a basket would cost them.
     *
     * A requested quote is work for the store — somebody has to price it — and
     * its amber label is the one that stands out in a list of every status;
     * "Requested" in the filter narrows to just those.
     * A quote is one price for one basket for one company (D68); it is never a
     * price list, and accepting it places the order at exactly those prices.
     *
     * The company filter arrives from a company's own page as `?company_id=`,
     * and is shown as a chip with a way out rather than as a silent narrowing.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney, relativeTime } from "$lib/format.js";
    import { QUOTE_STATUSES, quoteStatusClass, quoteStatusLabel } from "$lib/b2b.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ status: "", company_id: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const readable = $derived(can("quotes.read"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let quotes = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let companyName = $state("");
    let reqId = 0;

    const STATUS_OPTIONS = [
        { value: "", label: "Every quote" },
        ...QUOTE_STATUSES.map((s) => ({ value: s.value, label: s.hint ? `${s.label} — ${s.hint}` : s.label, short: s.label })),
    ];

    $effect(() => {
        list.params;
        if (readable && hasModule("b2b")) load();
    });

    $effect(() => {
        const cid = list.params.company_id;
        companyName = "";
        if (cid && readable && hasModule("b2b")) nameCompany(cid);
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = await api.get(
                "/api/admin/x/b2b/quotes" +
                    query({
                        status: p.status,
                        company_id: p.company_id,
                        page: p.page,
                        limit: p.limit,
                    }),
            );
            if (mine !== reqId) return;
            quotes = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    /* The quotes carry their company's name, so the first row usually answers
       this; a filter that matches nothing still needs one, from the company. */
    async function nameCompany(cid) {
        if (!can("companies.read")) {
            companyName = `Company ${cid}`;
            return;
        }
        try {
            companyName = (await api.get(`/api/admin/x/b2b/companies/${cid}`))?.name ?? `Company ${cid}`;
        } catch {
            companyName = `Company ${cid}`;
        }
    }

    const quantity = (q) => q.lines.reduce((n, l) => n + l.quantity, 0);
    const open = (q) => goto(`${base}/b2b/quotes/${q.id}`);
</script>

<svelte:head><title>Quotes · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="quotes.read" what="quotes" />
{:else}
    <div class="page page-quotes b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Orders</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Quotes</div>
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
                <ModuleMissing module="b2b" what="Quotes are served by ext/b2b, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    A buyer asks what a basket would cost; the store prices each line and sends it, and
                    the buyer can accept it as an order at exactly those prices until it expires.
                    Requested quotes are the ones waiting on you.
                </p>

                {#if list.params.company_id}
                    <div class="m-b-base b2b-notice">
                        <span class="label">
                            {companyName || "…"}
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                aria-label="Show every company's quotes"
                                title="Show every company's quotes"
                                onclick={() => list.set({ company_id: "" })}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </span>
                    </div>
                {/if}

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && quotes.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Quote</th>
                                <th>Company</th>
                                <th class="col-field-type-number min-width">Items</th>
                                <th class="col-field-type-number min-width">Total</th>
                                <th class="min-width">Status</th>
                                <th class="min-width">Expires</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !quotes.length}
                                {#each Array(4) as _, i (i)}
                                    <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each quotes as quote (quote.id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => open(quote)}
                                    onkeydown={(e) => rowKey(e, () => open(quote))}
                                >
                                    <td class="col-field-name-id" data-name="Quote">
                                        <div class="row-name row-name-stacked">
                                            <a
                                                href="{base}/b2b/quotes/{quote.id}"
                                                class="txt-bold txt-code"
                                                onclick={(e) => e.stopPropagation()}>{quote.number}</a
                                            >
                                            <span class="txt-hint txt-sm" title={formatDate(quote.created_at)}>
                                                {quote.requested_by_email || "a former buyer"} · {relativeTime(quote.created_at)}
                                            </span>
                                        </div>
                                    </td>
                                    <td data-name="Company">{quote.company_name}</td>
                                    <td class="col-field-type-number min-width" data-name="Items">
                                        {quantity(quote)}
                                        <span class="txt-hint txt-sm">
                                            in {quote.lines.length}
                                            {quote.lines.length === 1 ? "line" : "lines"}
                                        </span>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Total">
                                        {#if quote.total}
                                            {formatMoney(quote.total)}
                                        {:else}
                                            <span class="txt-hint">Not priced</span>
                                        {/if}
                                    </td>
                                    <td class="min-width" data-name="Status">
                                        <span class="label {quoteStatusClass(quote.status)}">{quoteStatusLabel(quote.status)}</span>
                                    </td>
                                    <td class="min-width txt-hint txt-sm" data-name="Expires">
                                        {quote.expires_at ? formatDate(quote.expires_at, { withTime: false }) : "—"}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !quotes.length}
                                <tr>
                                    <td colspan="6" class="txt-hint txt-center p-base">
                                        {#if list.params.status === "requested"}
                                            Nothing is waiting for a price.
                                        {:else}
                                            No quote matches that.
                                        {/if}
                                        Buyers ask for quotes from the storefront.
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
                        noun="quote"
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
