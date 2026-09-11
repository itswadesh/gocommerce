<script>
    /**
     * Re-parenting many categories at once.
     *
     * Moving a category is a one-field edit, and the panel only ever offered it
     * one category at a time through the editor drawer's Parent field. That is
     * fine for a tree somebody built by hand and useless for the one the engine
     * is sized for: importing Shopify's taxonomy puts fourteen thousand nodes in
     * the table, and reorganising a branch of it is forty drawer visits.
     *
     * There is no batch route and this does not invent one. It is N calls to
     * PATCH /api/admin/categories/{id} — the same route the drawer uses — so
     * each move is checked on its own by checkReparent, which is what refuses a
     * cycle and a subtree that would end up deeper than the limit. Those
     * refusals are per row by nature: moving six categories under one parent can
     * legally move four of them, and `runBulk` reports the two that could not
     * with the engine's own sentence rather than failing the lot.
     *
     * The one check made here is the one the engine cannot phrase as well: a
     * destination that is itself in the selection. The engine answers that with
     * "a category cannot be its own parent", which is true and confusing when
     * the operator picked a destination from a list they had also ticked.
     */
    import { api } from "$lib/api.js";
    import { runBulk } from "$lib/bulk.js";
    import { pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import Drawer from "$lib/components/Drawer.svelte";

    let {
        open = false,
        /** The categories to move, as the tree listing sends them. */
        selected = [],
        /** The flat tree for the picker, and whether it is only a slice of one. */
        categories = [],
        remote = false,
        onclose,
        /** Called after anything moved, so the tree behind can re-read itself. */
        ondone,
    } = $props();

    /* null is a real destination and the only one a plain pointer cannot
       express: it is the top level. CategoryPicker's own "none" row says so,
       which is why its placeholder is worded as a place rather than as an
       absence. */
    let parentID = $state(null);
    /*
     * Sibling order after the move, off by default.
     *
     * Every moved category keeps whatever position it had, and forty of them
     * arriving under one parent with positions from four different branches is
     * a storefront menu in an order nobody chose. Renumbering is offered rather
     * than done, because the positions may be the ones the operator wants.
     */
    let renumber = $state(false);
    let saving = $state(false);
    let error = $state("");
    let refusals = $state([]);

    let wasOpen = false;
    $effect(() => {
        if (open && !wasOpen) {
            parentID = null;
            renumber = false;
            error = "";
            refusals = [];
            saving = false;
        }
        wasOpen = open;
    });

    const chosen = $derived(selected ?? []);
    /* Ticking a destination that is also being moved. Named here because the
       engine's own sentence for it reads as a mistake the operator did not
       make. */
    const intoItself = $derived(parentID !== null && chosen.some((c) => c.id === parentID));
    /* Already there. Not an error — it is the normal state of half a selection
       — so it is counted and excluded rather than refused. */
    const moving = $derived(chosen.filter((c) => (c.parent_id ?? null) !== parentID));

    function submit(event) {
        event?.preventDefault();
        if (saving) return;
        apply();
    }

    async function apply() {
        error = "";
        refusals = [];
        if (!chosen.length) {
            error = "Nothing is selected.";
            return;
        }
        if (intoItself) {
            error = "One of the categories being moved is the destination. Pick another parent.";
            return;
        }
        if (!moving.length && !renumber) {
            error = "Every one of them is already there.";
            return;
        }

        // Renumbering sends the whole selection, because the order is the order
        // of the list as a whole; a plain move sends only what actually changes,
        // so an audit trail does not fill with edits that edited nothing.
        const rows = renumber ? chosen : moving;
        saving = true;
        const { done, failures } = await runBulk(
            rows,
            (row) =>
                api.patch(`/api/admin/categories/${row.id}`, {
                    parent_id: parentID,
                    ...(renumber ? { position: rows.indexOf(row) } : {}),
                }),
            { label: (row) => row.full_name || row.title },
        );
        saving = false;

        if (done) {
            toast.success(`Moved ${done} ${pluralize(done, "category", "categories")}`);
            ondone?.();
        }
        if (!failures.length) {
            onclose?.();
            return;
        }
        // The engine's own sentences, kept on screen beside the category each
        // one is about: "a category cannot be moved inside one of its own
        // descendants" and "that move would nest categories more than 8 deep"
        // are both answerable, and a count would say neither.
        refusals = failures.map((f) => ({ label: f.label, message: f.message }));
        error =
            failures.length === 1
                ? "One category could not be moved."
                : `${failures.length} categories could not be moved.`;
    }
</script>

<Drawer
    {open}
    size="sm"
    title="Move {chosen.length} {pluralize(chosen.length, 'category', 'categories')}"
    onclose={() => onclose?.()}
>
    <form id="category-move-form" onsubmit={submit}>
        <div class="field">
            <label for="move-parent">New parent</label>
            <CategoryPicker
                id="move-parent"
                bind:value={parentID}
                {categories}
                {remote}
                placeholder="Top level"
                onchange={() => (error = "")}
            />
        </div>
        <div class="field-help">
            Each one keeps its own subtree — the children come with it. A move that would put a
            category inside one of its own descendants, or push a subtree past the depth limit,
            is refused on its own and the rest still go.
        </div>

        <div class="field m-t-sm">
            <input id="move-renumber" type="checkbox" class="switch" bind:checked={renumber} />
            <label for="move-renumber">Renumber them in the order listed below</label>
        </div>
        <div class="field-help">
            Off, every one keeps the position it had under its old parent, which is rarely the
            order anybody wants once they are siblings.
        </div>
    </form>

    <h6 class="section-title m-t-base">Moving</h6>
    <ul class="list">
        {#each chosen as category (category.id)}
            <li class="list-item">
                <span class="txt-ellipsis">
                    <span class="txt-bold">{category.title}</span>
                    {#if category.full_name && category.full_name !== category.title}
                        <span class="txt-hint txt-sm">{category.full_name}</span>
                    {/if}
                </span>
                {#if (category.parent_id ?? null) === parentID}
                    <span class="txt-hint txt-sm m-l-auto txt-nowrap">already there</span>
                {:else if category.child_count > 0}
                    <span class="txt-hint txt-sm m-l-auto txt-nowrap">
                        with {category.child_count}
                        {pluralize(category.child_count, "child", "children")}
                    </span>
                {/if}
            </li>
        {/each}
        {#if !chosen.length}
            <li class="list-item txt-hint">Nothing selected.</li>
        {/if}
    </ul>

    {#if refusals.length}
        <div class="alert danger m-t-sm">
            <div>
                {#each refusals as refusal (refusal.label)}
                    <p><strong>{refusal.label}</strong> — {refusal.message}</p>
                {/each}
            </div>
        </div>
    {:else if error}
        <div class="field-help error m-t-sm">{error}</div>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <span class="txt txt-hint m-r-sm">
            {moving.length}
            {pluralize(moving.length, "move")}
        </span>
        <button
            type="submit"
            form="category-move-form"
            class="btn"
            class:loading={saving}
            disabled={saving || !chosen.length || intoItself || (!moving.length && !renumber)}
        >
            <span class="txt">Move them</span>
        </button>
    {/snippet}
</Drawer>
