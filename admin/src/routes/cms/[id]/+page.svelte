<script>
    /**
     * One content page.
     *
     * Language is read-only here, and that is the API being honest rather than
     * a control left out: the patch struct has no language field, because the
     * same slug in another language is a different page and no route moves one.
     * A control that always 400s would be worse than the sentence that says so.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page as route } from "$app/state";
    import { api, can } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import RichText from "$lib/components/RichText.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import Select from "$lib/components/Select.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const pageId = $derived(route.params.id);
    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    const missing = $derived(modulesKnown() && !hasModule("cms"));

    let record = $state(null);
    let loading = $state(true);
    let saving = $state(false);
    let deleteOpen = $state(false);
    let errors = $state({});

    let form = $state(shapeOf(null));
    let snapshot = $state(null);

    const dirty = $derived(!!snapshot && JSON.stringify(form) !== JSON.stringify(snapshot));

    /* Two fields with the same name send one value, and the last one wins —
       so the operator is told before saving rather than after. */
    const duplicateKey = $derived.by(() => {
        const seen = new Set();
        for (const field of form.seo) {
            const key = field.key.trim();
            if (!key) continue;
            if (seen.has(key)) return key;
            seen.add(key);
        }
        return "";
    });

    function shapeOf(p) {
        return {
            title: p?.title ?? "",
            slug: p?.slug ?? "",
            body: p?.body ?? "",
            excerpt: p?.excerpt ?? "",
            status: p?.status ?? "draft",
            seo: Object.entries(p?.seo ?? {}).map(([key, value]) => ({ key, value })),
        };
    }

    $effect(() => {
        pageId;
        // Waits for the module answer rather than asking a store that does not
        // serve this route — see the list screen.
        if (hasModule("cms")) load();
    });

    async function load() {
        loading = true;
        // The screen renders NoAccess without this right, so firing the
        // request first would bury that explanation under a 403 toast.
        if (!readable) {
            loading = false;
            return;
        }
        try {
            record = await api.get(`/api/admin/x/cms/pages/${pageId}`);
            form = shapeOf(record);
            snapshot = shapeOf(record);
        } catch (err) {
            // A deleted page and an uninstalled module both answer 404. The
            // module state is drawn by this screen; a missing page is not a
            // screen at all, so it goes back to the list.
            if (err.status === 404 && hasModule("cms")) {
                toast.error(err);
                goto(`${base}/cms`);
                return;
            }
            if (err.status !== 404) toast.error(err);
        } finally {
            loading = false;
        }
    }

    function reset() {
        form = shapeOf(record);
        errors = {};
    }

    function addField() {
        form.seo = [...form.seo, { key: "", value: "" }];
    }

    function removeField(index) {
        form.seo = form.seo.filter((_, i) => i !== index);
    }

    function seoObject() {
        const out = {};
        for (const field of form.seo) {
            const key = field.key.trim();
            if (key) out[key] = field.value;
        }
        return out;
    }

    async function save() {
        errors = {};
        if (!form.title.trim()) errors.title = "A page needs a title.";
        if (!form.slug.trim()) errors.slug = "A page needs a slug.";
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            record = await api.patch(`/api/admin/x/cms/pages/${pageId}`, {
                title: form.title.trim(),
                slug: form.slug.trim(),
                body: form.body,
                excerpt: form.excerpt,
                status: form.status,
                // Sent whole, because the engine replaces it whole: an empty
                // list is the way to clear the lot.
                seo: seoObject(),
            });
            form = shapeOf(record);
            snapshot = shapeOf(record);
            toast.success("Page saved");
        } catch (err) {
            // Renaming onto a slug another page holds in the same language.
            // The engine says which, so its message is the field's.
            if (err.status === 409) errors.slug = err.message;
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove() {
        await api.delete(`/api/admin/x/cms/pages/${pageId}`);
        toast.success("Page deleted");
        goto(`${base}/cms`);
    }
</script>

<svelte:head><title>{record?.title || "Page"} · GoCommerce</title></svelte:head>

{#if !can("catalog.read")}
    <NoAccess right="catalog.read" what="pages" />
{:else}
    <!-- The compact card skin the product editor uses: this is the panel's
         other long form, and two editors that look unrelated is worse than one
         extra class. -->
    <div class="page page-cms shopify-skin skin-recessed">
        <div class="page-content full-height">
            <SaveBar {dirty} {saving} onsave={save} ondiscard={reset} />

            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/cms">Pages</a>
                    <div>{record?.title || "…"}</div>
                </nav>

                <div class="page-header-primary-btns">
                    {#if record}
                        <span class="label" class:success={record.status === "published"}>
                            {record.status}
                        </span>
                    {/if}
                </div>
            </header>

            {#if missing}
                <ModuleMissing
                    module="cms"
                    what="Content pages are served by ext/cms, and this binary does not have it."
                />
            {:else if loading && !record}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if record}
                <div class="wrapper">
                    <section class="card">
                        <div class="fields">
                            <div class="field required" class:error={!!errors.title}>
                                <label for="title">Title</label>
                                <input
                                    id="title"
                                    type="text"
                                    disabled={!writable}
                                    bind:value={form.title}
                                />
                            </div>
                            <div class="field required" class:error={!!errors.slug}>
                                <label for="slug">Slug</label>
                                <input
                                    id="slug"
                                    type="text"
                                    disabled={!writable}
                                    bind:value={form.slug}
                                />
                            </div>
                        </div>
                        {#if errors.title}<div class="field-help error">{errors.title}</div>{/if}
                        {#if errors.slug}
                            <div class="field-help error">{errors.slug}</div>
                        {:else}
                            <div class="field-help">
                                The storefront reads this page at
                                <code class="txt-code">/x/cms/pages/{form.slug || "…"}</code>.
                            </div>
                        {/if}

                        <div class="field m-t-sm">
                            <label for="body">Body</label>
                            <RichText
                                id="body"
                                placeholder="What does this page say?"
                                disabled={!writable}
                                bind:value={form.body}
                            />
                        </div>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-text-block" aria-hidden="true"></i>
                            Excerpt
                        </h6>
                        <div class="field">
                            <textarea
                                id="excerpt"
                                rows="3"
                                aria-label="Excerpt"
                                disabled={!writable}
                                bind:value={form.excerpt}
                            ></textarea>
                        </div>
                        <div class="field-help">A summary for a listing page. Optional.</div>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-send-plane-line" aria-hidden="true"></i>
                            Publication
                        </h6>

                        <div class="field">
                            <label for="status">Status</label>
                            <Select
                                id="status"
                                value={form.status}
                                disabled={!writable}
                                options={[
                                    { value: "draft", label: "Draft — hidden from shoppers" },
                                    {
                                        value: "published",
                                        label: "Published — live on the storefront",
                                    },
                                ]}
                                onchange={(v) => (form.status = v)}
                            />
                        </div>
                        {#if record.published_at}
                            <div class="field-help">
                                First published {formatDate(record.published_at)}. Publishing again
                                does not move that date.
                            </div>
                        {/if}

                        <div class="field readonly-field m-t-sm">
                            <span class="readonly-label">Language</span>
                            <span class="readonly-value">{record.language}</span>
                        </div>
                        <div class="field-help">
                            Language is fixed when a page is created. To write this page in another
                            language, create a new one with the same slug.
                        </div>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-search-eye-line" aria-hidden="true"></i>
                            SEO
                        </h6>

                        {#if form.seo.length}
                            <div class="metafields m-b-sm">
                                {#each form.seo as field, i (i)}
                                    <div class="metafield-row">
                                        <div class="field">
                                            <label for="seo-key-{i}">Name</label>
                                            <input
                                                id="seo-key-{i}"
                                                type="text"
                                                placeholder="description"
                                                disabled={!writable}
                                                bind:value={field.key}
                                            />
                                        </div>
                                        <div class="field">
                                            <label for="seo-value-{i}">Value</label>
                                            <input
                                                id="seo-value-{i}"
                                                type="text"
                                                disabled={!writable}
                                                bind:value={field.value}
                                            />
                                        </div>
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary"
                                            aria-label="Remove {field.key || 'this field'}"
                                            title="Remove"
                                            disabled={!writable}
                                            onclick={() => removeField(i)}
                                        >
                                            <i class="ri-close-line" aria-hidden="true"></i>
                                        </button>
                                    </div>
                                {/each}
                            </div>
                        {/if}

                        {#if duplicateKey}
                            <div class="field-help error">
                                Two fields are called “{duplicateKey}”. Only the last would be
                                saved, so rename one first.
                            </div>
                        {/if}

                        {#if writable}
                            <button type="button" class="btn sm secondary" onclick={addField}>
                                <i class="ri-add-line" aria-hidden="true"></i>
                                <span class="txt">Add field</span>
                            </button>
                        {/if}

                        <div class="field-help">
                            Whatever the storefront reads — a description, an OG image. The engine
                            stores the pairs and renders none of them.
                        </div>
                    </section>

                    {#if writable}
                        <section class="card">
                            <h6 class="section-title">
                                <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                Danger zone
                            </h6>
                            <button
                                type="button"
                                class="btn danger"
                                onclick={() => (deleteOpen = true)}
                            >
                                <span class="txt">Delete this page</span>
                            </button>
                        </section>
                    {/if}
                </div>

                <footer class="page-footer">
                    <span class="txt txt-hint">Page #{record.id}</span>
                    <div class="flex-fill"></div>
                    <ThemeToggle />
                </footer>
            {/if}
        </div>
    </div>
{/if}

<Confirm
    bind:open={deleteOpen}
    title="Delete this page?"
    message={record
        ? `“${record.title}” and its SEO fields are removed. A storefront linking to /x/cms/pages/${record.slug} will 404.`
        : ""}
    confirmLabel="Delete"
    danger
    onconfirm={remove}
/>
