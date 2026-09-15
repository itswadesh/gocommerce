<script>
    /**
     * FAQ: the questions this shop is asked often, and its answers.
     *
     * The page reads as a sequence, so the screen is that sequence: the
     * sections in the order they appear, each question under its section
     * with the buttons to move it. Order is saved whole, the way the Menus
     * screen saves a tree — one request per moved row would leave the list
     * half-sorted if the second failed.
     */
    import { api, can, request } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    const missing = $derived(modulesKnown() && !hasModule("faq"));

    let entries = $state([]);
    let loading = $state(true);
    let saving = $state(false);
    let editing = $state(null);
    let form = $state({ question: "", answer: "", section: "", published: true });
    let open = $state(false);
    let removing = $state(null);

    $effect(() => {
        if (readable && hasModule("faq")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get("/api/admin/x/faq");
            entries = result.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* The list as the page reads it: sections in the order they first
       appear, never alphabetical — the arrangement is the shop's. */
    const sections = $derived.by(() => {
        const out = [];
        const at = new Map();
        for (const e of entries) {
            if (!at.has(e.section)) {
                at.set(e.section, out.length);
                out.push({ name: e.section, entries: [] });
            }
            out[at.get(e.section)].entries.push(e);
        }
        return out;
    });
    const knownSections = $derived([...new Set(entries.map((e) => e.section).filter(Boolean))]);
    const publishedCount = $derived(entries.filter((e) => e.published).length);

    function add() {
        editing = null;
        form = { question: "", answer: "", section: "", published: true };
        open = true;
    }
    function edit(e) {
        editing = e;
        form = { question: e.question, answer: e.answer, section: e.section, published: e.published };
        open = true;
    }

    async function save() {
        if (!form.question.trim() || !form.answer.trim()) {
            return toast.error("A question needs an answer — an unanswered one helps nobody.");
        }
        saving = true;
        try {
            const body = {
                question: form.question.trim(),
                answer: form.answer.trim(),
                section: form.section.trim(),
                published: form.published,
            };
            if (editing) await request("PATCH", `/api/admin/x/faq/${editing.id}`, { body });
            else await request("POST", "/api/admin/x/faq", { body });
            toast.success(editing ? "Saved" : "Added");
            open = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove() {
        if (!removing) return;
        try {
            await request("DELETE", `/api/admin/x/faq/${removing.id}`);
            toast.success("Removed");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            removing = null;
        }
    }

    async function togglePublished(e) {
        try {
            await request("PATCH", `/api/admin/x/faq/${e.id}`, {
                body: { question: e.question, answer: e.answer, section: e.section, published: !e.published },
            });
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    /* Moving is a swap inside the section, then the whole order saved —
       the API takes every entry, so the screen sends every entry. */
    async function move(entry, by) {
        const group = sections.find((s) => s.name === entry.section);
        const i = group.entries.indexOf(entry);
        const j = i + by;
        if (j < 0 || j >= group.entries.length) return;
        const reordered = [...group.entries];
        [reordered[i], reordered[j]] = [reordered[j], reordered[i]];
        const order = sections.flatMap((s) =>
            (s.name === entry.section ? reordered : s.entries).map((e) => ({ id: e.id, section: s.name })),
        );
        await saveOrder(order);
    }

    async function saveOrder(order) {
        try {
            // The reply is the whole list again, but in the envelope every
            // listing uses; re-reading is one request and no unwrapping.
            await request("PUT", "/api/admin/x/faq/order", { body: { entries: order } });
            await load();
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>FAQ · GoCommerce</title></svelte:head>

<div class="page page-faq shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Content</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">FAQ</div>
            </nav>
            {#if readable && !missing}
                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
                <div class="page-header-primary-btns">
                    {#if writable}
                        <button type="button" class="btn expanded" onclick={add}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">New question</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="catalog.read" what="the FAQ" />
        {:else if missing}
            <ModuleMissing module="faq" what="The FAQ is served by ext/faq, and this binary does not have it." />
        {:else}
            <p class="txt-hint m-b-base">
                The questions this shop is asked often, grouped into the sections you name and in the
                order you put them. A draft is invisible to shoppers. The storefront reads the
                published ones from <code>/x/faq</code>.
            </p>
            {#if !loading}
                <div class="txt-hint txt-sm m-b-base">
                    {entries.length} questions, {publishedCount} published
                </div>
            {/if}

            {#each sections as section (section.name)}
                <section class="card faq-section">
                    <h2 class="faq-section-name">
                        {section.name || "Ungrouped"}
                        <span class="txt-hint txt-sm">{section.entries.length}</span>
                    </h2>
                    {#each section.entries as entry, i (entry.id)}
                        <div class="faq-row" class:draft={!entry.published}>
                            <div class="faq-order">
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    title="Move up"
                                    aria-label="Move {entry.question} up"
                                    disabled={!writable || i === 0}
                                    onclick={() => move(entry, -1)}
                                >
                                    <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
                                </button>
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    title="Move down"
                                    aria-label="Move {entry.question} down"
                                    disabled={!writable || i === section.entries.length - 1}
                                    onclick={() => move(entry, 1)}
                                >
                                    <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
                                </button>
                            </div>
                            <div class="flex-fill faq-text">
                                <div class="faq-question">{entry.question}</div>
                                <p class="faq-answer">{entry.answer}</p>
                            </div>
                            {#if !entry.published}
                                <span class="label">Draft</span>
                            {/if}
                            {#if writable}
                                <button type="button" class="btn sm secondary" onclick={() => togglePublished(entry)}>
                                    <span class="txt">{entry.published ? "Unpublish" : "Publish"}</span>
                                </button>
                                <button type="button" class="btn circle sm transparent secondary" title="Edit" aria-label="Edit {entry.question}" onclick={() => edit(entry)}>
                                    <i class="ri-pencil-line" aria-hidden="true"></i>
                                </button>
                                <button type="button" class="btn circle sm transparent secondary" title="Remove" aria-label="Remove {entry.question}" onclick={() => (removing = entry)}>
                                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                </button>
                            {/if}
                        </div>
                    {/each}
                </section>
            {/each}

            {#if loading && !entries.length}
                <div class="txt-hint p-base">Loading…</div>
            {:else if !entries.length}
                <div class="block txt-center txt-hint p-base">
                    <div class="m-b-10"><i class="ri-question-answer-line" style="font-size: 32px" aria-hidden="true"></i></div>
                    <p class="tw:mx-auto tw:max-w-[62ch]">
                        <strong>No questions yet.</strong>
                        Start with the three every shop is asked: when it arrives, what delivery costs,
                        and how to send something back.
                    </p>
                </div>
            {/if}
        {/if}
    </div>
</div>

<Drawer {open} size="md" title={editing ? "Edit question" : "New question"} onclose={() => (open = false)}>
    <div class="field">
        <label for="faq-question">Question</label>
        <input id="faq-question" type="text" placeholder="When will my order arrive?" bind:value={form.question} />
    </div>
    <div class="field m-t-sm">
        <label for="faq-answer">Answer</label>
        <textarea id="faq-answer" rows="7" placeholder="Two to three working days." bind:value={form.answer}></textarea>
    </div>
    <div class="field m-t-sm">
        <label for="faq-section">Section</label>
        <input id="faq-section" type="text" list="faq-sections" placeholder="Delivery" bind:value={form.section} />
        <datalist id="faq-sections">
            {#each knownSections as name (name)}<option value={name}></option>{/each}
        </datalist>
    </div>
    <div class="field-help">Leave it empty to put the question above every section.</div>
    <div class="field m-t-sm">
        <input type="checkbox" id="faq-published" class="switch" bind:checked={form.published} />
        <label for="faq-published">Published</label>
    </div>
    <div class="field-help">A draft stays on this screen and off the storefront.</div>
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}><span class="txt">Cancel</span></button>
        <button type="button" class="btn" class:loading={saving} disabled={saving} onclick={save}>
            <span class="txt">{editing ? "Save" : "Add"}</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    open={!!removing}
    title="Remove this question?"
    message={removing ? `“${removing.question}” is gone for good. Unpublishing it instead keeps the wording.` : ""}
    confirmLabel="Remove"
    danger
    onconfirm={remove}
    oncancel={() => (removing = null)}
/>

<style>
    .faq-section {
        margin: 0 0 var(--smSpacing);
        padding: 0;
    }
    .faq-section-name {
        display: flex;
        align-items: baseline;
        gap: 8px;
        margin: 0;
        padding: 12px 16px;
        border-bottom: 1px solid var(--surfaceAlt2Color);
        font-size: 14px;
        font-weight: 600;
    }
    .faq-row {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 10px 16px;
    }
    .faq-row + .faq-row {
        border-top: 1px solid var(--surfaceAlt2Color);
    }
    /* A draft is dimmed rather than hidden: it is on this screen because
       somebody is still writing it. */
    .faq-row.draft .faq-text {
        opacity: 0.6;
    }
    .faq-order {
        display: flex;
        flex-direction: column;
        flex: 0 0 auto;
    }
    .faq-text {
        min-width: 0;
    }
    .faq-question {
        font-weight: 600;
    }
    .faq-answer {
        margin: 2px 0 0;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        max-width: 78ch;
    }
</style>
