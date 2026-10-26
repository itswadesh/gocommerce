<script>
    /**
     * Homepage (ext/layouts, D125): the storefront's home page as the shop
     * arranges it.
     *
     * The page is a list of sections read top to bottom. Between every two
     * rows, above the first and below the last, "Add section" puts a new one
     * exactly there; any type may be used any number of times. Each row moves
     * up and down, is duplicated, hidden or removed, and opens a drawer whose
     * form is drawn from the section type's fields in the engine's registry.
     *
     * Edits stay on this screen until Save draft, and the storefront changes
     * only on Publish. Preview opens the storefront in a tab with a short-lived
     * key for the saved draft — the panel's security policy does not let it
     * frame another site, so the preview is the real storefront, not a copy.
     */
    import { flip } from "svelte/animate";
    import { can, request, layouts as api } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import {
        blankSection,
        insertAt,
        moveSection,
        duplicateSection,
        removeSection,
        toggleSection,
        sectionSummary,
        sectionType,
    } from "$lib/layouts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise, flipDuration } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import PromptDialog from "$lib/components/PromptDialog.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import SectionFields from "$lib/components/SectionFields.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const PAGE = "home";
    const missing = $derived(modulesKnown() && !hasModule("layouts"));
    const readable = $derived(can("layouts.read"));
    const writable = $derived(can("layouts.write"));

    let registry = $state([]);
    let st = $state(null); // the engine's view: version, published revision, …
    let sections = $state([]);
    let theme = $state("default");
    let snapshot = $state("[]");
    let pluginOn = $state(true);
    let loading = $state(true);
    let busy = $state(""); // "save" | "publish" | "preview" | "rollback" | "switch"
    let conflict = $state(false);

    const dirty = $derived(JSON.stringify(sections) !== snapshot);

    function adopt(state) {
        st = state;
        sections = structuredClone(state.document?.sections ?? []);
        theme = state.document?.theme || "default";
        snapshot = JSON.stringify(sections);
        conflict = false;
    }

    async function load() {
        loading = true;
        try {
            const [reg, state, plugin] = await Promise.all([
                api.registry(),
                api.get(PAGE),
                api.plugin().catch(() => null),
            ]);
            registry = reg?.sections ?? [];
            adopt(state);
            pluginOn = plugin ? !!plugin.enabled : true;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        if (!missing && readable) load();
    });

    // ------------------------------------------------------------ editing

    let inserting = $state(null); // the index a new section goes in at
    let filter = $state("");
    const library = $derived(
        registry.filter((t) => {
            const q = filter.trim().toLowerCase();
            return !q || t.label.toLowerCase().includes(q) || t.description.toLowerCase().includes(q);
        }),
    );

    function openLibrary(index) {
        filter = "";
        inserting = index;
    }

    function add(type) {
        const at = inserting;
        const section = blankSection(type, registry, sections.map((s) => s.id));
        sections = insertAt(sections, at, section);
        inserting = null;
        // Its settings open once the library has slid away, not over it.
        setTimeout(() => (editing = at), flipDuration() ? 200 : 0);
    }

    let editing = $state(null); // the index of the section in the drawer
    const editingType = $derived(editing === null ? null : sectionType(sections[editing]?.type, registry));

    let removingIndex = $state(null);
    let confirmRemove = $state(false);

    function remove() {
        const i = removingIndex;
        const gone = sections[i];
        sections = removeSection(sections, i);
        toast.success(`${sectionSummary(gone, registry).label} removed. Discard the changes to bring it back.`);
    }

    // ------------------------------------------------------------ saving

    async function save() {
        if (busy) return false;
        busy = "save";
        try {
            adopt(await api.save(PAGE, st.version, { theme, sections }));
            toast.success("Draft saved");
            return true;
        } catch (err) {
            if (err?.status === 409) conflict = true;
            toast.error(err);
            return false;
        } finally {
            busy = "";
        }
    }

    async function saveIfNeeded() {
        if (dirty || !st?.saved) return save();
        return true;
    }

    let confirmPublish = $state(false);
    async function publish() {
        if (!(await saveIfNeeded())) return;
        busy = "publish";
        try {
            adopt(await api.publish(PAGE, st.version));
            toast.success(pluginOn ? "Published: the storefront shows this page now" : "Published. Switch the editor on for the storefront to show it.");
        } catch (err) {
            if (err?.status === 409) conflict = true;
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    async function switchOn() {
        busy = "switch";
        try {
            await api.switchOn();
            pluginOn = true;
            toast.success("The editor is on. The storefront shows the published page, or its built-in one until you publish.");
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    // ------------------------------------------------------------ preview

    const STOREFRONT_KEY = "gocommerce.storefront-address";
    let askAddress = $state(false);

    function rememberedAddress() {
        try {
            return localStorage.getItem(STOREFRONT_KEY) || "";
        } catch {
            return "";
        }
    }

    async function storefrontAddress() {
        try {
            const domains = (await request("GET", "/api/admin/domains")) ?? [];
            const d = domains.find((x) => x.verified && x.primary) ?? domains.find((x) => x.verified);
            if (d) return `https://${d.host}`;
        } catch {
            // No right to read domains, or none: the remembered address serves.
        }
        return rememberedAddress();
    }

    async function preview(addressGiven = "") {
        // The tab is opened now, while the click still counts as the
        // shopper's own, and pointed at the preview once the key arrives;
        // opened after an await, a browser would block it as a pop-up.
        const tab = window.open("", "_blank");
        if (!(await saveIfNeeded())) {
            tab?.close();
            return;
        }
        const base = (addressGiven || (await storefrontAddress())).replace(/\/+$/, "");
        if (!base) {
            tab?.close();
            askAddress = true;
            return;
        }
        busy = "preview";
        try {
            const { token } = await api.preview(PAGE);
            // The storefront's home page, with the key it passes to the engine.
            const url = new URL(base);
            url.pathname = "";
            url.searchParams.set("preview", token);
            if (tab) tab.location.href = url.href;
            else window.location.assign(url.href);
        } catch (err) {
            tab?.close();
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    function useAddress(value) {
        const address = value.trim();
        if (!/^https?:\/\/[^\s/]+/.test(address)) {
            toast.error("Type the storefront's address, starting with https://");
            return;
        }
        try {
            localStorage.setItem(STOREFRONT_KEY, address);
        } catch {
            // A private window keeps nothing; the address still serves now.
        }
        askAddress = false;
        preview(address);
    }

    // ------------------------------------------------------------ history

    let historyOpen = $state(false);
    let revisions = $state([]);
    let rollingBack = $state(null);
    let confirmRollback = $state(false);

    async function openHistory() {
        historyOpen = true;
        try {
            revisions = (await api.revisions(PAGE)) ?? [];
        } catch (err) {
            toast.error(err);
        }
    }

    async function rollback() {
        busy = "rollback";
        try {
            adopt(await api.rollback(PAGE, rollingBack.number));
            revisions = (await api.revisions(PAGE)) ?? [];
            toast.success(`Version ${rollingBack.number} is live again`);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    const when = (iso) =>
        new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
</script>

<svelte:head><title>{titled("Homepage")}</title></svelte:head>

{#snippet gap(index)}
    {#if writable}
        <div class="hp-gap">
            <button type="button" class="hp-add" onclick={() => openLibrary(index)} aria-label={index === 0 ? "Add a section at the top" : index === sections.length ? "Add a section at the bottom" : `Add a section after section ${index}`}>
                <i class="ri-add-line" aria-hidden="true"></i><span>Add section</span>
            </button>
        </div>
    {/if}
{/snippet}

<div class="page page-homepage shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Content</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Homepage</h1>
            </nav>
            {#if !missing && readable && st}
                <div class="page-header-primary-btns hp-head-btns">
                    <button type="button" class="btn transparent secondary" onclick={openHistory}>
                        <i class="ri-history-line" aria-hidden="true"></i><span class="txt">History</span>
                    </button>
                    {#if writable}
                        <button type="button" class="btn transparent secondary" class:loading={busy === "preview"} disabled={!!busy} onclick={() => preview()}>
                            <i class="ri-eye-line" aria-hidden="true"></i><span class="txt">Preview</span>
                        </button>
                        <button type="button" class="btn" class:loading={busy === "publish"} disabled={!!busy || (!dirty && st.saved && !st.changed)} onclick={() => (confirmPublish = true)}>
                            <i class="ri-upload-cloud-2-line" aria-hidden="true"></i><span class="txt">Publish</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing module="layouts" what="The homepage editor is ext/layouts, and this binary does not have it. Start it with -layouts." />
        {:else if !readable}
            <NoAccess right="layouts.read" what="the homepage editor" />
        {:else if loading && !st}
            <div class="hp-wrap"><span class="skeleton-loader"></span></div>
        {:else if st}
            <div class="hp-wrap" in:rise>
                <p class="hp-status txt-sm">
                    {#if st.published}
                        <span class="label success">Published</span> Version {st.published.number}, {when(st.published.published_at)}{#if st.published.published_by}, by {st.published.published_by}{/if}.
                        {#if dirty || st.changed}<span class="label warning">Unpublished changes</span>{/if}
                    {:else if st.saved}
                        <span class="label warning">Not published</span> The storefront shows its built-in home page until you publish.
                    {:else}
                        <span class="label">Starting point</span> This is the storefront's built-in home page as sections. Change it, then save and publish.
                    {/if}
                </p>

                {#if !pluginOn}
                    <div class="alert warning hp-alert" role="status">
                        <i class="ri-information-line" aria-hidden="true"></i>
                        <div class="content">
                            <p>The editor is switched off, so the storefront shows its built-in home page even after you publish.</p>
                        </div>
                        {#if can("plugins.write")}
                            <button type="button" class="btn sm" class:loading={busy === "switch"} disabled={!!busy} onclick={switchOn}>
                                <span class="txt">Switch it on</span>
                            </button>
                        {/if}
                    </div>
                {/if}

                {#if conflict}
                    <div class="alert danger hp-alert" role="alert">
                        <div class="content"><p>Somebody saved this page after you opened it. Reload to see their version; your changes here are not saved.</p></div>
                        <button type="button" class="btn sm" onclick={load}><span class="txt">Reload</span></button>
                    </div>
                {/if}

                {#if !sections.length}
                    <div class="hp-empty">
                        <p class="txt-hint">The page has no sections. The storefront's home page would be empty.</p>
                    </div>
                {/if}

                <ol class="hp-list" aria-label="Sections, top to bottom">
                    {#if writable}<li class="hp-item">{@render gap(0)}</li>{/if}
                    {#each sections as section, i (section.id)}
                        {@const sum = sectionSummary(section, registry)}
                        {@const known = !!sectionType(section.type, registry)}
                        <li class="hp-item" animate:flip={{ duration: flipDuration() }} in:rise>
                        <div class="hp-row" class:off={!section.enabled}>
                            <span class="hp-num txt-hint" aria-hidden="true">{i + 1}</span>
                            <i class="hp-icon {sectionType(section.type, registry)?.icon ?? 'ri-question-line'}" aria-hidden="true"></i>
                            <button type="button" class="hp-text" disabled={!known || !writable} onclick={() => (editing = i)}>
                                <span class="hp-label">{sum.label}</span>
                                {#if sum.text}<span class="hp-sub txt-hint txt-sm">{sum.text}</span>{/if}
                                {#if !section.enabled}<span class="label sm">Hidden</span>{/if}
                            </button>
                            {#if writable}
                                <div class="hp-actions">
                                    <div class="field hp-toggle">
                                        <input type="checkbox" class="switch" id="hp-on-{section.id}" checked={section.enabled} onchange={() => (sections = toggleSection(sections, i))} aria-label="Show {sum.label} on the storefront" />
                                        <label for="hp-on-{section.id}"></label>
                                    </div>
                                    <button type="button" class="btn sm circle transparent secondary" disabled={i === 0} onclick={() => (sections = moveSection(sections, i, -1))} aria-label="Move {sum.label} up">
                                        <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                    </button>
                                    <button type="button" class="btn sm circle transparent secondary" disabled={i === sections.length - 1} onclick={() => (sections = moveSection(sections, i, 1))} aria-label="Move {sum.label} down">
                                        <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                    </button>
                                    <button type="button" class="btn sm circle transparent secondary" disabled={!known} onclick={() => (sections = duplicateSection(sections, i))} aria-label="Duplicate {sum.label}">
                                        <i class="ri-file-copy-line" aria-hidden="true"></i>
                                    </button>
                                    <button type="button" class="btn sm circle transparent secondary" disabled={!known} onclick={() => (editing = i)} aria-label="Edit {sum.label}">
                                        <i class="ri-pencil-line" aria-hidden="true"></i>
                                    </button>
                                    <button type="button" class="btn sm circle transparent danger" onclick={() => { removingIndex = i; confirmRemove = true; }} aria-label="Remove {sum.label}">
                                        <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                    </button>
                                </div>
                            {/if}
                        </div>
                        {@render gap(i + 1)}
                        </li>
                    {/each}
                </ol>
            </div>
        {/if}
    </div>
</div>

<SaveBar {dirty} saving={busy === "save"} saveLabel="Save draft" onsave={save} ondiscard={() => (sections = JSON.parse(snapshot))} />

<Drawer open={inserting !== null} size="md" title="Add a section" onclose={() => (inserting = null)}>
    <div class="field">
        <label for="hp-filter">Search</label>
        <input id="hp-filter" type="search" autocomplete="off" bind:value={filter} placeholder="Hero, products, text…" />
    </div>
    <ul class="hp-library">
        {#each library as t (t.type)}
            <li>
                <button type="button" class="hp-type" onclick={() => add(t.type)}>
                    <i class={t.icon} aria-hidden="true"></i>
                    <span>
                        <span class="hp-label">{t.label}</span>
                        <span class="txt-hint txt-sm">{t.description}</span>
                    </span>
                </button>
            </li>
        {:else}
            <li class="txt-hint txt-sm">No section matches “{filter}”.</li>
        {/each}
    </ul>
</Drawer>

<Drawer open={editing !== null && !!editingType} size="md" title={editingType?.label ?? "Section"} onclose={() => (editing = null)}>
    {#if editing !== null && editingType}
        <p class="txt-hint txt-sm m-b-sm">{editingType.description}</p>
        {#key sections[editing]?.id}
            <SectionFields
                fields={editingType.fields}
                idPrefix="hp-{sections[editing].id}"
                bind:settings={() => sections[editing].settings ?? {}, (v) => (sections = sections.map((s, j) => (j === editing ? { ...s, settings: v } : s)))}
            />
        {/key}
    {/if}
    {#snippet footer()}
        <span class="txt-hint txt-sm m-r-auto">Changes are kept on this screen until you save the draft.</span>
        <button type="button" class="btn" onclick={() => (editing = null)}><span class="txt">Done</span></button>
    {/snippet}
</Drawer>

<Drawer open={historyOpen} size="md" title="Published versions" onclose={() => (historyOpen = false)}>
    {#if !revisions.length}
        <p class="txt-hint">Nothing has been published yet.</p>
    {:else}
        <ol class="hp-revisions">
            {#each revisions as rev (rev.number)}
                <li class="hp-rev">
                    <div>
                        <div class="hp-label">Version {rev.number}{#if st?.published?.number === rev.number} <span class="label success sm">Live</span>{/if}</div>
                        <div class="txt-hint txt-sm">
                            {when(rev.published_at)}{#if rev.published_by} · {rev.published_by}{/if} · {rev.sections} sections{#if rev.restored_from} · restored from version {rev.restored_from}{/if}
                        </div>
                    </div>
                    {#if writable && st?.published?.number !== rev.number}
                        <button type="button" class="btn sm transparent secondary" disabled={!!busy} onclick={() => { rollingBack = rev; confirmRollback = true; }}>
                            <span class="txt">Roll back</span>
                        </button>
                    {/if}
                </li>
            {/each}
        </ol>
    {/if}
</Drawer>

<Confirm bind:open={confirmRemove} title="Remove this section?" message="It leaves the draft. Until you save, Discard brings it back; once published, an earlier version still has it." confirmLabel="Remove" danger onconfirm={remove} />

<Confirm
    bind:open={confirmPublish}
    title="Publish the home page?"
    message={dirty ? "Your changes are saved as the draft first. Shoppers then see this page." : "Shoppers see this page from now on. You can roll back to an earlier version."}
    confirmLabel="Publish"
    onconfirm={publish}
/>

<Confirm
    bind:open={confirmRollback}
    title="Roll back to version {rollingBack?.number}?"
    message="It goes live again as a new version, and becomes the draft. Unsaved changes on this screen are replaced."
    confirmLabel="Roll back"
    onconfirm={rollback}
/>

<PromptDialog
    open={askAddress}
    title="Where is the storefront?"
    message="The preview opens your storefront in a new tab. This store has no verified domain yet, so type its address once; this browser remembers it."
    label="Storefront address"
    placeholder="https://shop.example.com"
    required
    initial={rememberedAddress()}
    confirmLabel="Open preview"
    onclose={() => (askAddress = false)}
    onconfirm={useAddress}
/>

<style>
    .hp-wrap {
        max-width: 860px;
    }
    .hp-head-btns {
        flex-wrap: wrap;
        gap: 6px;
    }
    .hp-status {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
        margin: 0 0 12px;
    }
    .hp-alert {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 10px;
        margin-bottom: 12px;
    }
    .hp-alert .content {
        flex: 1 1 220px;
    }
    .hp-empty {
        padding: 12px 0;
    }
    .hp-list,
    .hp-library,
    .hp-revisions {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .hp-row {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 10px;
        padding: 10px 12px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--cardFace);
        transition: opacity 180ms ease-out;
    }
    .hp-row.off {
        opacity: 0.6;
    }
    .hp-num {
        width: 1.5em;
        text-align: right;
        font-variant-numeric: tabular-nums;
    }
    .hp-icon {
        font-size: 1.2rem;
        color: var(--txtHintColor);
    }
    .hp-text {
        flex: 1 1 180px;
        min-width: 0;
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
        padding: 4px;
        margin: -4px;
        border: 0;
        background: none;
        text-align: left;
        cursor: pointer;
        border-radius: var(--borderRadius);
        transition: background-color 150ms ease-out;
    }
    .hp-text:hover:not(:disabled) {
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.04));
    }
    .hp-text:focus-visible,
    .hp-add:focus-visible,
    .hp-type:focus-visible {
        outline: 2px solid var(--markColor);
        outline-offset: 2px;
    }
    .hp-text:disabled {
        cursor: default;
    }
    .hp-label {
        font-weight: 600;
    }
    .hp-sub {
        overflow-wrap: anywhere;
    }
    .hp-actions {
        display: flex;
        align-items: center;
        gap: 2px;
        margin-left: auto;
    }
    .hp-toggle {
        margin: 0 6px 0 0;
    }
    .hp-gap {
        display: flex;
        justify-content: center;
        padding: 4px 0;
    }
    /* The insertion point: quiet until it is wanted, so a long page still
       reads as a page; a phone has no hover, so it is always visible there. */
    .hp-add {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        min-height: 32px;
        padding: 2px 12px;
        border: 1px dashed var(--surfaceAlt2Color);
        border-radius: 999px;
        background: var(--cardFace);
        color: var(--txtHintColor);
        font-size: 0.85rem;
        cursor: pointer;
        opacity: 0.55;
        transition:
            opacity 150ms ease-out,
            transform 150ms ease-out,
            color 150ms ease-out;
    }
    .hp-add:hover,
    .hp-add:focus-visible {
        opacity: 1;
        color: var(--txtPrimaryColor);
    }
    .hp-add:active {
        transform: scale(0.97);
    }
    @media (hover: none) {
        .hp-add {
            opacity: 1;
            min-height: 40px;
        }
    }
    .hp-library li + li {
        margin-top: 6px;
    }
    .hp-type {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        width: 100%;
        padding: 10px 12px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--cardFace);
        text-align: left;
        cursor: pointer;
        transition:
            border-color 150ms ease-out,
            transform 150ms ease-out;
    }
    .hp-type > span {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .hp-type i {
        font-size: 1.3rem;
        margin-top: 2px;
    }
    .hp-type:hover {
        border-color: var(--txtHintColor);
    }
    .hp-type:active {
        transform: scale(0.99);
    }
    .hp-rev {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        padding: 10px 0;
    }
    .hp-rev + .hp-rev {
        border-top: 1px solid var(--surfaceAlt2Color);
    }
    @media (max-width: 600px) {
        .hp-actions {
            width: 100%;
            justify-content: flex-end;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .hp-add,
        .hp-type,
        .hp-text,
        .hp-row {
            transition: none;
        }
        .hp-add:active,
        .hp-type:active {
            transform: none;
        }
    }
</style>
