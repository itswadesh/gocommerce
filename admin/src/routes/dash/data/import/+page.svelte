<script>
    /**
     * Import: files into the store.
     *
     * Split from Export, which used to sit above it on the same page. They are
     * two jobs with two rights, and one page could only be linked from the
     * settings rail under one of them — so an operator who may import and not
     * export had no link to this at all.
     */
    import { can, request } from "$lib/api.js";
    import { detectDialect, importBlocker, importKinds as kindsFor, importUrl } from "$lib/importer.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const mayImport = $derived(can("data.import"));

    /* Which of the two run buttons is working: "dry", "import", or "" for
       neither. Both wait while either runs, and the pressed one spins. */
    let running = $state("");
    let fireEvents = $state(false);
    // Overwrite is Shopify's own switch, with the opposite default: a
    // re-import of an edited export is what most files here are, and it
    // has to update what it finds.
    let overwrite = $state(true);
    let kind = $state("products");
    let csv = $state("");
    let result = $state(null);
    let fileInput = $state(null);

    // Taxonomy import, the other half of this screen.
    let importingTaxonomy = $state(false);
    let importingFields = $state(false);
    let taxonomyResult = $state(null);
    let fieldsResult = $state(null);
    let confirmOpen = $state(false);

    /* The engine reads the dialect off the header, so there is no switch to
       get wrong; the line under the textarea says what it saw. */
    const detected = $derived(detectDialect(kind, csv));

    /* What stops a run, printed beside the buttons so a greyed-out one is
       never left unexplained. */
    const blocker = $derived(importBlocker(csv));

    /* What this store can actually be given; a module that is not installed
       contributes no tab. */
    const importKinds = $derived(kindsFor(hasModule));

    /* A kind the store lost — the module went away while this screen was
       open, or a stale URL — falls back rather than posting into nothing. */
    $effect(() => {
        if (!importKinds.some((k) => k.value === kind)) kind = "products";
    });

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

    /*
     * Both buttons come here. A dry run and an import are one request apart —
     * the engine rolls the first back — so they share everything but the
     * flag, and neither button's name depends on a switch. When the name did
     * depend on one ("Dry run", on by default), the screen never showed a
     * button called Import at all.
     */
    async function runImport(dry) {
        if (blocker || running) return;
        running = dry ? "dry" : "import";
        result = null;
        try {
            result = await request("POST", importUrl(kind, { dryRun: dry, fireEvents, overwrite }), {
                body: csv,
                headers: { "Content-Type": "text/csv" },
            });

            if (result.errors?.length) {
                toast.warning(`${result.errors.length} row(s) had problems`);
            } else if (dry) {
                toast.success("Dry run looks good — nothing was written");
            } else {
                toast.success(`Imported: ${result.created} created, ${result.updated} updated`);
            }
        } catch (err) {
            toast.error(err);
        } finally {
            running = "";
        }
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
</script>

<svelte:head><title>Import · GoCommerce</title></svelte:head>

<!-- A pasted or loaded CSV that has not been run is the most expensive thing on
     this screen to lose: it may be a file somebody spent an afternoon on, and it
     is held nowhere but this textarea. It stops counting as unsaved once a run
     has reported, which is the point at which leaving is the ordinary thing to
     do. -->
<DirtyGuard
    dirty={!!csv.trim() && !result}
    message="The CSV in the import box has not been run. Leave and lose it?"
/>

{#if !mayImport}
    <!-- The settings rail already hides the link; this is the direct URL. -->
    <NoAccess right="data.import" what="imports" />
{:else}
<div class="page page-data page-import shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Import</div>
            </nav>
        </header>

        <div class="wrapper m-b-base">
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

                <!-- Wraps: on a 360px phone the second button ran off the
                     right edge, cut in half. -->
                <div class="flex gap-10 flex-wrap">
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

            <!-- Import is its own panel, not another heading in the column.
                 Everything above it hands a file out and is over in one
                 click; this is a form with a mode, a rehearsal switch and a
                 result that appears underneath. Giving it an edge is what
                 stops the result of an import reading as part of the export
                 controls above it. -->
            <section class="card import-panel">
            <h6 class="section-title">
                <i class="ri-upload-2-line" aria-hidden="true"></i>
                Import
            </h6>

            <!-- One tab per kind rather than a dropdown. What can be imported
                 is the first thing this section has to answer, and a closed
                 select answers it one option at a time: an operator holding a
                 reviews file had to open the list to learn whether reviews
                 were even possible here. The tabs also make the sentence
                 below them read as belonging to the chosen kind. -->
            <div class="tabs-header m-b-base" role="tablist" aria-label="What is in the file">
                {#each importKinds as k (k.value)}
                    <button
                        type="button"
                        role="tab"
                        id="kind-tab-{k.value}"
                        class="tab-item"
                        class:active={kind === k.value}
                        aria-selected={kind === k.value}
                        onclick={() => (kind = k.value)}
                    >
                        {k.label}
                    </button>
                {/each}
            </div>

            <div class="field-help">
                {#if kind === "products" || kind === "inventory" || kind === "orders"}
                    Either layout: the store's own, or a file exported from Shopify, as it is.
                    The engine reads which off the first line.
                {:else if kind === "categories"}
                    The trail from the root in one cell — <code>Apparel / Clothing / Shirts</code>
                    — with <code>slug</code>, <code>position</code> and <code>metadata</code> beside
                    it. A path the store already has is updated; one it does not have is built,
                    along with every step on the way to it.
                {:else if kind === "reviews"}
                    A row with an <code>id</code> updates that review; every other row is a new
                    one and needs <code>product_slug</code>, <code>name</code>,
                    <code>rating</code> and <code>body</code>. Blank cells are left alone, so
                    pasting one column of statuses back is a moderation run.
                {:else if kind === "menus"}
                    <code>menu</code> is the handle and <code>path</code> is the trail of titles.
                    Every menu the file names is rebuilt in the file's own row order; a menu it
                    does not name is untouched.
                {/if}
            </div>

            <!-- The switches that change what an import does. Dry run is not
                 one of them any more: it is a button beside Import. -->
            {#if kind === "products" || kind === "orders"}
                <div class="flex gap-20 flex-wrap m-t-base">
                    {#if kind === "products"}
                        <div class="field">
                            <input type="checkbox" id="overwrite" class="switch" bind:checked={overwrite} />
                            <label for="overwrite">Update products that already exist</label>
                        </div>
                    {/if}
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
            {/if}

            {#if kind === "products" && !overwrite}
                <div class="alert info m-t-base">
                    <p>
                        A product the store already has — same slug, or Handle — is left
                        exactly as it is, and only the new ones are created. That is how a
                        second catalogue is loaded beside the first without touching it.
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
                <!-- Only the three kinds that HAVE two dialects get the line
                     about which one this is. A category, review or menu file
                     has one layout, and telling its author it "does not look
                     like either" is a warning about a choice they were never
                     offered. -->
                {#if csv.trim() && (kind === "products" || kind === "inventory" || kind === "orders")}
                    <div class="field-help">
                        {#if detected === "shopify"}
                            Looks like a Shopify file.
                        {:else if detected === "gocommerce"}
                            Looks like the store's own layout.
                        {:else}
                            The first line does not look like either layout; the import will say
                            which column it is missing.
                        {/if}
                    </div>
                {/if}
            </div>

            <input
                type="file"
                accept=".csv,text/csv"
                bind:this={fileInput}
                onchange={onFile}
                hidden
            />

            <!-- Choose, then run: the file on the left, the two ways to run it
                 on the right. Dry run and Import are both always here, under
                 their own names. Until there is something to run they are
                 greyed out, and the line under them says so. -->
            <div class="import-run flex gap-10 flex-wrap m-t-base">
                <button type="button" class="btn secondary" onclick={pickFile}>
                    <i class="ri-folder-open-line" aria-hidden="true"></i>
                    <span class="txt">Choose a file…</span>
                </button>
                {#if csv}
                    <button
                        type="button"
                        class="btn transparent secondary"
                        onclick={() => (csv = "")}
                    >
                        <span class="txt">Clear</span>
                    </button>
                {/if}
                <div class="flex-fill"></div>
                <button
                    type="button"
                    class="btn secondary expanded"
                    class:loading={running === "dry"}
                    disabled={!!blocker || !!running}
                    aria-describedby={blocker ? "import-blocker" : undefined}
                    onclick={() => runImport(true)}
                >
                    <i class="ri-test-tube-line" aria-hidden="true"></i>
                    <span class="txt">Dry run</span>
                </button>
                <button
                    type="button"
                    class="btn expanded"
                    class:loading={running === "import"}
                    disabled={!!blocker || !!running}
                    aria-describedby={blocker ? "import-blocker" : undefined}
                    onclick={() => runImport(false)}
                >
                    <i class="ri-upload-2-line" aria-hidden="true"></i>
                    <span class="txt">Import</span>
                </button>
            </div>

            {#if blocker}
                <p id="import-blocker" class="field-help m-t-sm" in:rise>{blocker}</p>
            {/if}
            <div class="field-help m-t-sm">
                A dry run validates the whole file and rolls back, reporting what an import
                <em>would</em> do. Worth doing first, always.
            </div>

            {#if result}
                <div class="import-result" in:rise>
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
                            A bad row never aborts the file —
                            {result.dry_run ? "on Import, the good rows still apply." : "the good ones still applied."}
                        </div>
                    {/if}

                    <!-- After a rehearsal the next step is the real thing, and
                         the button for it is the one beside Dry run. -->
                    {#if result.dry_run}
                        <div class="field-help m-t-sm">
                            A dry run writes nothing. Press Import to apply the file.
                        </div>
                    {/if}
                </div>
            {/if}
            </section>

        </div>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">CSV in — the same shape it came out</span>
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
{/if}
