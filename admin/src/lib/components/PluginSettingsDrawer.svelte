<script>
    /**
     * One drawer for any plugin's settings, built from the plugin's own field
     * list: the engine says what each setting is called and what kind of
     * value it takes, so a module adds a plugin without touching the panel.
     *
     * Shared by the Plugins screen and by Notifications › Setup Email / SMS,
     * where a delivery backend's key is typed in. A copy of the settings is
     * edited, not the plugin itself, so Cancel really cancels.
     */
    import { request } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";

    let { plugin = null, onclose, onsaved } = $props();

    const SECRET_MASK = "••••••••";
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
        {#each plugin.fields ?? [] as f (f.key)}
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
        <button type="button" class="btn" class:loading={saving} disabled={saving} onclick={save}>
            <span class="txt">Save</span>
        </button>
    {/snippet}
</Drawer>
