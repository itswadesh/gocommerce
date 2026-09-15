<script>
    /**
     * Payment methods and Shipping providers: who this store can charge
     * through and who can carry its parcels, each a card with a switch.
     *
     * The engine reports every provider the build carries — ready or not —
     * and a module that registered a plugin is set up from here: Activate,
     * type the key, done, no restart. A provider wired entirely in code shows
     * as active with nothing to configure; the built-in ones (cash on
     * delivery, manual fulfilment) are always there. This is Litekart's
     * Payment Methods and Shipping Providers screens, with the engine
     * telling the truth about which ones can actually be used (D59).
     */
    import { can, request } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginSettingsDrawer from "$lib/components/PluginSettingsDrawer.svelte";

    /** @type {{ kind: "payments" | "shipping" }} */
    let { kind } = $props();

    const WORDS = {
        payments: {
            crumb: "Settings",
            title: "Payment methods",
            blurb: "How shoppers pay. A gateway that is active and set up is offered at checkout; an idle one is listed here and nowhere else. Cash on delivery is built in.",
            listKey: "payment_methods",
            category: "payments",
            builtIn: "Cash on delivery is built in: the order is confirmed and the money is collected at the door.",
            none: "No gateway module is installed in this binary. Build the store with -gateways and they appear here.",
            noun: "gateway",
        },
        shipping: {
            crumb: "Shipping",
            title: "Shipping providers",
            blurb: "Who carries the parcels. An aggregator that is active and set up is offered on the ship dialog; an idle one is listed here and nowhere else. Manual fulfilment is built in.",
            listKey: "fulfillment_providers",
            category: "shipping",
            builtIn: "Every order can be shipped by hand: a parcel, a carrier of your choosing, and its tracking number typed on the order.",
            none: "No carrier module is installed in this binary. Build the store with -carriers and they appear here.",
            noun: "carrier",
        },
    };
    const words = $derived(WORDS[kind]);
    const allowed = $derived(can("store.operate"));

    let loading = $state(true);
    let providers = $state([]);
    let plugins = $state([]);
    let working = $state("");
    let editing = $state(null);

    $effect(() => {
        kind;
        if (allowed) load();
    });

    async function load() {
        loading = true;
        try {
            const [settings, all] = await Promise.all([
                request("GET", "/api/admin/settings"),
                request("GET", "/api/admin/plugins"),
            ]);
            providers = settings[words.listKey] ?? [];
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
            let state;
            if (p.module === "core") state = { label: "Built in", cls: "label-success" };
            else if (p.configured) state = { label: "Active", cls: "label-success" };
            else if (plugin?.enabled) state = { label: "Needs settings", cls: "label-warning" };
            else state = { label: "Inactive", cls: "" };
            return { ...p, plugin, state };
        }),
    );
    const activeCount = $derived(cards.filter((c) => c.configured).length);

    async function toggle(card) {
        const p = card.plugin;
        if (!p) return;
        working = p.key;
        try {
            const updated = await request("PATCH", `/api/admin/plugins/${p.key}`, { body: { enabled: !p.enabled } });
            plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
            toast.success(updated.enabled ? `${p.title} activated` : `${p.title} deactivated`);
            if (updated.enabled && !updated.configured) editing = updated;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            working = "";
        }
    }

    async function saved(updated) {
        plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
        editing = null;
        await load();
    }
</script>

<svelte:head><title>{words.title} · GoCommerce</title></svelte:head>

<div class="page page-providers page-providers-{kind} shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">{words.crumb}</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{words.title}</div>
            </nav>
            <div class="inline-flex gap-sm">
                <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>
        </header>

        {#if !allowed}
            <NoAccess right="store.operate" what={words.title.toLowerCase()} />
        {:else}
            <p class="txt-hint m-b-base">{words.blurb}</p>
            {#if !loading}
                <div class="txt-hint txt-sm m-b-base">{cards.length} {words.noun === "gateway" ? "payment methods" : "providers"}, {activeCount} active</div>
            {/if}
            <div class="card-grid provider-grid">
                {#each cards as card (card.code)}
                    <section class="card provider-card" class:active={card.configured}>
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
                                {#if card.plugin.docs}
                                    <a class="btn sm transparent secondary m-l-auto" href={card.plugin.docs} target="_blank" rel="noreferrer">
                                        <span class="txt">Docs</span>
                                    </a>
                                {/if}
                            </div>
                        {:else if card.module !== "core"}
                            <p class="provider-description">Configured in code when the store was started.</p>
                        {:else}
                            <p class="provider-description">{words.builtIn}</p>
                        {/if}
                    </section>
                {/each}
                {#if loading && !cards.length}
                    <div class="txt-hint p-base">Loading…</div>
                {/if}
            </div>
            {#if !loading && cards.length <= 1}
                <p class="txt-hint txt-sm m-t-base">{words.none}</p>
            {/if}
        {/if}
    </div>
</div>

<PluginSettingsDrawer plugin={editing} onclose={() => (editing = null)} onsaved={saved} />

<style>
    .provider-card {
        display: flex;
        flex-direction: column;
        min-width: 0;
        margin: 0;
    }
    /* Active reads at a glance: a green edge and a whisper of green ground,
       the same treatment the Plugins screen gives a switched-on card. */
    .provider-card.active {
        border-color: var(--successColor);
        background: rgba(34, 169, 109, 0.07);
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
