<script>
    /**
     * Export: the store's data as files.
     *
     * Import used to live under this on the same page. They are two jobs with
     * two rights — the engine's export routes name data.export and its import
     * routes name data.import — and one page could only be linked from the
     * settings rail under one of them, so an operator who may import and not
     * export had no way in at all. Two screens, two links, two rights.
     */
    import { api, apiErrorFrom, can, getToken, query } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const mayExport = $derived(can("data.export"));

    /*
     * One dialect for every file that leaves. The store's own layout carries
     * everything; Shopify's is the same model in Shopify's column names, so
     * the file opens in every tool built for it and imports into a Shopify
     * store unchanged.
     */
    let exportFormat = $state("gocommerce");
    const formats = [
        { value: "gocommerce", label: "GoCommerce CSV" },
        { value: "shopify", label: "Shopify CSV" },
    ];

    // The export filters, and the two vocabularies they need. The category
    // control is a CategoryPicker rather than a Select on purpose: the taxonomy
    // import can put fourteen thousand rows in this tree, and the listing
    // answers with a bounded slice plus the real total — which a plain Select
    // would silently render as a prefix.
    let exportQ = $state("");
    let exportStatus = $state("");
    let exportCategory = $state(null);
    let exportCollection = $state("");
    /* The orders file's own filters. `to` is exclusive, which the label says
       and the sentence under the controls restates: this is the one place a
       reader could mistake it for "up to and including", and an accountant
       given a file missing its last day would not notice for a month. */
    let exportOrderStatus = $state("");
    let exportOrderFrom = $state("");
    let exportOrderTo = $state("");
    let categories = $state([]);
    let categoriesTruncated = $state(false);
    let collections = $state([]);

    $effect(() => {
        loadVocabulary();
    });

    async function loadVocabulary() {
        if (!mayExport) return;
        try {
            const [cats, cols] = await Promise.all([
                api.get("/api/admin/categories?flat=1"),
                api.get("/api/admin/collections" + query({ limit: 200 })),
            ]);
            categories = cats.data ?? [];
            categoriesTruncated = (cats.meta?.total ?? 0) > categories.length;
            collections = cols.data ?? [];
        } catch (err) {
            toast.error(err);
        }
    }

    // The file is named for what it holds and the dialect it is in, so two
    // on one desk can be told apart.
    const fileName = (kind) => (exportFormat === "shopify" ? `${kind}-shopify.csv` : `${kind}.csv`);

    function exportProducts() {
        const suffix = query({
            q: exportQ,
            status: exportStatus,
            category_id: exportCategory,
            collection_id: exportCollection,
            format: exportFormat,
        });
        download("/api/admin/export/admin-products" + suffix, fileName("products"));
    }

    function exportOrders() {
        const suffix = query({
            status: exportOrderStatus,
            from: exportOrderFrom,
            to: exportOrderTo,
            format: exportFormat,
        });
        download("/api/admin/export/admin-orders" + suffix, fileName("orders"));
    }

    function exportCustomers() {
        download("/api/admin/export/admin-customers" + query({ format: exportFormat }), fileName("customers"));
    }

    function exportInventory() {
        download("/api/admin/export/admin-inventory" + query({ format: exportFormat }), fileName("inventory"));
    }

    /*
     * The three files that have one layout and no Shopify dialect, so none of
     * them carries the format switch: a category tree, the reviews, and the
     * menus. Shopify's category export is its taxonomy file, which the Import
     * screen loads; its reviews and menus are not CSV at all.
     */
    function exportCategories() {
        download("/api/admin/export/admin-categories", "categories.csv");
    }

    function exportReviews() {
        download("/api/admin/x/reviews/export", "reviews.csv");
    }

    function exportMenus() {
        download("/api/admin/x/navigation/export", "menus.csv");
    }

    /* The window in words, so the exclusive bound is stated in the form a
       person reads rather than only in the label above the picker. */
    const exportOrderWindow = $derived.by(() => {
        if (!exportOrderFrom && !exportOrderTo) return "";
        const spoken = (iso, shift) => {
            const d = new Date(iso + "T00:00:00");
            if (shift) d.setDate(d.getDate() + shift);
            return formatDate(d.toISOString(), { withTime: false });
        };
        if (exportOrderFrom && exportOrderTo) {
            return `Orders placed from ${spoken(exportOrderFrom)} to ${spoken(exportOrderTo, -1)}, inclusive. `;
        }
        if (exportOrderFrom) return `Orders placed from ${spoken(exportOrderFrom)} onwards. `;
        return `Orders placed up to ${spoken(exportOrderTo, -1)}, inclusive. `;
    });

    async function download(path, filename) {
        try {
            // A fetch rather than a plain link, because the export needs the
            // admin token and a link cannot carry a header.
            const response = await fetch(path, {
                headers: { Authorization: "Bearer " + getToken() },
            });
            if (!response.ok) {
                // The engine says why — the rights middleware refuses by name —
                // and a bare status code throws that away. Nothing has read the
                // body yet, so apiErrorFrom is free to.
                toast.error(await apiErrorFrom(response));
                return;
            }
            const blob = await response.blob();
            const url = URL.createObjectURL(blob);
            const a = document.createElement("a");
            a.href = url;
            a.download = filename;
            a.click();
            URL.revokeObjectURL(url);
            toast.success("Export downloaded");
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>Export · GoCommerce</title></svelte:head>

{#if !mayExport}
    <!-- The settings rail already hides the link; this is the direct URL. -->
    <NoAccess right="data.export" what="exports" />
{:else}
<div class="page page-data shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Export</div>
            </nav>
        </header>

        <div class="wrapper m-b-base">
            <h6 class="section-title">
                <i class="ri-download-2-line" aria-hidden="true"></i>
                Export
            </h6>

            <div class="field-help m-b-sm">
                One row per variant, and one row per order line — the shape a spreadsheet and
                an accountant actually want. Cells that begin with <code>=</code>,
                <code>+</code>, <code>-</code> or <code>@</code> are escaped so opening the
                file cannot run them; importing strips the escape again, so the round trip is
                lossless.
            </div>

            <div class="field" style="max-width: 320px">
                <label for="export-format">Layout</label>
                <Select id="export-format" bind:value={exportFormat} options={formats} />
            </div>
            <div class="field-help m-b-base">
                {#if exportFormat === "shopify"}
                    Shopify's own column names — Handle, Variant SKU, Image Src and the rest —
                    so the file opens in every tool built for them and imports into a Shopify
                    store as it is. Prices are decimals in the store's currency.
                {:else}
                    The store's own layout: every field, prices in minor units, one stock
                    column per location, and pictures as URLs.
                {/if}
            </div>

            <h6 class="section-title">
                <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                Products
            </h6>

            <div class="grid m-b-sm">
                <div class="col-6">
                    <div class="field">
                        <label for="export-q">Search</label>
                        <input
                            id="export-q"
                            type="text"
                            placeholder="Title or description"
                            bind:value={exportQ}
                        />
                    </div>
                </div>
                <div class="col-6">
                    <div class="field">
                        <label for="export-status">Status</label>
                        <Select
                            id="export-status"
                            bind:value={exportStatus}
                            options={[
                                { value: "", label: "Any status" },
                                { value: "draft", label: "Draft" },
                                { value: "active", label: "Active" },
                                { value: "archived", label: "Archived" },
                            ]}
                        />
                    </div>
                </div>
                <div class="col-6">
                    <div class="field">
                        <label for="export-category">Category</label>
                        <CategoryPicker
                            id="export-category"
                            bind:value={exportCategory}
                            {categories}
                            remote={categoriesTruncated}
                            placeholder="Any category"
                        />
                    </div>
                </div>
                <div class="col-6">
                    <div class="field">
                        <label for="export-collection">Collection</label>
                        <Select
                            id="export-collection"
                            bind:value={exportCollection}
                            options={[
                                { value: "", label: "Any collection" },
                                ...collections.map((c) => ({
                                    value: String(c.id),
                                    label: c.title,
                                })),
                            ]}
                        />
                    </div>
                </div>
            </div>

            <div class="field-help m-b-sm">
                These filters apply to the products file, and it exports every row that
                matched rather than a page of them. A category takes everything nested
                under it; the collection list is the first 200. Vendor, product type and
                tag are honoured by the endpoint but have no control here, because the
                panel has no vocabulary of them to offer — the product editor distils its
                suggestions from a sample, which would quietly miss anything past it.
            </div>

            <div class="flex gap-10 m-b-base">
                <button type="button" class="btn secondary" onclick={exportProducts}>
                    <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                    <span class="txt">Products CSV</span>
                </button>
                <button type="button" class="btn secondary" onclick={exportInventory}>
                    <i class="ri-archive-line" aria-hidden="true"></i>
                    <span class="txt">Inventory CSV</span>
                </button>
            </div>
            <div class="field-help m-b-base">
                The inventory file is the stock-take's shape: every variant's count at every
                location, and nothing else. Edit the counts and import it back.
            </div>

            <!--
                The orders file, with the period and the status the endpoint has
                always taken. This button sent a bare path, so a store with any
                history got one row per line for every order ever placed — and
                "the last quarter", "just the refunded ones" and "everything
                since the reconciliation" were curl-only.
            -->
            <h6 class="section-title">
                <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                Orders
            </h6>

            <div class="grid m-b-sm">
                <div class="col-4">
                    <div class="field">
                        <label for="export-order-status">Status</label>
                        <Select
                            id="export-order-status"
                            bind:value={exportOrderStatus}
                            options={[
                                { value: "", label: "Any status" },
                                { value: "pending", label: "Pending" },
                                { value: "confirmed", label: "Confirmed" },
                                { value: "partial", label: "Partly shipped" },
                                { value: "shipped", label: "Shipped" },
                                { value: "delivered", label: "Delivered" },
                                { value: "cancelled", label: "Cancelled" },
                            ]}
                        />
                    </div>
                </div>
                <div class="col-4">
                    <div class="field">
                        <label for="export-order-from">From</label>
                        <input id="export-order-from" type="date" bind:value={exportOrderFrom} />
                    </div>
                </div>
                <div class="col-4">
                    <div class="field">
                        <label for="export-order-to">To (exclusive)</label>
                        <input id="export-order-to" type="date" bind:value={exportOrderTo} />
                    </div>
                </div>
            </div>

            <div class="field-help m-b-sm">
                {#if exportOrderWindow}
                    {exportOrderWindow}
                {:else}
                    Leave the dates empty and the whole order book is exported.
                {/if}
                <code>To</code> is exclusive, as it is everywhere else in this API — an order
                placed on that day is not in the file. The endpoint filters on status and the
                period and on nothing else; the orders screen has the search and the payment
                filter, and an Export button that carries what it can.
            </div>

            <div class="flex gap-10">
                <button type="button" class="btn secondary" onclick={exportOrders}>
                    <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                    <span class="txt">Orders CSV</span>
                </button>
                <button type="button" class="btn secondary" onclick={exportCustomers}>
                    <i class="ri-user-3-line" aria-hidden="true"></i>
                    <span class="txt">Customers CSV</span>
                </button>
            </div>
            <div class="field-help m-t-sm">
                Customers are a reading of the orders — one row per person, with what they
                have spent — so the file leaves but does not come back: there is no customer
                to import into.
            </div>

            <!--
                The structural files. None of them has a Shopify dialect, so
                none of them takes the format switch above: Shopify's category
                export is its taxonomy file, which the block below imports, and
                its reviews and menus are not CSV at all.
            -->
            <h6 class="section-title">
                <i class="ri-stack-line" aria-hidden="true"></i>
                Structure
            </h6>

            <div class="field-help m-b-sm">
                The tree, the menus and what shoppers have said — the parts of a store that
                are neither catalogue nor ledger. Each has one layout, and each imports back
                from the box below.
            </div>

            <div class="flex gap-10 flex-wrap">
                <button type="button" class="btn secondary" onclick={exportCategories}>
                    <i class="ri-node-tree" aria-hidden="true"></i>
                    <span class="txt">Categories CSV</span>
                </button>
                {#if hasModule("reviews")}
                    <button type="button" class="btn secondary" onclick={exportReviews}>
                        <i class="ri-star-line" aria-hidden="true"></i>
                        <span class="txt">Reviews CSV</span>
                    </button>
                {/if}
                {#if hasModule("navigation")}
                    <button type="button" class="btn secondary" onclick={exportMenus}>
                        <i class="ri-menu-line" aria-hidden="true"></i>
                        <span class="txt">Menus CSV</span>
                    </button>
                {/if}
            </div>
            <div class="field-help m-t-sm">
                The category file adds and updates; it never moves or deletes, because one
                typo in a path would otherwise drag a subtree and the products under it
                somewhere else, and a spreadsheet has no undo. Moving a category stays on the
                Categories screen, which shows what is nested under it first.
            </div>

        </div>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">CSV out — the same shape it goes back in</span>
        </footer>
    </div>
</div>
{/if}
