<script>
    /**
     * Invoices: the documents ext/invoices issued when an order was paid.
     *
     * There is no search box and the empty filter bar is deliberate. The
     * endpoint takes limit and offset and nothing else, so a box here would
     * search the twenty-five rows on screen and quietly lie about the rest. An
     * invoice is found from its order.
     *
     * The document is fetched with the admin token rather than linked to,
     * because it is behind admin authentication and a link cannot carry a
     * header. See $lib/download.js.
     */
    import { api, can } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { downloadFile, openDocument, safeFilename } from "$lib/download.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const PER_PAGE = 25;

    const list = listState({ page: 1 });

    let invoices = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    const missing = $derived(modulesKnown() && !hasModule("invoices"));

    $effect(() => {
        list.params;
        // hasModule() reads a `$state` object, so this re-runs when the answer
        // arrives — and a store without ext/invoices never makes the call.
        if (can("orders.read") && hasModule("invoices")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get("/api/admin/x/invoices" + list.query({ limit: PER_PAGE }));
            invoices = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (err.status !== 404) toast.error(err);
            invoices = [];
            meta = null;
        } finally {
            loading = false;
        }
    }

    /* The path parameter is the ORDER's id and not the invoice's — the row
       carries both, and this is the single easiest thing to get wrong here. */
    const documentPath = (row) => `/api/admin/x/invoices/${row.order_id}`;
    const documentName = (row) => safeFilename(row.number, "html");
</script>

<svelte:head><title>Invoices · GoCommerce</title></svelte:head>

{#if !can("orders.read")}
    <NoAccess right="orders.read" what="invoices" />
{:else}
    <div class="page page-invoices">
        <div class="page-content full-height">
            <header class="page-header">
                <nav class="breadcrumbs"><div>Invoices</div></nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        onclick={() => load()}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
            </header>

            {#if missing}
                <ModuleMissing
                    module="invoices"
                    what="Numbered invoices are issued by ext/invoices, and this binary does not have it."
                />
            {:else}
                <div class="wrapper m-b-sm">
                    <div class="field-help">
                        Invoices are listed newest first, one per paid order. There is no search —
                        an invoice is found from its order.
                    </div>
                    <!-- The question an operator looking at a wrong seller name
                         has no other way to answer. -->
                    <div class="field-help">
                        The seller name, address and tax ID printed on every document come from the
                        store's own <code class="txt-code">main()</code> and cannot be changed from
                        here.
                    </div>
                </div>

                <div class="page-table-wrapper">
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Number</th>
                                <th class="col-field-type-date min-width">Issued</th>
                                <th class="col-field-type-number min-width">Total</th>
                                <th class="col-field-type-number min-width">Order</th>
                                <th class="col-meta min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each invoices as row (row.id)}
                                <tr class="handle" onclick={() => openDocument(documentPath(row), documentName(row))}>
                                    <td class="col-field-name-id" data-name="Number">
                                        <span class="txt-code txt-bold">{row.number}</span>
                                    </td>
                                    <td
                                        class="col-field-type-date min-width txt-hint"
                                        data-name="Issued"
                                    >
                                        {formatDate(row.issued_at)}
                                    </td>
                                    <td
                                        class="col-field-type-number min-width txt-bold"
                                        data-name="Total"
                                    >
                                        {formatMoney(row.total)}
                                    </td>
                                    <!-- Plain text, not a link: an order has no
                                         address of its own in the panel yet, so
                                         a link would go nowhere useful. -->
                                    <td
                                        class="col-field-type-number min-width txt-hint"
                                        data-name="Order"
                                    >
                                        #{row.order_id}
                                    </td>
                                    <td class="col-meta min-width">
                                        <div class="inline-flex gap-5">
                                            <button
                                                type="button"
                                                class="btn circle sm transparent secondary"
                                                title="Open for printing"
                                                aria-label="Open invoice {row.number} for printing"
                                                onclick={(e) => {
                                                    e.stopPropagation();
                                                    openDocument(documentPath(row), documentName(row));
                                                }}
                                            >
                                                <i class="ri-printer-line" aria-hidden="true"></i>
                                            </button>
                                            <button
                                                type="button"
                                                class="btn circle sm transparent secondary"
                                                title="Download"
                                                aria-label="Download invoice {row.number}"
                                                onclick={(e) => {
                                                    e.stopPropagation();
                                                    downloadFile(documentPath(row), documentName(row));
                                                }}
                                            >
                                                <i class="ri-download-2-line" aria-hidden="true"></i>
                                            </button>
                                        </div>
                                    </td>
                                </tr>
                            {/each}

                            {#if loading && !invoices.length}
                                {#each Array(5) as _, i (i)}
                                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {:else if !invoices.length}
                                <tr>
                                    <td colspan="5" class="txt-center txt-hint p-base">
                                        No invoices yet. One is issued for each order as it is paid.
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                <footer class="page-footer">
                    <Pager {meta} {loading} noun="invoice" onpage={(n) => list.setPage(n)} />
                    <div class="flex-fill"></div>
                    <ThemeToggle />
                </footer>
            {/if}
        </div>
    </div>
{/if}
