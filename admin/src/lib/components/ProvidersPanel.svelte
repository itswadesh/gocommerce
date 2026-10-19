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
    import { activateNote } from "$lib/plugin-ready.js";
    import { providerIcon } from "$lib/provider-icons.js";
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
    const allowed = $derived(can("plugins.read"));

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
            // Green is for "on and working" alone; a built-in method is simply
            // there, so it reads neutral.
            if (p.module === "core") state = { label: "Built in", cls: "" };
            else if (p.configured) state = { label: "Active", cls: "label-success" };
            else if (plugin?.enabled) state = { label: "Needs settings", cls: "label-warning" };
            else state = { label: "Inactive", cls: "" };
            return { ...p, plugin, state, icon: providerIcon(p.code, kind) };
        }),
    );
    const activeCount = $derived(cards.filter((c) => c.configured).length);
    /*
     * In use first, the rest folded. Eleven equal cards made the one way a
     * store takes money as loud as ten it does not; what is on — built in,
     * set up in code, active, or switched on and waiting for its key — is
     * what this screen is about, and the others are a list to pick from.
     */
    const inUse = $derived(cards.filter((c) => c.module === "core" || c.configured || c.plugin?.enabled));
    const idle = $derived(cards.filter((c) => !inUse.includes(c)));
    let idleOpen = $state(false);

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

    /*
     * Set up: a gateway that still needs its keys can't be switched on (the
     * engine refuses), so its button opens the settings instead, and saving
     * them switches it on — one step for the owner, not "fill in, close,
     * find Activate".
     */
    let settingUp = $state("");
    function setUp(card) {
        settingUp = card.plugin.key;
        editing = card.plugin;
    }

    async function saved(updated) {
        plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
        editing = null;
        if (settingUp === updated.key && !updated.enabled && !activateNote(updated)) {
            settingUp = "";
            await toggle({ plugin: updated });
            return;
        }
        settingUp = "";
        await load();
    }
</script>

<svelte:head><title>{words.title} · GoCommerce</title></svelte:head>

<!-- The provider's name is printed beside it, so the mark is decoration to a
     screen reader and is hidden from one. -->
{#snippet mark(card)}
    <span class="provider-mark" aria-hidden="true">
        {#if card.icon.path}
            <svg viewBox="0 0 24 24" class:wide={card.icon.wide}><path d={card.icon.path} /></svg>
        {:else}
            <i class={card.icon.glyph}></i>
        {/if}
    </span>
{/snippet}

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
            <NoAccess right="plugins.read" what={words.title.toLowerCase()} />
        {:else}
            <p class="txt-hint m-b-base">{words.blurb}</p>
            {#if !loading}
                <div class="txt-hint txt-sm m-b-base">{cards.length} {words.noun === "gateway" ? "payment methods" : "providers"}, {activeCount} active</div>
            {/if}
            <div class="card-grid provider-grid">
                {#each inUse as card (card.code)}
                    <!-- Green only when its label says Active: a built-in method is
                         configured by nature, and green would read as "turned on". -->
                    <section class="card provider-card" class:active={card.state.label === "Active"}>
                        <div class="flex gap-10">
                            {@render mark(card)}
                            <div class="flex-fill">
                                <div class="provider-title">{card.plugin?.title ?? card.name}</div>
                                <div class="txt-hint txt-sm">
                                    {card.module === "core" ? "Built in" : "Module " + card.module}
                                    <span class="txt-code">{card.code}</span>
                                </div>
                            </div>
                            <span class="label {card.state.cls}">{#if card.state.cls === "label-success"}<i class="ri-check-line state-tick" aria-hidden="true"></i>{/if}{card.state.label}</span>
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
            {#if idle.length}
                <details class="card map-fold provider-more" bind:open={idleOpen}>
                    <summary>
                        <span class="map-fold-title">Add a {words.noun === "gateway" ? "payment method" : "carrier"}</span>
                        <span class="txt-hint txt-sm">
                            {idle.length} more installed, switched off
                        </span>
                        <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                        {#if !idleOpen}
                            <span class="map-fold-values">
                                {#each idle as card (card.code)}
                                    <span class="map-fold-value">{card.plugin?.title ?? card.name}</span>
                                {/each}
                            </span>
                        {/if}
                    </summary>
                    <div class="map-fold-body provider-rows">
                        {#each idle as card (card.code)}
                            <div class="provider-row">
                                <div class="provider-row-main">
                                    {@render mark(card)}
                                    <div class="provider-row-text">
                                        <div class="provider-title">{card.plugin?.title ?? card.name}</div>
                                        <p class="provider-description">
                                            {card.plugin?.description ?? "Configured in code when the store was started, so it is switched on there."}
                                        </p>
                                    </div>
                                </div>
                                {#if card.plugin}
                                <div class="provider-row-actions">
                                    {#if activateNote(card.plugin)}
                                        <button
                                            type="button"
                                            class="btn sm"
                                            class:loading={working === card.plugin.key}
                                            disabled={working === card.plugin.key}
                                            title={activateNote(card.plugin)}
                                            onclick={() => setUp(card)}
                                        >
                                            <span class="txt">Set up</span>
                                        </button>
                                    {:else}
                                        <button
                                            type="button"
                                            class="btn sm"
                                            class:loading={working === card.plugin.key}
                                            disabled={working === card.plugin.key}
                                            onclick={() => toggle(card)}
                                        >
                                            <span class="txt">Activate</span>
                                        </button>
                                        <button type="button" class="btn sm transparent secondary" onclick={() => (editing = card.plugin)}>
                                            <i class="ri-settings-3-line" aria-hidden="true"></i>
                                            <span class="txt">Configure</span>
                                        </button>
                                    {/if}
                                    {#if card.plugin.docs}
                                        <a class="btn sm transparent secondary" href={card.plugin.docs} target="_blank" rel="noreferrer">
                                            <span class="txt">Docs</span>
                                        </a>
                                    {/if}
                                </div>
                                {/if}
                            </div>
                        {/each}
                    </div>
                </details>
            {/if}
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
    /*
     * One tile size on the cards and in the fold, so a provider looks the same
     * wherever it is listed. The tile is neutral in every state: the card's
     * edge and label say whether it is on, and a coloured tile would say it a
     * second time, or say it wrongly.
     */
    .provider-mark {
        flex: 0 0 auto;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 40px;
        height: 40px;
        border-radius: var(--borderRadius);
        border: 1px solid var(--surfaceAlt3Color);
        background: var(--surfaceAlt1Color);
        color: var(--txtPrimaryColor);
        font-size: 22px;
        line-height: 1;
    }
    .provider-mark svg {
        display: block;
        width: 20px;
        height: 20px;
        fill: currentColor;
    }
    .provider-mark svg.wide {
        width: 32px;
        height: 32px;
    }
    /*
     * A card that arrives fades in: on the first load, and when switching a
     * provider on or off moves it between the grid and the fold, which would
     * otherwise be a card that is simply somewhere else. Opening the fold
     * fades its rows in alongside the fold's own reveal. It is opacity alone,
     * nothing travels, so it stays under reduced motion too.
     */
    .provider-card,
    .provider-row {
        animation: fadeIn var(--animationSpeed) ease-out;
    }
    .provider-more {
        margin-top: var(--smSpacing);
    }
    /* One row each, not a card each: a list to pick from. */
    .provider-row {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px var(--smSpacing);
        padding: 10px 0;
        border-top: 1px solid var(--surfaceAlt2Color);
    }
    .provider-row:first-child {
        border-top: 0;
    }
    /* The tile and the words wrap as one, so a narrow screen never leaves a
       tile alone on a line above its name. */
    .provider-row-main {
        display: flex;
        align-items: flex-start;
        gap: var(--smSpacing);
        flex: 1 1 280px;
        min-width: 0;
    }
    .provider-row-text {
        flex: 1 1 auto;
        min-width: 0;
    }
    .provider-row-actions {
        display: flex;
        gap: 6px;
    }
    .provider-description {
        flex: 1 1 auto;
        margin: var(--xsSpacing) 0 0;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
    }
</style>
