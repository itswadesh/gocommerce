<script>
    /**
     * Create from photos.
     *
     * Three steps on one screen, chosen by where the batch is:
     *
     *   1. Drop photos. Each is stored in the media library as it is, and two
     *      small JPEG copies (512 and 1024 px) go to the batch: the model is
     *      shown those, never the 6 MB original.
     *   2. Wait. One call sorts every photo into products and variants, then
     *      one call per product writes its listing; the bar counts products.
     *   3. Review. Rename, move a photo, merge, split one off, leave one out,
     *      then Create. Every product is a draft at price 0 with no stock, so
     *      nothing reaches a shopper until it is priced and published.
     *
     * The batch id is in the address, so a reload, or coming back later,
     * lands on the same step.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page } from "$app/state";
    import { onDestroy } from "svelte";
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatMoney, pluralize, relativeTime, sentence } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import { titled } from "$lib/brand.svelte.js";
    import { uploadMedia } from "$lib/components/MediaLibrary.svelte";
    import Select from "$lib/components/Select.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import {
        IMAGE_SIDE,
        THUMB_SIDE,
        costMinor,
        downscale,
        mergeInto,
        movePhoto,
        problems,
        progress,
        saveBody,
        setSkip,
        sizesFrom,
    } from "$lib/photoproducts.js";

    const API = "/api/admin/x/photo-products";
    const writable = $derived(can("catalog.write"));
    const missing = $derived(modulesKnown() && !hasModule("photo-products"));
    const batchID = $derived(page.url.searchParams.get("batch"));

    let status = $state(null); // { ready, reason, max_photos }
    let recent = $state([]);
    let batch = $state(null);
    let items = $state([]);
    let categories = $state(new Map());
    let loading = $state(true);

    // Step 1.
    let files = $state([]); // { file, url, state: queued|working|done|failed, error }
    let hasSizes = $state(false);
    let sizesText = $state("");
    let uploading = $state(false);
    let dragging = $state(false);
    let input = $state(null);

    let busy = $state(false);
    let timer = null;

    $effect(() => {
        if (writable && hasModule("photo-products")) load(batchID);
    });
    onDestroy(() => {
        clearTimeout(timer);
        for (const f of files) URL.revokeObjectURL(f.url);
    });

    async function load(id) {
        loading = true;
        clearTimeout(timer);
        try {
            status = await api.get(`${API}/status`);
            if (id) {
                take(await api.get(`${API}/batches/${id}`));
                loadCategories();
            } else {
                batch = null;
                const res = await api.get(`${API}/batches`);
                recent = (res.data ?? []).filter((b) => b.status !== "done" && b.photo_count > 0);
            }
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function loadCategories() {
        if (categories.size || !can("categories.read")) return;
        try {
            const res = await api.get("/api/admin/categories?limit=200");
            categories = new Map((res.data ?? []).map((c) => [c.id, c.full_name || c.title]));
        } catch {
            /* the review shows the category's number instead */
        }
    }

    /** The engine's answer becomes the screen; a working batch is asked again. */
    function take(b) {
        batch = b;
        items = (b.items ?? []).map((it) => ({ ...it, draft: { ...it.draft, highlights: it.draft.highlights ?? [], tags: it.draft.tags ?? [] } }));
        clearTimeout(timer);
        if (b.status === "grouping" || b.status === "describing") {
            timer = setTimeout(async () => {
                try {
                    take(await api.get(`${API}/batches/${b.id}`));
                } catch (err) {
                    toast.error(err);
                }
            }, 1500);
        }
    }

    // ------------------------------------------------------------- upload

    function addFiles(list) {
        const room = (status?.max_photos ?? 60) - files.length;
        const images = [...list].filter((f) => f.type.startsWith("image/"));
        if (images.length > room) toast.error(`A batch takes ${status?.max_photos ?? 60} photos; the rest were left out.`);
        files = [...files, ...images.slice(0, Math.max(0, room)).map((file) => ({ file, url: URL.createObjectURL(file), state: "queued", error: "" }))];
    }

    function removeFile(i) {
        URL.revokeObjectURL(files[i].url);
        files = files.filter((_, j) => j !== i);
    }

    async function startBatch() {
        if (uploading || !files.length) return;
        uploading = true;
        try {
            const sizes = hasSizes ? sizesFrom(sizesText) : [];
            const b = await api.post(`${API}/batches`, { sizes });
            // Three at a time: enough to keep a phone's upload busy, few
            // enough that one slow photo does not hold the rest.
            let next = 0;
            const worker = async () => {
                while (next < files.length) {
                    const i = next++;
                    if (files[i].state === "done") continue;
                    files[i].state = "working";
                    try {
                        const [thumb, image] = [await downscale(files[i].file, THUMB_SIDE), await downscale(files[i].file, IMAGE_SIDE)];
                        const media = await uploadMedia(files[i].file);
                        await api.post(`${API}/batches/${b.id}/photos`, { media_id: media.id, thumb, image });
                        files[i].state = "done";
                    } catch (err) {
                        files[i].state = "failed";
                        files[i].error = err?.message || "This photo could not be read.";
                    }
                }
            };
            await Promise.all([worker(), worker(), worker()]);
            const ok = files.filter((f) => f.state === "done").length;
            if (!ok) {
                toast.error("None of the photos could be uploaded.");
                return;
            }
            take(await api.post(`${API}/batches/${b.id}/start`));
            await goto(`${base}/dash/products/photos?batch=${b.id}`, { replaceState: true, noScroll: true });
        } catch (err) {
            toast.error(err);
        } finally {
            uploading = false;
        }
    }

    async function runAgain() {
        if (busy) return;
        busy = true;
        try {
            if (batch.status === "review") await api.put(`${API}/batches/${batch.id}/items`, saveBody(items));
            take(await api.post(`${API}/batches/${batch.id}/start`));
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    // ------------------------------------------------------------- review

    const photoURL = $derived(new Map((batch?.photos ?? []).map((p) => [p.id, p.url])));
    const issues = $derived(problems(items));
    const keeping = $derived(items.filter((it) => !it.skip && it.product_id == null).length);
    const undescribed = $derived(items.filter((it) => !it.described && it.product_id == null).length);

    /** Every place a photo can go, as the Move menu lists them. */
    const destinations = $derived([
        ...items.flatMap((it, i) =>
            it.draft.variants.map((v, j) => ({
                value: `${i}:${j}`,
                label: (it.draft.title || `Product ${i + 1}`) + (it.draft.variants.length > 1 ? ` · ${v.value || `variant ${j + 1}`}` : ""),
            })),
        ),
        { value: "new", label: "A new product of its own" },
    ]);

    function move(photoId, to) {
        items = movePhoto(items, photoId, to === "new" ? "new" : to.split(":").map(Number));
    }

    function merge(from, value) {
        items = mergeInto(items, from, Number(value));
    }

    async function create() {
        if (busy || issues.length) return;
        busy = true;
        try {
            await api.put(`${API}/batches/${batch.id}/items`, saveBody(items));
            const res = await api.post(`${API}/batches/${batch.id}/create`);
            take(res.batch);
            if (res.failed) toast.error(`${res.failed} ${pluralize(res.failed, "product", "products")} could not be created: ${res.errors[0]}`);
            else toast.success(`Created ${res.created} draft ${pluralize(res.created, "product", "products")}`);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    const lines = (list) => (list ?? []).join("\n");
    const unlines = (text) => text.split("\n").map((s) => s.trim()).filter(Boolean).slice(0, 8);
    const created = $derived(items.filter((it) => it.product_id != null).length);
    const spent = $derived(batch && batch.cost_micro_minor ? formatMoney({ amount_minor: costMinor(batch), currency: batch.currency }) : "");
</script>

<svelte:head><title>{titled("Create from photos")}</title></svelte:head>

{#if !writable}
    <NoAccess right="catalog.write" what="creating products from photos" />
{:else}
    <div class="page photo-products shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a class="breadcrumb-item tw:text-sm" href="{base}/dash/products">Products</a>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Create from photos</div>
                </nav>
            </header>

            {#if missing}
                <ModuleMissing module="photo-products" what="Creating products from photos is served by ext/photo-products, and this binary does not have it." />
            {:else if loading}
                <span class="skeleton-loader"></span>
                <span class="skeleton-loader"></span>
            {:else if !batch}
                {#if status && !status.ready}
                    <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base" role="status" in:rise>
                        <h2 class="tw:text-sm tw:font-semibold tw:mb-1">Set up the AI gateway first</h2>
                        <p class="tw:text-sm tw:text-muted-foreground tw:mb-1">{sentence(status.reason)}</p>
                        <p class="tw:text-xs tw:text-muted-foreground tw:mb-3">
                            It needs your Anthropic API key, a monthly cap, the strong model's prices, and Share documents switched on so the photos can be sent.
                        </p>
                        <a class="btn secondary" href="{base}/dash/plugins?open=ai-gateway">
                            <i class="ri-settings-3-line" aria-hidden="true"></i>
                            <span class="txt">AI gateway settings</span>
                        </a>
                    </section>
                {/if}

                {#each recent.slice(0, 1) as r (r.id)}
                    <p class="field-help m-b-base" in:rise>
                        A batch of {r.photo_count} photos from {relativeTime(r.created_at)} is {r.status === "review" ? "waiting for review" : r.status}.
                        <a href="{base}/dash/products/photos?batch={r.id}">Open it</a>
                    </p>
                {/each}

                <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
                    <div
                        class="drop"
                        class:dragging
                        role="button"
                        tabindex="0"
                        aria-label="Choose photos"
                        onclick={() => input?.click()}
                        onkeydown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), input?.click())}
                        ondragover={(e) => (e.preventDefault(), (dragging = true))}
                        ondragleave={() => (dragging = false)}
                        ondrop={(e) => (e.preventDefault(), (dragging = false), addFiles(e.dataTransfer.files))}
                    >
                        <i class="ri-image-add-line tw:text-2xl" aria-hidden="true"></i>
                        <span class="tw:text-sm tw:font-medium">Drop product photos here, or choose them</span>
                        <span class="tw:text-xs tw:text-muted-foreground">
                            Up to {status?.max_photos ?? 60} at once. Several angles of one item, and the same item in other colours, are fine
                            together: they are sorted into products and variants for you.
                        </span>
                    </div>
                    <input bind:this={input} type="file" accept="image/*" multiple hidden onchange={(e) => (addFiles(e.currentTarget.files), (e.currentTarget.value = ""))} />

                    {#if files.length}
                        <ul class="pp-thumbs m-t-sm" aria-label="Chosen photos">
                            {#each files as f, i (f.url)}
                                <li class="pp-thumb" data-state={f.state} in:rise>
                                    <img class="pp-img" src={f.url} alt="" />
                                    {#if f.state === "queued" && !uploading}
                                        <button type="button" class="btn circle sm transparent remove" aria-label="Remove {f.file.name}" onclick={() => removeFile(i)}>
                                            <i class="ri-close-line" aria-hidden="true"></i>
                                        </button>
                                    {:else if f.state === "working"}
                                        <span class="badge-state" aria-label="Uploading"><i class="ri-loader-4-line spin" aria-hidden="true"></i></span>
                                    {:else if f.state === "done"}
                                        <span class="badge-state ok" aria-label="Uploaded"><i class="ri-check-line" aria-hidden="true"></i></span>
                                    {:else if f.state === "failed"}
                                        <span class="badge-state bad" title={f.error} aria-label="Failed: {f.error}"><i class="ri-error-warning-line" aria-hidden="true"></i></span>
                                    {/if}
                                </li>
                            {/each}
                        </ul>
                    {/if}

                    <div class="field m-t-sm">
                        <label class="tw:flex tw:items-center tw:gap-2 tw:text-sm" for="pp-sizes-on">
                            <input id="pp-sizes-on" type="checkbox" bind:checked={hasSizes} />
                            These come in sizes
                        </label>
                    </div>
                    {#if hasSizes}
                        <div class="field" in:rise>
                            <label for="pp-sizes">Sizes</label>
                            <input id="pp-sizes" type="text" placeholder="S, M, L, XL" bind:value={sizesText} autocomplete="off" />
                        </div>
                        <p class="field-help">Every product gets these as a Size option. A photo does not show sizes, so they come from you.</p>
                    {/if}

                    <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-3 tw:mt-4">
                        <button type="button" class="btn" disabled={!files.length || uploading || !status?.ready} onclick={startBatch}>
                            {#if uploading}<i class="ri-loader-4-line spin" aria-hidden="true"></i>{:else}<i class="ri-sparkling-line" aria-hidden="true"></i>{/if}
                            <span class="txt">{uploading ? "Uploading…" : `Draft products from ${files.length || ""} ${pluralize(files.length, "photo", "photos")}`}</span>
                        </button>
                        <span class="tw:text-xs tw:text-muted-foreground">
                            Products are created as drafts with no price, for you to check, price and publish.
                        </span>
                    </div>
                </section>
            {:else if batch.status === "grouping" || batch.status === "describing" || batch.status === "creating"}
                <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base" aria-live="polite" in:rise>
                    <h2 class="tw:text-sm tw:font-semibold tw:mb-1">
                        {batch.status === "grouping" ? `Sorting ${batch.photo_count} photos into products` : batch.message}
                    </h2>
                    <p class="tw:text-xs tw:text-muted-foreground tw:mb-3">
                        {#if progress(batch) != null}{batch.step_done} of {batch.step_total} products written.{:else}Looking at every photo together.{/if}
                        You can leave this page; the work carries on.
                    </p>
                    <div
                        class="bar"
                        role="progressbar"
                        aria-label="Progress"
                        aria-valuemin="0"
                        aria-valuemax={batch.step_total || undefined}
                        aria-valuenow={progress(batch) != null ? batch.step_done : undefined}
                    >
                        {#if progress(batch) == null}
                            <span class="bar-fill indeterminate"></span>
                        {:else}
                            <span class="bar-fill" style="transform: scaleX({progress(batch)})"></span>
                        {/if}
                    </div>
                </section>
            {:else if batch.status === "failed"}
                <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base" role="alert" in:rise>
                    <h2 class="tw:text-sm tw:font-semibold tw:mb-1"><span class="dot bad"></span> This batch stopped</h2>
                    <p class="tw:text-sm tw:text-muted-foreground tw:mb-3">{batch.message}</p>
                    <button type="button" class="btn" disabled={busy} onclick={runAgain}>
                        <i class="ri-refresh-line" aria-hidden="true"></i><span class="txt">Try again</span>
                    </button>
                </section>
            {:else}
                {#if batch.status === "done"}
                    <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base" role="status" in:rise>
                        <h2 class="tw:text-sm tw:font-semibold tw:mb-1"><span class="dot ok"></span> {batch.message}</h2>
                        <p class="tw:text-sm tw:text-muted-foreground tw:mb-3">They are drafts at price 0: set their prices, then publish them.</p>
                        <a class="btn" href="{base}/dash/products?status=draft">
                            <i class="ri-price-tag-3-line" aria-hidden="true"></i><span class="txt">Set prices</span>
                        </a>
                    </section>
                {:else}
                    <div class="review-bar tw:rounded-xl tw:border tw:bg-card tw:p-4 m-b-base">
                        <div class="tw:text-sm">
                            <strong>{keeping} {pluralize(keeping, "product", "products")}</strong> from {batch.photo_count} photos.
                            {#if created}{created} already created.{/if}
                            {#if spent}<span class="tw:text-muted-foreground">AI cost {spent}.</span>{/if}
                            {#if issues.length}<div class="tw:text-xs tw:text-red-600 tw:mt-1" role="alert">{issues[0]}</div>{/if}
                        </div>
                        <div class="tw:flex tw:gap-2 tw:flex-wrap">
                            {#if undescribed}
                                <button type="button" class="btn secondary" disabled={busy} onclick={runAgain}>
                                    <i class="ri-refresh-line" aria-hidden="true"></i><span class="txt">Write the {undescribed} missing</span>
                                </button>
                            {/if}
                            <button type="button" class="btn" disabled={busy || !keeping || issues.length > 0} onclick={create}>
                                {#if busy}<i class="ri-loader-4-line spin" aria-hidden="true"></i>{/if}
                                <span class="txt">Create {keeping} draft {pluralize(keeping, "product", "products")}</span>
                            </button>
                        </div>
                    </div>
                {/if}

                <ol class="proposals">
                    {#each items as it, i (it.id ?? `new-${i}`)}
                        {@const d = it.draft}
                        <li class="proposal tw:rounded-xl tw:border tw:bg-card tw:p-4" class:skipped={it.skip} class:created={it.product_id != null} in:rise>
                            <div class="proposal-head">
                                <div class="field tw:flex-1">
                                    <label for="pp-title-{i}">Title</label>
                                    <input id="pp-title-{i}" type="text" bind:value={it.draft.title} disabled={it.product_id != null || it.skip} />
                                </div>
                                {#if it.product_id != null}
                                    <a class="btn secondary sm" href="{base}/dash/products/{it.product_id}"><span class="txt">Open</span></a>
                                {:else}
                                    <button type="button" class="btn secondary sm" aria-pressed={!!it.skip} onclick={() => (items = setSkip(items, i, !it.skip))}>
                                        <span class="txt">{it.skip ? "Include" : "Leave out"}</span>
                                    </button>
                                {/if}
                            </div>
                            {#if it.error}<p class="tw:text-xs tw:text-red-600 tw:mb-2" role="alert">{it.error}</p>{/if}
                            {#if !it.described && it.product_id == null && !it.error}<p class="tw:text-xs tw:text-muted-foreground tw:mb-2">No listing written yet.</p>{/if}

                            {#if d.variants.length > 1}
                                <div class="field m-b-sm">
                                    <label for="pp-option-{i}">Variants differ in</label>
                                    <input id="pp-option-{i}" type="text" bind:value={it.draft.option_name} disabled={it.product_id != null || it.skip} />
                                </div>
                            {/if}

                            {#each d.variants as v, j (j)}
                                <div class="variant">
                                    {#if d.variants.length > 1}
                                        <div class="field variant-name">
                                            <label for="pp-v-{i}-{j}">{d.option_name || "Variant"} {j + 1}</label>
                                            <input id="pp-v-{i}-{j}" type="text" bind:value={it.draft.variants[j].value} disabled={it.product_id != null || it.skip} />
                                        </div>
                                    {/if}
                                    <ul class="pp-thumbs">
                                        {#each v.photos as pid (pid)}
                                            <li class="pp-thumb">
                                                <img class="pp-img" src={photoURL.get(pid)} alt={d.alts?.[pid] ?? ""} loading="lazy" />
                                                {#if it.product_id == null && !it.skip}
                                                    <div class="thumb-move">
                                                        <Select
                                                            ariaLabel="Move this photo"
                                                            placeholder="Move"
                                                            value={null}
                                                            options={destinations.filter((o) => o.value !== `${i}:${j}`)}
                                                            onchange={(to) => move(pid, to)}
                                                        />
                                                    </div>
                                                {/if}
                                            </li>
                                        {/each}
                                    </ul>
                                </div>
                            {/each}

                            {#if it.product_id == null && !it.skip}
                                <details class="m-t-sm">
                                    <summary class="tw:text-sm tw:font-medium">Listing</summary>
                                    <div class="field m-t-sm">
                                        <label for="pp-desc-{i}">Description</label>
                                        <textarea id="pp-desc-{i}" rows="4" bind:value={it.draft.description}></textarea>
                                    </div>
                                    <div class="field">
                                        <label for="pp-hl-{i}">Highlights, one a line (up to 8, 80 characters each)</label>
                                        <textarea id="pp-hl-{i}" rows="3" value={lines(d.highlights)} oninput={(e) => (it.draft.highlights = unlines(e.currentTarget.value))}></textarea>
                                    </div>
                                    <div class="field">
                                        <label for="pp-type-{i}">Product type</label>
                                        <input id="pp-type-{i}" type="text" bind:value={it.draft.product_type} />
                                    </div>
                                    <div class="field">
                                        <label for="pp-tags-{i}">Tags</label>
                                        <input
                                            id="pp-tags-{i}"
                                            type="text"
                                            value={d.tags.join(", ")}
                                            oninput={(e) => (it.draft.tags = sizesFrom(e.currentTarget.value))}
                                        />
                                    </div>
                                    <p class="field-help">
                                        Category:
                                        {#if d.category_id}{categories.get(d.category_id) ?? `#${d.category_id}`}{:else}none{/if}{#if d.suggested_category}{" "}
                                            — suggested new category “{d.suggested_category}”, not created{/if}.
                                        {#if d.hs_code}HSN code suggestion {d.hs_code}, kept in the product's details for you to check.{/if}
                                        {#if d.sizes?.length}Sizes {d.sizes.join(", ")}.{/if}
                                    </p>
                                    {#if items.length > 1}
                                        <div class="field">
                                            <Select
                                                ariaLabel="Merge into another product"
                                                placeholder="Merge into…"
                                                value={null}
                                                options={items.map((o, k) => ({ value: String(k), label: o.draft.title || `Product ${k + 1}` })).filter((o, k) => k !== i && items[k].product_id == null)}
                                                onchange={(to) => merge(i, to)}
                                            />
                                        </div>
                                    {/if}
                                </details>
                            {/if}
                        </li>
                    {/each}
                </ol>
            {/if}
        </div>
    </div>
{/if}

<style>
    .drop {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 6px;
        padding: 28px 16px;
        text-align: center;
        border: 1px dashed var(--borderColor, currentColor);
        border-radius: 12px;
        cursor: pointer;
        transition:
            background-color 180ms ease,
            transform 180ms ease;
    }
    .drop:hover,
    .drop.dragging {
        background: var(--baseAlt1Color, rgba(127, 127, 127, 0.08));
    }
    .drop:active {
        transform: scale(0.99);
    }
    .drop:focus-visible {
        outline: 2px solid currentColor;
        outline-offset: 2px;
    }
    .pp-thumbs {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(min(120px, 40%), 1fr));
        gap: 8px;
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .pp-thumb {
        position: relative;
        display: flex;
        flex-direction: column;
        gap: 4px;
        min-width: 0;
    }
    .pp-img {
        width: 100%;
        aspect-ratio: 1;
        object-fit: cover;
        display: block;
        border-radius: 8px;
        border: 1px solid var(--borderColor, rgba(127, 127, 127, 0.3));
        transition: opacity 200ms ease;
    }
    .pp-thumb[data-state="working"] .pp-img,
    .pp-thumb[data-state="queued"] .pp-img {
        opacity: 0.7;
    }
    .pp-thumb .remove,
    .badge-state {
        position: absolute;
        top: 4px;
        right: 4px;
    }
    .badge-state {
        display: grid;
        place-items: center;
        width: 24px;
        height: 24px;
        border-radius: 999px;
        background: rgba(0, 0, 0, 0.55);
        color: #fff;
        animation: pop 200ms ease-out;
    }
    .badge-state.ok {
        background: rgb(16 185 129);
    }
    .badge-state.bad {
        background: rgb(239 68 68);
    }
    .thumb-move :global(.input.select) {
        min-height: 32px;
        font-size: 12px;
    }
    .bar {
        height: 8px;
        border-radius: 999px;
        overflow: hidden;
        background: var(--baseAlt2Color, rgba(127, 127, 127, 0.2));
    }
    .bar-fill {
        display: block;
        height: 100%;
        width: 100%;
        border-radius: inherit;
        background: currentColor;
        transform-origin: left;
        transition: transform 300ms ease;
    }
    .bar-fill.indeterminate {
        width: 35%;
        animation: slide 1.2s ease-in-out infinite;
    }
    .dot {
        display: inline-block;
        width: 8px;
        height: 8px;
        border-radius: 999px;
        margin-right: 4px;
        vertical-align: middle;
    }
    .dot.ok {
        background: rgb(16 185 129);
    }
    .dot.bad {
        background: rgb(239 68 68);
    }
    .review-bar {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        position: sticky;
        top: 0;
        z-index: 2;
    }
    .proposals {
        list-style: none;
        margin: 0;
        padding: 0;
        display: grid;
        gap: 12px;
    }
    .proposal {
        transition: opacity 200ms ease;
    }
    .proposal.skipped {
        opacity: 0.5;
    }
    .proposal-head {
        display: flex;
        align-items: flex-end;
        gap: 8px;
        margin-bottom: 8px;
    }
    .variant {
        margin-top: 8px;
    }
    .variant-name {
        margin-bottom: 6px;
        max-width: 280px;
    }
    .spin {
        display: inline-block;
        animation: spin 900ms linear infinite;
    }
    @keyframes spin {
        to {
            transform: rotate(360deg);
        }
    }
    @keyframes slide {
        from {
            transform: translateX(-100%);
        }
        to {
            transform: translateX(290%);
        }
    }
    @keyframes pop {
        from {
            transform: scale(0.6);
            opacity: 0;
        }
        to {
            transform: scale(1);
            opacity: 1;
        }
    }
    @keyframes pulse {
        50% {
            opacity: 0.4;
        }
    }
    /* The movement goes; the feedback stays. */
    @media (prefers-reduced-motion: reduce) {
        .drop,
        .bar-fill {
            transition: none;
        }
        .drop:active {
            transform: none;
        }
        .badge-state {
            animation: none;
        }
        .spin {
            animation: pulse 1.2s ease-in-out infinite;
        }
        .bar-fill.indeterminate {
            width: 100%;
            animation: pulse 1.2s ease-in-out infinite;
        }
    }
</style>
