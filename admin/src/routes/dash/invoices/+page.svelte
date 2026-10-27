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
    import { base } from "$app/paths";
    import { api, can } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { downloadFile, openDocument, safeFilename } from "$lib/download.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import { titled } from "$lib/brand.svelte.js";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    const list = listState({ page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);


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
            const result = await api.get("/api/admin/x/invoices" + list.query({ limit: perPage }));
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

    /* The Tally export (D127). The ledger names' 404 is how a store that
       issues no GST tax invoices says so, and then none of it is drawn. */
    const TALLY = "/api/admin/x/invoices/tally";
    const LEDGER_FIELDS = [
        { key: "company_name", label: "Tally company", hint: "Empty imports into the company open in Tally." },
        { key: "sales_ledger", label: "Sales ledger" },
        { key: "cgst", label: "CGST ledger" },
        { key: "sgst", label: "SGST ledger" },
        { key: "utgst", label: "UTGST ledger" },
        { key: "igst", label: "IGST ledger" },
        { key: "freight", label: "Delivery ledger" },
        { key: "unregistered_party", label: "Party for buyers without a GSTIN" },
        { key: "voucher_type", label: "Voucher type" },
    ];

    let ledgers = $state(null);
    let saving = $state(false);
    let downloading = $state("");

    /* A local date, not toISOString's UTC one, which is yesterday for an
       evening in India. */
    const day = (d) =>
        `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
    const now = new Date();
    let to = $state(day(now));
    let from = $state(`${now.getMonth() < 3 ? now.getFullYear() - 1 : now.getFullYear()}-04-01`);

    $effect(() => {
        if (can("invoices.read") && hasModule("invoices")) loadLedgers();
    });

    async function loadLedgers() {
        try {
            ledgers = await api.get(`${TALLY}/ledgers`);
        } catch (err) {
            if (err.status !== 404) toast.error(err);
            ledgers = null;
        }
    }

    async function saveLedgers() {
        if (saving) return;
        saving = true;
        try {
            ledgers = await api.put(`${TALLY}/ledgers`, ledgers);
            toast.success("Ledger names saved");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function exportPart(part) {
        if (downloading) return;
        downloading = part;
        try {
            await downloadFile(
                `${TALLY}?part=${part}&from=${from}&to=${to}`,
                `tally-${part}-${from}-${to}.xml`,
                "application/xml",
            );
        } finally {
            downloading = "";
        }
    }

    /* The path parameter is the ORDER's id and not the invoice's — the row
       carries both, and this is the single easiest thing to get wrong here. */
    const documentPath = (row) => `/api/admin/x/invoices/${row.order_id}`;
    const documentName = (row) => safeFilename(row.number, "html");
</script>

<svelte:head><title>{titled("Invoices")}</title></svelte:head>

{#if !can("orders.read")}
    <NoAccess right="orders.read" what="invoices" />
{:else}
    <div class="page page-invoices shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Invoices</div>
            </nav>

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
                        {#if invoices.some((row) => row.kind === "tax_invoice")}
                            GST tax invoices, marked GST, take the seller's name, address and GSTIN
                            from Settings as they were when each invoice was issued.
                        {:else}
                            The seller name, address and tax ID printed on every document come from the
                            store's own <code class="txt-code">main()</code> and cannot be changed from
                            here.
                        {/if}
                    </div>
                </div>

                {#if ledgers}
                    <section class="card m-b-sm">
                        <h2 class="tw:mb-3 tw:text-sm tw:font-semibold">Export for Tally</h2>
                        <form class="grid" onsubmit={(e) => e.preventDefault()}>
                            <div class="col-md-3">
                                <div class="field">
                                    <label for="tally-from">From</label>
                                    <input id="tally-from" type="date" bind:value={from} max={to || undefined} />
                                </div>
                            </div>
                            <div class="col-md-3">
                                <div class="field">
                                    <label for="tally-to">To</label>
                                    <input id="tally-to" type="date" bind:value={to} min={from || undefined} />
                                </div>
                            </div>
                            <div class="col-12">
                                <div class="field-help m-b-sm">
                                    Import the ledgers first, then the vouchers: in TallyPrime, Alt+O, Masters,
                                    then Alt+O, Transactions. Importing a voucher again replaces it.
                                </div>
                                <div class="inline-flex gap-sm">
                                    {#each ["ledgers", "vouchers"] as part, i (part)}
                                        <button
                                            type="button"
                                            class="btn sm secondary"
                                            disabled={!from || !to || !!downloading}
                                            aria-busy={downloading === part}
                                            onclick={() => exportPart(part)}
                                        >
                                            <i class="ri-download-2-line" aria-hidden="true"></i>
                                            <span class="txt">
                                                {downloading === part ? "Downloading…" : `${i + 1}. ${part === "ledgers" ? "Ledgers" : "Vouchers"}`}
                                            </span>
                                        </button>
                                    {/each}
                                </div>
                            </div>
                        </form>

                        <details class="map-fold m-t-sm">
                            <summary>
                                <span class="map-fold-title">Ledger names</span>
                                <span class="txt-hint txt-sm">Match the ledgers in your Tally company</span>
                                <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                            </summary>
                            <form class="grid m-t-sm" onsubmit={(e) => (e.preventDefault(), saveLedgers())}>
                                {#each LEDGER_FIELDS as f (f.key)}
                                    <div class="col-md-4">
                                        <div class="field" class:readonly={!can("invoices.configure")}>
                                            <label for="tally-{f.key}">{f.label}</label>
                                            <input
                                                id="tally-{f.key}"
                                                type="text"
                                                maxlength="100"
                                                readonly={!can("invoices.configure")}
                                                bind:value={ledgers[f.key]}
                                            />
                                            {#if f.hint}<div class="field-help">{f.hint}</div>{/if}
                                        </div>
                                    </div>
                                {/each}
                                <div class="col-12">
                                    <div class="field">
                                        <input
                                            id="tally-per-rate"
                                            type="checkbox"
                                            disabled={!can("invoices.configure")}
                                            bind:checked={ledgers.sales_per_rate}
                                        />
                                        <label for="tally-per-rate">
                                            One sales ledger per rate, named like “{ledgers.sales_ledger} 18%”
                                        </label>
                                    </div>
                                </div>
                                <div class="col-12">
                                    {#if can("invoices.configure")}
                                        <button type="submit" class="btn sm" disabled={saving}>
                                            <span class="txt">{saving ? "Saving…" : "Save ledger names"}</span>
                                        </button>
                                    {:else}
                                        <div class="field-help">Changing these needs invoices.configure.</div>
                                    {/if}
                                </div>
                            </form>
                        </details>
                    </section>
                {/if}

                <div class="page-table-wrapper tw:rounded-xl tw:border">
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
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => openDocument(documentPath(row), documentName(row))}
                                    onkeydown={(e) => rowKey(e, () => openDocument(documentPath(row), documentName(row)))}
                                >
                                    <td class="col-field-name-id" data-name="Number">
                                        <span class="inline-flex gap-5">
                                            <span class="txt-code txt-bold">{row.number}</span>
                                            {#if row.kind === "tax_invoice"}
                                                <span class="label" title="A GST tax invoice">GST</span>
                                            {/if}
                                        </span>
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
                                <tr class="empty-row">
                                    <td colspan="5">
                                        <EmptyState
                                            icon="ri-file-list-3-line"
                                            title="No invoices yet"
                                            text="One is issued for each order as it is paid, so the first paid order brings the first invoice."
                                        >
                                            <a class="btn secondary" href="{base}/dash/orders">
                                                <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                                                <span class="txt">Go to orders</span>
                                            </a>
                                        </EmptyState>
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
                        noun="invoice"
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
