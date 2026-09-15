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
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const allowed = $derived(can("store.operate"));

    let plugins = $state([]);
    let loading = $state(true);
    let search = $state("");
    let activeOnly = $state(false);
    let working = $state("");

    // The settings drawer: the plugin being edited and a copy of its
    // settings to type into, so Cancel really cancels.
    let editing = $state(null);
    let form = $state({});
    let saving = $state(false);

    const SECRET_MASK = "••••••••";

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
        operations: "Operations",
        integration: "Integrations",
    };
    const CATEGORY_ORDER = ["storefront", "marketing", "search", "integration", "analytics", "chat", "operations"];

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
        const draft = {};
        for (const f of p.fields ?? []) {
            const v = p.settings?.[f.key];
            draft[f.key] = f.kind === "bool" ? !!v : (v ?? f.default ?? "");
        }
        form = draft;
    }

    async function save() {
        if (!editing) return;
        saving = true;
        try {
            const settings = {};
            for (const f of editing.fields ?? []) {
                let v = form[f.key];
                if (f.kind === "number") v = v === "" || v === null ? "" : Number(v);
                settings[f.key] = v;
            }
            const updated = await request("PATCH", `/api/admin/plugins/${editing.key}`, {
                body: { settings },
            });
            plugins = plugins.map((x) => (x.key === editing.key ? updated : x));
            toast.success(`${editing.title} saved`);
            editing = null;
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

<svelte:head><title>Plugins · GoCommerce</title></svelte:head>

<div class="page page-plugins shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Plugins</div>
            </nav>
            <div class="flex-fill"></div>
            <ThemeToggle />
        </header>

        {#if !allowed}
            <NoAccess right="store.operate" />
        {:else}
            <div class="wrapper m-b-base">
                <div class="field-help m-b-base">
                    Features this store can switch on without touching code: storefront extras,
                    chat and analytics widgets, search, marketing. Everything here is already in
                    the binary — a switch decides whether it runs, and its settings decide how. A
                    storefront reads the enabled ones from <code>GET /api/plugins</code>.
                </div>

                <div class="flex gap-10 flex-wrap m-b-base">
                    <div class="field" style="min-width: 260px">
                        <input
                            id="plugin-search"
                            type="search"
                            placeholder="Find a plugin…"
                            bind:value={search}
                        />
                    </div>
                    <div class="field">
                        <input type="checkbox" id="active-only" class="switch" bind:checked={activeOnly} />
                        <label for="active-only">Enabled only</label>
                    </div>
                    <div class="flex-fill"></div>
                    <div class="txt-hint" style="align-self: center">
                        {plugins.length} plugins, {enabledCount} enabled
                    </div>
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

<!--
    One drawer for every plugin's settings, built from the plugin's own field
    list: the engine says what each setting is called and what kind of value
    it takes, so a module adds a plugin without touching the panel.
-->
<Drawer open={!!editing} title={editing ? `${editing.title} settings` : "Settings"} size="sm" onclose={() => (editing = null)}>
    {#if editing}
        <div class="field-help m-b-base">{editing.description}</div>
        {#each editing.fields as f (f.key)}
            <div class="field m-t-sm">
                {#if f.kind === "bool"}
                    <input type="checkbox" id="pf-{f.key}" class="switch" bind:checked={form[f.key]} />
                    <label for="pf-{f.key}">{f.label}</label>
                {:else}
                    <label for="pf-{f.key}">
                        {f.label}{#if f.required}<span class="txt-danger"> *</span>{/if}
                    </label>
                    {#if f.kind === "textarea"}
                        <textarea id="pf-{f.key}" rows="4" bind:value={form[f.key]}></textarea>
                    {:else if f.kind === "select"}
                        <Select
                            id="pf-{f.key}"
                            bind:value={form[f.key]}
                            options={[{ value: "", label: "—" }, ...(f.options ?? []).map((o) => ({ value: o, label: o }))]}
                        />
                    {:else if f.kind === "secret"}
                        <input
                            id="pf-{f.key}"
                            type="password"
                            autocomplete="off"
                            placeholder={form[f.key] === SECRET_MASK ? "Kept as it is; type to replace" : ""}
                            bind:value={form[f.key]}
                        />
                    {:else if f.kind === "number"}
                        <input id="pf-{f.key}" type="number" step="any" bind:value={form[f.key]} />
                    {:else}
                        <input id="pf-{f.key}" type={f.kind === "url" ? "url" : "text"} bind:value={form[f.key]} />
                    {/if}
                {/if}
                {#if f.help}
                    <div class="field-help">{f.help}</div>
                {/if}
            </div>
        {/each}
        {#if editing.fields.some((f) => f.public && f.kind !== "secret")}
            <div class="field-help m-t-base">
                Settings marked for the storefront are readable by anyone who visits it; a
                secret never leaves the server.
            </div>
        {/if}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (editing = null)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" class:loading={saving} disabled={saving} onclick={save}>
            <span class="txt">Save</span>
        </button>
    {/snippet}
</Drawer>

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
