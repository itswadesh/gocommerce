<script>
    /**
     * Themes (ext/layouts, D125, D129): the store's theme and the elements of
     * it the shop arranges — the homepage, the templates every product and
     * every collection is drawn in, and the header and footer every page is
     * drawn between (D131). Each opens the same editor, so a page
     * added to the engine's registry joins this list rather than the menu.
     * Theme settings — colours, fonts, corners (D127) — open in the
     * homepage's editor, since they publish with the home page.
     *
     * Two elements are not pages of the editor: the banners (ext/banners) and
     * the storefront extras (hello bar, ticker, badges, share buttons), which
     * are part of how the store looks rather than add-ons (D132). They are
     * listed here whether or not this build has the editor.
     */
    import { base } from "$app/paths";
    import { can, layouts as api } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { themeSettingsSummary, PAGES } from "$lib/layouts.js";
    import { rightLabel } from "$lib/rights.js";
    import FeatureSwitch from "$lib/components/FeatureSwitch.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const missing = $derived(modulesKnown() && !hasModule("layouts"));
    const readable = $derived(can("layouts.read"));
    const editor = $derived(!missing && readable);
    const showBanners = $derived(hasModule("banners") && can("banners.read"));
    const showExtras = $derived(can("plugins.read"));

    let themes = $state([]);
    let states = $state({}); // each page's state, by its key
    // The theme editor's switch, kept current by its card.
    let editorPlugin = $state(null);
    const pluginOn = $derived(editorPlugin ? !!editorPlugin.enabled : true);
    let loading = $state(true);

    const home = $derived(states.home ?? null);
    const theme = $derived(themes.find((t) => t.id === (home?.document?.theme || "default")) ?? themes[0] ?? null);

    async function load() {
        loading = true;
        try {
            const [reg, ...pages] = await Promise.all([
                api.registry(),
                // A page an older engine does not know answers 404; the list
                // still shows the ones it does.
                ...PAGES.map((p) => api.get(p.key).catch(() => null)),
            ]);
            themes = reg?.themes ?? [];
            states = Object.fromEntries(PAGES.map((p, i) => [p.key, pages[i]]));
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        if (editor) load();
        else loading = false;
    });

    function when(at) {
        return at ? new Date(at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : "";
    }

    function whenTime(at) {
        return new Date(at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
    }
</script>

<svelte:head><title>{titled("Themes")}</title></svelte:head>

<div class="page page-themes shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Content</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Themes</h1>
            </nav>
        </header>

        {#if !editor && !showBanners && !showExtras}
            {#if missing}
                <ModuleMissing module="layouts" what="Themes are ext/layouts, and this binary does not have it. Start it with -layouts." />
            {:else}
                <NoAccess right="layouts.read" what="themes" />
            {/if}
        {:else if editor && loading && !home}
            <div class="th-wrap"><span class="skeleton-loader"></span></div>
        {:else}
            <div class="th-wrap" in:rise>
                <section class="th-card">
                    {#if editor}
                        <div class="th-current">
                            <span class="txt-hint txt-sm">Current theme</span>
                            <h2 class="th-name">{theme?.label ?? "Default"}</h2>
                        </div>

                        {#if !pluginOn}
                            <p class="txt-hint txt-sm th-off">
                                The theme editor is switched off, so the storefront shows its built-in pages.
                            </p>
                        {/if}
                    {/if}

                    {#if !editor && !missing}
                        <p class="txt-hint txt-sm th-off">
                            The homepage, header, footer and page templates aren't listed: your role doesn't include
                            <strong>{rightLabel("layouts.read")}</strong>.
                        </p>
                    {/if}

                    <h3 class="th-subhead txt-sm">Elements</h3>
                    <ul class="th-list">
                        {#if editor}
                            <li>
                                <a class="th-row" href="{base}/dash/themes/homepage?panel=theme">
                                    <i class="ri-palette-line th-icon" aria-hidden="true"></i>
                                    <span class="th-row-text">
                                        <span class="th-row-title">Theme settings</span>
                                        <span class="txt-hint txt-sm">{themeSettingsSummary(home?.document?.theme_settings)}</span>
                                    </span>
                                    <i class="ri-arrow-right-s-line th-chevron" aria-hidden="true"></i>
                                </a>
                            </li>
                            {#each PAGES.filter((p) => states[p.key]) as p (p.key)}
                                {@const s = states[p.key]}
                                <li>
                                    <a class="th-row" href="{base}/dash/themes/{p.route}">
                                        <i class="{p.icon} th-icon" aria-hidden="true"></i>
                                        <span class="th-row-text">
                                            <span class="th-row-title">{p.title}</span>
                                            <span class="txt-hint txt-sm">
                                                {#if s.published}
                                                    Published, version {s.published.number}, {when(s.published.published_at)}{#if s.changed}{" · unpublished changes"}{/if}
                                                {:else if s.saved}
                                                    Draft saved, not published
                                                {:else}
                                                    Built-in {p.noun}
                                                {/if}
                                            </span>
                                            {#if s.scheduled}
                                                <span class="th-scheduled txt-sm">
                                                    <i class="ri-calendar-schedule-line" aria-hidden="true"></i>
                                                    Publishes {whenTime(s.scheduled.publish_at)}
                                                </span>
                                            {/if}
                                        </span>
                                        <i class="ri-arrow-right-s-line th-chevron" aria-hidden="true"></i>
                                    </a>
                                </li>
                            {/each}
                        {/if}
                        {#if showBanners}
                            <li>
                                <a class="th-row" href="{base}/dash/themes/banners">
                                    <i class="ri-image-2-line th-icon" aria-hidden="true"></i>
                                    <span class="th-row-text">
                                        <span class="th-row-title">Banners</span>
                                        <span class="txt-hint txt-sm">Slides across the top of the home page, and grids of pictures under a heading</span>
                                    </span>
                                    <i class="ri-arrow-right-s-line th-chevron" aria-hidden="true"></i>
                                </a>
                            </li>
                        {/if}
                        {#if showExtras}
                            <li>
                                <a class="th-row" href="{base}/dash/themes/extras">
                                    <i class="ri-magic-line th-icon" aria-hidden="true"></i>
                                    <span class="th-row-text">
                                        <span class="th-row-title">Storefront extras</span>
                                        <span class="txt-hint txt-sm">Hello bar, news ticker, trust badges and share buttons</span>
                                    </span>
                                    <i class="ri-arrow-right-s-line th-chevron" aria-hidden="true"></i>
                                </a>
                            </li>
                        {/if}
                    </ul>
                </section>

                {#if editor}
                    <div class="th-switch">
                        <FeatureSwitch pluginKey="layouts" bind:plugin={editorPlugin} />
                    </div>
                {/if}
            </div>
        {/if}
    </div>
</div>

<style>
    .th-wrap {
        max-width: 860px;
    }
    .th-card {
        background: var(--cardFace);
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        padding: 16px;
    }
    .th-name {
        margin: 2px 0 0;
        font-size: 1.125rem;
        font-weight: 600;
    }
    .th-switch {
        margin-top: 16px;
    }
    .th-off {
        margin: 8px 0 0;
    }
    .th-subhead {
        margin: 16px 0 6px;
        font-weight: 600;
        color: var(--surfaceTxtHintColor);
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .th-list {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .th-row {
        display: flex;
        align-items: center;
        gap: 12px;
        min-height: 56px;
        padding: 8px 10px;
        margin: 0 -10px;
        border-radius: var(--borderRadius);
        color: inherit;
        text-decoration: none;
        transition:
            background-color var(--activeAnimationSpeed) ease-out,
            transform var(--activeAnimationSpeed) ease-out;
    }
    .th-row:hover,
    .th-row:focus-visible {
        background: var(--surfaceAlt1Color);
    }
    .th-row:active {
        transform: scale(0.99);
    }
    .th-icon {
        font-size: 1.25rem;
        color: var(--surfaceTxtHintColor);
    }
    .th-row-text {
        display: flex;
        flex-direction: column;
        flex: 1 1 auto;
        min-width: 0;
    }
    .th-row-title {
        font-weight: 600;
    }
    .th-scheduled {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        color: var(--infoColor, var(--txtPrimaryColor));
    }
    .th-chevron {
        color: var(--surfaceTxtHintColor);
        transition: transform var(--activeAnimationSpeed) ease-out;
    }
    .th-row:hover .th-chevron {
        transform: translateX(2px);
    }
    @media (prefers-reduced-motion: reduce) {
        .th-row,
        .th-chevron {
            transition: none;
        }
        .th-row:active,
        .th-row:hover .th-chevron {
            transform: none;
        }
    }
</style>
