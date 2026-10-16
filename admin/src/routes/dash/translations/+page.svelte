<script>
    /**
     * Translations: the catalogue in the store's other languages (D87).
     *
     * Answer first — how much of the catalogue a language covers — then what
     * is left to do, untranslated first, because that is the list there is
     * work in. Each item opens beside its own text, so whoever translates
     * reads the original as they write.
     *
     * The main language is not a tab: its text is the products' own, edited
     * where they are. A store selling in one language has nothing to do here
     * and is told where to add another.
     */
    import { base } from "$app/paths";
    import { can, translations } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { languageName } from "$lib/languages.js";
    import { relativeTime, formatDate, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";

    const PER_PAGE = 25;
    const KINDS = [
        { value: "product", label: "Products", title: "Product", one: "product" },
        { value: "category", label: "Categories", title: "Category", one: "category" },
        { value: "collection", label: "Collections", title: "Collection", one: "collection" },
    ];
    const STATUSES = [
        { value: "missing", label: "Not translated" },
        { value: "translated", label: "Translated" },
        { value: "all", label: "All" },
    ];
    const FIELD_LABELS = {
        title: "Title",
        description: "Description",
        seo_title: "Search title",
        seo_description: "Search description",
    };
    const LONG = new Set(["description", "seo_description"]);

    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    const missingModule = $derived(modulesKnown() && !hasModule("translations"));
    // The store's other languages: the first is its main one.
    const others = $derived((settings.languages ?? []).slice(1));
    const main = $derived(settings.defaultLanguage);
    // "Hindi" from "Hindi · हिन्दी": the running text names a language once.
    const short = (tag) => languageName(tag).split(" · ")[0];

    const list = listState({ language: "", kind: "product", status: "missing", q: "", page: 1, limit: PER_PAGE });
    const language = $derived(others.includes(list.params.language) ? list.params.language : (others[0] ?? ""));
    const kind = $derived(KINDS.find((k) => k.value === list.params.kind) ?? KINDS[0]);
    const status = $derived(list.params.status);
    const search = $derived(list.params.q);
    const perPage = $derived(list.params.limit);

    let page = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);
    // Only the latest request is shown: switching kind or language while one
    // is in flight must not land the old list under the new tab.
    let latest = 0;

    $effect(() => {
        list.params;
        language;
        if (!readable || !language || !hasModule("translations")) return;
        load();
    });

    async function load() {
        const mine = ++latest;
        loading = true;
        try {
            const result = await translations.list(kind.value, {
                language,
                status: status === "all" ? "" : status,
                q: search,
                limit: perPage,
                offset: (list.page - 1) * perPage,
            });
            if (mine !== latest) return;
            // The last row of the last page translated away: show the page
            // before it rather than an empty one that says nothing is left.
            if (!result.items.length && result.matching && list.page > 1) {
                list.setPage(Math.ceil(result.matching / perPage));
                return;
            }
            page = result;
        } catch (err) {
            if (mine === latest) toast.error(err);
        } finally {
            if (mine === latest) loading = false;
        }
    }

    // The pager reads the engine's usual list meta; this route answers counts.
    const meta = $derived(
        page && {
            page: list.page,
            total: page.matching,
            total_pages: Math.ceil(page.matching / perPage),
            offset: page.offset,
            limit: page.limit,
        },
    );
    const share = $derived(page?.total ? page.translated / page.total : 0);

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draft.trim() });
    }

    // ------------------------------------------------------------- editing
    let editing = $state(null);
    let fields = $state({});
    let saving = $state(false);
    let removing = $state(false);

    // The item keeps the kind, language and fields it was opened under, so a
    // tab switched while the drawer is open cannot send its words elsewhere.
    function open(item) {
        editing = { ...item, kind: kind.value, language, fields: page.fields };
        fields = Object.fromEntries(page.fields.map((f) => [f, item.translation?.[f] ?? ""]));
    }

    const edited = $derived(
        !!editing && editing.fields.some((f) => (fields[f] ?? "").trim() !== (editing.translation?.[f] ?? "")),
    );

    async function save(e) {
        e?.preventDefault();
        if (!editing || saving || !writable) return;
        saving = true;
        try {
            await translations.save(editing.kind, editing.id, editing.language, fields);
            toast.success(`${short(editing.language)} saved for ${editing.original.title}`);
            editing = null;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove() {
        if (!editing || removing) return;
        removing = true;
        try {
            await translations.remove(editing.kind, editing.id, editing.language);
            toast.success(`Translation removed from ${editing.original.title}`);
            editing = null;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            removing = false;
        }
    }

    // A description is written over several lines, so Enter is a new line
    // there and Ctrl+Enter (⌘+Enter) saves, as everywhere else in the panel.
    function saveOnModEnter(e) {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) save(e);
    }
</script>

<svelte:head><title>Translations · GoCommerce</title></svelte:head>

<div class="page page-translations shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Products</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Translations</h1>
            </nav>
            {#if readable && language && !missingModule}
                <form class="fields searchbar" role="search" onsubmit={submitSearch}>
                    <div class="field">
                        <input type="search" class="p-l-20" placeholder="Search the {kind.one} titles" bind:value={draft} />
                    </div>
                    {#if draft || search}
                        <div class="field addon p-r-5">
                            {#if draft !== search}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>
                                Clear
                            </button>
                        </div>
                    {/if}
                </form>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="catalog.read" what="the store's translations" />
        {:else if missingModule}
            <ModuleMissing module="translations" what="Translations are kept by ext/translations, and this binary does not have it. Start it with -translations." />
        {:else if !language}
            <EmptyState
                icon="ri-translate-2"
                title="This store sells in {short(main)} only"
                text="Add the languages your customers read on Store settings. Each one then gets a column here, and products, categories and collections can be translated into it."
            >
                <a class="btn sm" href="{base}/dash/settings"><span class="txt">Choose the store's languages</span></a>
            </EmptyState>
        {:else}
            <div class="tr-head">
                <div class="tr-tabs" role="group" aria-label="Language">
                    {#each others as lang (lang)}
                        <button
                            type="button"
                            class="tr-tab"
                            class:on={language === lang}
                            aria-pressed={language === lang}
                            onclick={() => list.set({ language: lang })}>{languageName(lang)}</button
                        >
                    {/each}
                </div>
                <div class="tr-kinds" role="group" aria-label="What to translate">
                    {#each KINDS as k (k.value)}
                        <button
                            type="button"
                            class="btn sm {kind.value === k.value ? '' : 'transparent secondary'}"
                            aria-pressed={kind.value === k.value}
                            onclick={() => list.set({ kind: k.value })}><span class="txt">{k.label}</span></button
                        >
                    {/each}
                </div>
            </div>

            {#if page}
                <section class="card tr-progress" aria-live="polite">
                    <div class="tr-progress-text">
                        <strong>{page.translated} of {page.total}</strong>
                        {pluralize(page.total, kind.one, kind.label.toLowerCase())} translated into
                        {short(language)}
                    </div>
                    <div class="tr-bar" aria-hidden="true">
                        <div class="tr-bar-fill" style="transform: scaleX({share})"></div>
                    </div>
                    <div class="txt-hint txt-sm">
                        Shoppers reading {short(language)} see the store's
                        {short(main)} wherever nothing is translated yet.
                    </div>
                </section>
            {/if}

            <div class="tr-status" role="group" aria-label="Status">
                {#each STATUSES as s (s.value)}
                    <button
                        type="button"
                        class="tr-tab"
                        class:on={status === s.value}
                        aria-pressed={status === s.value}
                        onclick={() => list.set({ status: s.value })}>{s.label}</button
                    >
                {/each}
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">{kind.title}</th>
                            <th class="col-field-type-text">{short(language)}</th>
                            <th class="col-field-type-date min-width">Updated</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each page?.items ?? [] as item (item.id)}
                            <tr class="handle" tabindex="0" onclick={() => open(item)} onkeydown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), open(item))}>
                                <td class="col-field-name-id" data-name="Original">
                                    <div class="tr-title">{item.original.title || "Untitled"}</div>
                                    {#if item.path && item.path !== item.original.title}
                                        <div class="txt-hint txt-sm">{item.path}</div>
                                    {/if}
                                </td>
                                <td class="col-field-type-text" data-name={short(language)}>
                                    {#if item.translation?.title}
                                        <span class="tr-title" lang={language}>{item.translation.title}</span>
                                    {:else if item.translation}
                                        <span class="label sm">Partly translated</span>
                                    {:else}
                                        <span class="label sm warning">Not translated</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Updated" title={item.updated_at ? formatDate(item.updated_at) : ""}>
                                    {item.updated_at ? relativeTime(item.updated_at) : "—"}
                                </td>
                            </tr>
                        {:else}
                            {#if !loading}
                                <tr>
                                    <td colspan="3">
                                        {#if search}
                                            <EmptyState
                                                icon="ri-search-line"
                                                title="Nothing matches"
                                                text="No {kind.one} title contains that."
                                                onclear={() => ((draft = ""), list.set({ q: "" }))}
                                            />
                                        {:else if status === "missing" && page?.total}
                                            <EmptyState
                                                icon="ri-check-double-line"
                                                title="Every {kind.one} is translated"
                                                text="Each one has a {short(language)} translation. A {kind.one} added later appears here until it has one too."
                                            />
                                        {:else}
                                            <EmptyState
                                                icon="ri-translate-2"
                                                title={page?.total ? "None translated yet" : `No ${kind.label.toLowerCase()} yet`}
                                                text={page?.total
                                                    ? "Open a row under Not translated to write its translation."
                                                    : `When the store has ${kind.label.toLowerCase()}, they are listed here to translate.`}
                                            />
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        {/each}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun={kind.one} plural={kind.label.toLowerCase()} {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={!!editing} title={editing ? `${editing.original.title || "Untitled"} in ${short(editing.language)}` : ""} onclose={() => (editing = null)}>
    {#if editing}
        <form id="tr-form" class="tr-form" onsubmit={save}>
            {#each editing.fields as f (f)}
                <div class="tr-pair" in:rise>
                    <div class="tr-original">
                        <div class="tr-label">{FIELD_LABELS[f] ?? f} · {short(main)}</div>
                        <div class="tr-source" lang={main}>
                            {#if editing.original[f]}{editing.original[f]}{:else}<span class="txt-hint">Empty</span>{/if}
                        </div>
                    </div>
                    <div class="field">
                        <label for="tr-{f}">{FIELD_LABELS[f] ?? f} · {short(editing.language)}</label>
                        {#if LONG.has(f)}
                            <textarea id="tr-{f}" rows="5" lang={editing.language} readonly={!writable} bind:value={fields[f]} onkeydown={saveOnModEnter}></textarea>
                        {:else}
                            <input id="tr-{f}" type="text" lang={editing.language} readonly={!writable} bind:value={fields[f]} />
                        {/if}
                    </div>
                </div>
            {/each}
            <p class="txt-hint txt-sm">
                A field left empty shows the {short(main)} text. Have a native
                speaker read a translation before it goes live.
            </p>
        </form>
    {/if}
    {#snippet footer()}
        {#if editing && writable}
            {#if editing.translation}
                <button type="button" class="btn sm transparent danger" disabled={removing || saving} onclick={remove}>
                    <span class="txt">{removing ? "Removing…" : "Remove translation"}</span>
                </button>
            {/if}
            <div class="flex-fill"></div>
            <button type="button" class="btn sm transparent secondary" onclick={() => (editing = null)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="submit" form="tr-form" class="btn sm" disabled={saving || !edited}>
                <span class="txt">{saving ? "Saving…" : "Save translation"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<style>
    .tr-head {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 10px;
        margin-bottom: 12px;
    }
    .tr-tabs,
    .tr-status {
        display: flex;
        flex-wrap: wrap;
        gap: 2px;
        border-bottom: 1px solid var(--surfaceAlt2Color);
    }
    .tr-status {
        margin-bottom: 12px;
    }
    .tr-tab {
        min-height: 40px;
        padding: 8px 12px;
        border: 0;
        border-bottom: 2px solid transparent;
        background: none;
        color: var(--txtHintColor);
        font: inherit;
        font-weight: 500;
        cursor: pointer;
        transition:
            color 150ms ease-out,
            border-color 150ms ease-out;
    }
    .tr-tab:hover,
    .tr-tab:focus-visible {
        color: var(--txtPrimaryColor);
    }
    .tr-tab.on {
        border-bottom-color: var(--primaryColor);
        color: var(--txtPrimaryColor);
    }
    .tr-kinds {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }
    .tr-progress {
        display: grid;
        gap: 8px;
        margin-bottom: 12px;
    }
    .tr-progress-text strong {
        font-variant-numeric: tabular-nums;
    }
    .tr-bar {
        height: 6px;
        overflow: hidden;
        border-radius: 3px;
        background: var(--surfaceAlt2Color);
    }
    .tr-bar-fill {
        height: 100%;
        background: var(--successColor);
        transform-origin: left;
        transition: transform 300ms cubic-bezier(0.2, 0.7, 0.2, 1);
    }
    .tr-title {
        font-weight: 500;
        overflow-wrap: anywhere;
    }
    .tr-form {
        display: grid;
        gap: 16px;
    }
    .tr-pair {
        display: grid;
        gap: 8px;
    }
    .tr-label {
        font-size: 12px;
        font-weight: 600;
        color: var(--txtHintColor);
    }
    .tr-source {
        padding: 8px 10px;
        border-radius: var(--baseRadius);
        background: var(--surfaceAlt1Color, var(--baseAlt1Color));
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        max-height: 12em;
        overflow: auto;
    }
    @media (prefers-reduced-motion: reduce) {
        .tr-tab,
        .tr-bar-fill {
            transition: none;
        }
    }
</style>
