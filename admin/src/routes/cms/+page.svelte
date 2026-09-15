<script>
    /**
     * Pages: the store's content, from ext/cms.
     *
     * A page is catalog copy that happens not to carry a price, which is why
     * the screen is gated on the rights that already govern the rest of the
     * store's writing rather than on a content pair of its own.
     *
     * The language filter is a free-text box and not a Select, and that is the
     * API rather than a shortcut: nothing serves the list of languages a store
     * is configured for. `/api/admin/settings` reports the default and the ones
     * it knows about, so those are offered as a datalist, and anything else can
     * still be typed — an unknown language is an empty page, not an error.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, can } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    const list = listState({ language: "", status: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const language = $derived(list.params.language);
    const status = $derived(list.params.status);

    let pages = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    /* The module answer arrives a beat after boot, so "absent" is only said
       once it has: `!hasModule()` on its own would flash the wrong screen at
       every reload. */
    const missing = $derived(modulesKnown() && !hasModule("cms"));

    let createOpen = $state(false);
    let creating = $state(false);
    let form = $state({ title: "", slug: "", language: "", status: "draft" });
    let errors = $state({});

    /* The module clause is part of the condition rather than a wrapper around
       the result: hasModule() reads a `$state` object, so this effect re-runs
       when the answer arrives, and a store without cms never makes the call at
       all. */
    $effect(() => {
        list.params;
        if (can("catalog.read") && hasModule("cms")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/cms/pages" + list.query({ limit: perPage }),
            );
            pages = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            // A 404 here is the module answering that it is not installed,
            // which the screen already says in its own words.
            if (err.status !== 404) toast.error(err);
            pages = [];
            meta = null;
        } finally {
            loading = false;
        }
    }

    function openCreate() {
        // Prefilled with the store's own default, and editable: the field is
        // set once and never again, so it is worth getting right here.
        form = { title: "", slug: "", language: settings.defaultLanguage, status: "draft" };
        errors = {};
        createOpen = true;
    }

    async function create(event) {
        event.preventDefault();
        errors = {};
        if (!form.title.trim()) errors.title = "A page needs a title.";
        // The engine does not derive a slug from the title the way products do,
        // so an empty one is a 400 rather than a guess.
        if (!form.slug.trim()) errors.slug = "A page needs a slug.";
        if (Object.keys(errors).length) return;

        creating = true;
        try {
            const created = await api.post("/api/admin/x/cms/pages", {
                title: form.title.trim(),
                slug: form.slug.trim(),
                // Left blank on purpose when the store has not said: the engine
                // fills in its own default, and inventing one here would write
                // a language the store does not use.
                language: form.language.trim(),
                status: form.status,
            });
            createOpen = false;
            toast.success("Page created");
            goto(`${base}/cms/${created.id}`);
        } catch (err) {
            // The engine's own message names the language and the slug —
            // "a en page already exists at \"about\"" — which is more use than
            // anything this screen could invent.
            if (err.status === 409) errors.slug = err.message;
            toast.error(err);
        } finally {
            creating = false;
        }
    }
</script>

<svelte:head><title>Pages · GoCommerce</title></svelte:head>

{#if !can("catalog.read")}
    <NoAccess right="catalog.read" what="pages" />
{:else}
    <div class="page page-cms shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Pages</div>
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

                {#if !missing}
                    <div class="page-header-primary-btns">
                        <!-- A free-text box rather than a Select, and that is
                             the API rather than a shortcut: nothing serves the
                             list of languages a store is configured for. The
                             ones settings knows about are offered as a
                             datalist; anything else can still be typed, and an
                             unknown language is an empty page rather than an
                             error. It applies on Enter or on leaving the box.

                             size="10" rather than the default twenty
                             characters: the group does not wrap inside itself,
                             so at 390px a full-width box pushes New page off
                             the screen. -->
                        <div class="field">
                            <input
                                type="text"
                                list="cms-languages"
                                placeholder="Any language"
                                aria-label="Language"
                                size="10"
                                value={language}
                                onchange={(e) => list.set({ language: e.currentTarget.value.trim() })}
                            />
                            <datalist id="cms-languages">
                                {#each settings.languages as code (code)}
                                    <option value={code}></option>
                                {/each}
                            </datalist>
                        </div>
                        <div class="field">
                            <Select
                                id="cms-status"
                                ariaLabel="Status"
                                value={status}
                                options={[
                                    { value: "", label: "Any status" },
                                    { value: "draft", label: "Draft" },
                                    { value: "published", label: "Published" },
                                ]}
                                onchange={(v) => list.set({ status: v })}
                            />
                        </div>
                        {#if can("catalog.write")}
                            <button type="button" class="btn" onclick={openCreate}>
                                <i class="ri-add-line" aria-hidden="true"></i>
                                <span class="txt">New page</span>
                            </button>
                        {/if}
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cms"
                    what="Content pages are served by ext/cms, and this binary does not have it."
                />
            {:else}
                <div class="page-table-wrapper tw:rounded-xl tw:border">
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Page</th>
                                <th class="col-field-type-select min-width">Language</th>
                                <th class="col-field-type-select min-width">Status</th>
                                <th class="col-field-type-date min-width">Updated</th>
                                <th class="col-meta min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each pages as row (row.id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={(e) =>
                                        e.target.closest("a") || goto(`${base}/cms/${row.id}`)}
                                    onkeydown={(e) => rowKey(e, () => goto(`${base}/cms/${row.id}`))}
                                >
                                    <td class="col-field-name-id" data-name="Page">
                                        <div class="row-name">
                                            <!-- A real link as well as a row, so
                                                 a page can be opened in a second
                                                 tab beside the one being
                                                 edited. -->
                                            <a
                                                class="txt-bold txt-ellipsis"
                                                href="{base}/cms/{row.id}"
                                            >
                                                {row.title}
                                            </a>
                                            <span class="txt-hint txt-sm row-handle">/{row.slug}</span>
                                        </div>
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Language">
                                        <span class="label sm">{row.language}</span>
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Status">
                                        <span
                                            class="label"
                                            class:success={row.status === "published"}
                                        >
                                            {row.status}
                                        </span>
                                    </td>
                                    <td
                                        class="col-field-type-date min-width txt-hint"
                                        data-name="Updated"
                                    >
                                        {formatDate(row.updated_at)}
                                    </td>
                                    <td class="col-meta min-width">
                                        <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                    </td>
                                </tr>
                            {/each}

                            {#if loading && !pages.length}
                                {#each Array(5) as _, i (i)}
                                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {:else if !pages.length}
                                <tr>
                                    <td colspan="5" class="txt-center txt-hint p-base">
                                        {list.pristine
                                            ? "No pages yet. An about page, terms, a delivery policy — anything the storefront needs to read."
                                            : "No page matches that. Try clearing a filter."}
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
                        noun="page"
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

<Drawer open={createOpen} size="sm" title="New page" onclose={() => (createOpen = false)}>
    <form id="cms-create" onsubmit={create}>
        <div class="field required" class:error={!!errors.title}>
            <label for="new-title">Title</label>
            <input id="new-title" type="text" bind:value={form.title} />
        </div>
        {#if errors.title}<div class="field-help error">{errors.title}</div>{/if}

        <div class="field required m-t-sm" class:error={!!errors.slug}>
            <label for="new-slug">Slug</label>
            <input id="new-slug" type="text" placeholder="about" bind:value={form.slug} />
        </div>
        {#if errors.slug}
            <div class="field-help error">{errors.slug}</div>
        {:else}
            <div class="field-help">
                Where the storefront reads it: <code class="txt-code">/x/cms/pages/&lt;slug&gt;</code>.
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="new-language">Language</label>
            <input
                id="new-language"
                type="text"
                list="cms-languages"
                placeholder={settings.defaultLanguage || "en"}
                bind:value={form.language}
            />
        </div>
        <div class="field-help">
            Set once. The same slug in another language is a different page, and no route moves one.
            Leave it blank to take the store's own default.
        </div>

        <div class="field m-t-sm">
            <label for="new-status">Status</label>
            <Select
                id="new-status"
                value={form.status}
                options={[
                    { value: "draft", label: "Draft — hidden from shoppers" },
                    { value: "published", label: "Published — live on the storefront" },
                ]}
                onchange={(v) => (form.status = v)}
            />
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (createOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="cms-create"
            class="btn"
            class:loading={creating}
            disabled={creating}
        >
            <span class="txt">Create page</span>
        </button>
    {/snippet}
</Drawer>
