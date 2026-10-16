<script>
    /**
     * One video showcase (D88): the video, the products it shows, which one
     * is being shown now, and where on the storefront it appears.
     *
     * Two kinds of change, saved two ways. What the showcase *is* — its words,
     * its link, its products, its places — is edited and saved with the save
     * bar, like any other editor. What happens *during* a broadcast — the
     * product being shown, "we're live", "it ended" — saves the moment it is
     * pressed, because a presenter switching products mid-stream cannot stop
     * to find a save button.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page as route } from "$app/state";
    import { api, can, query, showcases } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatMoney, formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import CollectionProducts from "$lib/components/CollectionProducts.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import Select from "$lib/components/Select.svelte";

    const id = $derived(route.params.id);
    const missing = $derived(modulesKnown() && !hasModule("showcases"));
    const readable = $derived(can("showcases.read"));
    const writable = $derived(can("showcases.write"));

    let show = $state(null);
    let loading = $state(true);
    let notFound = $state(false);
    let draft = $state(null);
    let members = $state([]);
    let saving = $state(false);
    let acting = $state("");

    $effect(() => {
        id;
        if (readable && hasModule("showcases")) load();
    });

    async function load() {
        loading = true;
        notFound = false;
        try {
            take(await showcases.get(id));
        } catch (err) {
            if (err.status === 404) notFound = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    // The saved showcase, and a draft of the part the save bar saves.
    function take(s) {
        show = s;
        draft = {
            title: s.title,
            slug: s.slug,
            description: s.description,
            video_url: s.video.url,
            kind: s.kind,
            starts_at: local(s.starts_at),
            source: s.collection_id ? "collection" : "products",
            collection_id: s.collection_id,
            on_home: s.on_home,
            on_collection: s.on_collection,
            on_products: s.on_products,
        };
        members = s.collection_id ? [] : (s.products ?? []);
    }

    // datetime-local speaks the browser's own clock, without a zone.
    function local(iso) {
        if (!iso) return "";
        const d = new Date(iso);
        const pad = (n) => String(n).padStart(2, "0");
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    }

    const savedIds = $derived(show && !show.collection_id ? show.product_ids : []);
    const patch = $derived.by(() => {
        if (!show || !draft) return {};
        const out = {};
        for (const k of ["title", "slug", "description", "kind", "on_home", "on_collection", "on_products"]) {
            if (draft[k] !== show[k]) out[k] = draft[k];
        }
        if (draft.video_url.trim() !== show.video.url) out.video_url = draft.video_url.trim();
        if (draft.starts_at !== local(show.starts_at)) {
            out.starts_at = draft.starts_at ? new Date(draft.starts_at).toISOString() : "";
        }
        if (draft.source === "collection") {
            if (draft.collection_id && draft.collection_id !== show.collection_id) out.collection_id = draft.collection_id;
        } else {
            if (show.collection_id) out.collection_id = null;
            const ids = members.map((p) => p.id);
            if (show.collection_id || ids.join() !== savedIds.join()) out.product_ids = ids;
        }
        return out;
    });
    const dirty = $derived(Object.keys(patch).length > 0);

    async function save() {
        if (saving || !dirty) return;
        saving = true;
        try {
            take(await showcases.update(id, patch));
            toast.success("Showcase saved");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    // ------------------------------------------- what saves the moment it is pressed
    async function act(name, change, done) {
        if (acting) return;
        acting = name;
        try {
            take(await showcases.update(id, change));
            if (done) toast.success(done);
        } catch (err) {
            toast.error(err);
        } finally {
            acting = "";
        }
    }

    const feature = (p) =>
        act(`feature-${p?.id ?? "none"}`, { featured_product_id: p ? p.id : null }, p ? `Now showing ${p.title}` : "Nothing featured");

    const LABELS = {
        video: "Video",
        scheduled: "Scheduled",
        live: "Live",
        replay: "Replay",
    };
    const LABEL_SAYS = {
        video: "Shown as a video.",
        scheduled: "Shown as coming up, with its start time.",
        live: "Shown as live — because you said so. It reads as a replay after twelve hours, or when you end it.",
        replay: "Shown as a replay of the broadcast.",
    };

    // --------------------------------------------------------------- collections
    let collections = $state([]);
    $effect(() => {
        if (draft?.source === "collection" && !collections.length && can("collections.read")) {
            api.get("/api/admin/collections" + query({ limit: 200 }))
                .then((r) => (collections = (r?.data ?? r ?? []).map((c) => ({ value: c.id, label: c.title }))))
                .catch(() => {});
        }
    });
    let newCollection = $state("");
    async function makeCollection() {
        if (acting || !newCollection.trim()) return;
        acting = "collection";
        try {
            take(await showcases.makeCollection(id, newCollection.trim()));
            toast.success(`Collection "${newCollection.trim()}" made from these products`);
            newCollection = "";
        } catch (err) {
            toast.error(err);
        } finally {
            acting = "";
        }
    }

    // --------------------------------------------------------------- the video
    let checkNote = $state("");
    async function checkLink() {
        checkNote = "";
        try {
            const c = await showcases.check(draft.video_url.trim());
            checkNote = c.problem || "";
        } catch (err) {
            checkNote = err?.message || "That link cannot be played here.";
        }
    }

    let preview = $state("desktop");
    let removing = $state(false);
    async function remove() {
        try {
            await showcases.remove(id);
            toast.success(`Deleted ${show.title}`);
            goto(`${base}/dash/showcases`);
        } catch (err) {
            toast.error(err);
        }
    }
    const shown = $derived(show?.products ?? []);
</script>

<svelte:head><title>{show?.title || "Showcase"} · GoCommerce</title></svelte:head>

{#if missing}
    <ModuleMissing module="showcases" what="Video showcases are kept by ext/showcases, and this binary does not have it." />
{:else if !readable}
    <NoAccess right="showcases.read" what="video showcases" />
{:else}
    <div class="page page-showcase shopify-skin skin-recessed">
        <div class="page-content full-height">
            <SaveBar dirty={dirty && writable} {saving} message="Unsaved changes" onsave={save} ondiscard={() => take(show)} />

            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/dash/showcases">Video showcases</a>
                    <div>{show?.title || "…"}</div>
                </nav>
                {#if show && writable}
                    <div class="page-header-primary-btns">
                        {#if show.status === "published"}
                            <button type="button" class="btn secondary" disabled={!!acting} onclick={() => act("status", { status: "draft" }, "Taken off the storefront")}>
                                <span class="txt">Unpublish</span>
                            </button>
                        {:else}
                            <button type="button" class="btn" class:loading={acting === "status"} disabled={!!acting || dirty} title={dirty ? "Save your changes first" : ""} onclick={() => act("status", { status: "published" }, "Published")}>
                                <span class="txt">Publish</span>
                            </button>
                        {/if}
                        {#if show.status !== "archived"}
                            <button type="button" class="btn transparent secondary" disabled={!!acting} onclick={() => act("status", { status: "archived" }, "Archived")}>
                                <span class="txt">Archive</span>
                            </button>
                        {/if}
                        <button type="button" class="btn transparent danger" aria-label="Delete this showcase" title="Delete" onclick={() => (removing = true)}>
                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                        </button>
                    </div>
                {/if}
            </header>

            {#if notFound}
                <div class="txt-center txt-hint p-base">
                    No showcase with that id. It may have been deleted.
                    <a href="{base}/dash/showcases">Back to showcases</a>
                </div>
            {:else if loading && !show}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if show && draft}
                <div class="wrapper sc-editor">
                    <!-- What a buyer sees, first: the player beside its products. -->
                    <section class="card">
                        <div class="sc-head">
                            <h6 class="section-title">
                                <i class="ri-eye-line" aria-hidden="true"></i>
                                Preview
                            </h6>
                            <div class="sc-toggle" role="group" aria-label="Preview width">
                                {#each [["desktop", "Desktop"], ["phone", "Phone"]] as [value, label] (value)}
                                    <button type="button" class="btn xs {preview === value ? '' : 'transparent secondary'}" aria-pressed={preview === value} onclick={() => (preview = value)}>
                                        <span class="txt">{label}</span>
                                    </button>
                                {/each}
                            </div>
                        </div>
                        <div class="sc-stage" class:phone={preview === "phone"}>
                            <div class="sc-player">
                                <iframe
                                    src={show.video.embed_url}
                                    title="{show.title}, as the storefront plays it"
                                    loading="lazy"
                                    allow="encrypted-media; picture-in-picture; fullscreen"
                                    referrerpolicy="strict-origin-when-cross-origin"
                                ></iframe>
                            </div>
                            <div class="sc-products">
                                <div class="sc-products-head">
                                    <strong>Products in this video</strong>
                                    <span class="label sm" class:danger={show.label === "live"}>{LABELS[show.label]}</span>
                                </div>
                                {#each shown.filter((p) => p.status === "active") as p (p.id)}
                                    <div class="sc-card" class:featured={show.featured_product_id === p.id}>
                                        {#if p.image_url}<img src={p.image_url} alt="" loading="lazy" width="44" height="44" />{:else}<span class="sc-card-blank" aria-hidden="true"></span>{/if}
                                        <span class="sc-card-title">{p.title}</span>
                                        <span class="txt-hint txt-sm">{p.price ? formatMoney(p.price) : ""}</span>
                                    </div>
                                {:else}
                                    <div class="txt-hint txt-sm">No product a buyer can see yet. Attach some below.</div>
                                {/each}
                            </div>
                        </div>
                        <div class="field-help">
                            {LABEL_SAYS[show.label]} The storefront lays it out the same way: the player beside the products on a wide
                            screen, above them on a phone.
                            <a href={show.video.watch_url} target="_blank" rel="noopener noreferrer">Open on {show.video.provider === "youtube" ? "YouTube" : "Facebook"}</a>
                        </div>
                    </section>

                    <!-- During a broadcast: saves when pressed. -->
                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-broadcast-line" aria-hidden="true"></i>
                            Showing now
                        </h6>
                        {#if shown.length}
                            <div class="sc-feature" role="group" aria-label="Featured product">
                                <button type="button" class="btn sm {show.featured_product_id ? 'transparent secondary' : ''}" aria-pressed={!show.featured_product_id} disabled={!writable || !!acting} onclick={() => feature(null)}>
                                    <span class="txt">Nothing</span>
                                </button>
                                {#each shown as p (p.id)}
                                    <button type="button" class="btn sm {show.featured_product_id === p.id ? '' : 'transparent secondary'}" aria-pressed={show.featured_product_id === p.id} class:loading={acting === `feature-${p.id}`} disabled={!writable || !!acting} onclick={() => feature(p)}>
                                        <span class="txt">{p.title}</span>
                                    </button>
                                {/each}
                            </div>
                            <div class="field-help">The product the video is on now, shown first on the storefront. Change it as you present; the video carries on.</div>
                        {:else}
                            <div class="field-help">Attach products below, then pick the one the video is showing.</div>
                        {/if}

                        {#if show.kind === "live"}
                            <div class="sc-live" role="group" aria-label="Broadcast">
                                <button type="button" class="btn sm {show.live_state === 'scheduled' ? '' : 'transparent secondary'}" aria-pressed={show.live_state === "scheduled"} disabled={!writable || !!acting || !show.starts_at} title={show.starts_at ? "" : "Set the start time and save first"} onclick={() => act("live", { live_state: "scheduled" }, "Scheduled")}>
                                    <span class="txt">Scheduled</span>
                                </button>
                                <button type="button" class="btn sm {show.live_state === 'live' ? 'danger' : 'transparent secondary'}" aria-pressed={show.live_state === "live"} disabled={!writable || !!acting} onclick={() => act("live", { live_state: "live" }, "Shown as live")}>
                                    <i class="ri-record-circle-line" aria-hidden="true"></i>
                                    <span class="txt">We're live</span>
                                </button>
                                <button type="button" class="btn sm {show.live_state === 'ended' ? '' : 'transparent secondary'}" aria-pressed={show.live_state === "ended"} disabled={!writable || !!acting} onclick={() => act("live", { live_state: "ended" }, "Ended; shown as a replay")}>
                                    <span class="txt">Ended</span>
                                </button>
                            </div>
                            <div class="field-help">
                                Nothing is ever shown as live from the link alone: press "We're live" when the broadcast starts.
                                {#if show.live_since}Live since {formatDate(show.live_since)}.{/if}
                            </div>
                        {/if}
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-stack-line" aria-hidden="true"></i>
                            Products
                        </h6>
                        <div class="sc-toggle m-b-sm" role="group" aria-label="Where the products come from">
                            {#each [["products", "Pick products"], ["collection", "Use a collection"]] as [value, label] (value)}
                                <button type="button" class="btn sm {draft.source === value ? '' : 'transparent secondary'}" aria-pressed={draft.source === value} disabled={!writable} onclick={() => (draft.source = value)}>
                                    <span class="txt">{label}</span>
                                </button>
                            {/each}
                        </div>
                        {#if draft.source === "products"}
                            <CollectionProducts bind:members disabled={!writable} noun="showcase" />
                            {#if writable && savedIds.length && !dirty}
                                <div class="sc-make" in:rise>
                                    <div class="field">
                                        <label for="sc-new-collection">Make these a collection</label>
                                        <input id="sc-new-collection" type="text" maxlength="120" placeholder="Festive picks" bind:value={newCollection} />
                                    </div>
                                    <button type="button" class="btn sm secondary" class:loading={acting === "collection"} disabled={!newCollection.trim() || !!acting} onclick={makeCollection}>
                                        <span class="txt">Make collection</span>
                                    </button>
                                </div>
                                <div class="field-help">A collection of these products, in this order, which the showcase then shows — so it can be linked from a menu too.</div>
                            {/if}
                        {:else}
                            <div class="field">
                                <label for="sc-collection">Collection</label>
                                <Select id="sc-collection" bind:value={draft.collection_id} options={collections} placeholder="Choose a collection" disabled={!writable} />
                            </div>
                            <div class="field-help">Its products are read when the showcase is shown, so one added to the collection later appears here too.</div>
                            {#if shown.length}
                                <ul class="sc-list">
                                    {#each shown as p (p.id)}
                                        <li>{p.title}{#if p.status !== "active"} <span class="label sm">{p.status}</span>{/if}</li>
                                    {/each}
                                </ul>
                            {/if}
                        {/if}
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-video-line" aria-hidden="true"></i>
                            Video and words
                        </h6>
                        <form class="grid" onsubmit={(e) => (e.preventDefault(), writable && save())}>
                            <div class="col-12">
                                <div class="field">
                                    <label for="sc-url">Video link</label>
                                    <input id="sc-url" type="url" readonly={!writable} bind:value={draft.video_url} onchange={checkLink} />
                                </div>
                                {#if checkNote}
                                    <div class="field-help error" role="status">{checkNote}</div>
                                {/if}
                            </div>
                            <div class="col-md-6">
                                <div class="field">
                                    <label for="sc-title">Title</label>
                                    <input id="sc-title" type="text" maxlength="200" readonly={!writable} bind:value={draft.title} />
                                </div>
                            </div>
                            <div class="col-md-6">
                                <div class="field">
                                    <label for="sc-slug">Address</label>
                                    <input id="sc-slug" type="text" maxlength="80" readonly={!writable} bind:value={draft.slug} />
                                </div>
                                <div class="field-help">Read at <code class="txt-code">/x/showcases/{draft.slug}</code>.</div>
                            </div>
                            <div class="col-12">
                                <div class="field">
                                    <label for="sc-description">Description</label>
                                    <textarea id="sc-description" rows="3" maxlength="2000" readonly={!writable} bind:value={draft.description}></textarea>
                                </div>
                            </div>
                            <div class="col-md-6">
                                <div class="field">
                                    <label for="sc-kind">Kind</label>
                                    <select id="sc-kind" disabled={!writable} bind:value={draft.kind}>
                                        <option value="video">A recorded video</option>
                                        <option value="live">A livestream</option>
                                    </select>
                                </div>
                            </div>
                            {#if draft.kind === "live"}
                                <div class="col-md-6">
                                    <div class="field">
                                        <label for="sc-starts">Starts at</label>
                                        <input id="sc-starts" type="datetime-local" readonly={!writable} bind:value={draft.starts_at} />
                                    </div>
                                </div>
                            {/if}
                        </form>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-layout-masonry-line" aria-hidden="true"></i>
                            Where it shows
                        </h6>
                        <div class="sc-places">
                            <div class="field">
                                <input type="checkbox" id="sc-home" class="switch" disabled={!writable} bind:checked={draft.on_home} />
                                <label for="sc-home">The home page</label>
                            </div>
                            <div class="field">
                                <input type="checkbox" id="sc-products" class="switch" disabled={!writable} bind:checked={draft.on_products} />
                                <label for="sc-products">The pages of its products</label>
                            </div>
                            <div class="field">
                                <input type="checkbox" id="sc-collection-page" class="switch" disabled={!writable || draft.source !== "collection"} bind:checked={draft.on_collection} />
                                <label for="sc-collection-page">Its collection's page</label>
                            </div>
                        </div>
                        <div class="field-help">Always at its own address too. Each storefront decides how these places look.</div>
                    </section>

                    <section class="card">
                        <h6 class="section-title">
                            <i class="ri-bar-chart-2-line" aria-hidden="true"></i>
                            How it did
                        </h6>
                        <dl class="sc-stats">
                            <div><dt>Views</dt><dd>{show.stats.views.toLocaleString()}</dd></div>
                            <div><dt>Product clicks</dt><dd>{show.stats.clicks.toLocaleString()}</dd></div>
                            <div><dt>Added to cart</dt><dd>{show.stats.adds_to_cart.toLocaleString()}</dd></div>
                            <div><dt>Orders</dt><dd>{show.stats.orders.toLocaleString()}</dd></div>
                            <div><dt>From those orders</dt><dd>{formatMoney(show.stats.revenue)}</dd></div>
                        </dl>
                        <div class="field-help">
                            As the storefront reports them. An order counts here when its basket was filled from this showcase.
                            Nothing about the buyer is kept.
                        </div>
                    </section>
                </div>
            {/if}
        </div>
    </div>

    <Confirm bind:open={removing} title="Delete this showcase?" message="Gone for good, with its counts. Archive it instead to keep them." confirmLabel="Delete" danger onconfirm={remove} />
{/if}

<style>
    /* One column the width of the screen: a grid track otherwise grows to its
       widest content — the product table, a long address — and a phone
       loses the right-hand side of every card. */
    .sc-editor {
        display: grid;
        grid-template-columns: minmax(0, 1fr);
        gap: 12px;
    }
    .sc-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
    }
    .sc-toggle,
    .sc-feature,
    .sc-live {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }
    .sc-live {
        margin-top: 12px;
    }
    .sc-stage {
        display: grid;
        grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
        gap: 12px;
        margin: 8px 0;
        transition: max-width 250ms cubic-bezier(0.2, 0.7, 0.2, 1);
    }
    .sc-stage.phone {
        grid-template-columns: minmax(0, 1fr);
        max-width: 360px;
        margin-inline: auto;
    }
    @media (max-width: 720px) {
        .sc-stage {
            grid-template-columns: minmax(0, 1fr);
        }
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
    .sc-products {
        display: grid;
        align-content: start;
        gap: 6px;
        min-width: 0;
    }
    .sc-products-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
    }
    .sc-card {
        display: flex;
        align-items: center;
        gap: 8px;
        min-width: 0;
        padding: 6px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: 8px;
        transition:
            border-color 150ms ease-out,
            transform 150ms ease-out;
    }
    .sc-card.featured {
        border-color: var(--successColor);
    }
    @media (prefers-reduced-motion: no-preference) {
        .sc-card.featured {
            transform: translateX(2px);
        }
    }
    .sc-card img,
    .sc-card-blank {
        flex: 0 0 auto;
        width: 44px;
        height: 44px;
        object-fit: cover;
        border-radius: 6px;
        background: var(--surfaceAlt2Color);
    }
    .sc-card-title {
        flex: 1 1 auto;
        min-width: 0;
        overflow-wrap: anywhere;
        font-weight: 500;
    }
    .sc-make {
        display: flex;
        align-items: flex-end;
        gap: 8px;
        margin-top: 12px;
    }
    .sc-make .field {
        flex: 1 1 auto;
        min-width: 0;
    }
    .sc-list {
        margin: 8px 0 0;
        padding-left: 18px;
    }
    .sc-places {
        display: grid;
        gap: 6px;
    }
    .sc-stats {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
        gap: 8px;
        margin: 0;
    }
    .sc-stats dt {
        color: var(--txtHintColor);
        font-size: 12px;
    }
    .sc-stats dd {
        margin: 0;
        font-size: 18px;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
    }
    @media (prefers-reduced-motion: reduce) {
        .sc-stage,
        .sc-card {
            transition: none;
        }
    }
</style>
