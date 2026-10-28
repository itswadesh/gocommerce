<script>
    /**
     * One drawer for any plugin's settings, built from the plugin's own field
     * list: the engine says what each setting is called and what kind of
     * value it takes, so a module adds a plugin without touching the panel.
     *
     * Shared by the Plugins screen and by Notifications › Setup Email / SMS,
     * where a delivery backend's key is typed in. A copy of the settings is
     * edited, not the plugin itself, so Cancel really cancels.
     *
     * The controls themselves are PluginFields, because the Feeds screen wants
     * the same fields inline; what is left here is the drawer, the draft and
     * the save.
     */
    import { request } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import PluginFields from "$lib/components/PluginFields.svelte";
    import MeilisearchStatus from "$lib/components/MeilisearchStatus.svelte";

    let { plugin = null, onclose, onsaved } = $props();

    let form = $state({});
    let saving = $state(false);

    // A fresh draft whenever a different plugin opens.
    $effect(() => {
        if (!plugin) return;
        const draft = {};
        for (const f of plugin.fields ?? []) {
            const v = plugin.settings?.[f.key];
            draft[f.key] = f.kind === "bool" ? !!v : (v ?? f.default ?? "");
        }
        form = draft;
    });

    async function save() {
        if (!plugin) return;
        saving = true;
        try {
            const settings = {};
            for (const f of plugin.fields ?? []) {
                let v = form[f.key];
                if (f.kind === "number") v = v === "" || v === null ? "" : Number(v);
                settings[f.key] = v;
            }
            const updated = await request("PATCH", `/api/admin/plugins/${plugin.key}`, { body: { settings } });
            toast.success(`${plugin.title} saved`);
            onsaved?.(updated);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

<Drawer open={!!plugin} title={plugin ? `${plugin.title} settings` : "Settings"} size="sm" {onclose}>
    {#if plugin}
        <div class="field-help m-b-base">{plugin.description}</div>
        <!-- The one plugin with a running job of its own to report on. -->
        {#if plugin.key === "meilisearch"}
            <MeilisearchStatus enabled={plugin.enabled} />
        {/if}
        <form id="plugin-settings-form" onsubmit={(e) => (e.preventDefault(), saving || save())}>
            <PluginFields fields={plugin.fields} unopened={plugin.unopened} bind:form />
        </form>
        {#if (plugin.fields ?? []).some((f) => f.public && f.kind !== "secret")}
            <div class="field-help m-t-base">
                Settings marked for the storefront are readable by anyone who visits it; a
                secret never leaves the server.
            </div>
        {/if}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={onclose}>
            <span class="txt">Cancel</span>
        </button>
        <button type="submit" form="plugin-settings-form" class="btn" class:loading={saving} disabled={saving}>
            <span class="txt">Save</span>
        </button>
    {/snippet}
</Drawer>
