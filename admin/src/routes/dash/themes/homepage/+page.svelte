<script>
    /**
     * Themes › Homepage (ext/layouts, D125, D126): the storefront's home page
     * as the shop arranges it, one element of the store's theme.
     *
     * The page is a list of sections read top to bottom; any type may be used
     * any number of times. Every edit saves the draft by itself a moment after
     * the last change, and on leaving the screen; the storefront changes only
     * on Publish. Reset throws the draft away, back to what is published.
     * Undo and redo walk this session's edits (Ctrl/Cmd+Z, Shift+Z).
     *
     * On a wide screen it is a visual editor: the sections on the left — drag
     * to reorder, or move with the arrow keys — and the real storefront in a
     * frame beside them, drawing each edit as it is made; clicking a section
     * there picks it here. On a phone it is the list, with settings in a
     * drawer and the preview in a tab.
     */
    import { onMount } from "svelte";
    import { flip } from "svelte/animate";
    import { fade } from "svelte/transition";
    import { base } from "$app/paths";
    import { beforeNavigate } from "$app/navigation";
    import { can, layouts as api } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { getStore } from "$lib/store-scope.js";
    import {
        blankSection,
        insertAt,
        moveSection,
        moveTo,
        dropIndex,
        duplicateSection,
        removeSection,
        toggleSection,
        sectionSummary,
        sectionType,
        createHistory,
        createAutosave,
        previewAddress,
    } from "$lib/layouts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise, flipDuration } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import PromptDialog from "$lib/components/PromptDialog.svelte";
    import SectionFields from "$lib/components/SectionFields.svelte";
    import ThemePreview from "$lib/components/ThemePreview.svelte";
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
    let pluginOn = $state(true);
    let loading = $state(true);
    let busy = $state(""); // "publish" | "preview" | "rollback" | "switch" | "reset"

    const draftDoc = () => ({ theme, sections: JSON.parse(JSON.stringify(sections)) });
    const docJSON = () => JSON.stringify({ theme, sections });

    // A wide screen gets the visual editor; a phone the list.
    let narrow = $state(false);
    onMount(() => {
        const mq = matchMedia("(max-width: 760px)");
        narrow = mq.matches;
        const on = (e) => (narrow = e.matches);
        mq.addEventListener("change", on);
        return () => mq.removeEventListener("change", on);
    });

    // ------------------------------------------------------------ saving

    let saveState = $state("saved");
    let savedOnce = $state(false); // a save happened on this screen: say so
    let savedTick = $state(0); // tells the preview to re-read the saved draft
    const autosave = createAutosave({
        save: async (doc) => {
            const next = await api.save(PAGE, st.version, doc);
            // Only the engine's facts: the sections on screen may be newer
            // than the ones this save carried.
            st = { ...next, document: st.document };
            savedOnce = true;
            savedTick++;
        },
        onstate: (s) => (saveState = s),
    });

    const history = createHistory();
    let historyTick = $state(0);
    const canUndo = $derived(historyTick >= 0 && history.canUndo());
    const canRedo = $derived(historyTick >= 0 && history.canRedo());

    /** Every edit goes through here: one undo step, then the autosave. */
    function edit(next, key = null) {
        history.record(docJSON(), key);
        historyTick++;
        sections = next;
        autosave.change(draftDoc());
    }

    function restore(json) {
        const doc = JSON.parse(json);
        theme = doc.theme || "default";
        sections = doc.sections ?? [];
        if (selectedId && !sections.some((s) => s.id === selectedId)) selectedId = null;
        historyTick++;
        autosave.change(draftDoc());
    }

    function undo() {
        const prev = history.undo(docJSON());
        if (prev !== null) restore(prev);
    }

    function redo() {
        const next = history.redo(docJSON());
        if (next !== null) restore(next);
    }

    function onkeydown(e) {
        if (!writable || !st || !(e.ctrlKey || e.metaKey) || e.altKey) return;
        const k = e.key.toLowerCase();
        // A field keeps its own undo: these are the page's, for edits made
        // with the buttons, the drag and the switches.
        const t = e.target;
        if (t instanceof HTMLElement && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
        if (k === "z" && !e.shiftKey) {
            e.preventDefault();
            undo();
        } else if ((k === "z" && e.shiftKey) || k === "y") {
            e.preventDefault();
            redo();
        }
    }

    function adopt(state) {
        st = state;
        sections = JSON.parse(JSON.stringify(state.document?.sections ?? []));
        theme = state.document?.theme || "default";
        if (selectedId && !sections.some((s) => s.id === selectedId)) selectedId = null;
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
            autosave.reset();
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

    // Leaving the screen saves what is waiting. Within the panel an ordinary
    // save will do; leaving the page altogether, a browser cancels ordinary
    // requests as it goes, so pagehide sends the draft with keepalive, which
    // outlives the page.
    beforeNavigate((nav) => {
        if (nav.type !== "leave") autosave.flush();
    });
    onMount(() => {
        const hide = () => {
            if (document.visibilityState === "hidden") autosave.flush();
        };
        const gone = () => {
            if (autosave.pending() && st && writable) {
                api.save(PAGE, st.version, draftDoc(), { keepalive: true }).catch(() => {});
            }
        };
        const unload = (e) => {
            // Only a save that failed is worth stopping somebody for.
            if (autosave.state() === "error") e.preventDefault();
        };
        document.addEventListener("visibilitychange", hide);
        window.addEventListener("pagehide", gone);
        window.addEventListener("beforeunload", unload);
        return () => {
            document.removeEventListener("visibilitychange", hide);
            window.removeEventListener("pagehide", gone);
            window.removeEventListener("beforeunload", unload);
        };
    });

    async function reloadAfterConflict() {
        await load();
        selectedId = null;
    }

    // ------------------------------------------------------------ editing

    let selectedId = $state(null);
    // Wide enough for Shopify's three columns — sections, preview, settings —
    // the settings open on the right and the list stays put. Measured on the
    // editor rather than the window, since the panel's own menu takes a share.
    let editorWidth = $state(0);
    const threeCol = $derived(editorWidth >= 1100);
    const selectedIndex = $derived(selectedId === null ? -1 : sections.findIndex((s) => s.id === selectedId));
    const selected = $derived(selectedIndex < 0 ? null : sections[selectedIndex]);
    const selectedType = $derived(selected ? sectionType(selected.type, registry) : null);

    function select(id) {
        selectedId = id;
    }

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
        edit(insertAt(sections, at, section));
        inserting = null;
        // Its settings open once the library has slid away, not over it.
        setTimeout(() => (selectedId = section.id), flipDuration() ? 200 : 0);
    }

    function setSettings(id, settings) {
        edit(
            sections.map((s) => (s.id === id ? { ...s, settings } : s)),
            "settings:" + id,
        );
    }

    let removingId = $state(null);
    let confirmRemove = $state(false);

    function remove() {
        const i = sections.findIndex((s) => s.id === removingId);
        if (i < 0) return;
        const gone = sections[i];
        if (selectedId === gone.id) selectedId = null;
        edit(removeSection(sections, i));
        toast.success(`${sectionSummary(gone, registry).label} removed. Undo brings it back.`);
    }

    function duplicate(i) {
        const next = duplicateSection(sections, i);
        edit(next);
        selectedId = next[i + 1].id;
    }

    let announce = $state("");
    function move(i, dir) {
        const to = i + dir;
        if (to < 0 || to >= sections.length) return;
        edit(moveSection(sections, i, dir));
        announce = `${sectionSummary(sections[to], registry).label} moved to position ${to + 1} of ${sections.length}.`;
    }

    // ------------------------------------------------------------ drag to reorder

    let listEl = $state();
    let dragId = $state(null);
    let dragBefore = "";

    function dragStart(e, id) {
        if (e.button !== 0 || !writable) return;
        e.preventDefault();
        e.currentTarget.setPointerCapture(e.pointerId);
        dragBefore = docJSON();
        dragId = id;
    }

    function dragMove(e) {
        if (dragId === null || !listEl) return;
        // Layout positions, not the screen's: a row the list is animating
        // into place is drawn somewhere it is not, and measuring it there
        // made rows trade places back and forth.
        const rows = [...listEl.querySelectorAll("[data-row]")];
        const y = e.clientY - listEl.getBoundingClientRect().top + listEl.scrollTop;
        const middles = rows.map((r) => r.offsetTop + r.offsetHeight / 2);
        const from = sections.findIndex((s) => s.id === dragId);
        const to = dropIndex(middles, y);
        if (from >= 0 && to !== from) sections = moveTo(sections, from, to);
    }

    function dragEnd() {
        if (dragId === null) return;
        const id = dragId;
        dragId = null;
        if (docJSON() !== dragBefore) {
            history.record(dragBefore);
            historyTick++;
            autosave.change(draftDoc());
            const at = sections.findIndex((s) => s.id === id);
            announce = `${sectionSummary(sections[at], registry).label} moved to position ${at + 1} of ${sections.length}.`;
        }
    }

    function handleKey(e, i) {
        if (e.key === "ArrowUp") {
            e.preventDefault();
            move(i, -1);
        } else if (e.key === "ArrowDown") {
            e.preventDefault();
            move(i, 1);
        }
    }

    // ------------------------------------------------------------ publish, reset

    /** Everything on screen is in the engine, or false with the reason shown. */
    async function settled() {
        await autosave.flush();
        await autosave.idle();
        if (autosave.state() === "conflict" || autosave.state() === "error") {
            toast.error("The latest changes are not saved yet.");
            return false;
        }
        if (!st.saved) {
            try {
                st = await api.save(PAGE, st.version, draftDoc());
                savedTick++;
            } catch (err) {
                toast.error(err);
                return false;
            }
        }
        return true;
    }

    let confirmPublish = $state(false);
    async function publish() {
        busy = "publish";
        try {
            if (!(await settled())) return;
            st = await api.publish(PAGE, st.version);
            toast.success(pluginOn ? "Published: the storefront shows this page now" : "Published. Switch the editor on for the storefront to show it.");
        } catch (err) {
            if (err?.status === 409) saveState = "conflict";
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    let confirmReset = $state(false);
    async function reset() {
        busy = "reset";
        try {
            await autosave.cancel();
            const before = docJSON();
            const next = await api.reset(PAGE, st.version);
            history.record(before);
            historyTick++;
            adopt(next);
            savedTick++;
            toast.success(st.published ? "The draft is the published page again. Undo brings your changes back." : "The draft is the starting page again. Undo brings your changes back.");
        } catch (err) {
            if (err?.status === 409) saveState = "conflict";
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

    // The address a person typed for this store, which wins over the engine's
    // guess: a storefront may live somewhere the engine does not know of.
    const addressKey = () => "gocommerce.storefront-address:" + (getStore() || "default");

    function rememberedAddress() {
        try {
            return localStorage.getItem(addressKey()) || localStorage.getItem("gocommerce.storefront-address") || "";
        } catch {
            return "";
        }
    }

    let previewKey = $state("");
    let storefront = $state(""); // the engine's address for the store
    let typedAddress = $state("");
    let askAddress = $state(false);
    let frameBlocked = $state(false);
    let device = $state("desktop");

    const address = $derived(typedAddress || storefront);
    const frameOrigin = $derived.by(() => {
        try {
            return address ? new URL(address).origin : "";
        } catch {
            return "";
        }
    });
    const frameSrc = $derived(
        writable && previewKey && address ? previewAddress(address, previewKey, location.origin) : !writable && address ? address : "",
    );

    async function mintKey() {
        try {
            const res = await api.preview(PAGE);
            previewKey = res.token;
            storefront = res.storefront || "";
        } catch (err) {
            toast.error(err);
        }
    }

    // The visual editor needs a key while it is open; one lasts half an
    // hour, so a fresh one is fetched before that. It hangs on whether the
    // page has loaded, not on the page: a new key per save reloaded the frame.
    const opened = $derived(!!st);
    $effect(() => {
        if (narrow || !opened || !writable || missing) return;
        typedAddress = rememberedAddress();
        mintKey();
        const every = setInterval(mintKey, 25 * 60 * 1000);
        return () => clearInterval(every);
    });

    function blocked() {
        // The panel's policy names the storefronts of the store it was opened
        // for; opened for another, it reloads for this tab's store, once.
        const here = new URL(location.href);
        const store = getStore();
        if (store && here.searchParams.get("store") !== store) {
            here.searchParams.set("store", store);
            location.replace(here.href);
            return;
        }
        frameBlocked = true;
    }

    async function openTab() {
        // Opened now, while the click still counts as the person's own, and
        // pointed at the preview once the key arrives; opened after an await,
        // a browser would block it as a pop-up.
        const tab = window.open("", "_blank");
        busy = "preview";
        try {
            if (!(await settled())) {
                tab?.close();
                return;
            }
            const res = await api.preview(PAGE);
            const where = rememberedAddress() || res.storefront;
            if (!where) {
                tab?.close();
                askAddress = true;
                return;
            }
            const url = previewAddress(where, res.token, "");
            if (tab) tab.location.href = url;
            else window.location.assign(url);
        } catch (err) {
            tab?.close();
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    function useAddress(value) {
        const typed = value.trim();
        if (typed && !/^https?:\/\/[^\s/]+/.test(typed)) {
            toast.error("Type the storefront's address, starting with https://");
            return;
        }
        try {
            if (typed) localStorage.setItem(addressKey(), typed);
            else localStorage.removeItem(addressKey());
        } catch {
            // A private window keeps nothing; the address still serves now.
        }
        typedAddress = typed;
        frameBlocked = false;
        askAddress = false;
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
            await autosave.cancel();
            const before = docJSON();
            adopt(await api.rollback(PAGE, rollingBack.number));
            history.record(before);
            historyTick++;
            savedTick++;
            revisions = (await api.revisions(PAGE)) ?? [];
            toast.success(`Version ${rollingBack.number} is live again`);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    const when = (iso) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
    const hasChanges = $derived(!!st && (!st.saved || st.changed || saveState !== "saved"));
</script>

<svelte:head><title>{titled("Homepage · Themes")}</title></svelte:head>
<svelte:window {onkeydown} />

{#snippet settingsPanel(beside)}
    {@const sum = sectionSummary(selected, registry)}
    <div class="hp-settings" in:rise={{ y: 0, duration: 150 }}>
        <div class="hp-settings-head">
            <button type="button" class="btn sm circle transparent secondary" onclick={() => (selectedId = null)} aria-label={beside ? "Close the settings" : "Back to the sections"}>
                <i class={beside ? "ri-close-line" : "ri-arrow-left-line"} aria-hidden="true"></i>
            </button>
            <h2 class="hp-settings-title">{sum.label}</h2>
            {#if writable}
                <button type="button" class="btn sm circle transparent secondary" onclick={() => edit(toggleSection(sections, selectedIndex))} aria-label={selected.enabled ? `Hide ${sum.label}` : `Show ${sum.label}`} aria-pressed={!selected.enabled}>
                    <i class={selected.enabled ? "ri-eye-line" : "ri-eye-off-line"} aria-hidden="true"></i>
                </button>
                <button type="button" class="btn sm circle transparent secondary" onclick={() => duplicate(selectedIndex)} aria-label="Duplicate {sum.label}">
                    <i class="ri-file-copy-line" aria-hidden="true"></i>
                </button>
                <button type="button" class="btn sm circle transparent danger" onclick={() => { removingId = selected.id; confirmRemove = true; }} aria-label="Remove {sum.label}">
                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                </button>
            {/if}
        </div>
        {#if writable}
            {@render settingsBody(selected, selectedType)}
        {/if}
    </div>
{/snippet}

{#snippet gap(index)}
    {#if writable}
        <div class="hp-gap">
            <button type="button" class="hp-add" onclick={() => openLibrary(index)} aria-label={index === 0 ? "Add a section at the top" : index === sections.length ? "Add a section at the bottom" : `Add a section after section ${index}`}>
                <i class="ri-add-line" aria-hidden="true"></i><span>Add section</span>
            </button>
        </div>
    {/if}
{/snippet}

{#snippet saveStatus()}
    {#if writable && st}
        <span class="hp-save txt-sm" role="status" aria-live="polite">
            {#if saveState === "pending" || saveState === "saving"}
                <span class="hp-save-state" in:fade={{ duration: 150 }}><i class="ri-loader-4-line hp-spin" aria-hidden="true"></i>Saving…</span>
            {:else if saveState === "error"}
                <span class="hp-save-state danger" in:fade={{ duration: 150 }}>
                    <i class="ri-error-warning-line" aria-hidden="true"></i>Couldn't save
                    <button type="button" class="btn sm transparent" onclick={() => autosave.retry()}><span class="txt">Retry</span></button>
                </span>
            {:else if saveState === "saved" && savedOnce}
                <span class="hp-save-state" in:fade={{ duration: 150 }}><i class="ri-check-line" aria-hidden="true"></i>Saved</span>
            {/if}
        </span>
    {/if}
{/snippet}

{#snippet statusLine()}
    <p class="hp-status txt-sm">
        {#if st.published}
            <span class="label success">Published</span> Version {st.published.number}, {when(st.published.published_at)}{#if st.published.published_by}, by {st.published.published_by}{/if}.
            {#if st.changed || !st.saved}<span class="label warning">Unpublished changes</span>{/if}
        {:else if st.saved}
            <span class="label warning">Not published</span> The storefront shows its built-in home page until you publish.
        {:else}
            <span class="label">Starting point</span> This is the storefront's built-in home page as sections. Change it, then publish.
        {/if}
    </p>
{/snippet}

{#snippet alerts()}
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
    {#if saveState === "conflict"}
        <div class="alert danger hp-alert" role="alert" in:rise>
            <div class="content"><p>This page was saved somewhere else — another tab, or somebody else — after you opened it, so your latest changes were not saved. Reload to see that version.</p></div>
            <button type="button" class="btn sm" onclick={reloadAfterConflict}><span class="txt">Reload</span></button>
        </div>
    {/if}
{/snippet}

{#snippet settingsBody(section, type)}
    <p class="txt-hint txt-sm m-b-sm">{type.description}</p>
    {#key section.id}
        <SectionFields
            fields={type.fields}
            idPrefix="hp-{section.id}"
            bind:settings={() => sections.find((s) => s.id === section.id)?.settings ?? {}, (v) => setSettings(section.id, v)}
        />
    {/key}
{/snippet}

<div class="page page-homepage shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <a class="breadcrumb-item tw:text-sm" href="{base}/dash/themes">Themes</a>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Homepage</h1>
            </nav>
            {#if !missing && readable && st}
                <div class="page-header-primary-btns hp-head-btns">
                    {@render saveStatus()}
                    {#if writable}
                        <span class="hp-undo">
                            <button type="button" class="btn sm circle transparent secondary" disabled={!canUndo} onclick={undo} aria-label="Undo" title="Undo (Ctrl+Z)">
                                <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                            </button>
                            <button type="button" class="btn sm circle transparent secondary" disabled={!canRedo} onclick={redo} aria-label="Redo" title="Redo (Ctrl+Shift+Z)">
                                <i class="ri-arrow-go-forward-line" aria-hidden="true"></i>
                            </button>
                        </span>
                    {/if}
                    <button type="button" class="btn transparent secondary" onclick={openHistory} aria-label="History" title="Published versions">
                        <i class="ri-history-line" aria-hidden="true"></i><span class="txt">History</span>
                    </button>
                    {#if writable}
                        <button type="button" class="btn transparent secondary" class:loading={busy === "reset"} disabled={!!busy || !st.saved || (!st.changed && st.published && saveState === "saved")} onclick={() => (confirmReset = true)} aria-label="Reset" title="Reset the draft">
                            <i class="ri-restart-line" aria-hidden="true"></i><span class="txt">Reset</span>
                        </button>
                        {#if narrow}
                            <button type="button" class="btn transparent secondary" class:loading={busy === "preview"} disabled={!!busy} onclick={openTab} aria-label="Open preview">
                                <i class="ri-eye-line" aria-hidden="true"></i><span class="txt">Open preview</span>
                            </button>
                        {/if}
                        <button type="button" class="btn" class:loading={busy === "publish"} disabled={!!busy || !hasChanges || saveState === "conflict"} onclick={() => (confirmPublish = true)} aria-label="Publish">
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
        {:else if st && narrow}
            <!-- ------------------------------------------------ the phone's list -->
            <div class="hp-wrap" in:rise>
                {@render statusLine()}
                {@render alerts()}
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
                                <button type="button" class="hp-text" disabled={!known || !writable} onclick={() => select(section.id)}>
                                    <span class="hp-label">{sum.label}</span>
                                    {#if sum.text}<span class="hp-sub txt-hint txt-sm">{sum.text}</span>{/if}
                                    {#if !section.enabled}<span class="label sm">Hidden</span>{/if}
                                </button>
                                {#if writable}
                                    <div class="hp-actions">
                                        <div class="field hp-toggle">
                                            <input type="checkbox" class="switch" id="hp-on-{section.id}" checked={section.enabled} onchange={() => edit(toggleSection(sections, i))} aria-label="Show {sum.label} on the storefront" />
                                            <label for="hp-on-{section.id}"></label>
                                        </div>
                                        <button type="button" class="btn sm circle transparent secondary" disabled={i === 0} onclick={() => move(i, -1)} aria-label="Move {sum.label} up">
                                            <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                        </button>
                                        <button type="button" class="btn sm circle transparent secondary" disabled={i === sections.length - 1} onclick={() => move(i, 1)} aria-label="Move {sum.label} down">
                                            <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                        </button>
                                        <button type="button" class="btn sm circle transparent secondary" disabled={!known} onclick={() => duplicate(i)} aria-label="Duplicate {sum.label}">
                                            <i class="ri-file-copy-line" aria-hidden="true"></i>
                                        </button>
                                        <button type="button" class="btn sm circle transparent danger" onclick={() => { removingId = section.id; confirmRemove = true; }} aria-label="Remove {sum.label}">
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
        {:else if st}
            <!-- ------------------------------------------------ the visual editor -->
            {@render alerts()}
            <div class="hp-editor" class:three={threeCol && !!selected} bind:clientWidth={editorWidth} in:rise>
                <aside class="hp-rail" aria-label="Sections">
                    {#if selected && selectedType && !threeCol}
                        {@render settingsPanel(false)}
                    {:else}
                        <div class="hp-rail-list" in:rise={{ y: 0, duration: 150 }}>
                            {@render statusLine()}
                            {#if !sections.length}
                                <p class="txt-hint txt-sm">The page has no sections. The storefront's home page would be empty.</p>
                            {/if}
                            <ol class="hp-rows" bind:this={listEl} aria-label="Sections, top to bottom" aria-describedby="hp-drag-help">
                                {#each sections as section, i (section.id)}
                                    {@const sum = sectionSummary(section, registry)}
                                    {@const known = !!sectionType(section.type, registry)}
                                    <li class="hp-rrow" data-row class:off={!section.enabled} class:selected={threeCol && selectedId === section.id} class:dragging={dragId === section.id} animate:flip={{ duration: dragId === section.id ? 0 : flipDuration() }}>
                                        {#if writable}
                                            <button
                                                type="button"
                                                class="hp-handle"
                                                aria-label="Move {sum.label}: drag, or use the arrow keys"
                                                onpointerdown={(e) => dragStart(e, section.id)}
                                                onpointermove={dragMove}
                                                onpointerup={dragEnd}
                                                onpointercancel={dragEnd}
                                                onkeydown={(e) => handleKey(e, i)}
                                            >
                                                <i class="ri-draggable" aria-hidden="true"></i>
                                            </button>
                                        {/if}
                                        <button type="button" class="hp-rtext" disabled={!known} onclick={() => select(section.id)}>
                                            <i class="hp-icon {sectionType(section.type, registry)?.icon ?? 'ri-question-line'}" aria-hidden="true"></i>
                                            <span class="hp-rwords">
                                                <span class="hp-label">{sum.label}</span>
                                                {#if sum.text}<span class="hp-sub txt-hint txt-sm">{sum.text}</span>{/if}
                                            </span>
                                        </button>
                                        {#if writable}
                                            <span class="hp-rbtns">
                                                <button type="button" class="btn sm circle transparent secondary" disabled={i === 0} onclick={() => move(i, -1)} aria-label="Move {sum.label} up">
                                                    <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                                </button>
                                                <button type="button" class="btn sm circle transparent secondary" disabled={i === sections.length - 1} onclick={() => move(i, 1)} aria-label="Move {sum.label} down">
                                                    <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                                </button>
                                                <button type="button" class="btn sm circle transparent secondary" onclick={() => edit(toggleSection(sections, i))} aria-label={section.enabled ? `Hide ${sum.label}` : `Show ${sum.label}`} aria-pressed={!section.enabled}>
                                                    <i class={section.enabled ? "ri-eye-line" : "ri-eye-off-line"} aria-hidden="true"></i>
                                                </button>
                                            </span>
                                        {/if}
                                    </li>
                                {/each}
                            </ol>
                            <p id="hp-drag-help" class="hp-sr">Drag a section by its handle to move it, or focus the handle and use the up and down arrow keys.</p>
                            {#if writable}
                                <button type="button" class="hp-add hp-add-end" onclick={() => openLibrary(selectedIndex >= 0 ? selectedIndex + 1 : sections.length)}>
                                    <i class="ri-add-line" aria-hidden="true"></i><span>Add section</span>
                                </button>
                            {/if}
                        </div>
                    {/if}
                </aside>

                <section class="hp-stage" aria-label="Preview">
                    <div class="hp-stage-bar">
                        <div class="hp-devices" role="group" aria-label="Preview width">
                            <button type="button" class="btn sm transparent secondary" class:active={device === "desktop"} aria-pressed={device === "desktop"} onclick={() => (device = "desktop")}>
                                <i class="ri-computer-line" aria-hidden="true"></i><span class="txt">Desktop</span>
                            </button>
                            <button type="button" class="btn sm transparent secondary" class:active={device === "mobile"} aria-pressed={device === "mobile"} onclick={() => (device = "mobile")}>
                                <i class="ri-smartphone-line" aria-hidden="true"></i><span class="txt">Mobile</span>
                            </button>
                        </div>
                        {#if address}<span class="hp-address txt-hint txt-sm" title={address}>{frameOrigin}</span>{/if}
                        <button type="button" class="btn sm circle transparent secondary" onclick={() => (askAddress = true)} aria-label="Change the storefront's address" title="Storefront address">
                            <i class="ri-link" aria-hidden="true"></i>
                        </button>
                        {#if writable}
                            <button type="button" class="btn sm circle transparent secondary" class:loading={busy === "preview"} disabled={!!busy} onclick={openTab} aria-label="Open the preview in a new tab" title="Open in a new tab">
                                <i class="ri-external-link-line" aria-hidden="true"></i>
                            </button>
                        {/if}
                    </div>
                    {#if frameBlocked}
                        <div class="hp-stage-note" in:rise>
                            <p>This panel cannot show {frameOrigin} in a frame: it is not one of this store's addresses. Add it as a domain of the store, or open the preview in a tab.</p>
                            <button type="button" class="btn sm" onclick={openTab}><span class="txt">Open preview</span></button>
                        </div>
                    {:else if !address && (previewKey || !writable)}
                        <div class="hp-stage-note" in:rise>
                            <p>This store has no web address yet, so there is nothing to show beside the sections. Type the storefront's address to preview it here.</p>
                            <button type="button" class="btn sm" onclick={() => (askAddress = true)}><span class="txt">Set the address</span></button>
                        </div>
                    {:else}
                        <ThemePreview
                            src={frameSrc}
                            origin={frameOrigin}
                            document={writable ? { theme, sections } : null}
                            selected={selectedId}
                            {savedTick}
                            {device}
                            onselect={(id) => select(id)}
                            onblocked={blocked}
                        />
                    {/if}
                </section>

                {#if threeCol && selected && selectedType}
                    <aside class="hp-inspector" aria-label="Section settings">
                        {@render settingsPanel(true)}
                    </aside>
                {/if}
            </div>
        {/if}
        <p class="hp-sr" aria-live="polite">{announce}</p>
    </div>
</div>

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

<Drawer open={narrow && !!selected && !!selectedType} size="md" title={selectedType?.label ?? "Section"} onclose={() => (selectedId = null)}>
    {#if narrow && selected && selectedType}
        {@render settingsBody(selected, selectedType)}
    {/if}
    {#snippet footer()}
        <span class="txt-hint txt-sm m-r-auto">Changes save as you make them.</span>
        <button type="button" class="btn" onclick={() => (selectedId = null)}><span class="txt">Done</span></button>
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

<Confirm bind:open={confirmRemove} title="Remove this section?" message="It leaves the draft. Undo brings it back; once published, an earlier version still has it." confirmLabel="Remove" danger onconfirm={remove} />

<Confirm
    bind:open={confirmPublish}
    title="Publish the home page?"
    message="Shoppers see this page from now on. You can roll back to an earlier version."
    confirmLabel="Publish"
    onconfirm={publish}
/>

<Confirm
    bind:open={confirmReset}
    title="Reset the draft?"
    message={st?.published ? "Your unpublished changes are thrown away and the draft is the published page again. The storefront does not change." : "Your changes are thrown away and the draft is the starting page again. The storefront does not change."}
    confirmLabel="Reset"
    danger
    onconfirm={reset}
/>

<Confirm
    bind:open={confirmRollback}
    title="Roll back to version {rollingBack?.number}?"
    message="It goes live again as a new version, and becomes the draft."
    confirmLabel="Roll back"
    onconfirm={rollback}
/>

<PromptDialog
    open={askAddress}
    title="Where is the storefront?"
    message="The preview shows your storefront's home page. Type its address once; this browser remembers it for this store. Leave it empty to use the store's own address."
    label="Storefront address"
    placeholder="https://shop.example.com"
    initial={typedAddress || storefront}
    confirmLabel="Use this address"
    onclose={() => (askAddress = false)}
    onconfirm={useAddress}
/>

<style>
    .hp-wrap {
        max-width: 860px;
    }
    .hp-head-btns {
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
    }
    .hp-undo {
        display: inline-flex;
        gap: 2px;
    }
    .hp-save {
        display: inline-flex;
        align-items: center;
        min-height: 28px;
        color: var(--txtHintColor);
    }
    .hp-save-state {
        display: inline-flex;
        align-items: center;
        gap: 4px;
    }
    .hp-save-state.danger {
        color: var(--dangerColor);
    }
    .hp-spin {
        display: inline-block;
        animation: hp-spin 900ms linear infinite;
    }
    @keyframes hp-spin {
        to {
            transform: rotate(360deg);
        }
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
        flex: 1 1 200px;
    }
    .hp-empty {
        padding: 12px 0;
    }
    .hp-list,
    .hp-rows,
    .hp-library,
    .hp-revisions {
        list-style: none;
        margin: 0;
        padding: 0;
    }

    /* ---------------------------------------------- the visual editor */
    .hp-editor {
        flex: 1 1 auto;
        display: grid;
        grid-template-columns: minmax(280px, 340px) minmax(0, 1fr);
        gap: 16px;
        min-height: 520px;
    }
    .hp-editor.three {
        grid-template-columns: minmax(260px, 300px) minmax(0, 1fr) minmax(280px, 320px);
    }
    .hp-inspector {
        min-height: 0;
        overflow: auto;
        padding-left: 4px;
        animation: hp-slide-in 200ms ease-out;
    }
    @keyframes hp-slide-in {
        from {
            opacity: 0;
            transform: translateX(12px);
        }
    }
    .hp-rrow.selected {
        border-color: var(--primaryColor, var(--txtHintColor));
        box-shadow: 0 0 0 1px var(--primaryColor, var(--txtHintColor));
    }
    @media (prefers-reduced-motion: reduce) {
        .hp-inspector {
            animation: none;
        }
    }
    .hp-rail {
        min-height: 0;
        overflow: auto;
        padding-right: 4px;
    }
    .hp-rows {
        position: relative;
        display: flex;
        flex-direction: column;
        gap: 6px;
    }
    .hp-rrow {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--cardFace);
        transition:
            opacity 180ms ease-out,
            border-color 150ms ease-out,
            transform 150ms ease-out;
    }
    .hp-rrow.off .hp-rwords {
        opacity: 0.55;
    }
    .hp-rrow.dragging {
        position: relative;
        z-index: 1;
        border-color: var(--txtHintColor);
        transform: scale(1.02);
    }
    .hp-handle {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 32px;
        border: 0;
        border-radius: var(--borderRadius);
        background: none;
        color: var(--txtHintColor);
        cursor: grab;
        touch-action: none;
        transition: background-color 150ms ease-out;
    }
    .hp-handle:hover {
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.04));
    }
    .hp-rrow.dragging .hp-handle {
        cursor: grabbing;
    }
    .hp-rtext {
        flex: 1 1 auto;
        min-width: 0;
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 4px 6px;
        border: 0;
        border-radius: var(--borderRadius);
        background: none;
        text-align: left;
        cursor: pointer;
        transition: background-color 150ms ease-out;
    }
    .hp-rtext:hover:not(:disabled) {
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.04));
    }
    .hp-rtext:active:not(:disabled) {
        transform: scale(0.99);
    }
    .hp-rwords {
        display: flex;
        flex-direction: column;
        min-width: 0;
        transition: opacity 180ms ease-out;
    }
    .hp-rwords .hp-sub {
        overflow: hidden;
        white-space: nowrap;
        text-overflow: ellipsis;
    }
    /* Quiet until the row is wanted; always there on a touch screen. */
    .hp-rbtns {
        display: inline-flex;
        opacity: 0;
        transition: opacity 150ms ease-out;
    }
    .hp-rrow:hover .hp-rbtns,
    .hp-rrow:focus-within .hp-rbtns,
    .hp-rrow.off .hp-rbtns {
        opacity: 1;
    }
    @media (hover: none) {
        .hp-rbtns {
            opacity: 1;
        }
    }
    .hp-add-end {
        margin-top: 10px;
    }
    .hp-settings-head {
        display: flex;
        align-items: center;
        gap: 4px;
        margin-bottom: 8px;
    }
    .hp-settings-title {
        flex: 1 1 auto;
        min-width: 0;
        margin: 0;
        font-size: 1rem;
        font-weight: 600;
    }
    .hp-stage {
        display: flex;
        flex-direction: column;
        min-height: 0;
        min-width: 0;
    }
    .hp-stage-bar {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 8px;
    }
    .hp-devices {
        display: inline-flex;
        gap: 2px;
        margin-right: auto;
    }
    .hp-devices .btn.active {
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.06));
        color: var(--txtPrimaryColor);
    }
    .hp-address {
        overflow: hidden;
        white-space: nowrap;
        text-overflow: ellipsis;
        max-width: 40%;
    }
    .hp-stage-note {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 10px;
        padding: 16px;
        border: 1px dashed var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
    }
    .hp-stage-note p {
        margin: 0;
        max-width: 60ch;
    }

    /* Read out, not shown: the panel's sheets have no such class. */
    .hp-sr {
        position: absolute;
        width: 1px;
        height: 1px;
        padding: 0;
        margin: -1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
        border: 0;
    }

    /* ---------------------------------------------- the phone's list */
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
    .hp-rtext:focus-visible,
    .hp-handle:focus-visible,
    .hp-add:focus-visible,
    .hp-type:focus-visible {
        outline: 2px solid var(--markColor);
        outline-offset: 2px;
    }
    .hp-text:disabled,
    .hp-rtext:disabled {
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
        opacity: 0.75;
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
        .hp-rtext,
        .hp-row,
        .hp-rrow,
        .hp-rwords,
        .hp-rbtns,
        .hp-handle {
            transition: none;
        }
        .hp-add:active,
        .hp-type:active,
        .hp-rtext:active:not(:disabled),
        .hp-rrow.dragging {
            transform: none;
        }
        .hp-spin {
            animation: none;
        }
    }
</style>
