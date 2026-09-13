<script>
    /**
     * Collections: the merchandising lists a storefront navigates by.
     *
     * The engine has shipped full CRUD on these for a long time and the panel
     * could reach exactly two of it — the listing, to fill a picker, and the
     * inline create inside a product editor. PATCH and DELETE were mounted and
     * called from nowhere in the browser, so a collection could be made and
     * then never renamed, described, repositioned or removed. This screen is
     * the door.
     *
     * The listing takes no search parameter, so there is no search box here.
     * Inventing one would mean either a filter over the page in hand, which
     * lies about the ones it cannot see, or an endpoint that does not exist.
     *
     * `position` is the storefront's navigation order and the arrows renumber
     * it. They renumber the WHOLE list rather than swapping a pair, because a
     * store whose collections were all created at the default 0 has no order to
     * swap — the rows are tied and the engine is falling back to the id. They
     * are therefore offered only while the whole list is on screen: renumbering
     * a slice would collide with the positions on the pages either side.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate } from "$lib/format.js";
    import CollectionEditor from "$lib/components/CollectionEditor.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 50;

    const list = listState({ page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);


    let collections = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let ordering = $state(false);

    let editorOpen = $state(false);
    let editing = $state(null);

    let confirmOpen = $state(false);
    let pendingDelete = $state(null);

    /* Reordering is only honest while there is nothing off screen to collide
       with. Past one page the position field in the drawer is the way. */
    const wholeList = $derived(!!meta && collections.length >= (meta.total ?? 0));
    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));

    /* `n` creates one, from anywhere on this screen. The shell owns the
       keystroke and fires an event; what "new" means is the screen's. */
    $effect(() => {
        if (!writable) return;
        return onNewShortcut(openCreate);
    });

    $effect(() => {
        list.params;
        load();
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
            const result = await api.get(
                "/api/admin/collections" + query({ page: list.page, limit: perPage }),
            );
            collections = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function openCreate() {
        editing = null;
        editorOpen = true;
    }

    function openEdit(collection, event) {
        event?.stopPropagation();
        editing = collection;
        editorOpen = true;
    }

    function saved() {
        editorOpen = false;
        load();
    }

    function askDelete(collection, event) {
        event.stopPropagation();
        pendingDelete = collection;
        confirmOpen = true;
    }

    async function doDelete() {
        try {
            await api.delete(`/api/admin/collections/${pendingDelete.id}`);
            toast.success(`Deleted ${pendingDelete.title}`);
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    /**
     * Moving a collection up or down the menu.
     *
     * The new order is written as positions 0..n-1 and only the rows whose
     * number actually changes are sent, which on a list already numbered
     * sequentially is two requests. There is no batch route and this does not
     * invent one: each PATCH is the same per-row call the drawer makes, so each
     * lands in its own transaction and files its own audit record.
     */
    async function move(index, delta) {
        const to = index + delta;
        if (ordering || to < 0 || to >= collections.length) return;

        const next = [...collections];
        const [row] = next.splice(index, 1);
        next.splice(to, 0, row);

        ordering = true;
        const previous = collections;
        collections = next.map((c, i) => ({ ...c, position: i }));
        try {
            for (let i = 0; i < next.length; i++) {
                if (next[i].position === i) continue;
                await api.patch(`/api/admin/collections/${next[i].id}`, { position: i });
            }
            await load();
        } catch (err) {
            collections = previous;
            toast.error(err);
        } finally {
            ordering = false;
        }
    }
</script>

<svelte:head><title>Collections · GoCommerce</title></svelte:head>

{#if !can("catalog.read")}
    <NoAccess right="catalog.read" what="collections" />
{:else}
    <div class="page page-collections shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Collections</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if writable}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn" onclick={openCreate}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">New collection</span>
                        </button>
                    </div>
                {/if}
            </header>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="min-width"></th>
                            <th class="col-field-name-id">Collection</th>
                            <th class="col-field-type-text">Slug</th>
                            <th class="col-field-type-date">Updated</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each collections as collection, index (collection.id)}
                            <tr
                                class="handle"
                                tabindex="0"
                                onclick={(e) =>
                                    e.target.closest("a") || goto(`${base}/collections/${collection.id}`)}
                                onkeydown={(e) =>
                                    rowKey(e, () => goto(`${base}/collections/${collection.id}`))}
                            >
                                <td class="min-width" data-name="Order">
                                    {#if writable && wholeList}
                                        <div class="inline-flex gap-0">
                                            <button
                                                type="button"
                                                class="btn circle sm transparent secondary"
                                                title="Move up"
                                                aria-label="Move {collection.title} up"
                                                disabled={index === 0 || ordering}
                                                onclick={(e) => (e.stopPropagation(), move(index, -1))}
                                            >
                                                <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
                                            </button>
                                            <button
                                                type="button"
                                                class="btn circle sm transparent secondary"
                                                title="Move down"
                                                aria-label="Move {collection.title} down"
                                                disabled={index === collections.length - 1 ||
                                                    ordering}
                                                onclick={(e) => (e.stopPropagation(), move(index, 1))}
                                            >
                                                <i class="ri-arrow-down-s-line" aria-hidden="true"
                                                ></i>
                                            </button>
                                        </div>
                                    {:else}
                                        <span class="txt-hint txt-sm">{collection.position}</span>
                                    {/if}
                                </td>
                                <td class="col-field-name-id" data-name="Collection">
                                    <!-- Title and description on one line, as
                                         the product list has name and handle:
                                         a second line costs every row 15px on a
                                         screen whose whole job is to fit rows,
                                         and the description reads as what it is
                                         from its weight rather than its place. -->
                                    <div class="row-name">
                                        <!-- A real link as well as a row, so a
                                             collection can be middle-clicked
                                             into a second tab and read its
                                             destination on hover. The row's own
                                             handler stands aside for it. -->
                                        <a
                                            class="txt-bold txt-ellipsis"
                                            href="{base}/collections/{collection.id}"
                                        >
                                            {collection.title}
                                        </a>
                                        {#if collection.description}
                                            <span class="txt-hint txt-sm txt-ellipsis">
                                                {collection.description}
                                            </span>
                                        {/if}
                                    </div>
                                </td>
                                <td class="col-field-type-text txt-hint txt-sm" data-name="Slug">
                                    <span class="txt-ellipsis">{collection.slug}</span>
                                </td>
                                <td class="col-field-type-date txt-hint txt-sm" data-name="Updated">
                                    {formatDate(collection.updated_at)}
                                </td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary"
                                            title="Edit the title, slug, description and position"
                                            aria-label="Edit {collection.title}"
                                            onclick={(e) => openEdit(collection, e)}
                                        >
                                            <i class="ri-edit-line" aria-hidden="true"></i>
                                        </button>
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary row-delete"
                                            title="Delete"
                                            aria-label="Delete {collection.title}"
                                            onclick={(e) => askDelete(collection, e)}
                                        >
                                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                    <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                </td>
                            </tr>
                        {/each}

                        {#if loading && !collections.length}
                            {#each Array(4) as _, i (i)}
                                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !collections.length}
                            <tr>
                                <td colspan="5" class="txt-hint txt-center p-base">
                                    No collections yet. A collection is a hand-curated list of
                                    products — a storefront's “New in” or “Gifts under £25” — and
                                    a product can be in as many as you like.
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
                    noun="collection"
                    {perPage}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                <ThemeToggle />
            </footer>
        </div>
    </div>

    <CollectionEditor
        open={editorOpen}
        collection={editing}
        onsaved={saved}
        onclose={() => (editorOpen = false)}
    />

    <Confirm
        bind:open={confirmOpen}
        title="Delete this collection?"
        message={pendingDelete
            ? `${pendingDelete.title} will be removed and every product in it loses that grouping. The products themselves are untouched, and a storefront linking to /collections/${pendingDelete.slug} will stop finding anything.`
            : ""}
        confirmLabel="Delete"
        danger
        onconfirm={doDelete}
    />
{/if}
