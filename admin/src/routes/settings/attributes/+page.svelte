<script>
    /**
     * The attribute dictionary: what a category may ask of a product, and which
     * answers it offers.
     *
     * It is a settings screen rather than a top-level one because that is how
     * it is used — vocabulary an operator configures once and then consumes
     * from the categories drawer, not a screen anyone works in daily.
     *
     * The handle is the whole point of the table and the one thing this screen
     * will not let you edit after the fact. It is the key a category's metadata
     * names and the table's own primary key, so renaming it would detach every
     * category asking for the field and say nothing; the engine refuses it, and
     * a box that looked editable and then 400'd would be worse than a box that
     * says so. Moving a field is a create, an edit of the categories naming it,
     * and a delete.
     */
    import { api, can } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { toast } from "$lib/toast.svelte.js";
    import SettingsSidebar from "$lib/components/SettingsSidebar.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import TokenInput from "$lib/components/TokenInput.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import { listState } from "$lib/liststate.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    import { formatDate } from "$lib/format.js";

    const PER_PAGE = 50;

    /*
     * The search, the page and the page size live in the URL, like every other
     * list in the panel. The screen rendered a Pager against local state, so
     * page 3 of a search could not be bookmarked, was lost on Back, and could
     * not be sent to anybody — the exact defect liststate.svelte.js exists to
     * end. `set()` returns to page 1 on a search change, so the comment that
     * used to say so lives there now rather than here.
     */
    const list = listState({ q: "", page: 1, limit: PER_PAGE });
    const search = $derived(list.params.q);
    const perPage = $derived(list.params.limit);
    /* The box binds to its own draft and pushes to the URL, rather than reading
       its value back from it: a goto is asynchronous, and an input whose value
       came from the address bar drops characters typed while one is in flight. */
    let draftSearch = $state(list.params.q);

    let loading = $state(true);
    let entries = $state([]);
    let meta = $state(null);

    let open = $state(false);
    let editing = $state(null);
    let form = $state(blank());
    let saving = $state(false);

    let confirmOpen = $state(false);
    let doomed = $state(null);

    // The search box re-runs the load, and a new search starts at the first
    // page: page 4 of the old result is not a page of the new one.
    $effect(() => {
        list.params;
        load();
    });

    async function load() {
        if (!can("catalog.read")) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const res = await api.get(
                "/api/admin/taxonomy-attributes" +
                    list.query({ limit: perPage }),
            );
            entries = res.data ?? [];
            meta = res.meta ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function blank() {
        return { handle: "", label: "", choices: [] };
    }

    function openNew() {
        editing = null;
        form = blank();
        open = true;
    }

    function openEdit(entry) {
        editing = entry;
        form = {
            handle: entry.handle,
            label: entry.label,
            choices: [...(entry.choices ?? [])],
        };
        open = true;
    }

    async function save(event) {
        event.preventDefault();
        if (saving) return;
        saving = true;
        try {
            if (editing) {
                // Handle is absent from the patch on purpose: sending it is a
                // 400 from the engine, which is what it should be.
                await api.patch(`/api/admin/taxonomy-attributes/${encodeURIComponent(editing.handle)}`, {
                    label: form.label.trim(),
                    choices: form.choices,
                });
                toast.success("Field saved");
            } else {
                await api.post("/api/admin/taxonomy-attributes", {
                    handle: form.handle.trim(),
                    label: form.label.trim(),
                    choices: form.choices,
                });
                toast.success("Field added");
            }
            open = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(entry) {
        doomed = entry;
        confirmOpen = true;
    }

    async function remove() {
        if (!doomed) return;
        try {
            await api.delete(`/api/admin/taxonomy-attributes/${encodeURIComponent(doomed.handle)}`);
            toast.success(`${doomed.label} removed from the dictionary`);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            doomed = null;
        }
    }
</script>

<svelte:head><title>Attribute dictionary · GoCommerce</title></svelte:head>

<div class="page page-attributes shopify-skin">
    <SettingsSidebar />

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Attribute dictionary</div>
            </nav>
            <div class="flex-fill"></div>

            <form class="fields searchbar" onsubmit={(e) => e.preventDefault()}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search a handle or a label"
                        bind:value={draftSearch}
                        oninput={(e) => list.set({ q: e.currentTarget.value })}
                    />
                </div>
                {#if search}
                    <div class="field addon p-r-5">
                        <button
                            type="button"
                            class="btn sm pill secondary transparent"
                            onclick={() => ((draftSearch = ""), list.set({ q: "" }))}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            {#if can("catalog.write")}
                <div class="page-header-primary-btns">
                    <button type="button" class="btn" onclick={openNew}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New field</span>
                    </button>
                </div>
            {/if}
        </header>

        {#if !can("catalog.read")}
            <NoAccess right="catalog.read" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Handle</th>
                            <th class="col-field-type-text">Label</th>
                            <th class="col-field-type-text">Choices</th>
                            <th class="col-field-type-date">Updated</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each entries as entry (entry.handle)}
                            <tr
                                class="handle"
                                tabindex="0"
                                onclick={() => openEdit(entry)}
                                onkeydown={(e) => rowKey(e, () => openEdit(entry))}
                            >
                                <td class="col-field-name-id" data-name="Handle">
                                    <span class="txt-code">{entry.handle}</span>
                                </td>
                                <td class="col-field-type-text" data-name="Label">
                                    <span class="txt-bold">{entry.label}</span>
                                </td>
                                <td class="col-field-type-text" data-name="Choices">
                                    {#if entry.choices?.length}
                                        {#each entry.choices.slice(0, 3) as choice (choice)}
                                            <span class="label">{choice}</span>
                                        {/each}
                                        {#if entry.choices.length > 3}
                                            <span class="txt-hint txt-sm">
                                                and {entry.choices.length - 3} more
                                            </span>
                                        {/if}
                                    {:else}
                                        <span class="txt-hint">Free text</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-date" data-name="Updated">
                                    {formatDate(entry.updated_at)}
                                </td>
                                <td class="col-meta min-width">
                                    {#if can("catalog.write")}
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary row-delete"
                                            aria-label="Delete {entry.label}"
                                            title="Delete"
                                            onclick={(e) => (e.stopPropagation(), askDelete(entry))}
                                        >
                                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                    <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !entries.length}
                            {#each Array(3) as _, i (i)}
                                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {/if}

                        {#if !loading && !entries.length}
                            <tr>
                                <td colspan="5" class="txt-center txt-hint p-base">
                                    <div class="m-b-10">
                                        <i
                                            class="ri-list-settings-line"
                                            style="font-size: 32px"
                                            aria-hidden="true"
                                        ></i>
                                    </div>
                                    {#if search}
                                        Nothing matches “{search}”.
                                    {:else}
                                        The dictionary is empty. Add a field, or import Shopify's
                                        under Import / export — the taxonomy brings hundreds of
                                        them with it.
                                    {/if}
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
                    noun="field"
                    {perPage}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                <div class="flex-fill"></div>
                <ThemeToggle />
            </footer>
        {/if}
    </div>
</div>

<Drawer
    {open}
    size="popup"
    title={editing ? "Edit field" : "New field"}
    onclose={() => (open = false)}
>
    <form id="attribute-form" onsubmit={save}>
        <div class="field required">
            <label for="a-handle">Handle</label>
            {#if editing}
                <div class="txt-code">{editing.handle}</div>
            {:else}
                <input
                    id="a-handle"
                    type="text"
                    autocomplete="off"
                    placeholder="sleeve-length"
                    bind:value={form.handle}
                />
            {/if}
        </div>
        <div class="field-help">
            {#if editing}
                A handle cannot be changed: it is what every category asking for this field names,
                and what the answers already given are filed under.
            {:else}
                The key a category's metadata names. Lower case, no spaces and no slashes — it is
                also a URL segment. Published handles are hyphenated, like <code>sleeve-length</code
                >.
            {/if}
        </div>

        <div class="field required m-t-sm">
            <label for="a-label">Label</label>
            <input
                id="a-label"
                type="text"
                autocomplete="off"
                placeholder="Sleeve length"
                bind:value={form.label}
            />
        </div>
        <div class="field-help">What the field is called wherever it is asked.</div>

        <div class="field m-t-sm">
            <label for="a-choices">Choices</label>
            <TokenInput
                id="a-choices"
                bind:values={form.choices}
                emptyText="Type a value and press Enter. None means the field takes free text."
            />
        </div>
        <div class="field-help">
            The order you type is the order a product page offers them — sizes run XS to XL, not
            alphabetically. These values are the whole point of an entry here: a category names the
            handle, and the list comes from this row.
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}>
            <span class="txt">{can("catalog.write") ? "Cancel" : "Close"}</span>
        </button>
        <!-- A row opens this drawer for anyone who may read the catalog, since
             reading the field's values is the point of opening it. Save goes
             rather than greys for an operator the engine would refuse. -->
        {#if can("catalog.write")}
            <button
                type="submit"
                form="attribute-form"
                class="btn expanded"
                class:loading={saving}
                disabled={saving}
            >
                <span class="txt">{editing ? "Save changes" : "Add field"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Remove {doomed?.label ?? ''}?"
    message="Categories asking for this field keep it — it just stops offering a fixed list of values, and becomes free text."
    confirmLabel="Remove"
    danger
    onconfirm={remove}
/>
