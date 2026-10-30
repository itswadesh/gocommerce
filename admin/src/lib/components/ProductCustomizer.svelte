<script>
    /**
     * A product's Customizer card (D159): whether shoppers may design their
     * own print on it, and the editor they are given. The mockup is drawn
     * with the print area over it; the area is dragged, drawn anew or resized
     * by its corner, or typed into the boxes below, which is also the way in
     * by keyboard and on a phone.
     *
     * Saved by the page's one Save button, like the upsell cards: the page
     * reads `dirty`, asks `invalid()` before it writes anything, then calls
     * `save()`; Discard calls `reset()`.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { mediaQuery } from "$lib/media.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import {
        MAX_STICKERS,
        TOOLS,
        areaFromDrag,
        aspectGap,
        ASPECT_TOLERANCE,
        clampArea,
        fitToSize,
        formFromSetup,
        formProblem,
        moveArea,
        printPixels,
        resizeArea,
        setupBody,
        startingArea,
        toMockup,
    } from "$lib/customizer.js";
    import ImageField from "$lib/components/ImageField.svelte";
    import MediaLibrary from "$lib/components/MediaLibrary.svelte";

    let { productId, disabled = false, dirty = $bindable(false) } = $props();

    let form = $state(null);
    let saved = $state("null");
    let fonts = $state([]);
    let storeOn = $state(null);
    let mockupURL = $state("");
    let mockupProblem = $state("");
    let stickerItems = $state(new Map());

    const problem = $derived(form ? formProblem(form) : "");
    const pixels = $derived(form ? printPixels(form.print_size) : null);
    const gap = $derived(form ? aspectGap(form.print_area, form.print_size) : null);
    const pickable = $derived(can("media.read"));

    $effect(() => {
        dirty = !!form && JSON.stringify(setupBody(form)) !== saved;
    });

    $effect(() => {
        if (productId) load(productId);
    });

    async function load(id) {
        const [setup, list] = await Promise.all([
            api.get(`/api/admin/x/customizer/products/${id}`),
            api.get("/api/admin/x/customizer/fonts"),
        ]);
        fonts = list.fonts;
        take(setup);
        if (can("plugins.read")) {
            request("GET", "/api/admin/plugins/customizer")
                .then((p) => (storeOn = !!p.enabled))
                .catch(() => (storeOn = null));
        }
    }

    function take(setup) {
        form = formFromSetup(setup);
        saved = JSON.stringify(setupBody(form));
        mockupURL = form.mockup?.url ?? "";
        mockupProblem = "";
        loadStickers(form.sticker_media_ids);
    }

    export async function save() {
        if (!dirty) return;
        take(await request("PUT", `/api/admin/x/customizer/products/${productId}`, { body: setupBody(form) }));
    }

    export function invalid() {
        return dirty ? mockupProblem || problem : "";
    }

    export function reset() {
        take(JSON.parse(saved));
    }

    $effect(() => {
        if (form && mockupURL !== (form.mockup?.url ?? "")) takeMockup(mockupURL);
    });

    /** Reads a newly chosen mockup's size, which the engine checks against
     *  the file, and gives it a print area in the print's shape if it has none. */
    function takeMockup(url) {
        if (!url) {
            form.mockup = null;
            return;
        }
        const img = new Image();
        img.onload = () => {
            if (mockupURL !== url) return;
            const mockup = { url, width: img.naturalWidth, height: img.naturalHeight };
            form.mockup = mockup;
            form.print_area = form.print_area ? clampArea(form.print_area, mockup) : startingArea(mockup, form.print_size);
            mockupProblem = "";
        };
        img.onerror = () => {
            if (mockupURL === url) mockupProblem = "The mockup did not load. Choose a picture from Files.";
        };
        img.src = url;
    }

    /** A drag on the picture shorter than this, in mockup pixels, is a click. */
    const MIN_DRAWN = 8;

    let svg = $state(null);
    let drag = $state(null);

    function point(e) {
        return toMockup(e.clientX, e.clientY, svg.getBoundingClientRect(), form.mockup);
    }

    function down(e, mode) {
        if (disabled || e.button > 0) return;
        e.preventDefault();
        svg.setPointerCapture(e.pointerId);
        drag = { mode, start: point(e), area: form.print_area ? { ...form.print_area } : null };
    }

    function move(e) {
        if (!drag) return;
        const p = point(e);
        const dx = p.x - drag.start.x;
        const dy = p.y - drag.start.y;
        if (drag.mode === "move") form.print_area = moveArea(drag.area, dx, dy, form.mockup);
        else if (drag.mode === "resize") form.print_area = resizeArea(drag.area, dx, dy, form.mockup);
        else form.print_area = areaFromDrag(drag.start, p, form.mockup, drag.area?.radius ?? 0);
    }

    function up() {
        if (drag?.mode === "draw" && drag.area && (form.print_area.width < MIN_DRAWN || form.print_area.height < MIN_DRAWN)) {
            form.print_area = drag.area;
        }
        drag = null;
    }

    function settle() {
        if (form.mockup && form.print_area) form.print_area = clampArea(form.print_area, form.mockup);
    }

    function matchShape() {
        form.print_area = fitToSize(form.print_area, form.print_size, form.mockup);
    }

    let picking = $state(false);
    let library = $state([]);
    let libraryLoading = $state(false);
    let filters = $state({ q: "", kind: "image", usage: "", size: "", scope: "", sort: "" });
    let view = $state("grid");

    async function loadStickers(ids) {
        const missing = ids.filter((id) => !stickerItems.has(id));
        if (!missing.length || !pickable) return;
        const found = await Promise.all(missing.map((id) => api.get(`/api/admin/media/${id}`).catch(() => null)));
        const next = new Map(stickerItems);
        for (const item of found) if (item) next.set(item.id, item);
        stickerItems = next;
    }

    async function openLibrary() {
        picking = true;
        libraryLoading = true;
        try {
            const result = await api.get("/api/admin/media" + query(mediaQuery(filters, { page: 1, limit: 48 })));
            library = result.data;
        } catch (err) {
            toast.error(err);
        } finally {
            libraryLoading = false;
        }
    }

    function toggleSticker(item) {
        const ids = form.sticker_media_ids;
        if (ids.includes(item.id)) {
            form.sticker_media_ids = ids.filter((id) => id !== item.id);
            return;
        }
        if (ids.length >= MAX_STICKERS) {
            toast.error(`A product offers at most ${MAX_STICKERS} stickers.`);
            return;
        }
        stickerItems = new Map(stickerItems).set(item.id, item);
        form.sticker_media_ids = [...ids, item.id];
    }

    function toggleFont(key, on) {
        form.fonts = on ? [...form.fonts, key] : form.fonts.filter((f) => f !== key);
    }
</script>

<section class="card customizer-card">
    <h6 class="section-title">
        <i class="ri-brush-line" aria-hidden="true"></i>
        Customizer
    </h6>
    {#if !form}
        <p class="txt-hint txt-sm">Loading…</p>
    {:else}
        <p class="txt-hint txt-sm m-b-sm">
            Shoppers design their own print on this product: a photo, words, shapes and stickers on the
            print area. The order carries the print file.
        </p>
        {#if storeOn === false}
            <div class="alert warning m-b-sm" in:rise>
                The customizer is off in this store, so shoppers do not see it yet. Switch it on under
                <a href="{base}/dash/plugins">Plugins</a>.
            </div>
        {/if}

        <div class="field">
            <input id="cz-enabled" type="checkbox" class="switch" {disabled} bind:checked={form.enabled} />
            <label for="cz-enabled">Shoppers can design this product</label>
        </div>

        <div class="cz-pictures">
            <div>
                <ImageField
                    id="cz-mockup"
                    label="Mockup"
                    bind:value={mockupURL}
                    hint="The product as the shopper sees it while designing, such as a case from behind."
                />
                {#if mockupProblem}
                    <div class="field-help error" role="alert" in:rise>{mockupProblem}</div>
                {/if}
            </div>
            <div>
                <ImageField
                    id="cz-overlay"
                    label="Overlay (optional)"
                    bind:value={form.overlay_url}
                    hint="A transparent PNG the mockup's size, drawn over the design: the camera cut-out and the edges."
                />
            </div>
        </div>

        {#if form.mockup}
            <div class="cz-editor" in:rise>
                <div
                    class="cz-stage"
                    style="--w: {form.mockup.width}; --h: {form.mockup.height}"
                >
                    <img class="cz-layer" src={form.mockup.url} alt="" />
                    {#if form.overlay_url}
                        <img class="cz-layer" src={form.overlay_url} alt="" />
                    {/if}
                    <svg
                        bind:this={svg}
                        class="cz-layer cz-svg"
                        class:dragging={!!drag}
                        viewBox="0 0 {form.mockup.width} {form.mockup.height}"
                        aria-hidden="true"
                        onpointerdown={(e) => down(e, e.target.dataset?.mode ?? "draw")}
                        onpointermove={move}
                        onpointerup={up}
                        onpointercancel={up}
                    >
                        {#if form.print_area}
                            {@const a = form.print_area}
                            <path
                                class="cz-shade"
                                fill-rule="evenodd"
                                d="M0 0H{form.mockup.width}V{form.mockup.height}H0Z M{a.x + a.radius} {a.y}H{a.x + a.width - a.radius}A{a.radius} {a.radius} 0 0 1 {a.x + a.width} {a.y + a.radius}V{a.y + a.height - a.radius}A{a.radius} {a.radius} 0 0 1 {a.x + a.width - a.radius} {a.y + a.height}H{a.x + a.radius}A{a.radius} {a.radius} 0 0 1 {a.x} {a.y + a.height - a.radius}V{a.y + a.radius}A{a.radius} {a.radius} 0 0 1 {a.x + a.radius} {a.y}Z"
                            />
                            <rect
                                class="cz-area"
                                x={a.x}
                                y={a.y}
                                width={a.width}
                                height={a.height}
                                rx={a.radius}
                                vector-effect="non-scaling-stroke"
                                data-mode="move"
                            />
                            <circle
                                class="cz-handle"
                                cx={a.x + a.width}
                                cy={a.y + a.height}
                                r={Math.max(form.mockup.width, form.mockup.height) / 40}
                                data-mode="resize"
                            />
                        {/if}
                    </svg>
                </div>

                <div class="cz-fields">
                    <p class="txt-hint txt-sm m-b-xs">
                        Print area, in the mockup's pixels ({form.mockup.width} × {form.mockup.height}). Drag
                        it, draw a new one, or pull its corner.
                    </p>
                    {#if form.print_area}
                        <div class="cz-numbers">
                            {#each [["x", "Left"], ["y", "Top"], ["width", "Width"], ["height", "Height"], ["radius", "Corner radius"]] as [key, label] (key)}
                                <div class="field">
                                    <label for="cz-area-{key}">{label}</label>
                                    <input
                                        id="cz-area-{key}"
                                        type="number"
                                        inputmode="numeric"
                                        min="0"
                                        step="1"
                                        {disabled}
                                        bind:value={form.print_area[key]}
                                        onchange={settle}
                                    />
                                </div>
                            {/each}
                        </div>
                    {:else}
                        <button
                            type="button"
                            class="btn sm secondary"
                            {disabled}
                            onclick={() => (form.print_area = startingArea(form.mockup, form.print_size))}
                        >
                            <span class="txt">Mark the print area</span>
                        </button>
                    {/if}
                </div>
            </div>
        {/if}

        <h6 class="cz-heading">Print size</h6>
        <div class="cz-numbers">
            <div class="field">
                <label for="cz-width-mm">Width (mm)</label>
                <input id="cz-width-mm" type="number" inputmode="decimal" min="0" step="0.1" {disabled} bind:value={form.print_size.width_mm} />
            </div>
            <div class="field">
                <label for="cz-height-mm">Height (mm)</label>
                <input id="cz-height-mm" type="number" inputmode="decimal" min="0" step="0.1" {disabled} bind:value={form.print_size.height_mm} />
            </div>
            <div class="field">
                <label for="cz-dpi">Resolution (dpi)</label>
                <input id="cz-dpi" type="number" inputmode="numeric" min="72" max="600" step="1" {disabled} bind:value={form.print_size.dpi} />
            </div>
        </div>
        <div class="field-help" aria-live="polite">
            {#if pixels}
                The print file is {pixels.width} × {pixels.height} pixels.
            {:else}
                The print file's size shows once both sides are given.
            {/if}
        </div>
        {#if gap !== null && gap > ASPECT_TOLERANCE && form.mockup}
            <div class="cz-mismatch" in:rise>
                <span class="txt-sm">The print area is a different shape from the print size.</span>
                <button type="button" class="btn sm secondary" {disabled} onclick={matchShape}>
                    <span class="txt">Match the print area</span>
                </button>
            </div>
        {/if}

        <h6 class="cz-heading">Tools</h6>
        <div class="cz-checks">
            {#each TOOLS as tool (tool.key)}
                <div class="field">
                    <input id="cz-tool-{tool.key}" type="checkbox" class="switch" {disabled} bind:checked={form.tools[tool.key]} />
                    <label for="cz-tool-{tool.key}">{tool.label}</label>
                </div>
            {/each}
        </div>

        {#if form.tools.text}
            <fieldset class="cz-group" in:rise>
                <legend class="cz-heading">Fonts for words</legend>
                <div class="cz-fonts">
                    {#each fonts as font (font.key)}
                        <label class="cz-font" class:chosen={form.fonts.includes(font.key)}>
                            <input
                                type="checkbox"
                                {disabled}
                                checked={form.fonts.includes(font.key)}
                                onchange={(e) => toggleFont(font.key, e.currentTarget.checked)}
                            />
                            <span>{font.family}</span>
                        </label>
                    {/each}
                </div>
            </fieldset>
        {/if}

        {#if form.tools.stickers}
            <div class="cz-group" in:rise>
                <h6 class="cz-heading">Stickers</h6>
                {#if form.sticker_media_ids.length}
                    <ul class="cz-stickers">
                        {#each form.sticker_media_ids as id (id)}
                            {@const item = stickerItems.get(id)}
                            <li in:rise>
                                {#if item}
                                    <img src={item.url} alt={item.alt || item.filename || ""} />
                                {:else}
                                    <i class="ri-image-line" aria-hidden="true"></i>
                                {/if}
                                <button
                                    type="button"
                                    class="btn circle xs transparent secondary"
                                    {disabled}
                                    aria-label="Remove sticker {item?.filename ?? id}"
                                    onclick={() => (form.sticker_media_ids = form.sticker_media_ids.filter((s) => s !== id))}
                                >
                                    <i class="ri-close-line" aria-hidden="true"></i>
                                </button>
                            </li>
                        {/each}
                    </ul>
                {/if}
                {#if pickable && !disabled}
                    <button
                        type="button"
                        class="btn sm secondary"
                        aria-expanded={picking}
                        onclick={() => (picking ? (picking = false) : openLibrary())}
                    >
                        <i class={picking ? "ri-close-line" : "ri-image-add-line"} aria-hidden="true"></i>
                        <span class="txt">{picking ? "Done" : "Choose stickers"}</span>
                    </button>
                {/if}
                <div class="field-help">Transparent PNGs work best. Up to {MAX_STICKERS}.</div>
                {#if picking}
                    <div class="cz-library" in:rise>
                        <MediaLibrary
                            items={library}
                            loading={libraryLoading}
                            bind:filters
                            bind:view
                            onfilter={openLibrary}
                            onadded={(fresh) => (library = [...fresh, ...library])}
                            chosen={(item) => form.sticker_media_ids.includes(item.id)}
                            onchoose={toggleSticker}
                            tileVerb="Choose"
                        />
                    </div>
                {/if}
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="cz-min-photo">Smallest photo (pixels)</label>
            <input id="cz-min-photo" type="number" inputmode="numeric" min="0" step="1" {disabled} bind:value={form.min_photo_px} />
        </div>
        <div class="field-help">A photo whose shorter side is smaller may print soft; the shopper is warned.</div>

        {#if dirty && (mockupProblem || problem)}
            <div class="field-help error m-t-sm" role="status" in:rise>{mockupProblem || problem}</div>
        {/if}
    {/if}
</section>

<style>
    .cz-pictures {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
        gap: 12px;
        margin-top: 12px;
    }
    .cz-editor {
        display: flex;
        flex-wrap: wrap;
        gap: 16px;
        margin-top: 16px;
    }
    .cz-stage {
        position: relative;
        width: min(100%, 22rem, calc(28rem * var(--w) / var(--h)));
        aspect-ratio: var(--w) / var(--h);
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        overflow: hidden;
    }
    .cz-layer {
        position: absolute;
        inset: 0;
        width: 100%;
        height: 100%;
        pointer-events: none;
    }
    .cz-svg {
        pointer-events: auto;
        cursor: crosshair;
        touch-action: none;
    }
    .cz-shade {
        fill: rgba(0, 0, 0, 0.45);
        pointer-events: none;
    }
    .cz-area {
        fill: transparent;
        stroke: #fff;
        stroke-width: 2;
        stroke-dasharray: 6 4;
        cursor: move;
        transition: stroke-width 0.15s ease;
    }
    .cz-area:hover,
    .dragging .cz-area {
        stroke-width: 3;
    }
    .cz-handle {
        fill: #fff;
        stroke: rgba(0, 0, 0, 0.6);
        stroke-width: 2;
        vector-effect: non-scaling-stroke;
        cursor: nwse-resize;
        transition: opacity 0.15s ease;
    }
    .cz-handle:hover {
        opacity: 0.85;
    }
    @media (prefers-reduced-motion: no-preference) {
        .cz-handle {
            transition:
                opacity 0.15s ease,
                transform 0.15s ease;
            transform-box: fill-box;
            transform-origin: center;
        }
        .cz-handle:hover {
            transform: scale(1.2);
        }
    }
    .cz-fields {
        flex: 1 1 16rem;
        min-width: 0;
    }
    .cz-numbers {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(7.5rem, 1fr));
        gap: 8px 12px;
    }
    .cz-numbers .field {
        margin: 0;
    }
    .cz-heading {
        margin: 18px 0 8px;
        font-size: var(--fontSize);
        font-weight: 600;
    }
    .cz-group {
        border: 0;
        margin: 18px 0 0;
        padding: 0;
        min-width: 0;
    }
    .cz-group > .cz-heading {
        margin-top: 0;
        padding: 0;
    }
    .cz-checks {
        display: flex;
        flex-wrap: wrap;
        gap: 6px 20px;
    }
    .cz-checks .field {
        width: auto;
        margin: 0;
    }
    .cz-mismatch {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px;
        margin-top: 8px;
        color: var(--warningColor);
    }
    .cz-fonts {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
    }
    .cz-font {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-height: 36px;
        padding: 0 12px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        cursor: pointer;
        transition:
            border-color 0.15s ease,
            background-color 0.15s ease;
    }
    .cz-font.chosen {
        border-color: var(--surfaceTxtColor);
        background: var(--surfaceAlt1Color);
    }
    .cz-font:focus-within {
        outline: 2px solid var(--primaryColor);
        outline-offset: 2px;
    }
    .cz-stickers {
        list-style: none;
        margin: 0 0 8px;
        padding: 0;
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
    }
    .cz-stickers li {
        position: relative;
        display: grid;
        place-items: center;
        width: 64px;
        height: 64px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
    }
    .cz-stickers img {
        max-width: 56px;
        max-height: 56px;
        object-fit: contain;
    }
    .cz-stickers button {
        position: absolute;
        top: -8px;
        right: -8px;
        background: var(--surfaceColor);
    }
    .cz-library {
        margin-top: 10px;
        padding: 10px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
    }
</style>
