<script>
    import { api, apiErrorFrom, can, getToken, query, request } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import SettingsSidebar from "$lib/components/SettingsSidebar.svelte";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import Select from "$lib/components/Select.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    let importing = $state(false);
    let dryRun = $state(true);
    let fireEvents = $state(false);
    let kind = $state("products");
    let csv = $state("");
    let result = $state(null);
    let fileInput;

    // The export filters, and the two vocabularies they need. The category
    // control is a CategoryPicker rather than a Select on purpose: this is the
    // screen with the taxonomy-import button on it, so the tree here can be
    // fourteen thousand rows, and the listing answers with a bounded slice plus
    // the real total — which a plain Select would silently render as a prefix.
    let exportQ = $state("");
    let exportStatus = $state("");
    let exportCategory = $state(null);
    let exportCollection = $state("");
    let categories = $state([]);
    let categoriesTruncated = $state(false);
    let collections = $state([]);

    // Taxonomy import, the other half of this screen.
    let importingTaxonomy = $state(false);
    let importingFields = $state(false);
    let taxonomyResult = $state(null);
    let fieldsResult = $state(null);
    let confirmOpen = $state(false);

    $effect(() => {
        loadVocabulary();
    });

    async function loadVocabulary() {
        if (!can("data.export")) return;
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

    function exportProducts() {
        const suffix = query({
            q: exportQ,
            status: exportStatus,
            category_id: exportCategory,
            collection_id: exportCollection,
        });
        download("/api/admin/export/admin-products" + suffix, "products.csv");
    }

    async function importTaxonomy() {
        if (importingTaxonomy) return;
        importingTaxonomy = true;
        taxonomyResult = null;
        try {
            // No body: an empty one means the embedded set, and that call also
            // brings the field definitions with it.
            taxonomyResult = await request("POST", "/api/admin/import/taxonomy");
            toast.success(
                `${taxonomyResult.categories.created} categories added, ` +
                    `${taxonomyResult.categories.matched} already there`,
            );
        } catch (err) {
            toast.error(err);
        } finally {
            importingTaxonomy = false;
        }
    }

    async function importFields() {
        if (importingFields) return;
        importingFields = true;
        fieldsResult = null;
        try {
            fieldsResult = await request("POST", "/api/admin/import/category-attributes");
            toast.success(
                `${fieldsResult.attributes} fields defined, ` +
                    `${fieldsResult.categories} categories given theirs`,
            );
        } catch (err) {
            toast.error(err);
        } finally {
            importingFields = false;
        }
    }

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

    function pickFile() {
        fileInput?.click();
    }

    async function onFile(event) {
        const file = event.target.files?.[0];
        if (!file) return;
        csv = await file.text();
        toast.info(`Loaded ${file.name}`);
        event.target.value = "";
    }

    async function runImport() {
        if (!csv.trim() || importing) return;
        importing = true;
        result = null;
        try {
            const params = new URLSearchParams();
            if (dryRun) params.set("dry_run", "1");
            if (kind === "orders" && fireEvents) params.set("fire_events", "1");
            const suffix = params.toString() ? "?" + params.toString() : "";

            result = await request("POST", `/api/admin/import/${kind}${suffix}`, {
                body: csv,
                headers: { "Content-Type": "text/csv" },
            });

            if (result.errors?.length) {
                toast.warning(`${result.errors.length} row(s) had problems`);
            } else if (dryRun) {
                toast.success("Dry run looks good — nothing was written");
            } else {
                toast.success(`Imported: ${result.created} created, ${result.updated} updated`);
            }
        } catch (err) {
            toast.error(err);
        } finally {
            importing = false;
        }
    }
</script>

<div class="page page-data">
    <SettingsSidebar />

    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div>Settings</div>
                <div>Import / export</div>
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
                The filters apply to the products file only, and it exports every row that
                matched rather than a page of them. A category takes everything nested
                under it; the collection list is the first 200. Vendor, product type and
                tag are honoured by the endpoint but have no control here, because the
                panel has no vocabulary of them to offer — the product editor distils its
                suggestions from a sample, which would quietly miss anything past it.
            </div>

            <div class="flex gap-10">
                <button type="button" class="btn secondary" onclick={exportProducts}>
                    <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                    <span class="txt">Products CSV</span>
                </button>
                <button
                    type="button"
                    class="btn secondary"
                    onclick={() => download("/api/admin/export/admin-orders", "orders.csv")}
                >
                    <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                    <span class="txt">Orders CSV</span>
                </button>
            </div>

            {#if can("data.import")}
                <h6 class="section-title">
                    <i class="ri-node-tree" aria-hidden="true"></i>
                    Category taxonomy
                </h6>

                <div class="field-help m-b-sm">
                    Shopify's standard taxonomy ships inside this binary: about 14,000
                    categories, plus the fields each one asks of a product. Nothing imports
                    it for you, because a store that wanted six categories of its own
                    should not find fourteen thousand of Shopify's in it after an upgrade.
                    It is idempotent, so running it twice adds only what is missing — which
                    is also the remedy if a run times out. It can take a minute.
                </div>

                <div class="flex gap-10">
                    <button
                        type="button"
                        class="btn secondary"
                        class:loading={importingTaxonomy}
                        disabled={importingTaxonomy}
                        onclick={() => (confirmOpen = true)}
                    >
                        <i class="ri-download-cloud-2-line" aria-hidden="true"></i>
                        <span class="txt">Import the Shopify taxonomy</span>
                    </button>
                    <button
                        type="button"
                        class="btn secondary"
                        class:loading={importingFields}
                        disabled={importingFields}
                        onclick={importFields}
                    >
                        <i class="ri-list-settings-line" aria-hidden="true"></i>
                        <span class="txt">Field definitions only</span>
                    </button>
                </div>

                <div class="field-help m-t-sm">
                    The tree import brings the field definitions with it. The second button
                    is for a tree that is already in place — and it only attaches fields to
                    categories that came from this same source, since it matches them by
                    the taxonomy id the tree import wrote.
                </div>

                {#if taxonomyResult}
                    <div class="grid m-t-base">
                        <div class="col-4">
                            <div class="stat-card">
                                <span class="stat-label">Created</span>
                                <span class="stat-value txt-success"
                                    >{taxonomyResult.categories.created}</span
                                >
                            </div>
                        </div>
                        <div class="col-4">
                            <div class="stat-card">
                                <span class="stat-label">Already present</span>
                                <span class="stat-value">{taxonomyResult.categories.matched}</span>
                            </div>
                        </div>
                        <div class="col-4">
                            <div class="stat-card">
                                <span class="stat-label">Skipped</span>
                                <span class="stat-value txt-hint"
                                    >{taxonomyResult.categories.skipped}</span
                                >
                            </div>
                        </div>
                    </div>
                {/if}

                {#if taxonomyResult?.attributes || fieldsResult}
                    {@const fields = fieldsResult ?? taxonomyResult.attributes}
                    <div class="grid m-t-base">
                        <div class="col-4">
                            <div class="stat-card">
                                <span class="stat-label">Fields defined</span>
                                <span class="stat-value">{fields.attributes}</span>
                            </div>
                        </div>
                        <div class="col-4">
                            <div class="stat-card">
                                <span class="stat-label">Categories</span>
                                <span class="stat-value">{fields.categories}</span>
                            </div>
                        </div>
                        <div class="col-4">
                            <div class="stat-card">
                                <span class="stat-label">Unmatched</span>
                                <span class="stat-value txt-hint">{fields.unmatched}</span>
                            </div>
                        </div>
                    </div>
                {/if}
            {/if}

            <h6 class="section-title">
                <i class="ri-upload-2-line" aria-hidden="true"></i>
                Import
            </h6>

            <div class="field">
                <label for="kind">What is in the file</label>
                <Select
                    id="kind"
                    bind:value={kind}
                    options={[
                        { value: "products", label: "Products and variants" },
                        { value: "orders", label: "Historical orders" },
                    ]}
                />
            </div>

            <div class="flex gap-20 flex-wrap m-t-base">
                <div class="field">
                    <input type="checkbox" id="dry-run" class="switch" bind:checked={dryRun} />
                    <label for="dry-run">Dry run</label>
                </div>
                {#if kind === "orders"}
                    <div class="field">
                        <input
                            type="checkbox"
                            id="fire-events"
                            class="switch"
                            bind:checked={fireEvents}
                        />
                        <label for="fire-events">Fire events</label>
                    </div>
                {/if}
            </div>

            {#if dryRun}
                <div class="alert info m-t-base">
                    <p>
                        A dry run validates the whole file and rolls back, reporting what it
                        <em>would</em> have done. Worth doing first, always.
                    </p>
                </div>
            {/if}

            {#if kind === "orders" && fireEvents}
                <div class="alert warning m-t-base">
                    <p>
                        With events on, every imported order announces itself — which means
                        confirmation emails to people who bought something a year ago. Leave
                        this off for a migration.
                    </p>
                </div>
            {/if}

            <div class="field m-t-base">
                <label for="csv">CSV</label>
                <textarea
                    id="csv"
                    class="txt-code"
                    rows="8"
                    placeholder="Paste CSV here, or choose a file"
                    bind:value={csv}
                ></textarea>
            </div>

            <input
                type="file"
                accept=".csv,text/csv"
                bind:this={fileInput}
                onchange={onFile}
                hidden
            />

            <div class="flex gap-10 m-t-base">
                <button type="button" class="btn secondary" onclick={pickFile}>
                    <i class="ri-folder-open-line" aria-hidden="true"></i>
                    <span class="txt">Choose a file…</span>
                </button>
                <div class="flex-fill"></div>
                {#if csv}
                    <button
                        type="button"
                        class="btn transparent secondary"
                        onclick={() => (csv = "")}
                    >
                        <span class="txt">Clear</span>
                    </button>
                {/if}
                <button
                    type="button"
                    class="btn expanded"
                    class:loading={importing}
                    disabled={!csv.trim() || importing}
                    onclick={runImport}
                >
                    <span class="txt">{dryRun ? "Dry run" : "Import"}</span>
                </button>
            </div>

            {#if result}
                <h6 class="section-title">
                    <i
                        class={result.errors?.length
                            ? "ri-error-warning-line"
                            : "ri-checkbox-circle-line"}
                        aria-hidden="true"
                    ></i>
                    {result.dry_run ? "Dry run result" : "Import result"}
                    <span class="label">{result.duration}</span>
                </h6>

                <div class="grid">
                    <div class="col-4">
                        <div class="stat-card">
                            <span class="stat-label">Created</span>
                            <span class="stat-value txt-success">{result.created}</span>
                        </div>
                    </div>
                    <div class="col-4">
                        <div class="stat-card">
                            <span class="stat-label">Updated</span>
                            <span class="stat-value">{result.updated}</span>
                        </div>
                    </div>
                    <div class="col-4">
                        <div class="stat-card">
                            <span class="stat-label">Skipped</span>
                            <span class="stat-value txt-hint">{result.skipped}</span>
                        </div>
                    </div>
                </div>

                {#if result.errors?.length}
                    <h6 class="section-title">Rows that need attention</h6>

                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-type-number min-width">Line</th>
                                <th>Problem</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each result.errors as row, i (i)}
                                <tr>
                                    <td
                                        class="col-field-type-number min-width txt-code"
                                        data-name="Line"
                                    >
                                        {row.line}
                                    </td>
                                    <td class="txt-danger" data-name="Problem">{row.message}</td>
                                </tr>
                            {/each}
                        </tbody>
                    </table>

                    <div class="field-help">
                        A bad row never aborts the file — the good ones still applied.
                    </div>
                {/if}
            {/if}
        </div>

        <footer class="page-footer">
            <span class="txt">CSV in, CSV out — the same shape both ways</span>
            <ThemeToggle />
        </footer>

        <Confirm
            bind:open={confirmOpen}
            title="Import the Shopify taxonomy?"
            message="It writes about 14,000 categories into this store. Running it again later only adds what is missing, so it is safe to repeat — but there is no one-click way to take them out."
            confirmLabel="Import"
            onconfirm={importTaxonomy}
        />
    </div>
</div>
