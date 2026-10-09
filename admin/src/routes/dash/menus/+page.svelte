<script>
    /**
     * Menus: the storefront's navigation, arranged by hand.
     *
     * A menu is a tree; the editor is the tree drawn as nested rows, each a
     * title, a kind and a link, with the four moves a tree needs — up, down,
     * in under the row above, out to the level above — and Save writes the
     * whole tree in one call. No drag and drop: four buttons are learnable in
     * a second and work on a phone, and a menu is edited twice a year.
     *
     * The left column lists the menus; the right is the one being edited.
     */
    import { api, can, request } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    const missing = $derived(modulesKnown() && !hasModule("navigation"));

    let menus = $state([]);
    let loading = $state(true);
    let selected = $state(null); // the menu being edited, with a working copy of its items
    let items = $state([]);
    let dirty = $state(false);
    let saving = $state(false);

    let createOpen = $state(false);
    let form = $state({ title: "", handle: "" });
    let creating = $state(false);
    let confirmDelete = $state(false);

    const KINDS = [
        { value: "url", label: "Link (URL)" },
        { value: "home", label: "Home page" },
        { value: "collection", label: "Collection" },
        { value: "category", label: "Category" },
        { value: "product", label: "Product" },
        { value: "page", label: "Content page" },
    ];
    const PLACEHOLDER = {
        url: "https://… or /a/path",
        home: "",
        collection: "collection handle, e.g. summer",
        category: "category slug, e.g. shirts",
        product: "product slug, e.g. crew-tee",
        page: "page slug, e.g. about",
    };

    $effect(() => {
        if (readable && hasModule("navigation")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get("/api/admin/x/navigation/menus");
            menus = result.data ?? [];
            if (selected && !menus.some((m) => m.id === selected.id)) selected = null;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function open(menu) {
        if (dirty && !confirm("Discard the unsaved changes to this menu?")) return;
        try {
            const full = await request("GET", `/api/admin/x/navigation/menus/${menu.id}`);
            selected = full;
            items = strip(full.items ?? []);
            dirty = false;
        } catch (err) {
            toast.error(err);
        }
    }

    // The working copy holds only what the editor edits; ids and resolved
    // URLs are the engine's, and the save sends a fresh tree anyway.
    function strip(list) {
        return list.map((it) => ({
            title: it.title,
            kind: it.kind,
            target: it.target ?? "",
            children: strip(it.children ?? []),
        }));
    }

    function blank() {
        return { title: "", kind: "url", target: "", children: [] };
    }

    /* Every move addresses a row by its path — the indexes down from the
       root — so one set of functions serves every depth. */
    function containerOf(path) {
        let list = items;
        for (const i of path.slice(0, -1)) list = list[i].children;
        return list;
    }
    function touch() {
        dirty = true;
        items = [...items];
    }
    function add(path) {
        const list = path.length ? containerOf([...path, 0]) : items;
        list.push(blank());
        touch();
    }
    function remove(path) {
        containerOf(path).splice(path.at(-1), 1);
        touch();
    }
    function move(path, delta) {
        const list = containerOf(path);
        const i = path.at(-1);
        const j = i + delta;
        if (j < 0 || j >= list.length) return;
        [list[i], list[j]] = [list[j], list[i]];
        touch();
    }
    function indent(path) {
        const list = containerOf(path);
        const i = path.at(-1);
        if (i === 0 || path.length >= 4) return;
        const [row] = list.splice(i, 1);
        list[i - 1].children.push(row);
        touch();
    }
    function outdent(path) {
        if (path.length < 2) return;
        const list = containerOf(path);
        const [row] = list.splice(path.at(-1), 1);
        const parentPath = path.slice(0, -1);
        const grand = containerOf(parentPath);
        grand.splice(parentPath.at(-1) + 1, 0, row);
        touch();
    }

    async function save() {
        if (!selected) return;
        saving = true;
        try {
            const saved = await request("PUT", `/api/admin/x/navigation/menus/${selected.id}/items`, {
                body: { items },
            });
            selected = saved;
            items = strip(saved.items ?? []);
            dirty = false;
            menus = menus.map((m) => (m.id === saved.id ? { ...m, item_count: saved.item_count } : m));
            toast.success(`${saved.title} saved`);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function create() {
        creating = true;
        try {
            const menu = await request("POST", "/api/admin/x/navigation/menus", { body: form });
            createOpen = false;
            form = { title: "", handle: "" };
            await load();
            await open(menu);
            toast.success(`${menu.title} created — served at /x/navigation/menus/${menu.handle}`);
        } catch (err) {
            toast.error(err);
        } finally {
            creating = false;
        }
    }

    async function destroy() {
        if (!selected) return;
        try {
            await request("DELETE", `/api/admin/x/navigation/menus/${selected.id}`);
            toast.success(`${selected.title} deleted`);
            selected = null;
            items = [];
            dirty = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            confirmDelete = false;
        }
    }
</script>

<svelte:head><title>Menus · GoCommerce</title></svelte:head>

<div class="page page-menus shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Menus</div>
            </nav>
            <div class="flex-fill"></div>
            {#if writable && !missing}
                <div class="page-header-primary-btns">
                    <button type="button" class="btn expanded" onclick={() => (createOpen = true)}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New menu</span>
                    </button>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="catalog.read" what="the storefront's menus" />
        {:else if missing}
            <ModuleMissing
                module="navigation"
                what="Menus are served by ext/navigation, and this binary does not have it."
            />
        {:else}
            <div class="menus-layout">
                <aside class="card menus-list">
                    <h6 class="section-title m-t-0">Menus</h6>
                    {#if loading && !menus.length}
                        <div class="txt-hint">Loading…</div>
                    {:else if !menus.length}
                        <div class="txt-hint">
                            No menus yet. Create one — a "Main menu" and a "Footer" is where most stores start.
                        </div>
                    {:else}
                        {#each menus as m (m.id)}
                            <button
                                type="button"
                                class="menu-row"
                                class:active={selected?.id === m.id}
                                onclick={() => open(m)}
                            >
                                <span class="txt-bold">{m.title}</span>
                                <span class="txt-hint txt-sm">
                                    <span class="txt-code">{m.handle}</span> · {m.item_count}
                                    {m.item_count === 1 ? "item" : "items"}
                                </span>
                            </button>
                        {/each}
                    {/if}
                </aside>

                <section class="card menus-editor">
                    {#if !selected}
                        <div class="txt-hint p-base txt-center">
                            Pick a menu on the left, or create one. A storefront reads a menu from
                            <code>/x/navigation/menus/&lt;handle&gt;</code>.
                        </div>
                    {:else}
                        <div class="flex gap-10 m-b-base">
                            <div class="flex-fill">
                                <h6 class="section-title m-t-0 m-b-0">{selected.title}</h6>
                                <div class="txt-hint txt-sm">
                                    Served at <code>/x/navigation/menus/{selected.handle}</code>
                                </div>
                            </div>
                            {#if writable}
                                <button type="button" class="btn sm transparent secondary" onclick={() => (confirmDelete = true)}>
                                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                    <span class="txt">Delete menu</span>
                                </button>
                                <button
                                    type="button"
                                    class="btn sm"
                                    class:loading={saving}
                                    disabled={saving || !dirty}
                                    onclick={save}
                                >
                                    <span class="txt">{dirty ? "Save" : "Saved"}</span>
                                </button>
                            {/if}
                        </div>

                        {#snippet tree(list, path)}
                            <ol class="menu-tree" class:nested={path.length > 0}>
                                {#each list as row, i (row)}
                                    {@const here = [...path, i]}
                                    <li class="menu-item">
                                        <div class="menu-item-fields">
                                            <div class="field">
                                                <input
                                                    type="text"
                                                    placeholder="Title"
                                                    bind:value={row.title}
                                                    oninput={() => (dirty = true)}
                                                    disabled={!writable}
                                                />
                                            </div>
                                            <div class="field menu-kind">
                                                <Select
                                                    ariaLabel="Kind"
                                                    bind:value={row.kind}
                                                    options={KINDS}
                                                    onchange={() => (dirty = true)}
                                                />
                                            </div>
                                            <div class="field">
                                                <input
                                                    type="text"
                                                    placeholder={PLACEHOLDER[row.kind]}
                                                    bind:value={row.target}
                                                    oninput={() => (dirty = true)}
                                                    disabled={!writable || row.kind === "home"}
                                                />
                                            </div>
                                            {#if writable}
                                                <div class="menu-item-actions">
                                                    <button type="button" class="btn circle sm transparent secondary" title="Move up" aria-label="Move up" onclick={() => move(here, -1)}>
                                                        <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
                                                    </button>
                                                    <button type="button" class="btn circle sm transparent secondary" title="Move down" aria-label="Move down" onclick={() => move(here, 1)}>
                                                        <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
                                                    </button>
                                                    <button type="button" class="btn circle sm transparent secondary" title="Move under the item above" aria-label="Indent" onclick={() => indent(here)}>
                                                        <i class="ri-indent-increase" aria-hidden="true"></i>
                                                    </button>
                                                    <button type="button" class="btn circle sm transparent secondary" title="Move out a level" aria-label="Outdent" onclick={() => outdent(here)}>
                                                        <i class="ri-indent-decrease" aria-hidden="true"></i>
                                                    </button>
                                                    <button type="button" class="btn circle sm transparent secondary" title="Add an item under this one" aria-label="Add sub-item" onclick={() => add(here)}>
                                                        <i class="ri-add-line" aria-hidden="true"></i>
                                                    </button>
                                                    <button type="button" class="btn circle sm transparent danger" title="Remove" aria-label="Remove" onclick={() => remove(here)}>
                                                        <i class="ri-close-line" aria-hidden="true"></i>
                                                    </button>
                                                </div>
                                            {/if}
                                        </div>
                                        {#if row.children.length}
                                            {@render tree(row.children, here)}
                                        {/if}
                                    </li>
                                {/each}
                            </ol>
                        {/snippet}

                        {@render tree(items, [])}

                        {#if writable}
                            <button type="button" class="btn sm secondary m-t-sm" onclick={() => add([])}>
                                <i class="ri-add-line" aria-hidden="true"></i>
                                <span class="txt">Add item</span>
                            </button>
                        {/if}
                        <div class="field-help m-t-base">
                            A link is a URL; the other kinds take the handle of the thing — a product's
                            slug, a collection's, a category's, a page's — and the storefront gets the
                            path, resolved with the paths on the Plugins screen. Four levels at most.
                        </div>
                    {/if}
                </section>
            </div>
        {/if}

    </div>
</div>

<Drawer open={createOpen} size="sm" title="New menu" onclose={() => (createOpen = false)}>
    <div class="field">
        <label for="menu-title">Title</label>
        <input id="menu-title" type="text" bind:value={form.title} placeholder="Main menu" />
    </div>
    <div class="field m-t-sm">
        <label for="menu-handle">Handle</label>
        <input id="menu-handle" type="text" bind:value={form.handle} placeholder="main-menu" />
        <div class="field-help">
            What the storefront asks for. Lowercase letters, digits and dashes; taken from the
            title when left empty.
        </div>
    </div>
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (createOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" class:loading={creating} disabled={creating || !form.title.trim()} onclick={create}>
            <span class="txt">Create</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    open={confirmDelete}
    title="Delete this menu?"
    message="The storefront stops receiving it at once. There is no undo."
    confirmLabel="Delete"
    danger
    onconfirm={destroy}
    oncancel={() => (confirmDelete = false)}
/>

<style>
    .menus-layout {
        display: grid;
        grid-template-columns: 280px minmax(0, 1fr);
        gap: var(--smSpacing);
        align-items: start;
    }
    @media (max-width: 780px) {
        .menus-layout {
            grid-template-columns: minmax(0, 1fr);
        }
    }
    .menus-list,
    .menus-editor {
        margin: 0;
        min-width: 0;
    }
    .menu-row {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
        width: 100%;
        padding: 8px 10px;
        border: 0;
        border-radius: var(--baseRadius);
        background: transparent;
        text-align: left;
        cursor: pointer;
        color: inherit;
        font: inherit;
    }
    .menu-row:hover,
    .menu-row.active {
        background: var(--surfaceAlt2Color, rgba(0, 0, 0, 0.04));
    }
    .menu-tree {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .menu-tree.nested {
        margin-left: 28px;
        padding-left: 12px;
        border-left: 2px solid var(--surfaceAlt4Color, rgba(0, 0, 0, 0.12));
    }
    .menu-item {
        margin-top: 8px;
    }
    .menu-item-fields {
        display: grid;
        grid-template-columns: minmax(120px, 1.2fr) 170px minmax(140px, 1.6fr) auto;
        gap: 8px;
        align-items: center;
    }
    .menu-item-fields .field {
        margin: 0;
    }
    .menu-item-actions {
        display: flex;
        gap: 2px;
        white-space: nowrap;
    }
    @media (max-width: 900px) {
        .menu-item-fields {
            grid-template-columns: minmax(0, 1fr);
        }
    }
</style>
