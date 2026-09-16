<script>
    /**
     * A plugin's settings as form controls, built from the plugin's own field
     * list: the engine says what each setting is called and what kind of value
     * it takes, so a module adds a plugin without touching the panel.
     *
     * This was the body of PluginSettingsDrawer until the Feeds screen wanted
     * the same fields inline rather than in a drawer. Two copies of "how a
     * plugin field is rendered" is how a module ships a `select` and one of
     * the two places draws it as a text box — so there is one copy, and the
     * drawer is now a drawer around it.
     *
     * The caller owns the draft, and binds it in. That is deliberate: the
     * drawer edits a copy so Cancel can really cancel, and an inline form edits
     * a copy so Save is a decision rather than a side effect of typing. This
     * renders whatever it is given and decides nothing about when it is saved.
     */
    import Select from "$lib/components/Select.svelte";

    /** @type {{ fields?: any[], form: Record<string, any>, idPrefix?: string }} */
    let { fields = [], form = $bindable({}), idPrefix = "pf" } = $props();

    /* What the API sends back in place of a stored secret. Typing over it
       replaces the secret; leaving it alone keeps it. */
    const SECRET_MASK = "••••••••";
</script>

{#each fields ?? [] as f (f.key)}
    <div class="field m-t-sm">
        {#if f.kind === "bool"}
            <input type="checkbox" id="{idPrefix}-{f.key}" class="switch" bind:checked={form[f.key]} />
            <label for="{idPrefix}-{f.key}">{f.label}</label>
        {:else}
            <label for="{idPrefix}-{f.key}">
                {f.label}{#if f.required}<span class="txt-danger"> *</span>{/if}
            </label>
            {#if f.kind === "textarea"}
                <textarea id="{idPrefix}-{f.key}" rows="4" bind:value={form[f.key]}></textarea>
            {:else if f.kind === "select"}
                <Select
                    id="{idPrefix}-{f.key}"
                    bind:value={form[f.key]}
                    options={[{ value: "", label: "—" }, ...(f.options ?? []).map((o) => ({ value: o, label: o }))]}
                />
            {:else if f.kind === "secret"}
                <input
                    id="{idPrefix}-{f.key}"
                    type="password"
                    autocomplete="off"
                    placeholder={form[f.key] === SECRET_MASK ? "Kept as it is; type to replace" : ""}
                    bind:value={form[f.key]}
                />
            {:else if f.kind === "number"}
                <input id="{idPrefix}-{f.key}" type="number" step="any" bind:value={form[f.key]} />
            {:else}
                <input id="{idPrefix}-{f.key}" type={f.kind === "url" ? "url" : "text"} bind:value={form[f.key]} />
            {/if}
        {/if}
        {#if f.help}
            <div class="field-help">{f.help}</div>
        {/if}
    </div>
{/each}
