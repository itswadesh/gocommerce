<script>
    /**
     * A collection's own fields: what it is called, where it lives, and where
     * it sits among the others.
     *
     * Every one of these is something `CollectionPatch` accepts and nothing in
     * the panel could reach — a collection could be created from a product
     * editor and then never renamed, described, repositioned or deleted again,
     * because PATCH and DELETE were mounted and called from nowhere.
     *
     * Two fields need a word.
     *
     * `position` orders collections against each other — a storefront's
     * navigation — and NOT the products inside one. Those are different axes
     * and the engine keeps them in different columns; the member list on the
     * collection page is the other one.
     *
     * `metadata` is replaced whole by the patch, so anything this form does not
     * understand has to ride along untouched. The rows below edit the entries
     * whose value is a string, which is what an operator writes by hand;
     * anything nested was written by something else and is preserved rather
     * than flattened into a box that would stringify it.
     */
    import { api } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let { open = false, collection = null, onsaved, onclose } = $props();

    let form = $state(blank());
    let rows = $state([]);
    /** The metadata entries this form does not edit, kept to be written back. */
    let kept = $state({});
    let errors = $state({});
    let saving = $state(false);

    function blank() {
        return { title: "", slug: "", description: "", position: "0" };
    }

    /* Re-seeded whenever the drawer is pointed at another collection — or at
       none, which is the create form. */
    let seeded = $state(undefined);
    $effect(() => {
        if (!open) return;
        const id = collection?.id ?? null;
        if (seeded === id) return;
        seeded = id;
        errors = {};
        if (!collection) {
            form = blank();
            rows = [];
            kept = {};
            return;
        }
        form = {
            title: collection.title ?? "",
            slug: collection.slug ?? "",
            description: collection.description ?? "",
            position: String(collection.position ?? 0),
        };
        const meta = collection.metadata ?? {};
        rows = Object.entries(meta)
            .filter(([, value]) => typeof value === "string")
            .map(([key, value]) => ({ key, value }));
        kept = Object.fromEntries(
            Object.entries(meta).filter(([, value]) => typeof value !== "string"),
        );
    });

    const keptCount = $derived(Object.keys(kept).length);

    function addRow() {
        rows = [...rows, { key: "", value: "" }];
    }

    function removeRow(index) {
        rows = rows.filter((_, i) => i !== index);
    }

    /** The metadata as it goes on the wire: what was preserved, then what the
     *  form holds. A row with no name is one somebody started and abandoned. */
    function metadata() {
        const out = { ...kept };
        for (const row of rows) {
            const key = row.key.trim();
            if (key) out[key] = row.value;
        }
        return out;
    }

    async function save(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.title.trim()) errors.title = "A title is required.";
        const position = parseInt(form.position, 10);
        if (!Number.isFinite(position)) errors.position = "A position is a whole number.";
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            let saved;
            if (collection) {
                // The slug travels only when it has a value: the engine refuses
                // an empty one outright, and "leave it as it is" is what an
                // operator who cleared the box actually means.
                saved = await api.patch(`/api/admin/collections/${collection.id}`, {
                    title: form.title.trim(),
                    slug: form.slug.trim() || undefined,
                    description: form.description,
                    position,
                    metadata: metadata(),
                });
                toast.success("Collection saved");
            } else {
                saved = await api.post("/api/admin/collections", {
                    title: form.title.trim(),
                    slug: form.slug.trim() || undefined,
                    description: form.description,
                    position,
                    metadata: metadata(),
                });
                toast.success("Collection created");
            }
            // Re-seeding is keyed on the id, so a save that keeps the drawer
            // pointed at the same record has to release it by hand.
            seeded = undefined;
            onsaved?.(saved);
        } catch (err) {
            // A 409 here is the slug already being used by another collection,
            // and the engine's message says so.
            if (err.status === 409) errors.slug = err.message;
            else toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

<Drawer
    {open}
    title={collection ? collection.title : "New collection"}
    size="sm"
    onclose={() => ((seeded = undefined), onclose?.())}
>
    <form id="collection-form" onsubmit={save}>
        <div class="field required" class:error={!!errors.title}>
            <label for="col_title">Title</label>
            <input id="col_title" type="text" autocomplete="off" bind:value={form.title} />
        </div>
        {#if errors.title}<div class="field-help error">{errors.title}</div>{/if}

        <div class="field m-t-sm" class:error={!!errors.slug}>
            <label for="col_slug">Slug</label>
            <input id="col_slug" type="text" autocomplete="off" bind:value={form.slug} />
        </div>
        {#if errors.slug}
            <div class="field-help error">{errors.slug}</div>
        {:else}
            <div class="field-help">
                Derived from the title when left empty. A storefront reads a collection at
                <code>/api/collections/{form.slug.trim() || "<slug>"}</code>, so changing
                it changes that address.
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="col_description">Description</label>
            <textarea id="col_description" rows="3" bind:value={form.description}></textarea>
        </div>
        <div class="field-help">Shown at the head of the collection on a storefront.</div>

        <div class="field m-t-sm" class:error={!!errors.position}>
            <label for="col_position">Position</label>
            <input
                id="col_position"
                type="number"
                step="1"
                autocomplete="off"
                bind:value={form.position}
            />
        </div>
        {#if errors.position}
            <div class="field-help error">{errors.position}</div>
        {:else}
            <div class="field-help">
                Where this collection sits among the others — a navigation menu, lowest first.
                It has nothing to do with the order of the products inside it, which is curated
                on the collection's own page.
            </div>
        {/if}

        <h6 class="section-title">
            <i class="ri-price-tag-2-line" aria-hidden="true"></i>
            Metadata
        </h6>

        {#if rows.length}
            <div class="metafields m-b-sm">
                {#each rows as row, i (i)}
                    <div class="metafield-row">
                        <div class="field">
                            <label for="col-meta-key-{i}">Name</label>
                            <input
                                id="col-meta-key-{i}"
                                type="text"
                                placeholder="banner_theme"
                                bind:value={row.key}
                            />
                        </div>
                        <div class="field">
                            <label for="col-meta-value-{i}">Value</label>
                            <input id="col-meta-value-{i}" type="text" bind:value={row.value} />
                        </div>
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            aria-label="Remove {row.key || 'this entry'}"
                            title="Remove"
                            onclick={() => removeRow(i)}
                        >
                            <i class="ri-close-line" aria-hidden="true"></i>
                        </button>
                    </div>
                {/each}
            </div>
        {/if}

        <button type="button" class="btn sm secondary" onclick={addRow}>
            <i class="ri-add-line" aria-hidden="true"></i>
            <span class="txt">Add an entry</span>
        </button>

        <div class="field-help">
            Your own fields, for a storefront or an integration to read.
            {#if keptCount}
                {keptCount}
                {keptCount === 1 ? "entry holds" : "entries hold"} a nested value written by something
                else; {keptCount === 1 ? "it is" : "they are"} kept exactly as {keptCount === 1
                    ? "it is"
                    : "they are"}.
            {/if}
        </div>
    </form>

    {#snippet footer()}
        <button
            type="button"
            class="btn transparent m-r-auto"
            onclick={() => ((seeded = undefined), onclose?.())}
        >
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="collection-form"
            class="btn"
            class:loading={saving}
            disabled={saving}
        >
            <span class="txt">{collection ? "Save changes" : "Create collection"}</span>
        </button>
    {/snippet}
</Drawer>
