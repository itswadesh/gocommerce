<script>
    /**
     * Video showcases (D88): the store's own videos, from YouTube or
     * Facebook, beside the products they show.
     *
     * The list answers "what is running and how is it doing": each showcase
     * with what it is called on the storefront now (live, replay, video) and
     * its views, clicks, adds to the cart and orders. A new one starts from
     * the link, because the video is the thing a merchant already has — the
     * title comes from it, and the products are attached on the next screen.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { can, showcases } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { rowKey } from "$lib/rowkey.js";
    import { formatMoney, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const missing = $derived(modulesKnown() && !hasModule("showcases"));
    const readable = $derived(can("showcases.read"));
    const writable = $derived(can("showcases.write"));

    const list = listState({ status: "" });
    const status = $derived(list.params.status);
    const FILTERS = [
        { value: "", label: "All" },
        { value: "published", label: "Published" },
        { value: "draft", label: "Drafts" },
        { value: "archived", label: "Archived" },
    ];

    let rows = $state([]);
    let loading = $state(true);
    let latest = 0;

    $effect(() => {
        list.params;
        if (readable && hasModule("showcases")) load();
    });

    async function load() {
        const mine = ++latest;
        loading = true;
        try {
            const result = await showcases.list({ status });
            if (mine === latest) rows = result ?? [];
        } catch (err) {
            if (mine === latest) toast.error(err);
        } finally {
            if (mine === latest) loading = false;
        }
    }

    const LABELS = { video: "Video", scheduled: "Scheduled", live: "Live", replay: "Replay" };
    const PROVIDERS = { youtube: "YouTube", facebook: "Facebook" };
    const open = (row) => goto(`${base}/dash/showcases/${row.id}`);

    // -------------------------------------------------------------- new one
    let creating = $state(false);
    let link = $state("");
    let title = $state("");
    let titleTouched = $state(false);
    let checked = $state(null);
    let checkError = $state("");
    let checking = $state(false);
    let saving = $state(false);

    function startNew() {
        link = "";
        title = "";
        titleTouched = false;
        checked = null;
        checkError = "";
        creating = true;
    }

    // Checked as it is pasted, so the player and the video's own title are
    // there before anything is saved — and a link that will not play is
    // said so before it is a showcase.
    async function check() {
        const url = link.trim();
        if (!url || checking) return;
        checking = true;
        checkError = "";
        try {
            checked = await showcases.check(url);
            if (!titleTouched && checked.title) title = checked.title;
        } catch (err) {
            checked = null;
            checkError = err?.message || "That link cannot be played here.";
        } finally {
            checking = false;
        }
    }

    async function create(e) {
        e?.preventDefault();
        if (saving) return;
        if (!checked) await check();
        if (!checked) return;
        saving = true;
        try {
            const made = await showcases.create({ title: title.trim() || checked.title || "", video_url: link.trim() });
            toast.success("Showcase saved as a draft");
            creating = false;
            goto(`${base}/dash/showcases/${made.id}`);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

<svelte:head><title>Video showcases · GoCommerce</title></svelte:head>

<div class="page page-showcases shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Content</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Video showcases</h1>
            </nav>
            {#if !missing && readable && writable}
                <div class="page-header-primary-btns">
                    <button type="button" class="btn" onclick={startNew}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New showcase</span>
                    </button>
                </div>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing module="showcases" what="Video showcases are kept by ext/showcases, and this binary does not have it. Start it with -showcases." />
        {:else if !readable}
            <NoAccess right="showcases.read" what="the store's video showcases" />
        {:else}
            <div class="sc-filters" role="group" aria-label="Status">
                {#each FILTERS as f (f.value)}
                    <button
                        type="button"
                        class="btn sm {status === f.value ? '' : 'transparent secondary'}"
                        aria-pressed={status === f.value}
                        onclick={() => list.set({ status: f.value })}><span class="txt">{f.label}</span></button
                    >
                {/each}
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Showcase</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-number min-width">Views</th>
                            <th class="col-field-type-number min-width">Clicks</th>
                            <th class="col-field-type-number min-width">Added to cart</th>
                            <th class="col-field-type-number min-width">Orders</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr class="handle" tabindex="0" onclick={() => open(row)} onkeydown={(e) => rowKey(e, () => open(row))}>
                                <td class="col-field-name-id" data-name="Showcase">
                                    <div class="sc-row">
                                        {#if row.video.thumbnail_url}
                                            <img class="sc-thumb" src={row.video.thumbnail_url} alt="" loading="lazy" width="96" height="54" />
                                        {:else}
                                            <span class="sc-thumb sc-thumb-blank" aria-hidden="true"><i class="ri-video-line"></i></span>
                                        {/if}
                                        <div class="sc-text">
                                            <div class="sc-title">{row.title}</div>
                                            <div class="txt-hint txt-sm sc-sub">
                                                <span class="label sm" class:danger={row.label === "live"}>{LABELS[row.label]}</span>
                                                {PROVIDERS[row.video.provider]} ·
                                                {row.collection_id ? "a collection" : `${row.product_ids.length} ${row.product_ids.length === 1 ? "product" : "products"}`}
                                                · {relativeTime(row.updated_at)}
                                            </div>
                                        </div>
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    <span class="label sm" class:success={row.status === "published"}>
                                        {row.status === "published" ? "Published" : row.status === "draft" ? "Draft" : "Archived"}
                                    </span>
                                </td>
                                <td class="col-field-type-number min-width sc-num" data-name="Views">{row.stats.views.toLocaleString()}</td>
                                <td class="col-field-type-number min-width sc-num" data-name="Clicks">{row.stats.clicks.toLocaleString()}</td>
                                <td class="col-field-type-number min-width sc-num" data-name="Added to cart">{row.stats.adds_to_cart.toLocaleString()}</td>
                                <td class="col-field-type-number min-width sc-num" data-name="Orders">
                                    {row.stats.orders.toLocaleString()}
                                    {#if row.stats.orders}<span class="txt-hint txt-sm">· {formatMoney(row.stats.revenue)}</span>{/if}
                                </td>
                            </tr>
                        {:else}
                            {#if !loading}
                                <tr>
                                    <td colspan="6">
                                        {#if status}
                                            <EmptyState icon="ri-filter-line" title="None here" text="No showcase has that status." onclear={() => list.set({ status: "" })} />
                                        {:else}
                                            <EmptyState
                                                icon="ri-video-line"
                                                title="Show what you sell"
                                                text="Paste a YouTube or Facebook video of your team showing your products, attach those products, and the storefront plays it beside them. The video stays on YouTube or Facebook; nothing is uploaded here."
                                            >
                                                {#if writable}
                                                    <button type="button" class="btn sm" onclick={startNew}><span class="txt">New showcase</span></button>
                                                {/if}
                                            </EmptyState>
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        {/each}
                    </tbody>
                </table>
            </div>
        {/if}
    </div>
</div>

<Drawer open={creating} size="md" title="New showcase" onclose={() => (creating = false)}>
    <form id="sc-new" onsubmit={create}>
        <div class="field">
            <label for="sc-link">Video link</label>
            <input
                id="sc-link"
                type="url"
                inputmode="url"
                placeholder="https://www.youtube.com/watch?v=…"
                autocomplete="off"
                bind:value={link}
                onchange={check}
                aria-describedby="sc-link-help"
            />
        </div>
        <div class="field-help" id="sc-link-help">
            A video on YouTube or Facebook, or a livestream's own link. It stays there: the storefront plays it with
            the platform's own player.
        </div>
        {#if checkError}
            <div class="alert danger m-t-sm sc-shake" role="alert"><p>{checkError}</p></div>
        {/if}
        {#if checking}
            <div class="txt-hint txt-sm m-t-sm"><span class="loader sm"></span> Checking the link…</div>
        {:else if checked}
            <div class="m-t-sm" in:rise>
                {#if checked.problem}
                    <div class="alert warning m-b-sm" role="status"><p>{checked.problem}</p></div>
                {/if}
                <div class="sc-player">
                    <iframe
                        src={checked.embed_url}
                        title="The video, as the storefront will play it"
                        loading="lazy"
                        allow="encrypted-media; picture-in-picture; fullscreen"
                        referrerpolicy="strict-origin-when-cross-origin"
                    ></iframe>
                </div>
            </div>
        {/if}
        <div class="field m-t-sm">
            <label for="sc-title">Title</label>
            <input id="sc-title" type="text" maxlength="200" bind:value={title} oninput={() => (titleTouched = true)} />
        </div>
        <div class="field-help">What the storefront heads it with. From the video's own title where YouTube says it.</div>
    </form>
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (creating = false)}><span class="txt">Cancel</span></button>
        <button type="submit" form="sc-new" class="btn" class:loading={saving} disabled={saving || checking || !link.trim()}>
            <span class="txt">Save as draft</span>
        </button>
    {/snippet}
</Drawer>

<style>
    .sc-filters {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
        margin-bottom: 12px;
    }
    .sc-row {
        display: flex;
        align-items: center;
        gap: 12px;
        min-width: 0;
    }
    .sc-thumb {
        flex: 0 0 auto;
        width: 96px;
        height: 54px;
        object-fit: cover;
        border-radius: 6px;
        background: var(--surfaceAlt2Color);
    }
    .sc-thumb-blank {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        color: var(--txtHintColor);
        font-size: 20px;
    }
    .sc-text {
        min-width: 0;
    }
    .sc-title {
        font-weight: 600;
        overflow-wrap: anywhere;
    }
    .sc-sub {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 4px;
        margin-top: 2px;
    }
    .sc-num {
        font-variant-numeric: tabular-nums;
    }
    .sc-player {
        position: relative;
        aspect-ratio: 16 / 9;
        overflow: hidden;
        border-radius: 8px;
        background: #000;
    }
    .sc-player iframe {
        position: absolute;
        inset: 0;
        width: 100%;
        height: 100%;
        border: 0;
    }
    @media (prefers-reduced-motion: no-preference) {
        .sc-shake {
            animation: sc-shake 300ms ease-out;
        }
    }
    @keyframes sc-shake {
        25% {
            transform: translateX(-4px);
        }
        75% {
            transform: translateX(4px);
        }
    }
</style>
