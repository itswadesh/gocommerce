<script>
    /**
     * Plugins: the features an operator switches on from here rather than
     * from a config file. A top-level screen rather than a settings page,
     * because switching a feature on is something a store does often and
     * wants to find at a glance, the way Litekart and WooCommerce place it.
     *
     * Everything on this screen is compiled into the binary already — a
     * built-in storefront feature that is only settings, or a module that
     * registered its plugin. The screen decides whether each one runs and
     * with what. Nothing is downloaded, which is the whole reason a
     * store.operate right is enough to use it.
     *
     * A card's state is three things at once: on or off, configured or not
     * (every required field filled), and where it came from. All three are
     * shown, because "enabled but not configured" is the state that sends an
     * operator here in the first place.
     */
    import { can, request } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginSettingsDrawer from "$lib/components/PluginSettingsDrawer.svelte";

    const allowed = $derived(can("store.operate"));

    let plugins = $state([]);
    let loading = $state(true);
    let search = $state("");
    let activeOnly = $state(false);
    let working = $state("");

    // The settings drawer's plugin; the drawer itself is shared with the
    // Setup Email and Setup SMS screens.
    let editing = $state(null);

    $effect(() => {
        if (allowed) load();
    });

    async function load() {
        loading = true;
        try {
            plugins = await request("GET", "/api/admin/plugins");
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const CATEGORY_LABEL = {
        storefront: "Storefront",
        marketing: "Marketing",
        analytics: "Analytics",
        chat: "Chat",
        search: "Search",
        notifications: "Email and SMS",
        operations: "Operations",
        integration: "Integrations",
    };
    const CATEGORY_ORDER = ["storefront", "marketing", "search", "notifications", "integration", "analytics", "chat", "operations"];

    const shown = $derived.by(() => {
        const needle = search.trim().toLowerCase();
        return plugins.filter((p) => {
            if (activeOnly && !p.enabled) return false;
            if (!needle) return true;
            return (p.title + " " + p.description + " " + p.key).toLowerCase().includes(needle);
        });
    });
    const groups = $derived.by(() => {
        const byCategory = new Map();
        for (const p of shown) {
            const key = p.category || "integration";
            if (!byCategory.has(key)) byCategory.set(key, []);
            byCategory.get(key).push(p);
        }
        return [...byCategory.entries()].sort(
            ([a], [b]) => CATEGORY_ORDER.indexOf(a) - CATEGORY_ORDER.indexOf(b),
        );
    });
    const enabledCount = $derived(plugins.filter((p) => p.enabled).length);

    function stateOf(p) {
        if (!p.enabled) return { label: "Off", cls: "" };
        if (!p.configured) return { label: "Needs settings", cls: "label-warning" };
        return { label: "Enabled", cls: "label-success" };
    }

    async function toggle(p) {
        working = p.key;
        try {
            const updated = await request("PATCH", `/api/admin/plugins/${p.key}`, {
                body: { enabled: !p.enabled },
            });
            plugins = plugins.map((x) => (x.key === p.key ? updated : x));
            toast.success(updated.enabled ? `${p.title} enabled` : `${p.title} disabled`);
            // Switching on something that still needs a value opens the form
            // rather than leaving a warning chip for the operator to notice.
            if (updated.enabled && !updated.configured && p.fields?.length) open(updated);
        } catch (err) {
            toast.error(err);
        } finally {
            working = "";
        }
    }

    function open(p) {
        editing = p;
    }

    function saved(updated) {
        plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
        editing = null;
    }
</script>

<svelte:head><title>Plugins · GoCommerce</title></svelte:head>

<div class="page page-plugins shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Plugins</div>
            </nav>
            {#if allowed}
                <!-- The panel's own search bar, the one every list screen has,
                     rather than a bare input: same magnifier, same clear. -->
                <form class="fields searchbar" onsubmit={(e) => e.preventDefault()}>
                    <div class="field">
                        <input
                            id="plugin-search"
                            type="text"
                            class="p-l-20"
                            placeholder="Find a plugin"
                            bind:value={search}
                        />
                    </div>
                    {#if search}
                        <div class="field addon p-r-5">
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => (search = "")}>
                                Clear
                            </button>
                        </div>
                    {/if}
                </form>
                <div class="page-header-primary-btns">
                    <div class="field">
                        <input type="checkbox" id="active-only" class="switch" bind:checked={activeOnly} />
                        <label for="active-only">Enabled only</label>
                    </div>
                </div>
            {/if}
        </header>

        {#if !allowed}
            <NoAccess right="store.operate" />
        {:else}
            <div class="m-b-base">
                <div class="field-help m-b-base">
                    Features this store can switch on without touching code: storefront extras,
                    chat and analytics widgets, search, marketing. Everything here is already in
                    the binary — a switch decides whether it runs, and its settings decide how. A
                    storefront reads the enabled ones from <code>GET /api/plugins</code>.
                </div>

                <div class="txt-hint txt-sm m-b-base">
                    {plugins.length} plugins, {enabledCount} enabled{#if search || activeOnly}, {shown.length} shown{/if}
                </div>

                {#if loading}
                    <div class="txt-hint p-base">Loading…</div>
                {:else if !shown.length}
                    <div class="txt-hint p-base">Nothing matches.</div>
                {:else}
                    {#each groups as [category, items] (category)}
                        <h6 class="section-title">{CATEGORY_LABEL[category] ?? category}</h6>
                        <div class="plugin-grid m-b-base">
                            {#each items as p (p.key)}
                                {@const st = stateOf(p)}
                                <section class="card plugin-card">
                                    <div class="flex gap-10">
                                        <div class="flex-fill">
                                            <div class="plugin-title">{p.title}</div>
                                            <div class="txt-hint txt-sm">
                                                {p.builtin ? "Built in" : `Module: ${p.module}`}
                                            </div>
                                        </div>
                                        <span class="label {st.cls}">{st.label}</span>
                                    </div>
                                    <p class="plugin-description">{p.description}</p>
                                    <div class="flex gap-10 m-t-sm">
                                        <button
                                            type="button"
                                            class="btn sm {p.enabled ? 'secondary' : ''}"
                                            class:loading={working === p.key}
                                            disabled={working === p.key}
                                            onclick={() => toggle(p)}
                                        >
                                            <span class="txt">{p.enabled ? "Deactivate" : "Activate"}</span>
                                        </button>
                                        {#if p.fields?.length}
                                            <button type="button" class="btn sm transparent secondary" onclick={() => open(p)}>
                                                <i class="ri-settings-3-line" aria-hidden="true"></i>
                                                <span class="txt">Settings</span>
                                            </button>
                                        {/if}
                                        {#if p.docs}
                                            <a class="btn sm transparent secondary m-l-auto" href={p.docs} target="_blank" rel="noreferrer">
                                                <span class="txt">Docs</span>
                                            </a>
                                        {/if}
                                    </div>
                                </section>
                            {/each}
                        </div>
                    {/each}
                {/if}
            </div>
        {/if}

    </div>
</div>

<PluginSettingsDrawer plugin={editing} onclose={() => (editing = null)} onsaved={saved} />

<style>
    .plugin-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
        gap: var(--smSpacing);
    }
    .plugin-card {
        display: flex;
        flex-direction: column;
        min-width: 0;
        margin: 0;
    }
    .plugin-title {
        font-weight: 600;
    }
    .plugin-description {
        flex: 1 1 auto;
        margin: var(--xsSpacing) 0 0;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
    }
</style>
