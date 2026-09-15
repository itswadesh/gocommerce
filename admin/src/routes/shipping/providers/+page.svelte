<script>
    /**
     * Shipping providers: who carries the parcels.
     *
     * The engine reports every fulfilment provider registered on this store —
     * the built-in manual one, and each carrier module — and a module that
     * registered a plugin is configured from here, the way a delivery backend
     * is under Notifications › Setup Email. Litekart keeps this screen beside
     * Shipping Settings; so does this one.
     */
    import { can, request } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginSettingsDrawer from "$lib/components/PluginSettingsDrawer.svelte";

    const allowed = $derived(can("store.operate"));
    let loading = $state(true);
    let providers = $state([]);
    let plugins = $state([]);
    let working = $state("");
    let editing = $state(null);

    $effect(() => {
        if (allowed) load();
    });

    async function load() {
        loading = true;
        try {
            const [settings, all] = await Promise.all([
                request("GET", "/api/admin/settings"),
                request("GET", "/api/admin/plugins"),
            ]);
            providers = settings.fulfillment_providers ?? [];
            plugins = all ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const cards = $derived(
        providers.map((p) => {
            const plugin = p.module === "core" ? null : plugins.find((x) => x.module === p.module);
            let state = { label: "Active", cls: "label-success" };
            if (plugin && !plugin.enabled) state = { label: "Inactive", cls: "" };
            else if (plugin && !plugin.configured) state = { label: "Needs settings", cls: "label-warning" };
            return { ...p, plugin, state };
        }),
    );

    async function toggle(card) {
        const p = card.plugin;
        if (!p) return;
        working = p.key;
        try {
            const updated = await request("PATCH", `/api/admin/plugins/${p.key}`, { body: { enabled: !p.enabled } });
            plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
            toast.success(updated.enabled ? `${p.title} activated` : `${p.title} deactivated`);
            if (updated.enabled && !updated.configured) editing = updated;
        } catch (err) {
            toast.error(err);
        } finally {
            working = "";
        }
    }

    function saved(updated) {
        plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
        editing = null;
    }
</script>

<svelte:head><title>Shipping providers · GoCommerce</title></svelte:head>

<div class="page page-shipping-providers shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Shipping</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Shipping providers</div>
            </nav>
            <div class="inline-flex gap-sm">
                <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>
        </header>

        {#if !allowed}
            <NoAccess right="store.operate" what="shipping providers" />
        {:else}
            <p class="txt-hint m-b-base">
                Who carries the parcels. Manual fulfilment is built in — an operator marks the
                parcel shipped and types the tracking number — and a carrier module joins this
                list by registering a provider. What the shopper pays is under Shipping and
                delivery.
            </p>
            <div class="provider-grid">
                {#each cards as card (card.code)}
                    <section class="card provider-card">
                        <div class="flex gap-10">
                            <div class="flex-fill">
                                <div class="provider-title">{card.plugin?.title ?? card.name}</div>
                                <div class="txt-hint txt-sm">
                                    {card.module === "core" ? "Built in" : "Module " + card.module}
                                    <span class="txt-code">{card.code}</span>
                                </div>
                            </div>
                            <span class="label {card.state.cls}">{card.state.label}</span>
                        </div>
                        {#if card.plugin}
                            <p class="provider-description">{card.plugin.description}</p>
                            <div class="flex gap-10 m-t-sm">
                                <button
                                    type="button"
                                    class="btn sm {card.plugin.enabled ? 'secondary' : ''}"
                                    class:loading={working === card.plugin.key}
                                    disabled={working === card.plugin.key}
                                    onclick={() => toggle(card)}
                                >
                                    <span class="txt">{card.plugin.enabled ? "Deactivate" : "Activate"}</span>
                                </button>
                                <button type="button" class="btn sm transparent secondary" onclick={() => (editing = card.plugin)}>
                                    <i class="ri-settings-3-line" aria-hidden="true"></i>
                                    <span class="txt">Configure</span>
                                </button>
                            </div>
                        {:else if card.module !== "core"}
                            <p class="provider-description">Configured in code when the store was started.</p>
                        {:else}
                            <p class="provider-description">
                                Every order can be shipped by hand: a parcel, a carrier of your
                                choosing, and its tracking number typed on the order.
                            </p>
                        {/if}
                    </section>
                {/each}
                {#if loading && !cards.length}
                    <div class="txt-hint p-base">Loading…</div>
                {/if}
            </div>
            {#if !loading && cards.length <= 1}
                <p class="txt-hint txt-sm m-t-base">
                    No carrier module is installed in this binary. Build the store with one
                    (ext/fulfill-delhivery, for instance) and it appears here.
                </p>
            {/if}
        {/if}
    </div>
</div>

<PluginSettingsDrawer plugin={editing} onclose={() => (editing = null)} onsaved={saved} />

<style>
    .provider-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
        gap: var(--smSpacing);
    }
    .provider-card {
        display: flex;
        flex-direction: column;
        min-width: 0;
        margin: 0;
    }
    .provider-title {
        font-weight: 600;
    }
    .provider-description {
        flex: 1 1 auto;
        margin: var(--xsSpacing) 0 0;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
    }
</style>
