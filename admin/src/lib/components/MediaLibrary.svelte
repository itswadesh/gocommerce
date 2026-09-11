<script module>
    import { getToken, ApiError, apiErrorFrom } from "$lib/api.js";

    // The URL form is a popover, and a popover is addressed by id. Two
    // libraries on one page — the zone's picker and, one day, anything else —
    // would otherwise both answer to `add-media-url` and the caret would open
    // the wrong one.
    let seq = 0;

    /**
     * uploadMedia posts one file and returns the record the engine made.
     *
     * It does not go through api.js, and cannot. `request()` JSON-encodes any
     * body that is not a string, and a multipart upload has to keep the
     * boundary FormData generated for it — so this is a bare fetch, built to
     * throw the same ApiError, out of the same envelope, as everything else.
     *
     * It is exported because a product's own drop zone uploads too, and two
     * copies of a hand-rolled fetch is two places for a 401 to be handled
     * differently.
     */
    export async function uploadMedia(file) {
        const body = new FormData();
        body.append("file", file);

        const token = getToken();
        const headers = token ? { Authorization: "Bearer " + token } : {};

        let response;
        try {
            response = await fetch("/api/admin/media", { method: "POST", headers, body });
        } catch {
            throw new ApiError(
                0,
                "network_error",
                "Could not reach the store. Is it still running?",
            );
        }

        // The ok-check comes first because apiErrorFrom reads the body, and a
        // body can only be read once. This is one of exactly two places in the
        // panel that bypasses request(); without it a 401 mid-upload keeps
        // looping while everything else has already recovered.
        if (!response.ok) throw await apiErrorFrom(response);
        const payload = await response.json().catch(() => null);
        return payload?.data ?? payload;
    }
</script>

<script>
    /**
     * The store's media library: search it, filter it, add to it, choose from
     * it.
     *
     * PLAN §39a says media is a library rather than a per-product list,
     * "because the same photograph belongs to several products and should be
     * stored once". This is that library's one surface. It was written inside
     * MediaZone's picker drawer, where only a product editor could reach it;
     * lifting it out is what let `/media` exist at all, and the picker now
     * renders the same component with different callbacks.
     *
     * It deliberately does NOT fetch. The two hosts page differently — the
     * picker accumulates behind a "Load more" so a selection made on page one
     * survives page two, while the screen shows a window with the page in the
     * URL — and a component that owned the request would have to be told which
     * it was. So the host owns `items`, `meta` and `loading`, and this owns
     * every control that decides what they should be.
     *
     * `filters` is the host's object and is bound both ways, because on the
     * screen it is a mirror of the address bar and on the picker it is plain
     * component state. Changing one calls `onfilter`, which is where the host
     * decides what that means.
     */
    import { api } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import Select from "$lib/components/Select.svelte";
    import {
        SIZE_BUCKETS,
        mediaLabel,
        kindIcon,
        fileType,
        fileSize,
        mediaFiltered,
    } from "$lib/media.js";

    let {
        items = [],
        loading = false,
        filters = $bindable({ q: "", kind: "", usage: "", size: "", scope: "", sort: "" }),
        view = $bindable("grid"),
        onfilter,

        /** The label for the "on this product" scope option. Empty hides the
         *  filter entirely, which is what every host but a product editor
         *  wants — there is no product for it to mean. */
        scopeLabel = "",

        showUpload = true,
        /** Called with the records the engine made, newest first. Adding is
         *  this component's job — it draws the controls, so it owns the calls —
         *  and the host only has to say what a new file means to it. */
        onadded,

        chosen = () => false,
        badge = () => "",
        onchoose,
        /** When given, the corner box selects and the tile itself does
         *  something else — opening the file, on the library screen. With it
         *  omitted the two are the same action, which is what a picker wants. */
        onselect = null,
        tileVerb = "Select",

        emptyText = "",
        /** Rendered under the grid: a pager, a load-more button, a help line. */
        after,
    } = $props();

    const uid = `media-library-${++seq}`;

    /* Set once the engine has answered 501, for a store whose settings call
       never landed. The settings flag below is the same answer arriving before
       a file is picked, which is the one PLAN §39a asks the panel to use. */
    let blocked = $state(false);
    let blockedMessage = $state("");

    /**
     * Whether an upload can succeed.
     *
     * PLAN §39a promises that a store with no media directory "still runs: the
     * library records media by URL and the panel says why upload is unavailable
     * rather than offering a button that fails". `media_uploads_enabled` is how
     * the store says so, and reading it here is what keeps that promise at the
     * point of use rather than after a file has been chosen and sent.
     */
    const uploadsOff = $derived(blocked || !settings.mediaUploadsEnabled);
    const uploadsReason = $derived(
        blockedMessage ||
            "This store has nowhere to put uploads — no media directory is configured.",
    );

    const filtered = $derived(mediaFiltered(filters));

    let over = $state(false);
    let fileInput = $state(null);
    let uploadBusy = $state(false);
    let linkUrl = $state("");
    let linking = $state(false);
    let urlPopover = $state(null);

    /**
     * The search box asks the server, so it is debounced: a filter that fires a
     * request per keystroke sends five for "shirt" and the answers can land out
     * of order, leaving the list showing the results for "shi".
     */
    let draft = $state(filters.q ?? "");
    let searchTimer = null;

    /*
     * An external reset — the picker clears its filters every time it opens —
     * has to reach the box. The echo of this component's own push must not,
     * or a slow keystroke lands in a box that has just been rewritten. A
     * pending timer is exactly the difference between the two.
     */
    $effect(() => {
        const incoming = filters.q ?? "";
        if (searchTimer === null && incoming !== draft) draft = incoming;
    });

    function onSearchInput() {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(() => {
            searchTimer = null;
            filters.q = draft;
            onfilter?.();
        }, 250);
    }

    function clearSearch() {
        clearTimeout(searchTimer);
        searchTimer = null;
        draft = "";
        filters.q = "";
        onfilter?.();
    }

    function changed() {
        onfilter?.();
    }

    async function addFiles(files) {
        const list = [...(files ?? [])];
        if (!list.length || uploadsOff) return;

        uploadBusy = true;
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
                        blocked = true;
                        blockedMessage = err.message;
                        break;
                    }
                    toast.error(`${file.name}: ${err.message}`);
                }
            }
        } finally {
            uploadBusy = false;
        }
        // Newest first, which is the listing's own default order.
        if (added.length) onadded?.(added.reverse());
    }

    function onPick(event) {
        addFiles(event.currentTarget.files);
        // Clearing it means picking the same file twice in a row still fires.
        event.currentTarget.value = "";
    }

    function onDrop(event) {
        event.preventDefault();
        over = false;
        addFiles(event.dataTransfer?.files);
    }

    /**
     * "Add media from URL". No kind picker, which is Shopify's shape and is
     * only honest because the engine reads the kind off the URL's extension —
     * see kindForURL in media.go. Sending an explicit kind here would just be
     * this side guessing instead.
     */
    async function submitLink(event) {
        event?.preventDefault();
        const url = linkUrl.trim();
        if (linking || !url) return;
        linking = true;
        try {
            const item = await api.post("/api/admin/media/link", { url });
            // Cleared only on success, so a rejected URL is still in the box to
            // be corrected rather than retyped.
            linkUrl = "";
            if (urlPopover?.matches(":popover-open")) urlPopover.hidePopover();
            onadded?.([item]);
        } catch (err) {
            toast.error(err);
        } finally {
            linking = false;
        }
    }
</script>

<!-- Search left, layout right, as Shopify has it. Together on one row so the
     toggle does not strand on a line of its own once four filter chips fill
     the row below. -->
<div class="media-toolbar m-b-sm">
    <div class="fields searchbar">
        <div class="field">
            <input
                type="text"
                class="p-l-30"
                placeholder="Search files"
                aria-label="Search files"
                bind:value={draft}
                oninput={onSearchInput}
            />
            <!-- After the input, not before it: `.field` is display:block and
                 the input is width:100%, so a leading sibling would stack above
                 it. The icon is positioned out of flow — see gocommerce.css. -->
            <i class="ri-search-line searchbar-icon" aria-hidden="true"></i>
        </div>
        {#if draft}
            <div class="field addon p-r-5">
                <button
                    type="button"
                    title="Clear"
                    class="btn sm secondary transparent circle"
                    onclick={clearSearch}
                >
                    <i class="ri-close-line" aria-hidden="true"></i>
                </button>
            </div>
        {/if}
    </div>

    <div class="split-btn" role="group" aria-label="Layout">
        <button
            type="button"
            class="btn sm secondary"
            class:active={view === "grid"}
            aria-pressed={view === "grid"}
            title="Grid"
            aria-label="Grid"
            onclick={() => (view = "grid")}
        >
            <i class="ri-grid-fill" aria-hidden="true"></i>
        </button>
        <button
            type="button"
            class="btn sm secondary split-btn-toggle"
            class:active={view === "list"}
            aria-pressed={view === "list"}
            title="List"
            aria-label="List"
            onclick={() => (view = "list")}
        >
            <i class="ri-list-unordered" aria-hidden="true"></i>
        </button>
    </div>
</div>

<div class="media-filters m-b-sm">
    <Select
        class="sm"
        bind:value={filters.kind}
        onchange={changed}
        options={[
            { value: "", label: "File type: any" },
            { value: "image", label: "Images" },
            { value: "video", label: "Video" },
            { value: "model", label: "3D models" },
        ]}
    />
    <Select
        class="sm"
        bind:value={filters.size}
        onchange={changed}
        options={[
            { value: "", label: "File size: any" },
            ...Object.entries(SIZE_BUCKETS).map(([value, b]) => ({ value, label: b.label })),
        ]}
    />
    <!-- "Not used anywhere" is the orphan filter: the one question a library
         can answer that a per-product list cannot. -->
    <Select
        class="sm"
        bind:value={filters.usage}
        onchange={changed}
        options={[
            { value: "", label: "Used in: anything" },
            { value: "used", label: "Used on a product" },
            { value: "unused", label: "Not used anywhere" },
        ]}
    />
    {#if scopeLabel}
        <Select
            class="sm"
            bind:value={filters.scope}
            onchange={changed}
            options={[
                { value: "", label: "Product: any" },
                { value: "product", label: scopeLabel },
            ]}
        />
    {/if}
    <Select
        class="sm"
        bind:value={filters.sort}
        onchange={changed}
        options={[
            { value: "", label: "Sort: newest" },
            { value: "filename:asc", label: "Sort: name A–Z" },
            { value: "size_bytes:desc", label: "Sort: largest first" },
        ]}
    />
</div>

{#if showUpload}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
        class="media-dropzone m-b-base"
        data-over={over}
        ondragover={(e) => (e.preventDefault(), (over = !uploadsOff))}
        ondragleave={() => (over = false)}
        ondrop={onDrop}
    >
        <input
            bind:this={fileInput}
            type="file"
            multiple
            class="hidden"
            aria-label="Files to add to the library"
            onchange={onPick}
        />

        <!-- Shopify's split button: the label uploads, the caret opens the URL
             form. Two buttons rather than one with a menu, because the common
             action should not cost a menu to reach.

             With uploads unconfigured the left half is not disabled but gone:
             a greyed control invites a hunt for what would ungrey it, and what
             this store can still do — record a file hosted elsewhere — is the
             half that remains. -->
        <!-- `.split-btn` squares the inner edges of the pair, which is wrong
             for the lone button the blocked case leaves behind. -->
        <div class:split-btn={!uploadsOff}>
            {#if !uploadsOff}
                <button
                    type="button"
                    class="btn sm secondary"
                    class:loading={uploadBusy}
                    disabled={uploadBusy}
                    onclick={() => fileInput?.click()}
                >
                    <span class="txt">Add media</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary split-btn-toggle"
                    popovertarget={uid}
                    aria-label="Add media from URL"
                    title="Add media from URL"
                >
                    <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
                </button>
            {:else}
                <button type="button" class="btn sm secondary" popovertarget={uid}>
                    <i class="ri-link" aria-hidden="true"></i>
                    <span class="txt">Add media from URL</span>
                </button>
            {/if}

            <div bind:this={urlPopover} id={uid} class="dropdown add-media-url" popover="auto">
                <div class="add-media-url-title txt-bold">Add media from URL</div>
                <form onsubmit={submitLink}>
                    <div class="field">
                        <label for="{uid}-url">Image, video or 3D model URL</label>
                        <input
                            id="{uid}-url"
                            type="text"
                            placeholder="https://"
                            bind:value={linkUrl}
                        />
                    </div>
                    <button
                        type="submit"
                        class="btn sm m-t-sm"
                        class:loading={linking}
                        disabled={linking || !linkUrl.trim()}
                    >
                        <span class="txt">Add file</span>
                    </button>
                </form>
            </div>
        </div>

        {#if uploadsOff}
            <div class="txt-sm txt-hint">
                {uploadsReason} A file hosted somewhere else can still be recorded here by its
                URL.
            </div>
        {:else}
            <div class="txt-sm txt-hint">Drag and drop images, videos, 3D models, and files</div>
        {/if}
    </div>
{/if}

<div class="media-grid" class:media-list={view === "list"} role="list">
    {#each items as item (item.id)}
        {@const isChosen = chosen(item)}
        {@const tag = badge(item)}
        <div class="media-cell" role="listitem">
            <div class="media-tile" data-dropinto={isChosen}>
                <button
                    type="button"
                    class="btn transparent p-0 block"
                    style="height: 100%"
                    aria-pressed={onselect ? undefined : isChosen}
                    aria-label="{isChosen && !onselect ? 'Deselect' : tileVerb} {mediaLabel(item)}"
                    title={mediaLabel(item)}
                    onclick={() => onchoose?.(item)}
                >
                    {#if item.kind === "image"}
                        <img src={item.url} alt={mediaLabel(item)} />
                    {:else}
                        <!-- A .glb cannot be drawn without a viewer library, and
                             this panel ships no third-party JavaScript. An icon
                             that admits what the file is beats a broken <img>. -->
                        <i class={kindIcon(item.kind)} style="font-size: 30px" aria-hidden="true"
                        ></i>
                    {/if}
                </button>

                <!--
                    The corner box. It is a button rather than the decorative
                    span it used to be, because on the library screen the tile
                    itself opens the file and something still has to select it.
                    Not an <input type="checkbox">: `form.css` styles any
                    `.field` containing one as though the whole field were a
                    checkbox row, and this is not in a field at all.
                -->
                <button
                    type="button"
                    class="media-check p-0"
                    data-checked={isChosen}
                    aria-pressed={isChosen}
                    aria-label="{isChosen ? 'Deselect' : 'Select'} {mediaLabel(item)}"
                    onclick={() => (onselect ?? onchoose)?.(item)}
                >
                    {#if isChosen}<i class="ri-check-line" aria-hidden="true"></i>{/if}
                </button>

                {#if tag}
                    <span class="label sm media-tile-badge">{tag}</span>
                {/if}
            </div>

            <!-- The filename, like Shopify — not `mediaLabel`, which prefers the
                 alt text. Alt describes the picture; this line identifies the
                 file, and two files can share a description. -->
            <div class="media-cell-name txt-sm txt-ellipsis" title={mediaLabel(item)}>
                {item.filename || mediaLabel(item)}
            </div>
            <div class="media-cell-meta txt-sm txt-hint">
                {fileType(item)}{#if view === "list" && item.size_bytes}
                    · {fileSize(item.size_bytes)}{/if}
            </div>
        </div>
    {/each}
</div>

{#if loading && !items.length}
    <div class="block txt-center p-base"><span class="loader lg"></span></div>
{/if}

{#if !loading && !items.length}
    <div class="txt-center txt-hint p-base">
        {#if filtered}
            No files match these filters.
        {:else}
            {emptyText ||
                "The library is empty. Add a file above, or paste the URL of one hosted elsewhere."}
        {/if}
    </div>
{/if}

{@render after?.()}
