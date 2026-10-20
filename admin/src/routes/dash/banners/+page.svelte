<script>
    /**
     * Banners (ext/banners, D99): the pictures on the storefront's pages.
     *
     * Each page is shown as it reads — its slides across the top, then each
     * grid under its heading; only the home page for now, the one page a
     * storefront shows banners on — and every banner is edited in place: a drawer
     * for its picture, title and link, a switch to take it off the storefront
     * without losing it, and arrows to move it one place among its kind. The
     * arrows send the page's whole order, as the engine wants it.
     */
    import { flip } from "svelte/animate";
    import { can, banners as bannersAPI } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { groupBanners, movedOrder, pageLabel } from "$lib/banners.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise, flipDuration } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ImageField from "$lib/components/ImageField.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const missing = $derived(modulesKnown() && !hasModule("banners"));
    const readable = $derived(can("banners.read"));
    const writable = $derived(can("banners.write"));

    let rows = $state([]);
    let loading = $state(true);
    const pages = $derived(groupBanners(rows));

    async function load() {
        loading = true;
        try {
            rows = (await bannersAPI.list())?.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        if (!missing && readable) load();
    });

    // ---------------------------------------------------------------- edit

    const blank = () => ({
        page: "home",
        placement: "slide",
        group_title: "",
        title: "",
        link: "",
        image_url: "",
        mobile_image_url: "",
        active: true,
    });
    let editing = $state(null); // the banner being edited, or {} for a new one
    let form = $state(blank());
    let saving = $state(false);
    let problem = $state("");
    let attempt = $state(0);

    const PLACEMENTS = [
        { value: "slide", label: "A slide across the top of the page" },
        { value: "grid", label: "A tile in a grid under a heading" },
    ];

    function startNew(page = "home", placement = "slide", groupTitle = "") {
        form = { ...blank(), page, placement, group_title: groupTitle };
        problem = "";
        editing = {};
    }

    // What the engine takes back: a banner's own fields, without its id,
    // position or dates, which it refuses as unknown.
    const toInput = (banner) => ({
        page: banner.page,
        placement: banner.placement,
        group_title: banner.group_title,
        title: banner.title,
        link: banner.link,
        image_url: banner.image_url,
        mobile_image_url: banner.mobile_image_url,
        active: banner.active,
    });

    function startEdit(banner) {
        form = toInput(banner);
        problem = "";
        editing = banner;
    }

    async function save(e) {
        e?.preventDefault();
        if (saving) return;
        saving = true;
        problem = "";
        try {
            if (editing?.id) {
                const updated = await bannersAPI.update(editing.id, form);
                rows = rows.map((r) => (r.id === updated.id ? updated : r));
                toast.success("Banner saved");
            } else {
                const made = await bannersAPI.create(form);
                rows = [...rows, made];
                toast.success("Banner added");
            }
            editing = null;
        } catch (err) {
            attempt += 1;
            problem = err?.message || "The banner could not be saved.";
        } finally {
            saving = false;
        }
    }

    // A banner switched off stays on this screen and leaves the storefront.
    // Its switch waits for the save, so two flips cannot land out of order.
    let toggling = $state(new Set());
    async function toggle(banner) {
        if (toggling.has(banner.id)) return;
        toggling = new Set([...toggling, banner.id]);
        const next = !banner.active;
        rows = rows.map((r) => (r.id === banner.id ? { ...r, active: next } : r));
        try {
            const updated = await bannersAPI.update(banner.id, { ...toInput(banner), active: next });
            rows = rows.map((r) => (r.id === updated.id ? updated : r));
        } catch (err) {
            rows = rows.map((r) => (r.id === banner.id ? { ...r, active: banner.active } : r));
            toast.error(err);
        } finally {
            toggling = new Set([...toggling].filter((id) => id !== banner.id));
        }
    }

    let moving = $state(false);
    async function move(banner, dir) {
        const ids = movedOrder(rows, banner.id, dir);
        if (!ids || moving) return;
        moving = true;
        // Shown at once, put right if the engine refuses.
        const before = rows;
        const place = new Map(ids.map((id, i) => [id, i]));
        rows = rows.map((r) => (place.has(r.id) ? { ...r, position: place.get(r.id) } : r));
        try {
            const updated = await bannersAPI.reorder(banner.page, ids);
            const fresh = new Map((updated?.data ?? []).map((r) => [r.id, r]));
            rows = rows.map((r) => fresh.get(r.id) ?? r);
        } catch (err) {
            rows = before;
            toast.error(err);
        } finally {
            moving = false;
        }
    }

    let removing = $state(null);
    let confirmOpen = $state(false);
    async function remove() {
        const banner = removing;
        try {
            await bannersAPI.remove(banner.id);
            rows = rows.filter((r) => r.id !== banner.id);
            toast.success("Banner removed");
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>Banners · GoCommerce</title></svelte:head>

{#snippet bannerRow(banner, kin)}
    {@const i = kin.findIndex((x) => x.id === banner.id)}
        <button type="button" class="bn-thumb" onclick={() => startEdit(banner)} aria-label="Edit {banner.title || 'this banner'}">
            <img src={banner.image_url} alt="" loading="lazy" />
        </button>
        <div class="bn-text">
            <div class="bn-title">{banner.title || "Untitled"}</div>
            <div class="txt-hint txt-sm bn-sub">
                {#if banner.link}<span class="bn-link">{banner.link}</span>{:else}No link{/if}
                {#if banner.mobile_image_url}<span class="label sm">Phone picture</span>{/if}
                {#if !banner.active}<span class="label sm">Off</span>{/if}
            </div>
        </div>
        {#if writable}
            <div class="bn-actions">
                <!-- In a .field, where the panel draws a checkbox.switch as a
                     switch; the label draws it, so the name is on the input. -->
                <div class="field bn-toggle">
                    <input
                        type="checkbox"
                        class="switch"
                        id="bn-on-{banner.id}"
                        checked={banner.active}
                        disabled={toggling.has(banner.id)}
                        onchange={() => toggle(banner)}
                        aria-label="Show {banner.title || 'this banner'} on the storefront"
                    />
                    <label for="bn-on-{banner.id}"></label>
                </div>
                <button type="button" class="btn sm circle transparent secondary" disabled={moving || i === 0} onclick={() => move(banner, -1)} aria-label="Move {banner.title || 'this banner'} up">
                    <i class="ri-arrow-up-line" aria-hidden="true"></i>
                </button>
                <button type="button" class="btn sm circle transparent secondary" disabled={moving || i === kin.length - 1} onclick={() => move(banner, 1)} aria-label="Move {banner.title || 'this banner'} down">
                    <i class="ri-arrow-down-line" aria-hidden="true"></i>
                </button>
                <button type="button" class="btn sm circle transparent secondary" onclick={() => startEdit(banner)} aria-label="Edit {banner.title || 'this banner'}">
                    <i class="ri-pencil-line" aria-hidden="true"></i>
                </button>
                <button
                    type="button"
                    class="btn sm circle transparent danger"
                    onclick={() => {
                        removing = banner;
                        confirmOpen = true;
                    }}
                    aria-label="Remove {banner.title || 'this banner'}"
                >
                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                </button>
            </div>
        {/if}
{/snippet}

<div class="page page-banners shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Content</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Banners</h1>
            </nav>
            {#if !missing && readable && writable}
                <div class="page-header-primary-btns">
                    <button type="button" class="btn" onclick={() => startNew()}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New banner</span>
                    </button>
                </div>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing module="banners" what="Banners are kept by ext/banners, and this binary does not have it. Start it with -banners." />
        {:else if !readable}
            <NoAccess right="banners.read" what="the storefront's banners" />
        {:else if loading && !rows.length}
            <div class="bn-page"><span class="skeleton-loader"></span></div>
        {:else if !pages.length}
            <EmptyState
                icon="ri-image-2-line"
                title="No banners yet"
                text="Banners are the pictures across the top of a storefront page, and the grids of tiles under a heading. Each has a picture, an optional crop for phones, and a link."
            >
                {#if writable}
                    <button type="button" class="btn sm" onclick={() => startNew()}><span class="txt">New banner</span></button>
                {/if}
            </EmptyState>
        {:else}
            {#each pages as page (page.page)}
                <section class="bn-page" in:rise>
                    <h2 class="bn-page-title">{pageLabel(page.page)}</h2>

                    <div class="bn-group">
                        <div class="bn-group-head">
                            <h3 class="bn-group-title">Slides</h3>
                            {#if writable}
                                <button type="button" class="btn sm transparent secondary" onclick={() => startNew(page.page, "slide")}>
                                    <i class="ri-add-line" aria-hidden="true"></i><span class="txt">Add a slide</span>
                                </button>
                            {/if}
                        </div>
                        {#if page.slides.length}
                            <ul class="bn-list">
                                {#each page.slides as banner (banner.id)}
                                    <li class="bn-row" class:off={!banner.active} animate:flip={{ duration: flipDuration() }} in:rise>
                                        {@render bannerRow(banner, page.slides)}
                                    </li>
                                {/each}
                            </ul>
                        {:else}
                            <p class="txt-hint txt-sm">No slides on this page.</p>
                        {/if}
                    </div>

                    {#each page.grids as grid (grid.title)}
                        <div class="bn-group">
                            <div class="bn-group-head">
                                <h3 class="bn-group-title">Grid: {grid.title || "untitled"}</h3>
                                {#if writable}
                                    <button type="button" class="btn sm transparent secondary" onclick={() => startNew(page.page, "grid", grid.title)}>
                                        <i class="ri-add-line" aria-hidden="true"></i><span class="txt">Add a tile</span>
                                    </button>
                                {/if}
                            </div>
                            <ul class="bn-list">
                                {#each grid.banners as banner (banner.id)}
                                    <li class="bn-row" class:off={!banner.active} animate:flip={{ duration: flipDuration() }} in:rise>
                                        {@render bannerRow(banner, grid.banners)}
                                    </li>
                                {/each}
                            </ul>
                        </div>
                    {/each}
                    {#if writable}
                        <button type="button" class="btn sm transparent secondary m-t-sm" onclick={() => startNew(page.page, "grid")}>
                            <i class="ri-layout-grid-line" aria-hidden="true"></i><span class="txt">Add a grid</span>
                        </button>
                    {/if}
                </section>
            {/each}
        {/if}
    </div>
</div>

<Drawer open={!!editing} size="md" title={editing?.id ? "Edit banner" : "New banner"} onclose={() => (editing = null)}>
    <form id="bn-form" onsubmit={save}>
        <div class="field">
            <label for="bn-placement">Where it shows</label>
            <Select id="bn-placement" bind:value={form.placement} options={PLACEMENTS} />
        </div>
        {#if form.placement === "grid"}
            <div class="field m-t-sm" in:rise>
                <label for="bn-group">Grid heading</label>
                <input id="bn-group" type="text" maxlength="200" bind:value={form.group_title} placeholder="Shop by category" />
            </div>
            <div class="field-help">Tiles with the same heading on a page form one grid.</div>
        {/if}
        <div class="field m-t-sm">
            <label for="bn-title">Title</label>
            <input id="bn-title" type="text" maxlength="200" bind:value={form.title} />
        </div>
        <div class="field-help">What the banner says. Screen readers read it out too.</div>
        <div class="field m-t-sm">
            <label for="bn-link">Link</label>
            <input id="bn-link" type="text" inputmode="url" autocomplete="off" spellcheck="false" bind:value={form.link} placeholder="/collections/new-in" />
        </div>
        <div class="field-help">A path on your storefront, such as /collections/new-in, or a full web address. Leave it empty for a picture that goes nowhere.</div>
        <div class="m-t-sm">
            <ImageField
                id="bn-image"
                label="Picture"
                bind:value={form.image_url}
                required
                hint={form.placement === "slide"
                    ? "Wide, for a desktop: about 1500 × 380 pixels."
                    : "Square or nearly so: grids show tiles side by side."}
            />
        </div>
        <div class="m-t-sm">
            <ImageField
                id="bn-mobile"
                label="Phone picture (optional)"
                bind:value={form.mobile_image_url}
                hint="A narrower crop for phones, about 720 × 380 pixels. Without one, phones show the picture above."
            />
        </div>
        <div class="field m-t-sm bn-on">
            <input id="bn-active" type="checkbox" class="switch" bind:checked={form.active} />
            <label for="bn-active">Show on the storefront</label>
        </div>
        {#key attempt}
            {#if problem}
                <div class="alert danger m-t-sm bn-shake" role="alert"><p>{problem}</p></div>
            {/if}
        {/key}
    </form>
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (editing = null)}><span class="txt">Cancel</span></button>
        <button type="submit" form="bn-form" class="btn" class:loading={saving} disabled={saving || !form.image_url.trim()}>
            <span class="txt">{editing?.id ? "Save" : "Add banner"}</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Remove this banner?"
    message="It leaves the storefront and this screen. The picture stays in your Files."
    confirmLabel="Remove"
    danger
    onconfirm={remove}
/>

<style>
    .bn-page {
        margin-bottom: 24px;
    }
    .bn-page-title {
        font-size: 1.1rem;
        font-weight: 600;
        margin: 0 0 8px;
    }
    .bn-group {
        margin-bottom: 16px;
    }
    .bn-group-head {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        margin-bottom: 6px;
    }
    .bn-group-title {
        font-size: 0.95rem;
        font-weight: 600;
        margin: 0;
        overflow-wrap: anywhere;
    }
    .bn-list {
        list-style: none;
        margin: 0;
        padding: 0;
        border: 1px solid var(--baseAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--baseColor);
    }
    .bn-row {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 12px;
        padding: 10px 12px;
        transition: opacity 180ms ease-out;
    }
    .bn-row + .bn-row {
        border-top: 1px solid var(--baseAlt2Color);
    }
    .bn-row.off {
        opacity: 0.6;
    }
    .bn-thumb {
        flex: 0 0 auto;
        padding: 0;
        border: 0;
        background: none;
        cursor: pointer;
        border-radius: var(--borderRadius);
        transition: transform 150ms ease-out;
    }
    .bn-thumb:hover {
        transform: scale(1.03);
    }
    .bn-thumb:focus-visible {
        outline: 2px solid var(--primaryColor);
        outline-offset: 2px;
    }
    .bn-thumb img {
        display: block;
        width: 120px;
        height: 54px;
        object-fit: cover;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color);
    }
    .bn-text {
        flex: 1 1 160px;
        min-width: 0;
    }
    .bn-title {
        font-weight: 600;
        overflow-wrap: anywhere;
    }
    .bn-sub {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
        margin-top: 2px;
    }
    .bn-link {
        overflow-wrap: anywhere;
    }
    .bn-actions {
        display: flex;
        align-items: center;
        gap: 2px;
        margin-left: auto;
    }
    .bn-toggle {
        margin: 0 6px 0 0;
    }
    .bn-on {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    @media (prefers-reduced-motion: reduce) {
        .bn-thumb,
        .bn-thumb:hover {
            transition: none;
            transform: none;
        }
    }
    @media (prefers-reduced-motion: no-preference) {
        .bn-shake {
            animation: bn-shake 300ms ease-out;
        }
    }
    @keyframes bn-shake {
        25% {
            transform: translateX(-4px);
        }
        75% {
            transform: translateX(4px);
        }
    }
</style>
