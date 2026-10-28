<script>
    /**
     * Themes › a page (ext/layouts, D125, D126, D129): the storefront's home
     * page, or the template every product or every collection is drawn in, as
     * the shop arranges it — one element of the store's theme. A product or
     * collection page has a core the catalogue draws (its pictures, price and
     * buy box; its grid), which the page always holds once: the shop moves
     * it, and arranges everything else around it.
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
     *
     * Theme settings (D127) — the store's colours, fonts and corner rounding —
     * are part of the home page's draft: they save, undo, preview and publish
     * with it, and every page of the storefront applies them once published.
     *
     * Any page's draft can also be set to publish at a time (D129).
     *
     * The header and the footer (D131) open here too: each is one section,
     * the storefront's own, drawn on every page, so the screen opens straight
     * on its settings and offers nothing to add, move or remove. The preview
     * is the home page, which shows both.
     */
    import { onMount, untrack } from "svelte";
    import { page as pageState } from "$app/state";
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
        themeWarnings,
        pageMeta,
        libraryFor,
        isFixed,
        previewPath,
        localInputValue,
        fromLocalInput,
        scheduleLine,
        isChrome,
    } from "$lib/layouts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise, flipDuration } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import PromptDialog from "$lib/components/PromptDialog.svelte";
    import SectionFields from "$lib/components/SectionFields.svelte";
    import ThemePreview from "$lib/components/ThemePreview.svelte";
    import PreviewSubject from "$lib/components/PreviewSubject.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import { titled } from "$lib/brand.svelte.js";

    /** @type {{ page: "home" | "product" | "collection" | "header" | "footer" }} */
    let { page } = $props();
    // Each page's screen mounts an editor of its own, so the page never
    // changes under one.
    const PAGE = untrack(() => page);
    const meta = pageMeta(PAGE);
    // The design tokens publish with the home page alone (D127).
    const HOME = PAGE === "home";
    // The header or footer: one section of settings, nothing to arrange.
    const CHROME = isChrome(PAGE);
    const missing = $derived(modulesKnown() && !hasModule("layouts"));
    const readable = $derived(can("layouts.read"));
    const writable = $derived(can("layouts.write"));

    let registry = $state([]);
    let pageInfos = $state([]); // the engine's pages: which have a core, which are only that
    let tokenGroups = $state([]); // the theme settings' fields, in titled groups
    let themeSettings = $state({});
    let st = $state(null); // the engine's view: version, published revision, …
    let sections = $state([]);
    let theme = $state("default");
    let pluginOn = $state(true);
    let loading = $state(true);
    let busy = $state(""); // "publish" | "preview" | "rollback" | "switch" | "reset"

    const draftDoc = () =>
        HOME
            ? { theme, theme_settings: JSON.parse(JSON.stringify(themeSettings)), sections: JSON.parse(JSON.stringify(sections)) }
            : { theme, sections: JSON.parse(JSON.stringify(sections)) };
    const docJSON = () => JSON.stringify(HOME ? { theme, theme_settings: themeSettings, sections } : { theme, sections });

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

    function editTheme(next) {
        history.record(docJSON(), "theme-settings");
        historyTick++;
        themeSettings = next;
        autosave.change(draftDoc());
    }

    function restore(json) {
        const doc = JSON.parse(json);
        theme = doc.theme || "default";
        themeSettings = doc.theme_settings ?? {};
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
        themeSettings = JSON.parse(JSON.stringify(state.document?.theme_settings ?? {}));
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
            pageInfos = reg?.pages ?? [];
            // A font or rounding left empty is the storefront's own, so each
            // choice list starts with that.
            tokenGroups = (reg?.theme_settings ?? []).map((g) => ({
                ...g,
                fields: g.fields.map((f) => (f.kind === "select" ? { ...f, options: [{ value: "", label: "Storefront's own" }, ...(f.options ?? [])] } : f)),
            }));
            adopt(state);
            autosave.reset();
            // There is nothing to choose between: open its settings.
            if (CHROME && sections.length) selectedId = sections[0].id;
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
        // The header's or footer's one section stays open: load picked it.
        if (!CHROME) selectedId = null;
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
    // The theme settings open in the same place as a section's, one at a time.
    // ?panel=theme (the Themes screen's link) opens them on arrival.
    let themeOpen = $state(HOME && pageState.url.searchParams.get("panel") === "theme");
    const panelOpen = $derived(themeOpen || !!(selected && selectedType));
    const warnings = $derived(themeWarnings(themeSettings));

    function select(id) {
        themeOpen = false;
        selectedId = id;
    }

    function openTheme() {
        selectedId = null;
        themeOpen = true;
    }

    function closePanel() {
        selectedId = null;
        themeOpen = false;
    }

    // Nothing can be added to the header or the footer.
    const addable = $derived(!CHROME);

    let inserting = $state(null); // the index a new section goes in at
    let filter = $state("");
    const library = $derived(
        libraryFor(registry, PAGE, pageInfos).filter((t) => {
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
        setTimeout(() => select(section.id), flipDuration() ? 200 : 0);
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
        if (isFixed(sections[i], registry)) return;
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

    // ------------------------------------------------------------ scheduling (D129)

    let scheduleOpen = $state(false);
    let scheduleAt = $state("");
    let confirmCancelSchedule = $state(false);
    const scheduleTime = $derived(fromLocalInput(scheduleAt));
    const minSchedule = $derived(scheduleOpen ? localInputValue(new Date()) : "");

    function openSchedule() {
        // The time already set, or the top of the next hour but one: a time
        // that needs changing rather than one that is about to go by.
        const next = new Date();
        next.setHours(next.getHours() + 2, 0, 0, 0);
        scheduleAt = localInputValue(st.scheduled ? new Date(st.scheduled.publish_at) : next);
        scheduleOpen = true;
    }

    async function schedule() {
        const at = scheduleTime;
        if (!at || at.getTime() <= Date.now()) {
            toast.error("Choose a time in the future.");
            return;
        }
        busy = "schedule";
        try {
            if (!(await settled())) return;
            st = await api.schedule(PAGE, st.version, at.toISOString());
            scheduleOpen = false;
            toast.success(`Scheduled: the ${meta.noun} publishes ${when(at)}`);
        } catch (err) {
            if (err?.status === 409) saveState = "conflict";
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    async function cancelSchedule() {
        busy = "schedule";
        try {
            st = { ...(await api.cancelSchedule(PAGE)), document: st.document };
            toast.success("The schedule is cancelled. Nothing publishes by itself.");
        } catch (err) {
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
            toast.success("The editor is on. The storefront shows what you publish; until then, the starting home page and its own other pages.");
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
    // The product or collection a template is shown on: one of the store's
    // own, which the shop picks; the home page needs none.
    let subject = $state(null);
    let subjectStatus = $state("loading"); // "loading" | "empty" | "ready"
    const path = $derived(previewPath(PAGE, subject));
    const frameSrc = $derived(
        !path
            ? ""
            : writable && previewKey && address
              ? previewAddress(address, previewKey, location.origin, path, CHROME ? PAGE : "")
              : !writable && frameOrigin
                ? frameOrigin + path
                : "",
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
            if (!path) {
                tab?.close();
                toast.error(`Pick a ${PAGE} to preview the page on.`);
                return;
            }
            const res = await api.preview(PAGE);
            const where = rememberedAddress() || res.storefront;
            if (!where) {
                tab?.close();
                askAddress = true;
                return;
            }
            const url = previewAddress(where, res.token, "", path, CHROME ? PAGE : "");
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

<svelte:head><title>{titled(`${meta.title} · Themes`)}</title></svelte:head>
<svelte:window {onkeydown} />

{#snippet settingsPanel(beside)}
    {@const sum = sectionSummary(selected, registry)}
    <div class="hp-settings" in:rise={{ y: 0, duration: 150 }}>
        <div class="hp-settings-head">
            <button type="button" class="btn sm circle transparent secondary" onclick={closePanel} aria-label={beside ? "Close the settings" : "Back to the sections"}>
                <i class={beside ? "ri-close-line" : "ri-arrow-left-line"} aria-hidden="true"></i>
            </button>
            <h2 class="hp-settings-title">{sum.label}</h2>
            {#if writable && !selectedType.fixed}
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

{#snippet themePanel(beside)}
    <div class="hp-settings" in:rise={{ y: 0, duration: 150 }}>
        <div class="hp-settings-head">
            <button type="button" class="btn sm circle transparent secondary" onclick={closePanel} aria-label={beside ? "Close the theme settings" : "Back to the sections"}>
                <i class={beside ? "ri-close-line" : "ri-arrow-left-line"} aria-hidden="true"></i>
            </button>
            <h2 class="hp-settings-title">Theme settings</h2>
        </div>
        {@render themeBody()}
    </div>
{/snippet}

{#snippet themeBody()}
    <p class="txt-hint txt-sm m-b-sm">Colours, fonts and corners for every page of the storefront, published with the home page. Leave one empty to keep the storefront's own.</p>
    {#if warnings.length}
        <div class="alert warning hp-contrast" role="status" in:rise>
            <i class="ri-contrast-2-line" aria-hidden="true"></i>
            <div class="content">
                {#each warnings as w (w.keys.join("-"))}<p>{w.message}</p>{/each}
            </div>
        </div>
    {/if}
    {#each tokenGroups as g, gi (g.label)}
        <fieldset class="hp-group" disabled={!writable}>
            <legend>{g.label}</legend>
            <SectionFields fields={g.fields} idPrefix="hp-theme-{gi}" bind:settings={() => themeSettings, (v) => editTheme(v)} />
        </fieldset>
    {/each}
{/snippet}

{#snippet themeRow()}
    <button type="button" class="hp-theme-row" class:selected={themeOpen} onclick={openTheme} aria-pressed={themeOpen}>
        <i class="ri-palette-line hp-icon" aria-hidden="true"></i>
        <span class="hp-rwords">
            <span class="hp-label">Theme settings</span>
            <span class="hp-sub txt-hint txt-sm">Colours, fonts and corners</span>
        </span>
        {#if Object.keys(themeSettings).length}
            <span class="hp-swatches" aria-hidden="true">
                {#each ["background", "text", "accent", "muted"].filter((k) => themeSettings[k]) as k (k)}
                    <span class="hp-dot" style:background={themeSettings[k]}></span>
                {/each}
            </span>
        {/if}
        <i class="ri-arrow-right-s-line hp-chev" aria-hidden="true"></i>
    </button>
{/snippet}

{#snippet gap(index)}
    {#if writable && addable}
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
            <!-- The owner, 2026-10-28: an unpublished store shows the starting home page (D140). -->
            <span class="label warning">Not published</span> The storefront shows {page === "home" ? "the starting home page" : `its built-in ${meta.noun}`} until you publish.
        {:else if CHROME}
            <span class="label">Starting point</span> These are the settings of the storefront's own {meta.noun}. Change them, then publish to show them on every page.
        {:else if page === "home"}
            <!-- D140: the home page starts from its own look, not a copy of the built-in one. -->
            <span class="label">Starting point</span> Your storefront shows this page until you publish your own. Swap in your pictures, make the benefits match your policies, then publish.
        {:else}
            <span class="label">Starting point</span> This is the storefront's built-in {meta.noun} as sections. Change it, then publish.
        {/if}
    </p>
    {#if st.scheduled}
        <div class="hp-scheduled txt-sm" class:outdated={st.scheduled.outdated} role="status" in:rise>
            <i class="ri-calendar-schedule-line" aria-hidden="true"></i>
            <span class="hp-scheduled-text">{scheduleLine(st.scheduled, when)}</span>
            {#if writable}
                <span class="hp-scheduled-btns">
                    <button type="button" class="btn sm transparent secondary" disabled={!!busy} onclick={openSchedule}><span class="txt">Change</span></button>
                    <button type="button" class="btn sm transparent danger" disabled={!!busy} onclick={() => (confirmCancelSchedule = true)}><span class="txt">Cancel it</span></button>
                </span>
            {/if}
        </div>
    {/if}
{/snippet}

{#snippet alerts()}
    {#if !pluginOn}
        <div class="alert warning hp-alert" role="status">
            <i class="ri-information-line" aria-hidden="true"></i>
            <div class="content">
                <p>The editor is switched off, so the storefront shows its built-in {meta.noun} even after you publish.</p>
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
    {#if type.fixed && !type.fields?.length}
        <p class="hp-fixed-note txt-sm" in:rise>
            <i class="ri-lock-line" aria-hidden="true"></i>
            Always on the {meta.noun}. What it shows is each {PAGE}'s own, from the catalogue; change it there.
        </p>
    {:else if !type.fields?.length}
        <p class="txt-hint txt-sm">This section has nothing to set: move it, hide it or remove it.</p>
    {/if}
    {#key section.id}
        <SectionFields
            fields={type.fields ?? []}
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
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{meta.title}</h1>
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
                        <button type="button" class="btn transparent secondary" class:loading={busy === "schedule"} disabled={!!busy || saveState === "conflict"} onclick={openSchedule} aria-label="Schedule" title="Publish at a time you choose">
                            <i class="ri-calendar-schedule-line" aria-hidden="true"></i><span class="txt">Schedule</span>
                        </button>
                        <button type="button" class="btn" class:loading={busy === "publish"} disabled={!!busy || !hasChanges || saveState === "conflict"} onclick={() => (confirmPublish = true)} aria-label="Publish">
                            <i class="ri-upload-cloud-2-line" aria-hidden="true"></i><span class="txt">Publish</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing module="layouts" what="The theme editor is ext/layouts, and this binary does not have it. Start it with -layouts." />
        {:else if !readable}
            <NoAccess right="layouts.read" what="the theme editor" />
        {:else if loading && !st}
            <div class="hp-wrap"><span class="skeleton-loader"></span></div>
        {:else if st && narrow}
            <!-- ------------------------------------------------ the phone's list -->
            <div class="hp-wrap" in:rise>
                {@render statusLine()}
                {@render alerts()}
                {#if HOME}
                    {@render themeRow()}
                {:else if writable && !CHROME}
                    <div class="hp-subject-row">
                        <span class="txt-hint txt-sm">Preview on</span>
                        <PreviewSubject kind={PAGE} bind:subject bind:status={subjectStatus} />
                    </div>
                {/if}
                {#if !sections.length}
                    <div class="hp-empty">
                        <p class="txt-hint">The page has no sections. The storefront's {meta.noun} would be empty.</p>
                    </div>
                {/if}
                <ol class="hp-list" aria-label="Sections, top to bottom">
                    {#if writable}<li class="hp-item">{@render gap(0)}</li>{/if}
                    {#each sections as section, i (section.id)}
                        {@const sum = sectionSummary(section, registry)}
                        {@const known = !!sectionType(section.type, registry)}
                        {@const fixed = isFixed(section, registry)}
                        <li class="hp-item" animate:flip={{ duration: flipDuration() }} in:rise>
                            <div class="hp-row" class:off={!section.enabled} class:fixed>
                                <span class="hp-num txt-hint" aria-hidden="true">{i + 1}</span>
                                <i class="hp-icon {sectionType(section.type, registry)?.icon ?? 'ri-question-line'}" aria-hidden="true"></i>
                                <button type="button" class="hp-text" disabled={!known || !writable} onclick={() => select(section.id)}>
                                    <span class="hp-label">{sum.label}{#if fixed}<i class="ri-lock-line hp-lock" aria-label="always on the page"></i>{/if}</span>
                                    {#if sum.text}<span class="hp-sub txt-hint txt-sm">{sum.text}</span>{/if}
                                    {#if !section.enabled}<span class="label sm">Hidden</span>{/if}
                                </button>
                                {#if writable && addable}
                                    <div class="hp-actions">
                                        {#if !fixed}
                                            <div class="field hp-toggle">
                                                <input type="checkbox" class="switch" id="hp-on-{section.id}" checked={section.enabled} onchange={() => edit(toggleSection(sections, i))} aria-label="Show {sum.label} on the storefront" />
                                                <label for="hp-on-{section.id}"></label>
                                            </div>
                                        {/if}
                                        <button type="button" class="btn sm circle transparent secondary" disabled={i === 0} onclick={() => move(i, -1)} aria-label="Move {sum.label} up">
                                            <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                        </button>
                                        <button type="button" class="btn sm circle transparent secondary" disabled={i === sections.length - 1} onclick={() => move(i, 1)} aria-label="Move {sum.label} down">
                                            <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                        </button>
                                        {#if !fixed}
                                            <button type="button" class="btn sm circle transparent secondary" disabled={!known} onclick={() => duplicate(i)} aria-label="Duplicate {sum.label}">
                                                <i class="ri-file-copy-line" aria-hidden="true"></i>
                                            </button>
                                            <button type="button" class="btn sm circle transparent danger" onclick={() => { removingId = section.id; confirmRemove = true; }} aria-label="Remove {sum.label}">
                                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                            </button>
                                        {/if}
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
            <div class="hp-editor" class:three={threeCol && panelOpen} bind:clientWidth={editorWidth} in:rise>
                <aside class="hp-rail" aria-label="Sections">
                    {#if panelOpen && !threeCol}
                        {#if themeOpen}{@render themePanel(false)}{:else}{@render settingsPanel(false)}{/if}
                    {:else}
                        <div class="hp-rail-list" in:rise={{ y: 0, duration: 150 }}>
                            {@render statusLine()}
                            {#if HOME}{@render themeRow()}{/if}
                            {#if !sections.length}
                                <p class="txt-hint txt-sm">The page has no sections. The storefront's {meta.noun} would be empty.</p>
                            {/if}
                            <ol class="hp-rows" bind:this={listEl} aria-label="Sections, top to bottom" aria-describedby="hp-drag-help">
                                {#each sections as section, i (section.id)}
                                    {@const sum = sectionSummary(section, registry)}
                                    {@const known = !!sectionType(section.type, registry)}
                                    {@const fixed = isFixed(section, registry)}
                                    <li class="hp-rrow" data-row class:off={!section.enabled} class:fixed class:selected={threeCol && selectedId === section.id} class:dragging={dragId === section.id} animate:flip={{ duration: dragId === section.id ? 0 : flipDuration() }}>
                                        {#if writable && addable}
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
                                                <span class="hp-label">{sum.label}{#if fixed}<i class="ri-lock-line hp-lock" aria-label="always on the page"></i>{/if}</span>
                                                {#if sum.text}<span class="hp-sub txt-hint txt-sm">{sum.text}</span>{/if}
                                            </span>
                                        </button>
                                        {#if writable && addable}
                                            <span class="hp-rbtns">
                                                <button type="button" class="btn sm circle transparent secondary" disabled={i === 0} onclick={() => move(i, -1)} aria-label="Move {sum.label} up">
                                                    <i class="ri-arrow-up-line" aria-hidden="true"></i>
                                                </button>
                                                <button type="button" class="btn sm circle transparent secondary" disabled={i === sections.length - 1} onclick={() => move(i, 1)} aria-label="Move {sum.label} down">
                                                    <i class="ri-arrow-down-line" aria-hidden="true"></i>
                                                </button>
                                                {#if !fixed}
                                                    <button type="button" class="btn sm circle transparent secondary" onclick={() => edit(toggleSection(sections, i))} aria-label={section.enabled ? `Hide ${sum.label}` : `Show ${sum.label}`} aria-pressed={!section.enabled}>
                                                        <i class={section.enabled ? "ri-eye-line" : "ri-eye-off-line"} aria-hidden="true"></i>
                                                    </button>
                                                {/if}
                                            </span>
                                        {/if}
                                    </li>
                                {/each}
                            </ol>
                            <p id="hp-drag-help" class="hp-sr">Drag a section by its handle to move it, or focus the handle and use the up and down arrow keys.</p>
                            {#if writable && addable}
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
                        {#if !HOME && !CHROME}
                            <PreviewSubject kind={PAGE} bind:subject bind:status={subjectStatus} />
                        {:else if address}
                            <span class="hp-address txt-hint txt-sm" title={address}>{frameOrigin}</span>
                        {/if}
                        <button type="button" class="btn sm circle transparent secondary" onclick={() => (askAddress = true)} aria-label="Change the storefront's address" title="Storefront address">
                            <i class="ri-link" aria-hidden="true"></i>
                        </button>
                        {#if writable}
                            <button type="button" class="btn sm circle transparent secondary" class:loading={busy === "preview"} disabled={!!busy} onclick={openTab} aria-label="Open the preview in a new tab" title="Open in a new tab">
                                <i class="ri-external-link-line" aria-hidden="true"></i>
                            </button>
                        {/if}
                    </div>
                    {#if !HOME && !CHROME && subjectStatus === "empty"}
                        <div class="hp-stage-note" in:rise>
                            <p>The store has no {PAGE === "product" ? "active products" : "collections"} yet, so there is nothing to show the {meta.noun} on. The page still saves and publishes as you arrange it.</p>
                        </div>
                    {:else if frameBlocked}
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
                            document={writable ? (HOME ? { theme, theme_settings: themeSettings, sections } : { theme, sections }) : null}
                            selected={selectedId}
                            {savedTick}
                            {device}
                            onselect={(id) => select(id)}
                            onblocked={blocked}
                        />
                    {/if}
                </section>

                {#if threeCol && panelOpen}
                    <aside class="hp-inspector" aria-label={themeOpen ? "Theme settings" : "Section settings"}>
                        {#if themeOpen}{@render themePanel(true)}{:else}{@render settingsPanel(true)}{/if}
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

<Drawer open={narrow && panelOpen} size="md" title={themeOpen ? "Theme settings" : (selectedType?.label ?? "Section")} onclose={closePanel}>
    {#if narrow && themeOpen}
        {@render themeBody()}
    {:else if narrow && selected && selectedType}
        {@render settingsBody(selected, selectedType)}
    {/if}
    {#snippet footer()}
        <span class="txt-hint txt-sm m-r-auto">Changes save as you make them.</span>
        <button type="button" class="btn" onclick={closePanel}><span class="txt">Done</span></button>
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
                            {when(rev.published_at)}{#if rev.scheduled} · on schedule{/if}{#if rev.published_by} · {rev.scheduled ? "set by " : ""}{rev.published_by}{/if} · {rev.sections} sections{#if rev.restored_from} · restored from version {rev.restored_from}{/if}
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
    title="Publish the {meta.noun}?"
    message={HOME
        ? "Shoppers see this page from now on. You can roll back to an earlier version."
        : CHROME
          ? `Every page of the storefront shows this ${PAGE} from now on. You can roll back to an earlier version.`
          : `Every ${PAGE} is shown this way from now on. You can roll back to an earlier version.`}
    confirmLabel="Publish"
    onconfirm={publish}
/>

<Drawer open={scheduleOpen} size="md" title="Schedule publishing" onclose={() => (scheduleOpen = false)}>
    <p class="txt-hint txt-sm m-b-sm">
        The draft as it is now goes live at this time, within a minute of it. Later edits keep saving to the draft but are not in it; schedule again to include them.
    </p>
    <div class="field">
        <label for="hp-schedule-at">Publish at</label>
        <input id="hp-schedule-at" type="datetime-local" min={minSchedule} bind:value={scheduleAt} />
    </div>
    <p class="txt-hint txt-sm hp-zone">
        {#if scheduleTime}{when(scheduleTime)}, this computer's time ({Intl.DateTimeFormat().resolvedOptions().timeZone}).{:else}Choose a date and a time.{/if}
    </p>
    {#if st?.scheduled}
        <p class="txt-sm">This replaces the schedule already set for {when(st.scheduled.publish_at)}.</p>
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent secondary m-r-auto" onclick={() => (scheduleOpen = false)}><span class="txt">Close</span></button>
        <button type="button" class="btn" class:loading={busy === "schedule"} disabled={!!busy || !scheduleTime} onclick={schedule}>
            <i class="ri-calendar-schedule-line" aria-hidden="true"></i><span class="txt">Schedule</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmCancelSchedule}
    title="Cancel the schedule?"
    message="Nothing publishes by itself. The draft and the storefront stay as they are."
    confirmLabel="Cancel the schedule"
    danger
    onconfirm={cancelSchedule}
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
    message="The preview shows your storefront. Type its address once; this browser remembers it for this store. Leave it empty to use the store's own address."
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

    /* ---------------------------------------------- theme settings */
    .hp-theme-row {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        min-height: 48px;
        margin: 0 0 10px;
        padding: 8px 10px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--cardFace);
        color: inherit;
        text-align: left;
        cursor: pointer;
        transition:
            border-color 150ms ease-out,
            background-color 150ms ease-out,
            transform 150ms ease-out;
    }
    .hp-theme-row:hover {
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.04));
    }
    .hp-theme-row:active {
        transform: scale(0.99);
    }
    .hp-theme-row:focus-visible {
        outline: 2px solid var(--markColor);
        outline-offset: 2px;
    }
    .hp-theme-row.selected {
        border-color: var(--primaryColor, var(--txtHintColor));
        box-shadow: 0 0 0 1px var(--primaryColor, var(--txtHintColor));
    }
    .hp-theme-row .hp-rwords {
        flex: 1 1 auto;
    }
    .hp-swatches {
        display: inline-flex;
        padding-left: 4px;
    }
    .hp-dot {
        width: 16px;
        height: 16px;
        margin-left: -4px;
        border: 2px solid var(--cardFace);
        border-radius: 50%;
        box-shadow: 0 0 0 1px var(--surfaceAlt2Color);
    }
    .hp-chev {
        color: var(--txtHintColor);
        transition: transform 150ms ease-out;
    }
    .hp-theme-row:hover .hp-chev {
        transform: translateX(2px);
    }
    .hp-group {
        margin: 0 0 14px;
        padding: 0;
        border: 0;
        min-width: 0;
    }
    .hp-group legend {
        margin-bottom: 8px;
        padding: 0;
        font-weight: 600;
        font-size: 0.8rem;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        color: var(--txtHintColor);
    }
    .hp-contrast {
        display: flex;
        gap: 8px;
        margin-bottom: 12px;
    }
    .hp-contrast p {
        margin: 0;
    }
    .hp-contrast p + p {
        margin-top: 4px;
    }
    @media (prefers-reduced-motion: reduce) {
        .hp-theme-row,
        .hp-chev {
            transition: none;
        }
        .hp-theme-row:active,
        .hp-theme-row:hover .hp-chev {
            transform: none;
        }
    }

    /* ---------------------------------------------- page templates and schedules (D129) */
    .hp-scheduled {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px 8px;
        margin: -4px 0 12px;
        padding: 8px 10px;
        border: 1px solid var(--surfaceAlt2Color);
        border-left: 3px solid var(--infoColor, var(--txtHintColor));
        border-radius: var(--borderRadius);
        background: var(--cardFace);
    }
    .hp-scheduled.outdated {
        border-left-color: var(--warningColor, var(--txtHintColor));
    }
    .hp-scheduled-text {
        flex: 1 1 200px;
        min-width: 0;
    }
    .hp-scheduled-btns {
        display: inline-flex;
        gap: 2px;
    }
    .hp-fixed-note {
        display: flex;
        gap: 6px;
        align-items: flex-start;
        margin: 0 0 12px;
        padding: 8px 10px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color, rgba(0, 0, 0, 0.04));
    }
    .hp-lock {
        margin-left: 6px;
        font-size: 0.85em;
        font-weight: 400;
        color: var(--txtHintColor);
    }
    .hp-rrow.fixed,
    .hp-row.fixed {
        border-style: dashed;
    }
    .hp-subject-row {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px;
        margin: 0 0 12px;
    }
    .hp-zone {
        margin: 6px 0 12px;
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
