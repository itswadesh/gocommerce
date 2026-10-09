<script>
    /**
     * The media library, as a screen.
     *
     * PLAN §39a says media is a library rather than a per-product list,
     * "because the same photograph belongs to several products and should be
     * stored once". The panel had the library and no door to it: every filter
     * on this page already existed inside the picker drawer of a product
     * editor, so browsing the store's files meant opening a product first, and
     * a file attached to nothing could not be found at all.
     *
     * Two things this screen can do that the picker never could.
     *
     * `usage=unused` finds orphans — every failed upload, wrong crop and stale
     * linked URL — and DELETE now answers it. The engine refuses while any
     * product still displays a file, which is the safety story, so nothing here
     * pre-empts that with a guess.
     *
     * And alt text is editable at last, through the details drawer. It is the
     * only mutable property a media record has and the panel had no field for
     * it anywhere.
     *
     * Selection is deliberately separate from opening: the corner box selects
     * for the bulk bar, the tile itself opens the file. A library screen whose
     * only click was "select" would make looking at a file impossible.
     */
    import { api, query } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { can } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { pluralize } from "$lib/format.js";
    import { mediaQuery, mediaLabel, fileSize } from "$lib/media.js";
    import MediaLibrary from "$lib/components/MediaLibrary.svelte";
    import MediaDetails from "$lib/components/MediaDetails.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 48;

    /* Every filter lives in the URL, like every other list screen: a hunt for
       the orphans in a store is worth being able to send to somebody, and Back
       out of a file has to land on the same set. `size` and `sort` are the
       operator's vocabulary rather than the engine's — see mediaQuery, which is
       the one place that translation happens. */
    const list = listState({
        q: "",
        kind: "",
        usage: "",
        size: "",
        sort: "",
        page: 1,
        limit: PER_PAGE,
    });
    const perPage = $derived(list.params.limit);


    let items = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let view = $state("grid");

    /* The filter row's own object, mirrored from the address bar. MediaLibrary
       binds to it and calls back when something changes; this pushes that to
       the URL, and the mirror below brings it home again. */
    let filters = $state({ q: "", kind: "", usage: "", size: "", scope: "", sort: "" });

    $effect(() => {
        const p = list.params;
        filters = { q: p.q, kind: p.kind, usage: p.usage, size: p.size, scope: "", sort: p.sort };
    });

    const sel = selection();

    /* Two filter changes leave two replies in flight, and the grid would
       otherwise settle on whichever arrived last. */
    let reqId = 0;

    let detailOpen = $state(false);
    let detail = $state(null);
    let confirmOpen = $state(false);

    $effect(() => {
        list.params;
        load();
    });

    /*
     * A selection is cleared by a filter change and survives a page change.
     * The rows the operator picked are not on screen any more after a filter,
     * and a bulk delete of files nobody can see is exactly the accident this
     * exists to prevent. Page is deliberately not read here.
     */
    $effect(() => {
        list.params.q;
        list.params.kind;
        list.params.usage;
        list.params.size;
        sel.clear();
    });

    async function load() {
        // The screen renders NoAccess without this right, so firing the
        // request first would bury that explanation under a 403 toast.
        if (!can("catalog.read")) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/media" +
                    query(mediaQuery(list.params, { page: list.page, limit: perPage })),
            );
            if (mine !== reqId) return;
            items = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function applyFilters() {
        list.set({
            q: filters.q,
            kind: filters.kind,
            usage: filters.usage,
            size: filters.size,
            sort: filters.sort,
        });
    }

    /**
     * A new file goes straight on screen rather than through a reload.
     *
     * It is shown even when a filter would exclude it — the operator just added
     * it and a file that vanished on arrival reads as a failed upload. It sits
     * at the head because the listing's own order is newest first.
     */
    function added(fresh) {
        items = [...fresh, ...items];
        if (meta) meta = { ...meta, total: meta.total + fresh.length };
    }

    function open(item) {
        detail = item;
        detailOpen = true;
    }

    function saved(updated) {
        items = items.map((m) => (m.id === updated.id ? updated : m));
        detail = updated;
    }

    function deleted(gone) {
        items = items.filter((m) => m.id !== gone.id);
        if (meta) meta = { ...meta, total: Math.max(meta.total - 1, 0) };
        detailOpen = false;
        detail = null;
    }

    async function deleteSelected() {
        const rows = sel.pick(items);
        const { done } = await runBulk(rows, (m) => api.delete(`/api/admin/media/${m.id}`), {
            describe: "Deleted",
            noun: "file",
            label: mediaLabel,
        });
        sel.clear();
        // Reloaded rather than spliced: a partial failure leaves a selection
        // half gone, and the honest picture of what survived is the server's.
        if (done) await load();
    }

    /* What is on this page weighs this much. The engine reports no total for
       the whole library — there is no aggregate route and summing every page to
       invent one would be a dozen requests for a footnote — so this says which
       number it is. */
    const pageBytes = $derived(items.reduce((sum, m) => sum + (m.size_bytes || 0), 0));
</script>

<svelte:head><title>Media · GoCommerce</title></svelte:head>

{#if !can("catalog.read")}
    <NoAccess right="catalog.read" what="the media library" />
{:else}
    <div class="page page-media shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Media</div>
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
            </header>

            <MediaLibrary
                {items}
                {loading}
                bind:filters
                bind:view
                onfilter={applyFilters}
                showUpload={can("catalog.write")}
                onadded={added}
                chosen={(item) => sel.has(item.id)}
                onchoose={open}
                onselect={(item) => sel.toggle(item.id)}
                tileVerb="Open"
                emptyText="The library is empty. Every file a product shows lives here — add one above, or paste the URL of one hosted elsewhere."
            >
                {#snippet after()}
                    <div class="field-help">
                        One library for the whole store: attaching a file to a second product
                        reuses it rather than copying it. Filter to “Not used anywhere” to find
                        what nothing displays any more.
                    </div>
                {/snippet}
            </MediaLibrary>

            <BulkBar count={sel.count} noun="file" onclear={() => sel.clear()}>
                {#if can("catalog.write")}
                    <button
                        type="button"
                        class="btn sm danger"
                        onclick={() => (confirmOpen = true)}
                    >
                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                        <span class="txt">Delete</span>
                    </button>
                {/if}
            </BulkBar>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager
                    {meta}
                    {loading}
                    noun="file"
                    {perPage}
                    sizes={[24, 48, 96, 192]}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                {#if pageBytes}
                    <span class="txt-hint txt-sm">· {fileSize(pageBytes)} on this page</span>
                {/if}
            </footer>
        </div>
    </div>

    <MediaDetails
        open={detailOpen}
        item={detail}
        onsaved={saved}
        ondeleted={deleted}
        onclose={() => (detailOpen = false)}
    />

    <Confirm
        bind:open={confirmOpen}
        title="Delete {sel.count} {pluralize(sel.count, 'file')}?"
        message="They are removed from the library for good, along with the stored bytes for any uploaded here. Any that a product still displays will be refused, and named."
        confirmLabel="Delete"
        danger
        onconfirm={deleteSelected}
    />
{/if}
