<script>
    /**
     * One picture, by its address: chosen from the store's Files, or typed in
     * as a link. Used where a record holds a picture's URL rather than a media
     * item — a banner, which may as well show a picture held on a CDN.
     *
     * Picking from Files needs media.read; without it the field is the link
     * alone, which is still enough to set a picture. The library opens in
     * place, under the field, rather than in a drawer of its own: the field
     * usually sits in a drawer already, and a drawer over a drawer fights it
     * for the keyboard and for Escape.
     */
    import { api, can, query } from "$lib/api.js";
    import { mediaQuery } from "$lib/media.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import MediaLibrary from "$lib/components/MediaLibrary.svelte";

    let { id, label, value = $bindable(""), hint = "", required = false } = $props();

    const PER_PAGE = 24;
    const pickable = $derived(can("media.read"));

    let open = $state(false);
    let items = $state([]);
    let loading = $state(false);
    let page = $state(1);
    let meta = $state(null);
    let filters = $state({ q: "", kind: "image", usage: "", size: "", scope: "", sort: "" });
    let view = $state("grid");
    const hasMore = $derived(!!meta && items.length < meta.total);
    // A preview that failed to load says so, rather than leaving a broken box.
    let broken = $state(false);
    $effect(() => {
        value;
        broken = false;
    });

    function openLibrary() {
        page = 1;
        items = [];
        open = true;
        load();
    }

    async function load() {
        loading = true;
        try {
            const result = await api.get("/api/admin/media" + query(mediaQuery(filters, { page, limit: PER_PAGE })));
            items = page === 1 ? result.data : [...items, ...result.data];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function reload() {
        page = 1;
        load();
    }

    function choose(item) {
        value = item.url;
        open = false;
    }
</script>

<div class="field">
    <label for={id}>{label}</label>
    <input
        {id}
        type="text"
        inputmode="url"
        autocomplete="off"
        spellcheck="false"
        placeholder="/media/… or https://…"
        {required}
        bind:value
        aria-describedby={hint ? `${id}-hint` : undefined}
    />
</div>
<div class="image-field-row">
    <!-- Not keyed on the value: typing an address would replay the entrance
         and fetch a picture per keystroke. -->
    {#if value}
        <span class="image-field-preview" in:rise>
            {#if broken}
                <i class="ri-image-line" aria-hidden="true"></i>
                <span class="txt-hint txt-sm">This picture did not load.</span>
            {:else}
                <img src={value} alt="" loading="lazy" onerror={() => (broken = true)} />
            {/if}
        </span>
    {/if}
    {#if pickable}
        <button
            type="button"
            class="btn sm secondary"
            aria-expanded={open}
            aria-controls="{id}-library"
            onclick={() => (open ? (open = false) : openLibrary())}
        >
            <i class={open ? "ri-close-line" : "ri-image-add-line"} aria-hidden="true"></i>
            <span class="txt">{open ? "Close Files" : value ? "Choose another" : "Choose from Files"}</span>
        </button>
    {/if}
    {#if value && !required}
        <button type="button" class="btn sm transparent secondary" onclick={() => (value = "")}>
            <span class="txt">Remove</span>
        </button>
    {/if}
</div>
{#if hint}
    <div class="field-help" id="{id}-hint">{hint}</div>
{/if}

{#if pickable && open}
    <div class="image-field-library" id="{id}-library" in:rise>
        <MediaLibrary
            {items}
            {loading}
            bind:filters
            bind:view
            onfilter={reload}
            onadded={(fresh) => (items = [...fresh, ...items])}
            chosen={(item) => item.url === value}
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
                        onclick={() => {
                            page += 1;
                            load();
                        }}><span class="txt">Show more</span></button
                    >
                {/if}
            {/snippet}
        </MediaLibrary>
    </div>
{/if}

<style>
    .image-field-row {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px;
        margin-top: 8px;
    }
    .image-field-library {
        margin-top: 10px;
        padding: 10px;
        border: 1px solid var(--baseAlt2Color);
        border-radius: var(--borderRadius);
    }
    .image-field-preview {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        max-width: 100%;
    }
    .image-field-preview img {
        display: block;
        width: 160px;
        max-width: 100%;
        height: 72px;
        object-fit: cover;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color);
    }
</style>
