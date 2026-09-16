<script>
    /**
     * A screen for one plugin: its switch, its settings, and the URLs it
     * serves. Feeds and Sitemap are this — a storefront extra whose whole
     * operation is "switch it on, tell it where the storefront lives, and
     * paste these addresses into Google" — and a nav item that lands on the
     * one card is kinder than a search on the Plugins screen.
     */
    import { can, request } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginSettingsDrawer from "$lib/components/PluginSettingsDrawer.svelte";

    /** @type {{ pluginKey: string, module: string, title: string, crumb?: string, blurb: string, links: {label: string, path: string, hint?: string}[] }} */
    let { pluginKey, module, title, crumb = "Content", blurb, links = [] } = $props();

    const allowed = $derived(can("plugins.read"));
    const missing = $derived(modulesKnown() && !hasModule(module));
    let plugin = $state(null);
    let loading = $state(true);
    let working = $state(false);
    let editing = $state(null);

    $effect(() => {
        pluginKey;
        if (allowed && hasModule(module)) load();
    });

    async function load() {
        loading = true;
        try {
            plugin = await request("GET", `/api/admin/plugins/${pluginKey}`);
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function toggle() {
        if (!plugin) return;
        working = true;
        try {
            plugin = await request("PATCH", `/api/admin/plugins/${pluginKey}`, { body: { enabled: !plugin.enabled } });
            toast.success(plugin.enabled ? `${plugin.title} activated` : `${plugin.title} deactivated`);
            if (plugin.enabled && !plugin.configured) editing = plugin;
        } catch (err) {
            toast.error(err);
        } finally {
            working = false;
        }
    }

    const state = $derived.by(() => {
        if (!plugin) return { label: "", cls: "" };
        if (!plugin.enabled) return { label: "Inactive", cls: "" };
        if (!plugin.configured) return { label: "Needs settings", cls: "label-warning" };
        return { label: "Active", cls: "label-success" };
    });
    const origin = $derived(typeof window === "undefined" ? "" : window.location.origin);

    async function copy(url) {
        try {
            await navigator.clipboard.writeText(url);
            toast.success("Copied");
        } catch {
            toast.error("Could not copy; select it and copy by hand.");
        }
    }
</script>

<svelte:head><title>{title} · GoCommerce</title></svelte:head>

<div class="page page-plugin-{pluginKey} shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">{crumb}</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{title}</div>
            </nav>
        </header>

        {#if !allowed}
            <NoAccess right="plugins.read" what={title.toLowerCase()} />
        {:else if missing}
            <ModuleMissing {module} what="{title} is served by ext/{module}, and this binary does not have it." />
        {:else}
            <p class="txt-hint m-b-base">{blurb}</p>
            {#if plugin}
                <section class="card plugin-one" class:active={plugin.enabled && plugin.configured}>
                    <div class="flex gap-10">
                        <div class="flex-fill">
                            <div class="plugin-title">{plugin.title}</div>
                            <div class="txt-hint txt-sm">Module {plugin.module}</div>
                        </div>
                        <span class="label {state.cls}">{state.label}</span>
                    </div>
                    <p class="plugin-description">{plugin.description}</p>
                    <div class="flex gap-10 m-t-sm">
                        <button type="button" class="btn sm {plugin.enabled ? 'secondary' : ''}" class:loading={working} disabled={working} onclick={toggle}>
                            <span class="txt">{plugin.enabled ? "Deactivate" : "Activate"}</span>
                        </button>
                        <button type="button" class="btn sm transparent secondary" onclick={() => (editing = plugin)}>
                            <i class="ri-settings-3-line" aria-hidden="true"></i>
                            <span class="txt">Settings</span>
                        </button>
                        {#if plugin.docs}
                            <a class="btn sm transparent secondary m-l-auto" href={plugin.docs} target="_blank" rel="noreferrer"><span class="txt">Docs</span></a>
                        {/if}
                    </div>
                </section>

                {#if links.length}
                    <!-- A list rather than a table.
                         As a table the first cell carried `min-width`, which
                         in table.css means "shrink to the content" — so the
                         label column collapsed to about fourteen pixels and
                         "Google Merchant" wrapped one letter per line. Two or
                         three addresses are not tabular data anyway: each is a
                         name, an address and a button. -->
                    <h2 class="plugin-links-title">Addresses</h2>
                    <div class="plugin-links">
                        {#each links as link (link.path)}
                            <div class="plugin-link">
                                <div class="plugin-link-name">
                                    <span class="txt-bold">{link.label}</span>
                                    {#if link.hint}<span class="txt-hint txt-sm">{link.hint}</span>{/if}
                                </div>
                                <a
                                    class="plugin-link-url txt-code"
                                    href={origin + link.path}
                                    target="_blank"
                                    rel="noreferrer">{origin}{link.path}</a
                                >
                                <button
                                    type="button"
                                    class="btn sm secondary plugin-link-copy"
                                    onclick={() => copy(origin + link.path)}
                                >
                                    <i class="ri-file-copy-line" aria-hidden="true"></i>
                                    <span class="txt">Copy</span>
                                </button>
                            </div>
                        {/each}
                    </div>
                    {#if !plugin.enabled || !plugin.configured}
                        <p class="txt-hint txt-sm m-t-sm">
                            These answer 404 until the plugin is active and set up.
                        </p>
                    {/if}
                {/if}
            {:else if loading}
                <div class="txt-hint p-base">Loading…</div>
            {/if}
        {/if}
    </div>
</div>

<PluginSettingsDrawer plugin={editing} onclose={() => (editing = null)} onsaved={(u) => ((plugin = u), (editing = null))} />

<style>
    .plugin-one {
        margin: 0;
        max-width: 640px;
    }
    .plugin-one.active {
        border-color: var(--successColor);
        background: rgba(34, 169, 109, 0.07);
    }
    .plugin-title {
        font-weight: 600;
    }
    .plugin-description {
        /* Literals, not --xsSpacing and --txtHintColor: neither of those is
           defined anywhere in the panel, so this had no top margin and
           inherited the primary text colour instead of the hint. */
        margin: 6px 0 0;
        color: var(--surfaceTxtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
    }

    /* The addresses sit in the same column as the card above them, so the page
       reads as one thing rather than a 640px card beside a full-width table. */
    .plugin-links-title {
        max-width: 640px;
        margin: var(--smSpacing) 0 8px;
        font-size: var(--smFontSize);
        font-weight: 600;
    }
    .plugin-links {
        max-width: 640px;
        border: 1px solid var(--surfaceAlt3Color);
        border-radius: var(--borderRadius);
        overflow: hidden;
    }
    .plugin-link {
        display: grid;
        /* The name takes what it needs and no less; the address takes the
           rest and wraps inside its own cell rather than forcing the row. */
        grid-template-columns: minmax(150px, auto) 1fr auto;
        align-items: center;
        gap: 12px;
        padding: 10px 12px;
        border-bottom: 1px solid var(--surfaceAlt2Color);
    }
    .plugin-link:last-child {
        border-bottom: 0;
    }
    .plugin-link-name {
        display: flex;
        flex-direction: column;
        gap: 1px;
        min-width: 0;
    }
    .plugin-link-url {
        min-width: 0;
        word-break: break-all;
        font-size: var(--smFontSize);
    }

    @media (max-width: 700px) {
        /* Three columns will not hold on a phone: the name goes on its own
           line, the address under it, the button beside the address. */
        .plugin-link {
            grid-template-columns: 1fr auto;
        }
        .plugin-link-name {
            grid-column: 1 / -1;
        }
    }
</style>
