<script>
    /**
     * The product list's segmenting filters, in a drawer.
     *
     * `GET /api/admin/products` narrows on vendor, product type, tag, category
     * and collection, and the list screen used to send none of them — so a
     * catalogue could be authored with all five and segmented by none. They do
     * not fit in the header beside the search box and the status select, and
     * five more controls up there collapse into a scrollbar at 950px, so they
     * live behind one "Filter" button.
     *
     * The drawer edits a *draft* and applies it in one go. Each control
     * committing on its own would be one request per keystroke in a combobox,
     * and five requests for a five-part filter where the operator meant one
     * question. Applying is also where the page goes back to 1, which is
     * listState's job and not this component's.
     *
     * The three free-text axes are comboboxes rather than selects for the same
     * reason the product editor's are: vendor, product type and tag are columns
     * on `products`, not tables, so the suggestions are only ever "what the
     * catalogue already says" — and a catalogue larger than the page that
     * seeds them must still be filterable by a name typed in full.
     */
    import Combobox from "$lib/components/Combobox.svelte";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";
    import TokenInput from "$lib/components/TokenInput.svelte";
    import { api } from "$lib/api.js";

    let {
        open = false,
        // The filter as the URL currently holds it. The draft is seeded from
        // this every time the drawer opens, so cancelling really does cancel.
        value = {
            vendor: "",
            product_type: "",
            tag: "",
            category_id: 0,
            collection_id: 0,
            attrs: {},
        },
        vendors = [],
        productTypes = [],
        tags = [],
        categories = [],
        categoriesTruncated = false,
        collections = [],
        loadingVocabulary = false,
        onapply,
        onclose,
    } = $props();

    let draft = $state(blank());

    // category_id is null rather than 0 in the draft, because that is what the
    // picker means by "None" and binding it is the only way the picker can put
    // its own answer back. apply() is where it becomes the 0 the URL holds.
    function blank() {
        return {
            vendor: "",
            product_type: "",
            tag: "",
            category_id: null,
            collection_id: 0,
            // Keyed by the attribute handle, each holding the values that count
            // as a match. Several values of one attribute widen the result, so
            // a multi-value control is the honest one here rather than a select.
            attrs: {},
        };
    }

    /*
     * Seeding on open rather than on every change to `value`: the drawer is the
     * only thing editing the draft while it is up, and re-seeding underneath a
     * half-typed vendor would throw it away.
     */
    $effect(() => {
        if (!open) return;
        draft = {
            vendor: value.vendor ?? "",
            product_type: value.product_type ?? "",
            tag: value.tag ?? "",
            // The picker's "none" is null and the URL's is 0; they mean the
            // same thing and only one of them can be a query parameter.
            category_id: value.category_id || null,
            collection_id: value.collection_id || 0,
            attrs: structuredClone(value.attrs ?? {}),
        };
    });

    /**
     * The fields the chosen category asks about, which is what there is to
     * filter on.
     *
     * Scoped to the category rather than offered as a flat list, and that is
     * not a simplification: the shared dictionary holds several thousand
     * attributes, so "all of them" is a control nobody can use. It is also the
     * same shape the product editor uses — a field belongs to a category, and
     * the answers only exist under one.
     *
     * The ancestors route already returns each node's fields with their choices
     * resolved, and a leaf inherits its parents', so one request answers it.
     */
    let categoryFields = $state([]);
    let loadingFields = $state(false);

    $effect(() => {
        const id = draft.category_id;
        if (!id) {
            categoryFields = [];
            return;
        }
        let cancelled = false;
        loadingFields = true;
        api.get(`/api/admin/categories/${id}/ancestors`)
            .then((result) => {
                if (cancelled) return;
                // The route answers an envelope, not a bare array. Reading it
                // as the array threw inside the promise, where the catch below
                // turned it into "this category asks no fields" — a shape bug
                // that looked exactly like a category with nothing to ask.
                const chain = result.data ?? [];
                // The leaf wins a key its ancestor also declares, so a category
                // can narrow a parent's choices — the product editor's rule.
                const byKey = new Map();
                for (const node of chain ?? []) {
                    for (const attr of node.attributes ?? []) {
                        if (!attr?.key) continue;
                        byKey.set(attr.key, {
                            key: attr.key,
                            label: attr.label ?? attr.key,
                            choices: (attr.choices ?? []).filter(Boolean),
                        });
                    }
                }
                categoryFields = [...byKey.values()];
                // Every field needs a real array behind it before the markup
                // binds to it: `bind:values` on an absent key is
                // props_invalid_value, which takes the whole screen to the
                // error boundary rather than failing the one control.
                for (const field of categoryFields) {
                    if (!Array.isArray(draft.attrs[field.key])) draft.attrs[field.key] = [];
                }
            })
            .catch(() => {
                // A filter that cannot list its fields is a filter without
                // those fields, not a broken drawer.
                if (!cancelled) categoryFields = [];
            })
            .finally(() => {
                if (!cancelled) loadingFields = false;
            });
        return () => {
            cancelled = true;
        };
    });

    function apply() {
        // Only fields the chosen category still asks about, and only those with
        // an answer: an emptied list is not a filter, and leaving it in the URL
        // would make an untouched control look like a live one.
        const attrs = {};
        for (const field of categoryFields) {
            const values = (draft.attrs[field.key] ?? []).filter(Boolean);
            if (values.length) attrs[field.key] = [...values];
        }
        // The labels travel with the values because only this drawer has
        // looked them up: the list screen holds handles, and a chip reading
        // "bag-case-material" is true but unkind.
        const labels = {};
        for (const field of categoryFields) labels[field.key] = field.label;
        onapply?.({
            vendor: draft.vendor.trim(),
            product_type: draft.product_type.trim(),
            tag: draft.tag.trim(),
            category_id: draft.category_id || 0,
            collection_id: draft.collection_id || 0,
            attrs,
            labels,
        });
    }

    function clear() {
        draft = blank();
        onapply?.(blank());
    }
</script>

<Drawer {open} size="sm" title="Filter products" {onclose}>
    {#if loadingVocabulary}
        <div class="txt-center txt-hint p-base">
            <span class="loader"></span>
        </div>
    {/if}

    <div class="field">
        <label for="filter-vendor">Vendor</label>
        <Combobox
            id="filter-vendor"
            bind:value={draft.vendor}
            options={vendors}
            placeholder="Any vendor"
            emptyText="No vendors in the catalog yet"
        />
    </div>

    <div class="field m-t-sm">
        <label for="filter-product-type">Product type</label>
        <Combobox
            id="filter-product-type"
            bind:value={draft.product_type}
            options={productTypes}
            placeholder="Any product type"
            emptyText="No product types in the catalog yet"
        />
    </div>

    <div class="field m-t-sm">
        <label for="filter-tag">Tag</label>
        <Combobox
            id="filter-tag"
            bind:value={draft.tag}
            options={tags}
            placeholder="Any tag"
            emptyText="No tags in the catalog yet"
        />
    </div>
    <div class="field-help">
        These three match exactly, and the suggestions come from a page of the
        catalogue — a name it did not reach can still be typed in full.
    </div>

    <div class="field m-t-sm">
        <label for="filter-category">Category</label>
        <CategoryPicker
            id="filter-category"
            bind:value={draft.category_id}
            {categories}
            remote={categoriesTruncated}
            placeholder="Any category"
        />
    </div>
    <div class="field-help">
        A category matches everything nested under it too, so filtering by
        Clothing shows the shirts filed beneath it.
    </div>

    <!-- The category's own fields, which appear once one is chosen. They are
         not offered before that on purpose: the shared dictionary holds
         thousands of attributes, and a flat list of them is a control nobody
         can use. -->
    {#if draft.category_id}
        {#if loadingFields}
            <div class="txt-center txt-hint p-sm"><span class="loader"></span></div>
        {:else if categoryFields.length}
            <div class="cat-meta-rows m-t-sm">
                {#each categoryFields as field (field.key)}
                    <div class="cat-meta-row">
                        <span class="cat-meta-label">{field.label}</span>
                        <TokenInput
                            bind:values={draft.attrs[field.key]}
                            options={field.choices}
                            emptyText={field.choices.length
                                ? "Nothing left to choose"
                                : "This field has no set choices — type a value."}
                        />
                    </div>
                {/each}
            </div>
            <div class="field-help">
                Two values of one field widen the result — canvas or leather —
                while two different fields narrow it. Matching is exact.
            </div>
        {:else}
            <div class="field-help">This category asks no fields of a product.</div>
        {/if}
    {/if}

    <div class="field m-t-sm">
        <label for="filter-collection">Collection</label>
        <Select
            id="filter-collection"
            placeholder="Any collection"
            value={draft.collection_id}
            onchange={(v) => (draft.collection_id = v || 0)}
            options={[
                { value: 0, label: "Any collection" },
                ...collections.map((c) => ({ value: c.id, label: c.title })),
            ]}
        />
    </div>
    <div class="field-help">
        A collection also sets the order: its members come back in the order
        they were curated in rather than the catalogue's.
    </div>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={clear}>
            <span class="txt">Clear all</span>
        </button>
        <button type="button" class="btn secondary" onclick={onclose}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" onclick={apply}>
            <span class="txt">Apply</span>
        </button>
    {/snippet}
</Drawer>
