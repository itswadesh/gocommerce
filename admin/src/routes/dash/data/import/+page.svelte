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
    import {
        csvFiles,
        detectDialect,
        importBlocker,
        importKinds as kindsFor,
        importModes,
        importSources,
        importUrl,
        resultFor,
        runTotals,
        unsavedImport,
    } from "$lib/importer.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { downloadFile } from "$lib/download.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const mayImport = $derived(can("data.import"));

    /* Which of the two run buttons is working: "dry", "import", or "" for
       neither. Both wait while either runs, and the pressed one spins. */
    let running = $state("");
    let fireEvents = $state(false);
    // Upsert by default: a re-import of an edited export is what most files
    // here are, and it has to update what it finds.
    let mode = $state("upsert");
    let kind = $state("products");
    let csv = $state("");
    /* Files dropped or picked, read on arrival: { id, name, size, text }. */
    let files = $state([]);
    let nextFileId = 0;
    let over = $state(false);
    /* The last run, the tab it ran under, and a result per file; `results`
       is what of it belongs on screen now. */
    let run = $state(null);
    const results = $derived(resultFor(run, kind));
    let fileInput = $state(null);
    /* The delete a dry run has counted and the operator has yet to confirm. */
    let pendingDelete = $state(null);
    let deleteOpen = $state(false);

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
    const sources = $derived(importSources(files, csv));
    const blocker = $derived(importBlocker(sources));

    /* The modes this kind takes; a kind with one behaviour offers none. */
    const modes = $derived(importModes(kind));
    const activeMode = $derived(modes.some((m) => m.value === mode) ? mode : "upsert");

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

    /* Read now rather than at run time, so a file that cannot be read is
       said while the operator still has the folder open. */
    async function addFiles(list) {
        const { ok, refused } = csvFiles(list);
        if (refused.length) toast.warning(`Not a CSV file, left out: ${refused.join(", ")}`);
        if (!ok.length) return;
        const read = await Promise.all(ok.map(async (f) => ({ id: ++nextFileId, name: f.name, size: f.size, text: await f.text() })));
        files = [...files, ...read];
        run = null;
    }

    async function onFile(event) {
        await addFiles(event.target.files);
        event.target.value = "";
    }

    function onDragOver(event) {
        event.preventDefault();
        over = true;
    }

    function onDrop(event) {
        event.preventDefault();
        over = false;
        addFiles(event.dataTransfer?.files);
    }

    function removeFile(index) {
        files = files.filter((_, i) => i !== index);
        run = null;
    }

    function clearCsv() {
        csv = "";
        files = [];
        run = null;
    }

    /*
     * Every source in turn, under the kind and mode as they were when the
     * run began. A file the engine refuses outright is that file's result,
     * not the run's end: the next one still goes.
     */
    async function runAll(dry) {
        const ranKind = kind;
        const ranMode = activeMode;
        const ran = sources;
        running = dry ? "dry" : "import";
        run = null;
        const out = [];
        const applied = new Set();
        try {
            for (const src of ran) {
                try {
                    const answer = await request(
                        "POST",
                        importUrl(ranKind, { dryRun: dry, fireEvents, mode: ranMode }),
                        { body: src.text, headers: { "Content-Type": "text/csv" } },
                    );
                    out.push({ name: src.name, result: answer });
                    if (src.id) applied.add(src.id);
                } catch (err) {
                    out.push({ name: src.name, error: err?.message ?? String(err) });
                }
                run = { kind: ranKind, mode: ranMode, dry, results: [...out] };
            }
        } finally {
            running = "";
        }
        // A file that went in leaves the list, and its result stays on screen.
        // One the engine refused stays, to be fixed and run again; one
        // dropped while this run was going was never part of it, so it stays
        // too.
        if (!dry) files = files.filter((f) => !applied.has(f.id));
        return out;
    }

    function report(out, dry) {
        const t = runTotals(out);
        const across = t.files > 1 ? ` across ${t.files} files` : "";
        const applied = dry
            ? "Dry run done, nothing was written"
            : run?.mode === "delete"
              ? `Deleted ${t.deleted}`
              : `Imported: ${t.created} created, ${t.updated} updated`;
        if (t.failed) {
            toast.error(`${t.failed} of ${t.files} file(s) could not be imported`);
        } else if (t.attention) {
            toast.warning(`${applied}; ${t.attention} row(s) need attention${across}`);
        } else if (dry) {
            toast.success("Dry run looks good — nothing was written");
        } else {
            toast.success(`${applied}${across}`);
        }
    }

    /*
     * Both buttons come here. A dry run and an import are one request apart —
     * the engine rolls the first back — so they share everything but the
     * flag, and neither button's name depends on a switch. When the name did
     * depend on one ("Dry run", on by default), the screen never showed a
     * button called Import at all.
     *
     * A delete is never sent straight: it is rehearsed first, so the
     * confirmation can name the number the engine itself counted, and only
     * the confirmation sends it.
     */
    async function runImport(dry) {
        if (blocker || running) return;
        if (!dry && activeMode === "delete") {
            const out = await runAll(true);
            const t = runTotals(out);
            if (!t.deleted) {
                toast.warning(
                    `Nothing in ${t.files > 1 ? "these files" : "the file"} matches a record the store has, so there is nothing to delete`,
                );
                return;
            }
            pendingDelete = { count: t.deleted, files: t.files, kind };
            deleteOpen = true;
            return;
        }
        report(await runAll(dry), dry);
    }

    async function confirmDelete() {
        report(await runAll(false), false);
        pendingDelete = null;
    }

    /* "3 products", "1 category": what the confirmation is about to delete. */
    function counted(n, k) {
        const [one, many] = k === "categories" ? ["category", "categories"] : ["product", "products"];
        return `${n} ${n === 1 ? one : many}`;
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

<svelte:head><title>{titled("Import")}</title></svelte:head>

<!-- A pasted or loaded CSV that has not been imported is the most expensive
     thing on this screen to lose: it may be a file somebody spent an afternoon
     on, and it is held nowhere but this textarea. A dry run writes nothing, so
     it stays unsaved through one; it stops counting once an import has
     reported, which is the point at which leaving is the ordinary thing to
     do. -->
<DirtyGuard
    dirty={unsavedImport(sources, run)}
    message="The CSV on this screen has not been imported. Leave and lose it?"
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
                        <span class="txt">Import the taxonomy</span>
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
                 The taxonomy above it is over in one click; this is a form
                 with a kind, a file, two ways to run it and a result that
                 appears underneath. Giving it an edge is what stops the
                 result of an import reading as part of the controls above
                 it. -->
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
                    {#if kind === "products"}
                        <!-- The file to fill in: Shopify's columns (D182) and an example
                             product in two sizes, saved as a draft if imported as it is. -->
                        <div class="m-t-sm">
                            <button
                                type="button"
                                class="btn sm secondary"
                                onclick={() => downloadFile("/api/admin/import/products/template", "products-template.csv", "text/csv")}
                            >
                                <i class="ri-file-download-line" aria-hidden="true"></i>
                                <span class="txt">Download a template</span>
                            </button>
                            <span class="txt-hint txt-sm">
                                Shopify's product CSV, so a file exported from Shopify imports as it
                                is. The file explains every column at the top.
                            </span>
                        </div>
                    {/if}
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

            <!-- What a row does with its key. Offered only for a kind that
                 takes more than one answer; the others say theirs in the
                 sentence above. Radios rather than a select so all four
                 are read at once, each with what it does. -->
            {#if modes.length}
                <fieldset class="import-modes m-t-base">
                    <legend class="import-modes-legend">What each row does</legend>
                    <div class="import-modes-options">
                        {#each modes as m (m.value)}
                            <label
                                class="import-mode"
                                class:is-on={activeMode === m.value}
                                class:danger={m.value === "delete"}
                            >
                                <input type="radio" name="import-mode" value={m.value} bind:group={mode} />
                                <span class="import-mode-text">
                                    <span class="import-mode-title">{m.label}</span>
                                    <span class="txt-hint txt-sm">{m.help}</span>
                                </span>
                            </label>
                        {/each}
                    </div>
                </fieldset>
            {/if}

            <!-- The switches that change what an import does. Dry run is not
                 one of them any more: it is a button beside Import. -->
            {#if kind === "orders"}
                <div class="flex gap-20 flex-wrap m-t-base">
                    <div class="field">
                        <input
                            type="checkbox"
                            id="fire-events"
                            class="switch"
                            bind:checked={fireEvents}
                        />
                        <label for="fire-events">Fire events</label>
                    </div>
                </div>
            {/if}

            {#if activeMode === "delete"}
                <div class="alert danger m-t-base" in:rise>
                    <p>
                        Import deletes what the file names, and that cannot be undone. A dry
                        run goes first, and nothing is deleted until you confirm the number it
                        found.
                        {#if kind === "categories"}
                            A category that still has subcategories or products is refused, not
                            deleted.
                        {/if}
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

            <!-- Several files at once, imported one after another under
                 the tab and mode above, each with a result of its own. The
                 zone is the drop target; the button inside it is the
                 keyboard's way in. -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
                class="media-dropzone import-drop m-t-base"
                data-over={over}
                ondragover={onDragOver}
                ondragleave={() => (over = false)}
                ondrop={onDrop}
            >
                <i class="ri-file-upload-line import-drop-icon" aria-hidden="true"></i>
                <div class="txt-sm">Drop CSV files here. Each is imported in turn, with a result of its own.</div>
                <button type="button" class="btn secondary sm" onclick={pickFile}>
                    <i class="ri-folder-open-line" aria-hidden="true"></i>
                    <span class="txt">Choose files…</span>
                </button>
            </div>

            <input
                type="file"
                accept=".csv,text/csv"
                multiple
                bind:this={fileInput}
                onchange={onFile}
                aria-label="CSV files to import"
                hidden
            />

            {#if files.length}
                <ul class="import-files m-t-sm" aria-label="Files to import">
                    {#each files as f, i (f.id)}
                        <li class="import-file" transition:rise>
                            <i class="ri-file-text-line" aria-hidden="true"></i>
                            <span class="import-file-name">{f.name}</span>
                            <span class="txt-hint txt-sm">{(f.size / 1024).toFixed(1)} KB</span>
                            {#if detectDialect(kind, f.text) === "shopify"}
                                <span class="label sm">Shopify</span>
                            {/if}
                            <button
                                type="button"
                                class="btn transparent sm circle secondary"
                                aria-label="Remove {f.name}"
                                disabled={!!running}
                                onclick={() => removeFile(i)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </li>
                    {/each}
                </ul>
            {/if}

            <div class="field m-t-base">
                <label for="csv">Or paste CSV</label>
                <textarea
                    id="csv"
                    class="txt-code"
                    rows="6"
                    placeholder="Paste CSV here"
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

            <!-- Choose, then run: the file on the left, the two ways to run it
                 on the right. Dry run and Import are both always here, under
                 their own names. Until there is something to run they are
                 greyed out and the line under them says so: aria-disabled
                 rather than disabled, so a keyboard still reaches them and a
                 screen reader still reads the reason. -->
            <div class="import-run flex gap-10 flex-wrap m-t-base">
                {#if sources.length > 1}
                    <span class="import-run-count txt-hint txt-sm">
                        {sources.length} CSVs, one after another
                    </span>
                {/if}
                {#if csv || files.length}
                    <button
                        type="button"
                        class="btn transparent secondary"
                        onclick={clearCsv}
                    >
                        <span class="txt">Clear</span>
                    </button>
                {/if}
                <div class="import-run-actions">
                    <button
                        type="button"
                        class="btn secondary expanded"
                        class:loading={running === "dry"}
                        class:disabled={!!blocker}
                        disabled={!!running}
                        aria-disabled={blocker ? "true" : undefined}
                        aria-busy={running === "dry" ? "true" : undefined}
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
                        class:disabled={!!blocker}
                        disabled={!!running}
                        aria-disabled={blocker ? "true" : undefined}
                        aria-busy={running === "import" ? "true" : undefined}
                        aria-describedby={blocker ? "import-blocker" : undefined}
                        onclick={() => runImport(false)}
                    >
                        <i
                            class={activeMode === "delete" ? "ri-delete-bin-line" : "ri-upload-2-line"}
                            aria-hidden="true"
                        ></i>
                        <span class="txt">Import</span>
                    </button>
                </div>
            </div>

            {#if blocker}
                <p id="import-blocker" class="field-help m-t-sm" in:rise>{blocker}</p>
            {/if}
            <div class="field-help m-t-sm">
                A dry run validates the whole file and rolls back, reporting what an import
                <em>would</em> do. Worth doing first, always.
            </div>

            {#each results ?? [] as r, i (r.name + i)}
                {@const result = r.result}
                <div class="import-result" in:rise>
                    <h6 class="section-title">
                        <i
                            class={!result
                                ? "ri-close-circle-line txt-danger"
                                : result.errors?.length
                                  ? "ri-error-warning-line"
                                  : "ri-checkbox-circle-line"}
                            aria-hidden="true"
                        ></i>
                        <span class="import-file-name">{r.name}</span>
                        <span class="label">{run.dry ? "Dry run" : "Imported"}</span>
                        {#if result?.duration}<span class="label">{result.duration}</span>{/if}
                    </h6>

                    {#if !result}
                        <div class="alert danger">
                            <p>Not imported: {r.error}</p>
                        </div>
                    {:else}
                        <div class="grid">
                            {#if run.mode === "delete"}
                                <div class="col-6">
                                    <div class="stat-card">
                                        <span class="stat-label">{run.dry ? "Would delete" : "Deleted"}</span>
                                        <span class="stat-value txt-danger">{result.deleted ?? 0}</span>
                                    </div>
                                </div>
                            {:else}
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
                            {/if}
                            <div class={run.mode === "delete" ? "col-6" : "col-4"}>
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
                                    {#each result.errors as row, j (j)}
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
                                {run.dry ? "on Import, the good rows still apply." : "the good ones still applied."}
                            </div>
                        {/if}
                    {/if}
                </div>
            {/each}

            <!-- After a rehearsal the next step is the real thing, and the
                 button for it is the one beside Dry run. -->
            {#if results?.length && run.dry && !running && !deleteOpen}
                <div class="field-help m-t-sm">
                    A dry run writes nothing. Press Import to apply {results.length > 1 ? "the files" : "the file"}.
                </div>
            {/if}
            </section>

        </div>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">CSV in — the same shape it came out</span>
        </footer>

        <Confirm
            bind:open={confirmOpen}
            title="Import the taxonomy?"
            message="It writes about 14,000 categories into this store. Running it again later only adds what is missing, so it is safe to repeat — but there is no one-click way to take them out."
            confirmLabel="Import"
            onconfirm={importTaxonomy}
        />

        <!-- The number is the dry run's own, counted by the engine a moment
             ago, never a guess from the file's row count. -->
        <Confirm
            bind:open={deleteOpen}
            danger
            title="Delete {counted(pendingDelete?.count ?? 0, pendingDelete?.kind)}?"
            message="The dry run found {counted(pendingDelete?.count ?? 0, pendingDelete?.kind)} to delete in {pendingDelete?.files === 1 ? 'the file' : `${pendingDelete?.files} files`}, and its results are on the screen. Deleting cannot be undone."
            confirmLabel="Delete {pendingDelete?.count ?? 0}"
            onconfirm={confirmDelete}
        />
    </div>
</div>
{/if}
