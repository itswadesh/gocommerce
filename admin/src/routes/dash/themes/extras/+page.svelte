<script>
    /**
     * Storefront extras: the small things a storefront draws around its pages
     * — a bar across the top, a ticker, badges under Add to cart, share
     * buttons. Each is part of the product rather than an add-on (D132), so it
     * is set up here, under Themes, beside the rest of how the store looks.
     *
     * They are still the same switches and settings they were on the Plugins
     * screen, read by the storefront from GET /api/plugins, so a store that
     * uses one today sees no change.
     */
    import { base } from "$app/paths";
    import { can } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import FeatureSwitch from "$lib/components/FeatureSwitch.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import { hasModule } from "$lib/modules.svelte.js";
    import { titled } from "$lib/brand.svelte.js";

    const readable = $derived(can("plugins.read"));
    const EXTRAS = ["hello-bar", "news-ticker", "trust-badges", "social-sharing", "sold-count", "cart-suggestions"];
</script>

<svelte:head><title>{titled("Storefront extras")}</title></svelte:head>

<div class="page page-extras shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <a class="breadcrumb-item tw:text-sm" href="{base}/dash/themes">Themes</a>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Storefront extras</h1>
            </nav>
        </header>

        {#if !readable}
            <NoAccess right="plugins.read" what="the storefront extras" />
        {:else}
            <div class="ex-wrap" in:rise>
                <p class="txt-hint m-b-base">
                    Small things the storefront shows around its pages. Each is off until it is
                    switched on here.
                </p>
                {#if hasModule("layouts")}
                    <p class="field-help m-b-base">
                        The <a href="{base}/dash/themes/header">Header</a> page has an announcement
                        strip of its own, which the theme editor previews. The hello bar does the same
                        job, so a store usually needs only one of them.
                    </p>
                {/if}
                {#each EXTRAS as key (key)}
                    <FeatureSwitch pluginKey={key} />
                {/each}
            </div>
        {/if}
    </div>
</div>

<style>
    .ex-wrap {
        max-width: 860px;
    }
</style>
