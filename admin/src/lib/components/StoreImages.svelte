<script>
    /**
     * The store's logo, its logo for dark backgrounds and its icon (D148):
     * pictures from the media library, held on the store profile by id.
     *
     * Each one saves the moment it is chosen, uploaded or removed, rather than
     * waiting for the details form's button: a picture is one decision, and
     * the preview above it already shows what the storefront will. The save
     * sends the profile as last read plus the one picture, so a half-typed
     * address in the form below is neither saved nor lost.
     */
    import { api, can, query, storeProfile } from "$lib/api.js";
    import { mediaQuery } from "$lib/media.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import MediaLibrary, { uploadMedia } from "$lib/components/MediaLibrary.svelte";

    let { profile, editable = false, onsaved } = $props();

    // The engine refuses more than this (MaxStoreImageBytes); saying so before
    // the upload saves a phone the time it takes to send a photograph.
    const MAX_BYTES = 2 << 20;
    // What the media store serves back as a picture; SVG is not among them.
    const ACCEPT = "image/png,image/jpeg,image/webp,image/gif,image/avif";

    const SLOTS = [
        {
            key: "logo",
            label: "Logo",
            hint: "Shown in the storefront's header. A wide PNG or WebP with a transparent background, about 400 × 100 pixels.",
        },
        {
            key: "logo_dark",
            label: "Logo for dark backgrounds",
            hint: "Optional. Used where a theme draws the logo on a dark header or footer.",
        },
        {
            key: "icon",
            label: "Icon",
            hint: "The browser tab and a phone's home screen. A square picture, at least 192 × 192 pixels.",
        },
    ];

    const pickable = $derived(can("media.read"));
    const uploadable = $derived(can("media.write"));

    // Per slot: "" | "uploading" | "saving" | "saved".
    let status = $state({});
    let open = $state("");
    let iconShape = $state(null); // { w, h } of the icon once it has loaded

    const urlOf = (key) => profile?.[`${key}_url`] ?? "";
    const darkLogo = $derived(urlOf("logo_dark") || urlOf("logo"));
    const name = $derived(profile?.name || "Your store");
    const iconSquare = $derived(!iconShape || iconShape.w === iconShape.h);

    async function save(key, id) {
        status[key] = "saving";
        try {
            const saved = await storeProfile.save({ ...profile, [`${key}_media_id`]: id });
            onsaved?.(saved);
            status[key] = "saved";
            toast.success(id ? "Picture saved" : "Picture removed");
            setTimeout(() => status[key] === "saved" && (status[key] = ""), 1600);
        } catch (err) {
            status[key] = "";
            toast.error(err);
        }
    }

    async function upload(key, event) {
        const file = event.currentTarget.files?.[0];
        event.currentTarget.value = "";
        if (!file) return;
        if (file.size > MAX_BYTES) {
            toast.error(`That picture is ${(file.size / 1048576).toFixed(1)} MB; every page loads it, so the limit is 2 MB.`);
            return;
        }
        status[key] = "uploading";
        try {
            const item = await uploadMedia(file);
            await save(key, item.id);
        } catch (err) {
            status[key] = "";
            toast.error(err);
        }
    }

    // The library, opened under one slot at a time.
    let items = $state([]);
    let loading = $state(false);
    let page = $state(1);
    let meta = $state(null);
    let filters = $state({ q: "", kind: "image", usage: "", size: "", scope: "", sort: "" });
    let view = $state("grid");
    const hasMore = $derived(!!meta && items.length < meta.total);

    function toggleLibrary(key) {
        if (open === key) {
            open = "";
            return;
        }
        open = key;
        page = 1;
        items = [];
        load();
    }

    async function load() {
        loading = true;
        try {
            const result = await api.get("/api/admin/media" + query(mediaQuery(filters, { page, limit: 24 })));
            items = page === 1 ? result.data : [...items, ...result.data];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function choose(item) {
        const key = open;
        open = "";
        save(key, item.id);
    }
</script>

<section class="card store-images" id="logo">
    <h2 class="tw:mb-1 tw:text-sm tw:font-semibold">Logo and icon</h2>
    <p class="field-help tw:mb-3">Every storefront page shows these. Without a logo, the store's name is shown instead.</p>

    <!-- What the storefront's header and a browser tab will look like. -->
    <div class="store-images-preview" aria-label="Preview">
        <div class="store-images-strip light">
            {#if urlOf("logo")}
                {#key urlOf("logo")}<img src={urlOf("logo")} alt="{name} logo on a light header" in:rise />{/key}
            {:else}
                <span class="store-images-name">{name}</span>
            {/if}
            <span class="store-images-menu" aria-hidden="true"><i></i><i></i><i></i></span>
        </div>
        <div class="store-images-strip dark">
            {#if darkLogo}
                {#key darkLogo}<img src={darkLogo} alt="{name} logo on a dark header" in:rise />{/key}
            {:else}
                <span class="store-images-name">{name}</span>
            {/if}
            <span class="store-images-menu" aria-hidden="true"><i></i><i></i><i></i></span>
        </div>
        <div class="store-images-tab">
            {#if urlOf("icon")}
                {#key urlOf("icon")}
                    <img
                        src={urlOf("icon")}
                        alt="{name} icon in a browser tab"
                        width="16"
                        height="16"
                        in:rise
                        onload={(e) => (iconShape = { w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
                    />
                {/key}
            {:else}
                <i class="ri-global-line" aria-hidden="true"></i>
            {/if}
            <span class="store-images-tab-title">{name}</span>
        </div>
    </div>

    <div class="store-images-slots">
        {#each SLOTS as slot (slot.key)}
            {@const url = urlOf(slot.key)}
            {@const busy = status[slot.key] === "uploading" || status[slot.key] === "saving"}
            <div class="store-images-slot" class:busy aria-busy={busy}>
                <div class="store-images-thumb" class:dark={slot.key === "logo_dark"}>
                    {#if url}
                        {#key url}<img src={url} alt="" in:rise out:rise />{/key}
                    {:else}
                        <i class="ri-image-line" aria-hidden="true"></i>
                    {/if}
                    {#if status[slot.key] === "saved"}
                        <span class="store-images-done" in:rise out:rise><i class="ri-check-line" aria-hidden="true"></i></span>
                    {/if}
                </div>
                <div class="store-images-body">
                    <span class="store-images-label">{slot.label}</span>
                    <span class="field-help" id="si-{slot.key}-hint">{slot.hint}</span>
                    {#if slot.key === "icon" && url && !iconSquare}
                        <span class="field-help txt-warning" in:rise>
                            This picture is {iconShape.w} × {iconShape.h}. A tab and a home screen show it square,
                            so it will be squeezed: crop it to a square and upload it again.
                        </span>
                    {/if}
                    {#if busy}
                        <span class="store-images-progress txt-sm" role="status" in:rise>
                            <span class="loader sm"></span>
                            {status[slot.key] === "uploading" ? "Uploading…" : "Saving…"}
                        </span>
                    {:else if editable}
                        <div class="store-images-btns">
                            {#if uploadable}
                                <label class="btn sm secondary store-images-upload">
                                    <i class="ri-upload-2-line" aria-hidden="true"></i>
                                    <span class="txt">{url ? "Replace" : "Upload"}</span>
                                    <input
                                        type="file"
                                        accept={ACCEPT}
                                        aria-describedby="si-{slot.key}-hint"
                                        onchange={(e) => upload(slot.key, e)}
                                    />
                                </label>
                            {/if}
                            {#if pickable}
                                <button
                                    type="button"
                                    class="btn sm transparent secondary"
                                    aria-expanded={open === slot.key}
                                    aria-controls="si-{slot.key}-library"
                                    onclick={() => toggleLibrary(slot.key)}
                                >
                                    <span class="txt">{open === slot.key ? "Close Files" : "Choose from Files"}</span>
                                </button>
                            {/if}
                            {#if url}
                                <button
                                    type="button"
                                    class="btn sm transparent danger"
                                    aria-label="Remove the {slot.label.toLowerCase()}"
                                    onclick={() => save(slot.key, 0)}
                                >
                                    <span class="txt">Remove</span>
                                </button>
                            {/if}
                        </div>
                    {/if}
                </div>
            </div>
            {#if open === slot.key}
                <div class="store-images-library" id="si-{slot.key}-library" in:rise>
                    <MediaLibrary
                        {items}
                        {loading}
                        bind:filters
                        bind:view
                        onfilter={() => ((page = 1), load())}
                        onadded={(fresh) => (items = [...fresh, ...items])}
                        chosen={(item) => item.url === url}
                        onchoose={choose}
                        tileVerb="Choose"
                    >
                        {#snippet after()}
                            {#if hasMore}
                                <button
                                    type="button"
                                    class="btn expanded block m-t-sm"
                                    class:loading
                                    disabled={loading}
                                    onclick={() => ((page += 1), load())}><span class="txt">Show more</span></button
                                >
                            {/if}
                        {/snippet}
                    </MediaLibrary>
                </div>
            {/if}
        {/each}
    </div>
    {#if !editable}
        <p class="field-help">Changing them needs store.write.</p>
    {/if}
</section>
