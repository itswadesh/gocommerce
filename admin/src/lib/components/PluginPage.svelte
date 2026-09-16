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
                    <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Addresses</h2>
                    <div class="page-table-wrapper tw:rounded-xl tw:border">
                        <table class="table">
                            <tbody>
                                {#each links as link (link.path)}
                                    <tr>
                                        <td class="col-field-name-id min-width">
                                            <div class="row-name row-name-stacked">
                                                <span class="txt-bold">{link.label}</span>
                                                {#if link.hint}<span class="txt-hint txt-sm">{link.hint}</span>{/if}
                                            </div>
                                        </td>
                                        <td><a class="txt-code" href={origin + link.path} target="_blank" rel="noreferrer">{origin}{link.path}</a></td>
                                        <td class="min-width txt-right">
                                            <button type="button" class="btn sm secondary" onclick={() => copy(origin + link.path)}>
                                                <i class="ri-file-copy-line" aria-hidden="true"></i>
                                                <span class="txt">Copy</span>
                                            </button>
                                        </td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                    {#if !plugin.enabled || !plugin.configured}
                        <p class="txt-hint txt-sm m-t-sm">These answer once the plugin is active and set up.</p>
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
        margin: var(--xsSpacing) 0 0;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
    }
</style>
