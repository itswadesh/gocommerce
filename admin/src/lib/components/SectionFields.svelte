<script>
    /**
     * A homepage section's settings form, drawn from its type's fields in the
     * engine's registry (ext/layouts, D125): one control per kind, so a section
     * type added in the engine gets its form here without a screen of its own.
     *
     * Images use the Files library (ImageField), products and collections the
     * discount screen's picker (TargetPicker); a list repeats its item fields
     * with add, move and remove. A colour is a swatch and its #rrggbb, and an
     * unset one says the storefront keeps its own (D127).
     */
    import Select from "$lib/components/Select.svelte";
    import ImageField from "$lib/components/ImageField.svelte";
    import TargetPicker from "$lib/components/TargetPicker.svelte";
    import { visibleFields } from "$lib/layouts.js";
    import { rise } from "$lib/motion.js";
    import Self from "./SectionFields.svelte";

    let { fields = [], settings = $bindable({}), idPrefix = "sf", errorKey = "" } = $props();

    const shown = $derived(visibleFields(fields, settings));

    function set(key, v) {
        const next = { ...settings };
        if (v === "" || v === null || v === undefined || (Array.isArray(v) && !v.length)) delete next[key];
        else next[key] = v;
        settings = next;
    }

    // A collection is one id; the picker speaks in lists, so the newest pick
    // replaces the one before.
    function collectionValue(key) {
        return settings[key] ? [settings[key]] : [];
    }

    const HEX = /^#[0-9a-f]{6}$/i;
    // Only a whole colour is kept: a half-typed one stays in the box until it is.
    function setColor(key, raw) {
        const v = raw.trim();
        if (v === "") set(key, null);
        else if (HEX.test(v)) set(key, v.toLowerCase());
    }

    function listItems(key) {
        return Array.isArray(settings[key]) ? settings[key] : [];
    }
    function setItem(key, i, item) {
        set(key, listItems(key).map((x, j) => (j === i ? item : x)));
    }
    function addItem(key) {
        set(key, [...listItems(key), {}]);
    }
    function moveItem(key, i, dir) {
        const items = [...listItems(key)];
        const to = i + dir;
        if (to < 0 || to >= items.length) return;
        [items[i], items[to]] = [items[to], items[i]];
        set(key, items);
    }
    function removeItem(key, i) {
        set(key, listItems(key).filter((_, j) => j !== i));
    }
</script>

{#each shown as f (f.key)}
    {@const id = `${idPrefix}-${f.key}`}
    <div class="sf-field" in:rise>
        {#if f.kind === "image" || f.kind === "video"}
            <ImageField {id} kind={f.kind} label={f.label} hint={f.help ?? ""} required={f.required} bind:value={() => settings[f.key] ?? "", (v) => set(f.key, v)} />
        {:else if f.kind === "color"}
            <div class="field" class:error={errorKey === f.key}>
                <label for={id}>{f.label}</label>
                <div class="sf-color">
                    <input
                        type="color"
                        class="sf-swatch"
                        class:unset={!settings[f.key]}
                        aria-label="Pick {f.label}"
                        value={settings[f.key] || "#ffffff"}
                        oninput={(e) => setColor(f.key, e.currentTarget.value)}
                    />
                    <input
                        {id}
                        type="text"
                        maxlength="7"
                        autocomplete="off"
                        spellcheck="false"
                        placeholder="Storefront's own"
                        value={settings[f.key] ?? ""}
                        onchange={(e) => setColor(f.key, e.currentTarget.value)}
                        oninput={(e) => setColor(f.key, e.currentTarget.value)}
                    />
                    {#if settings[f.key]}
                        <button type="button" class="btn sm transparent secondary" onclick={() => set(f.key, null)} aria-label="Clear {f.label}" in:rise={{ y: 0, duration: 150 }}>
                            <span class="txt">Clear</span>
                        </button>
                    {/if}
                </div>
            </div>
            {#if f.help}<div class="field-help">{f.help}</div>{/if}
        {:else if f.kind === "bool"}
            <div class="field sf-switch">
                <input {id} type="checkbox" class="switch" checked={!!settings[f.key]} onchange={(e) => set(f.key, e.currentTarget.checked)} />
                <label for={id}>{f.label}</label>
            </div>
        {:else if f.kind === "list"}
            <fieldset class="sf-list">
                <legend>{f.label}{#if f.required}<span class="sf-req" aria-hidden="true"> *</span>{/if}</legend>
                {#each listItems(f.key) as item, i (i)}
                    <div class="sf-item" in:rise>
                        <div class="sf-item-head">
                            <span class="txt-hint txt-sm">{i + 1}</span>
                            <div class="sf-item-actions">
                                <button type="button" class="btn sm circle transparent secondary" disabled={i === 0} onclick={() => moveItem(f.key, i, -1)} aria-label="Move item {i + 1} up">
                                    <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                </button>
                                <button type="button" class="btn sm circle transparent secondary" disabled={i === listItems(f.key).length - 1} onclick={() => moveItem(f.key, i, 1)} aria-label="Move item {i + 1} down">
                                    <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                </button>
                                <button type="button" class="btn sm circle transparent danger" onclick={() => removeItem(f.key, i)} aria-label="Remove item {i + 1}">
                                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                </button>
                            </div>
                        </div>
                        <Self fields={f.fields ?? []} bind:settings={() => item, (v) => setItem(f.key, i, v)} idPrefix="{id}-{i}" />
                    </div>
                {/each}
                {#if !f.max_items || listItems(f.key).length < f.max_items}
                    <button type="button" class="btn sm transparent secondary" onclick={() => addItem(f.key)}>
                        <i class="ri-add-line" aria-hidden="true"></i><span class="txt">Add</span>
                    </button>
                {/if}
            </fieldset>
        {:else}
            <div class="field" class:required={f.required} class:error={errorKey === f.key}>
                <label for={id}>{f.label}</label>
                {#if f.kind === "textarea"}
                    <textarea {id} rows="4" maxlength={f.max_length || undefined} value={settings[f.key] ?? ""} oninput={(e) => set(f.key, e.currentTarget.value)}></textarea>
                {:else if f.kind === "select"}
                    <Select {id} options={f.options ?? []} bind:value={() => settings[f.key] ?? f.default ?? "", (v) => set(f.key, v)} />
                {:else if f.kind === "number"}
                    <input
                        {id}
                        type="number"
                        inputmode="numeric"
                        min={f.min}
                        max={f.max}
                        step="1"
                        value={settings[f.key] ?? f.default ?? ""}
                        oninput={(e) => set(f.key, e.currentTarget.value === "" ? null : Number(e.currentTarget.value))}
                    />
                {:else if f.kind === "products"}
                    <TargetPicker {id} kind="products" bind:value={() => settings[f.key] ?? [], (v) => set(f.key, v)} />
                {:else if f.kind === "collections"}
                    <TargetPicker {id} kind="collections" bind:value={() => settings[f.key] ?? [], (v) => set(f.key, v)} />
                {:else if f.kind === "collection"}
                    <TargetPicker {id} kind="collections" bind:value={() => collectionValue(f.key), (v) => set(f.key, v.length ? v[v.length - 1] : null)} />
                {:else}
                    <input
                        {id}
                        type="text"
                        maxlength={f.max_length || undefined}
                        inputmode={f.kind === "url" ? "url" : undefined}
                        autocomplete="off"
                        spellcheck={f.kind === "url" ? "false" : undefined}
                        placeholder={f.kind === "url" ? "/collections/new-in" : ""}
                        value={settings[f.key] ?? ""}
                        oninput={(e) => set(f.key, e.currentTarget.value)}
                    />
                {/if}
            </div>
            {#if f.help}<div class="field-help">{f.help}</div>{/if}
        {/if}
    </div>
{/each}

<style>
    .sf-field + .sf-field {
        margin-top: 12px;
    }
    .sf-switch {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .sf-color {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .sf-color input[type="text"] {
        flex: 1 1 auto;
        min-width: 0;
        font-variant-numeric: tabular-nums;
    }
    .sf-swatch {
        flex: 0 0 auto;
        width: 40px;
        height: 36px;
        padding: 2px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--cardFace);
        cursor: pointer;
        transition:
            transform 150ms ease-out,
            opacity 150ms ease-out;
    }
    .sf-swatch:active {
        transform: scale(0.95);
    }
    /* Unset: the storefront's own colour, which this panel does not know. */
    .sf-swatch.unset {
        opacity: 0.45;
    }
    @media (prefers-reduced-motion: reduce) {
        .sf-swatch {
            transition: none;
        }
        .sf-swatch:active {
            transform: none;
        }
    }
    .sf-list {
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        padding: 8px 12px 12px;
        margin: 0;
    }
    .sf-list legend {
        font-weight: 600;
        padding: 0 4px;
    }
    .sf-req {
        color: var(--dangerColor);
    }
    .sf-item {
        padding: 8px 0;
    }
    .sf-item + .sf-item {
        border-top: 1px solid var(--surfaceAlt2Color);
    }
    .sf-item-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 4px;
    }
    .sf-item-actions {
        display: flex;
        gap: 2px;
    }
</style>
