<script>
    /**
     * A product's media: a drop zone, a reorderable grid of thumbnails, and a
     * picker onto the store-wide library.
     *
     * Three things here are shaped by the engine rather than by taste.
     *
     * The list is written with PUT and the whole order every time, because that
     * is what the route takes — an add and a reorder are the same request, so a
     * drag cannot settle half applied.
     *
     * The upload does not go through api.js; `uploadMedia` in MediaLibrary is
     * the one hand-rolled multipart fetch in the panel, and this calls it
     * rather than keeping a second copy.
     *
     * And the list is read back before it is written. `readable` guards a build
     * whose engine mounts PUT on this path but not GET; the shipped engine
     * mounts both, and the notice exists because a zone that silently showed an
     * empty grid would invite an operator to replace files they cannot see.
     *
     * The library itself is no longer written here. It is `/media` now, and
     * MediaLibrary is the component both surfaces render — the picker is that
     * component in a drawer, with a selection instead of a details drawer.
     */
    import { api, request, query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { pluralize } from "$lib/format.js";
    import { mediaQuery, mediaLabel } from "$lib/media.js";
    import { base } from "$app/paths";
    import Drawer from "$lib/components/Drawer.svelte";
    import MediaLibrary, { uploadMedia } from "$lib/components/MediaLibrary.svelte";
    import MediaDetails from "$lib/components/MediaDetails.svelte";

    let { productId, media = $bindable([]), disabled = false } = $props();

    const LIBRARY_PER_PAGE = 24;

    let busy = $state(false);
    let readable = $state(true);
    let over = $state(false);
    let fileInput = $state(null);

    /* Set once the engine answers 501: the store has nowhere to put files,
       which is a configuration answer rather than a failure to retry. The
       store's own `media_uploads_enabled` says the same thing before a file is
       picked, which is what PLAN §39a asks the panel to use. */
    let uploadsBlocked = $state(false);
    let uploadsMessage = $state("");

    const uploadsOff = $derived(uploadsBlocked || !settings.mediaUploadsEnabled);
    const uploadsReason = $derived(
        uploadsMessage ||
            "This store has nowhere to put uploads — no media directory is configured.",
    );

    let dragIndex = $state(-1);
    let overIndex = $state(-1);

    let libraryOpen = $state(false);
    let libraryItems = $state([]);
    let libraryMeta = $state(null);
    let libraryLoading = $state(false);
    let libraryPage = $state(1);
    let libraryView = $state("grid");
    /* Deliberately NOT in the address bar, unlike the /media screen: a drawer
       that writes to the URL leaves a stale ?usage= behind when it closes and
       fights the host screen's own parameters. */
    let libraryFilters = $state({ q: "", kind: "", usage: "", size: "", scope: "", sort: "" });
    let picked = $state([]);

    let detailOpen = $state(false);
    let detail = $state(null);

    const attachedIds = $derived(new Set(media.map((m) => m.id)));
    const libraryHasMore = $derived(!!libraryMeta && libraryItems.length < libraryMeta.total);

    $effect(() => {
        const id = productId;
        if (!id) return;

        let cancelled = false;
        (async () => {
            try {
                const result = await api.get(`/api/admin/products/${id}/media`);
                if (cancelled) return;
                media = result?.data ?? result ?? [];
                readable = true;
            } catch (err) {
                if (cancelled) return;
                // 405 is what the router answers for a path it serves under a
                // different method, which is exactly this case; 404 covers a
                // build where the path is not mounted at all.
                if (err.status === 405 || err.status === 404) {
                    readable = false;
                    media = [];
                    return;
                }
                toast.error(err);
            }
        })();
        return () => {
            cancelled = true;
        };
    });

    /**
     * persist writes the whole ordered list and takes the server's answer back
     * as the new truth. The optimistic assignment is undone on failure: a grid
     * left showing an order the engine refused is worse than a jump.
     */
    async function persist(next) {
        const previous = media;
        media = next;
        busy = true;
        try {
            const result = await request("PUT", `/api/admin/products/${productId}/media`, {
                body: { media_ids: next.map((m) => m.id) },
            });
            media = result?.data ?? result ?? [];
        } catch (err) {
            media = previous;
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    async function addFiles(files) {
        const list = [...(files ?? [])];
        if (!list.length || disabled || uploadsOff) return;

        busy = true;
        const added = [];
        try {
            for (const file of list) {
                try {
                    added.push(await uploadMedia(file));
                } catch (err) {
                    if (err.status === 501) {
                        // The envelope carries the reason and the fix; nothing
                        // this layer could invent would be more useful, and the
                        // rest of the batch would fail identically.
                        uploadsBlocked = true;
                        uploadsMessage = err.message;
                        break;
                    }
                    toast.error(`${file.name}: ${err.message}`);
                }
            }
        } finally {
            busy = false;
        }

        if (added.length) {
            await persist([...media, ...added]);
            toast.success(`Added ${added.length} ${pluralize(added.length, "file")}`);
        }
    }

    function onPick(event) {
        addFiles(event.currentTarget.files);
        // Clearing it means picking the same file twice in a row still fires.
        event.currentTarget.value = "";
    }

    function onDragOverZone(event) {
        if (disabled) return;
        event.preventDefault();
        over = true;
    }

    function onDropZone(event) {
        if (disabled) return;
        event.preventDefault();
        over = false;
        // A tile being dragged within the grid is a reorder, never an upload —
        // belt to dropTile's braces, since a drop that misses every tile still
        // lands here with the dragged image on the dataTransfer.
        if (dragIndex >= 0) {
            dragIndex = -1;
            overIndex = -1;
            return;
        }
        addFiles(event.dataTransfer?.files);
    }

    function remove(index) {
        persist(media.filter((_, i) => i !== index));
    }

    function move(from, to) {
        if (from === to || to < 0 || to >= media.length) return;
        const next = [...media];
        const [item] = next.splice(from, 1);
        next.splice(to, 0, item);
        persist(next);
    }

    function startDrag(event, index) {
        if (disabled) return;
        dragIndex = index;
        // Firefox will not begin a drag unless the payload is set.
        event.dataTransfer?.setData("text/plain", String(index));
        if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
    }

    function dragOverTile(event, index) {
        if (dragIndex < 0) return;
        event.preventDefault();
        // See dropTile: the zone behind this tile is a file drop target, and it
        // must not treat a reorder as an upload in progress.
        event.stopPropagation();
        overIndex = index;
    }

    /**
     * Reordering, and the reason it stops propagating.
     *
     * The zone behind these tiles accepts dropped files, and this event bubbles
     * straight into it. Chrome puts a dragged <img> on the dataTransfer as a
     * file, so dropping a tile back on the grid read as "the operator dropped a
     * picture here" — the reorder ran, and then the same image uploaded again as
     * a second library item. One drag, two copies.
     */
    function dropTile(event, index) {
        if (dragIndex < 0) return;
        event.preventDefault();
        event.stopPropagation();
        over = false;
        const from = dragIndex;
        dragIndex = -1;
        overIndex = -1;
        move(from, index);
    }

    function endDrag() {
        dragIndex = -1;
        overIndex = -1;
    }

    /**
     * The arrow keys on the handle are not a nicety: dragging is the only way
     * to reorder, and a pointer gesture has no keyboard equivalent unless one
     * is written.
     */
    function onHandleKey(event, index) {
        if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
            event.preventDefault();
            move(index, index - 1);
        } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
            event.preventDefault();
            move(index, index + 1);
        }
    }

    function openLibrary() {
        picked = [];
        libraryPage = 1;
        libraryItems = [];
        libraryFilters = { q: "", kind: "", usage: "", size: "", scope: "", sort: "" };
        libraryOpen = true;
        loadLibrary();
    }

    async function loadLibrary() {
        libraryLoading = true;
        try {
            const result = await api.get(
                "/api/admin/media" +
                    query(
                        mediaQuery(
                            {
                                ...libraryFilters,
                                // "On this product" is the same filter the engine
                                // takes by id; the picker just knows which
                                // product it is.
                                product_id: libraryFilters.scope === "product" ? productId : "",
                            },
                            { page: libraryPage, limit: LIBRARY_PER_PAGE },
                        ),
                    ),
            );
            libraryItems =
                libraryPage === 1 ? result.data : [...libraryItems, ...result.data];
            libraryMeta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            libraryLoading = false;
        }
    }

    function reloadLibrary() {
        libraryPage = 1;
        loadLibrary();
    }

    /**
     * A file added from inside the picker lands in the library and is
     * pre-selected rather than attached straight to the product, because the
     * drawer's contract is "choose things, then press Done" — attaching behind
     * Cancel would write something the operator then could not take back.
     */
    function addedToLibrary(fresh) {
        libraryItems = [...fresh, ...libraryItems];
        picked = [...picked, ...fresh];
    }

    function togglePick(item) {
        picked = picked.some((p) => p.id === item.id)
            ? picked.filter((p) => p.id !== item.id)
            : [...picked, item];
    }

    async function addPicked() {
        const fresh = picked.filter((p) => !attachedIds.has(p.id));
        libraryOpen = false;
        if (fresh.length) await persist([...media, ...fresh]);
    }

    function openDetails(item) {
        detail = item;
        detailOpen = true;
    }

    /**
     * Alt text saved from the tile has to land in three places, because three
     * lists hold the same record: the product's own order, whatever the picker
     * has loaded, and the drawer looking at it. Nothing is refetched — the
     * engine returned the row it just wrote.
     */
    function detailSaved(updated) {
        media = media.map((m) => (m.id === updated.id ? { ...m, ...updated } : m));
        libraryItems = libraryItems.map((m) => (m.id === updated.id ? updated : m));
        detail = updated;
    }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
    class="media-zone"
    ondragover={onDragOverZone}
    ondragleave={() => (over = false)}
    ondrop={onDropZone}
>
    {#if media.length}
        <div class="media-grid m-b-sm" role="list">
            {#each media as item, index (item.id)}
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <div
                    class="media-tile"
                    role="listitem"
                    draggable={!disabled}
                    data-dragging={dragIndex === index}
                    data-dropinto={overIndex === index && dragIndex !== index}
                    ondragstart={(e) => startDrag(e, index)}
                    ondragover={(e) => dragOverTile(e, index)}
                    ondrop={(e) => dropTile(e, index)}
                    ondragend={endDrag}
                >
                    {#if item.kind === "image"}
                        <img src={item.url} alt={mediaLabel(item)} />
                    {:else if item.kind === "video"}
                        <!-- svelte-ignore a11y_media_has_caption -->
                        <video src={item.url} preload="metadata" muted playsinline></video>
                    {:else}
                        <!--
                            A .glb cannot be drawn without a viewer library, and
                            this panel ships no third-party JavaScript. An icon
                            that admits what the file is beats a broken <img>.
                        -->
                        <i class="ri-box-3-line" style="font-size: 30px" aria-hidden="true"></i>
                        <span class="media-tile-name txt-sm">{item.filename || "3D model"}</span>
                    {/if}

                    <div class="media-tile-actions">
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            {disabled}
                            title="Drag to reorder, or use the arrow keys"
                            aria-label="Reorder {mediaLabel(item)} — use the arrow keys to move it"
                            onkeydown={(e) => onHandleKey(e, index)}
                        >
                            <i class="ri-draggable" aria-hidden="true"></i>
                        </button>
                        <!-- Alt text is edited here rather than on a form of its
                             own: the picture is the only context in which the
                             sentence describing it can be judged. -->
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            title="File details and alt text"
                            aria-label="Details for {mediaLabel(item)}"
                            onclick={() => openDetails(item)}
                        >
                            <i class="ri-information-line" aria-hidden="true"></i>
                        </button>
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            {disabled}
                            title="Detach from this product. The file stays in the library."
                            aria-label="Detach {mediaLabel(item)} from this product"
                            onclick={() => remove(index)}
                        >
                            <i class="ri-close-line" aria-hidden="true"></i>
                        </button>
                    </div>

                    {#if index === 0}
                        <span class="label sm media-tile-badge">First</span>
                    {/if}
                </div>
            {/each}
        </div>
    {/if}

    <div class="media-dropzone" data-over={over}>
        <i class="ri-image-add-line" style="font-size: 26px" aria-hidden="true"></i>
        <div class="txt-sm">Accepts images, videos, or 3D models</div>

        <input
            bind:this={fileInput}
            type="file"
            multiple
            class="hidden"
            aria-label="Files to upload"
            onchange={onPick}
        />

        <div class="inline-flex gap-sm flex-wrap">
            <!-- Gone rather than greyed when the store has nowhere to put a
                 file: PLAN §39a asks the panel to say why upload is unavailable
                 instead of offering a button that fails, and a disabled control
                 with no explanation only invites a hunt for what would enable
                 it. "Select existing" still records a file by URL. -->
            {#if !uploadsOff}
                <button
                    type="button"
                    class="btn sm"
                    class:loading={busy}
                    disabled={disabled || busy}
                    onclick={() => fileInput?.click()}
                >
                    <i class="ri-upload-cloud-2-line" aria-hidden="true"></i>
                    <span class="txt">Upload new</span>
                </button>
            {/if}
            <button type="button" class="btn sm secondary" {disabled} onclick={openLibrary}>
                <i class="ri-folder-image-line" aria-hidden="true"></i>
                <span class="txt">Select existing</span>
            </button>
        </div>
    </div>

    {#if uploadsOff}
        <div class="field-help">
            {uploadsReason} "Select existing" can still record a file hosted somewhere else by
            its URL.
        </div>
    {/if}

    {#if !readable}
        <div class="field-help error">
            This engine has no route that reads a product's media back — only one that replaces
            it — so this grid shows what was attached here, not what the product already has.
            Adding or removing anything writes the whole list, which will drop files that were
            attached elsewhere.
        </div>
    {/if}

    <div class="field-help">
        The first file leads. Removing one detaches it from this product and keeps it in your
        <a href="{base}/media">media library</a>, which is where a file is renamed, described or
        deleted for good.
    </div>
</div>

<Drawer open={libraryOpen} title="Select file" onclose={() => (libraryOpen = false)}>
    <MediaLibrary
        items={libraryItems}
        loading={libraryLoading}
        bind:filters={libraryFilters}
        bind:view={libraryView}
        onfilter={reloadLibrary}
        scopeLabel="On this product"
        onadded={addedToLibrary}
        chosen={(item) => picked.some((p) => p.id === item.id)}
        badge={(item) => (attachedIds.has(item.id) ? "On product" : "")}
        onchoose={togglePick}
    >
        {#snippet after()}
            {#if libraryHasMore}
                <button
                    type="button"
                    class="btn expanded block m-t-sm"
                    class:loading={libraryLoading}
                    disabled={libraryLoading}
                    onclick={() => (libraryPage += 1, loadLibrary())}
                >
                    <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
                    <span class="txt">Load more</span>
                </button>
            {/if}

            <div class="field-help">
                Search covers your whole library. A URL only records a link — the file is never
                copied, and off-origin ones will not preview here.
            </div>
        {/snippet}
    </MediaLibrary>

    {#snippet footer()}
        <button
            type="button"
            class="btn transparent m-r-auto"
            onclick={() => (libraryOpen = false)}
        >
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" disabled={!picked.length} onclick={addPicked}>
            <span class="txt">
                Done{picked.length ? ` (${picked.length})` : ""}
            </span>
        </button>
    {/snippet}
</Drawer>

<!--
    Deleting is not offered from inside a product editor. Every file reachable
    here is displayed by the product being edited, so the engine's ON DELETE
    RESTRICT would refuse every one of them; the library screen is where a file
    that nothing displays gets removed.
-->
<MediaDetails
    open={detailOpen}
    item={detail}
    allowDelete={false}
    onsaved={detailSaved}
    onclose={() => (detailOpen = false)}
/>
