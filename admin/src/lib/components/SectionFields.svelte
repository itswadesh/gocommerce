<script module>
    import { api } from "$lib/api.js";

    // The store's menus, read once per visit to the panel for every menu
    // choice on the screen; null until they arrive, [] when they cannot be
    // read (no Menus module, or no right to list them).
    let menus = $state(null);
    let menusAsked = false;
    function askMenus() {
        if (menusAsked) return;
        menusAsked = true;
        api.get("/api/admin/x/navigation/menus")
            .then((list) => (menus = Array.isArray(list) ? list : Array.isArray(list?.data) ? list.data : []))
            .catch(() => (menus = []));
    }
</script>

<script>
    /**
     * A homepage section's settings form, drawn from its type's fields in the
     * engine's registry (ext/layouts, D125): one control per kind, so a section
     * type added in the engine gets its form here without a screen of its own.
     *
     * Images use the Files library (ImageField), products and collections the
     * discount screen's picker (TargetPicker); a list repeats its item fields
     * with add, move and remove. A colour is a swatch and its #rrggbb, and an
     * unset one says the storefront keeps its own (D127). A menu is chosen
     * from the store's menus by its handle (D131). An icon is picked from a
     * grid of the engine's fixed set, each shown as itself (D139). A note is
     * a warning shown beside the colour it names (key), such as a pair that
     * reads poorly (D155); it never stops the edit.
     *
     * readonly draws the same form for a role that may only look: every
     * control disabled with its value showing, and nothing offered that would
     * change one (no Add, Remove, Move, Clear or Files).
     */
    import Select from "$lib/components/Select.svelte";
    import ImageField from "$lib/components/ImageField.svelte";
    import TargetPicker from "$lib/components/TargetPicker.svelte";
    import { visibleFields, menuOptions, iconChoices } from "$lib/layouts.js";
    import { rise } from "$lib/motion.js";
    import Self from "./SectionFields.svelte";

    let { fields = [], settings = $bindable({}), idPrefix = "sf", errorKey = "", notes = [], readonly = false } = $props();

    const shown = $derived(visibleFields(fields, settings));
    $effect(() => {
        if (fields.some((f) => f.kind === "menu")) askMenus();
    });

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
    <div class="sf-field" class:readonly in:rise>
        {#if f.kind === "image" || f.kind === "video"}
            <ImageField {id} kind={f.kind} label={f.label} hint={f.help ?? ""} required={f.required} disabled={readonly} bind:value={() => settings[f.key] ?? "", (v) => set(f.key, v)} />
        {:else if f.kind === "color"}
            {@const fieldNotes = notes.filter((n) => n.key === f.key)}
            <div class="field" class:error={errorKey === f.key}>
                <label for={id}>{f.label}</label>
                <div class="sf-color">
                    <input
                        type="color"
                        class="sf-swatch"
                        class:unset={!settings[f.key]}
                        aria-label="Pick {f.label}"
                        value={settings[f.key] || "#ffffff"}
                        disabled={readonly}
                        oninput={(e) => setColor(f.key, e.currentTarget.value)}
                    />
                    <input
                        {id}
                        type="text"
                        maxlength="7"
                        autocomplete="off"
                        spellcheck="false"
                        placeholder="Storefront's own"
                        aria-describedby={fieldNotes.length ? `${id}-notes` : undefined}
                        value={settings[f.key] ?? ""}
                        disabled={readonly}
                        onchange={(e) => setColor(f.key, e.currentTarget.value)}
                        oninput={(e) => setColor(f.key, e.currentTarget.value)}
                    />
                    {#if settings[f.key] && !readonly}
                        <button type="button" class="btn sm transparent secondary" onclick={() => set(f.key, null)} aria-label="Clear {f.label}" in:rise={{ y: 0, duration: 150 }}>
                            <span class="txt">Clear</span>
                        </button>
                    {/if}
                </div>
            </div>
            {#if f.help}<div class="field-help">{f.help}</div>{/if}
            <!-- In the page before a note arrives, so a screen reader announces it. -->
            <div id="{id}-notes" role="status">
                {#each fieldNotes as n (n.message)}
                    <p class="alert warning sf-note" in:rise={{ y: 0, duration: 150 }}><i class="ri-contrast-2-line" aria-hidden="true"></i>{n.message}</p>
                {/each}
            </div>
        {:else if f.kind === "icon"}
            {@const current = settings[f.key] ?? ""}
            <fieldset class="sf-icons">
                <legend>{f.label}</legend>
                <div class="sf-icon-grid">
                    {#each iconChoices(f.options) as o (o.value)}
                        <label class="sf-icon" class:active={current === o.value} title={o.label}>
                            <input type="radio" class="sf-icon-input" name={id} value={o.value} checked={current === o.value} disabled={readonly} aria-label={o.label} onchange={() => set(f.key, o.value)} />
                            <i class={o.glyph} aria-hidden="true"></i>
                        </label>
                    {/each}
                </div>
            </fieldset>
            {#if f.help}<div class="field-help">{f.help}</div>{/if}
        {:else if f.kind === "bool"}
            <div class="field sf-switch">
                <input {id} type="checkbox" class="switch" checked={!!settings[f.key]} disabled={readonly} onchange={(e) => set(f.key, e.currentTarget.checked)} />
                <label for={id}>{f.label}</label>
            </div>
            {#if f.help}<div class="field-help">{f.help}</div>{/if}
        {:else if f.kind === "list"}
            <fieldset class="sf-list">
                <legend>{f.label}{#if f.required}<span class="sf-req" aria-hidden="true"> *</span>{/if}</legend>
                {#each listItems(f.key) as item, i (i)}
                    <div class="sf-item" in:rise>
                        <div class="sf-item-head">
                            <span class="txt-hint txt-sm">{i + 1}</span>
                            {#if !readonly}
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
                            {/if}
                        </div>
                        <Self fields={f.fields ?? []} bind:settings={() => item, (v) => setItem(f.key, i, v)} idPrefix="{id}-{i}" {readonly} />
                    </div>
                {:else}
                    {#if readonly}<p class="txt-hint txt-sm m-0">None</p>{/if}
                {/each}
                {#if !readonly && (!f.max_items || listItems(f.key).length < f.max_items)}
                    <button type="button" class="btn sm transparent secondary" onclick={() => addItem(f.key)}>
                        <i class="ri-add-line" aria-hidden="true"></i><span class="txt">Add</span>
                    </button>
                {/if}
            </fieldset>
        {:else}
            <!-- The engine refuses it too; saying why here beats a bare "Couldn't save". -->
            {@const offStore = f.kind === "path" && !!settings[f.key] && !String(settings[f.key]).trim().startsWith("/")}
            <div class="field" class:required={f.required} class:error={errorKey === f.key || offStore}>
                <label for={id}>{f.label}</label>
                {#if f.kind === "textarea"}
                    <textarea {id} rows="4" maxlength={f.max_length || undefined} value={settings[f.key] ?? ""} disabled={readonly} oninput={(e) => set(f.key, e.currentTarget.value)}></textarea>
                {:else if f.kind === "select"}
                    <Select {id} options={f.options ?? []} disabled={readonly} bind:value={() => settings[f.key] ?? f.default ?? "", (v) => set(f.key, v)} />
                {:else if f.kind === "menu"}
                    {#if menus === null}
                        <span class="skeleton-loader sf-menu-wait" aria-hidden="true"></span>
                    {:else}
                        <Select {id} options={menuOptions(menus, settings[f.key] ?? f.default ?? "")} disabled={readonly} bind:value={() => settings[f.key] ?? f.default ?? "", (v) => set(f.key, v)} />
                    {/if}
                {:else if f.kind === "number"}
                    <input
                        {id}
                        type="number"
                        inputmode="numeric"
                        min={f.min}
                        max={f.max}
                        step="1"
                        value={settings[f.key] ?? f.default ?? ""}
                        disabled={readonly}
                        oninput={(e) => set(f.key, e.currentTarget.value === "" ? null : Number(e.currentTarget.value))}
                    />
                {:else if f.kind === "products"}
                    <TargetPicker {id} kind="products" disabled={readonly} bind:value={() => settings[f.key] ?? [], (v) => set(f.key, v)} />
                {:else if f.kind === "collections"}
                    <TargetPicker {id} kind="collections" disabled={readonly} bind:value={() => settings[f.key] ?? [], (v) => set(f.key, v)} />
                {:else if f.kind === "collection"}
                    <TargetPicker {id} kind="collections" disabled={readonly} bind:value={() => collectionValue(f.key), (v) => set(f.key, v.length ? v[v.length - 1] : null)} />
                {:else}
                    <input
                        {id}
                        type="text"
                        maxlength={f.max_length || undefined}
                        inputmode={f.kind === "url" || f.kind === "path" ? "url" : undefined}
                        autocomplete="off"
                        spellcheck={f.kind === "url" || f.kind === "path" ? "false" : undefined}
                        placeholder={f.kind === "url" ? "/collections/new-in" : ""}
                        aria-invalid={offStore || undefined}
                        aria-describedby={offStore ? `${id}-error` : undefined}
                        value={settings[f.key] ?? ""}
                        disabled={readonly}
                        oninput={(e) => set(f.key, e.currentTarget.value)}
                    />
                {/if}
            </div>
            {#if offStore}<div class="field-help txt-danger" id="{id}-error" in:rise={{ y: 0, duration: 150 }}>Must be a page on your store, starting with /.</div>{/if}
            {#if f.help}<div class="field-help">{f.help}</div>{/if}
        {/if}
    </div>
{/each}

<style>
    .sf-note {
        display: flex;
        gap: 6px;
        min-height: 0;
        margin: 6px 0 0;
        padding: 6px 8px;
        border-width: 1px;
        font-size: var(--smFontSize);
        line-height: 1.4;
    }
    .sf-note i {
        color: var(--warningColor);
    }
    .sf-menu-wait {
        display: block;
        height: 36px;
    }
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
    .sf-icons {
        border: 0;
        margin: 0;
        padding: 0;
        min-width: 0;
    }
    .sf-icons legend {
        font-weight: 600;
        padding: 0;
        margin-bottom: 6px;
    }
    .sf-icon-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(40px, 1fr));
        gap: 6px;
    }
    .sf-icon {
        position: relative;
        display: flex;
        align-items: center;
        justify-content: center;
        height: 40px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--cardFace);
        color: var(--surfaceTxtHintColor);
        font-size: 1.25rem;
        cursor: pointer;
        transition:
            transform 150ms ease-out,
            opacity 150ms ease-out;
    }
    /* The radio stays in the page for the keyboard and screen readers; the
       tile is what is seen. */
    .sf-icon-input {
        position: absolute;
        inset: 0;
        margin: 0;
        opacity: 0;
        cursor: pointer;
    }
    .sf-icon:hover {
        background: var(--surfaceAlt1Color);
        color: var(--surfaceTxtColor);
        transform: translateY(-1px);
    }
    .sf-icon:active {
        transform: scale(0.94);
    }
    .sf-icon.active {
        border-color: var(--markColor);
        background: var(--surfaceAlt1Color);
        color: var(--markColor);
        box-shadow: inset 0 0 0 1px var(--markColor);
    }
    .sf-icon.active i {
        animation: sf-icon-pick 220ms ease-out;
    }
    .sf-icon:has(.sf-icon-input:focus-visible) {
        outline: 2px solid var(--markColor);
        outline-offset: 2px;
    }
    /* High contrast drops the shadow and forces the colours: a real outline
       keeps the chosen tile visible there. */
    @media (forced-colors: active) {
        .sf-icon.active {
            outline: 2px solid Highlight;
            outline-offset: -2px;
        }
    }
    @keyframes sf-icon-pick {
        from {
            transform: scale(0.7);
            opacity: 0.4;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .sf-icon,
        .sf-icon:hover,
        .sf-icon:active {
            transition: none;
            transform: none;
        }
        .sf-icon.active i {
            animation: none;
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
