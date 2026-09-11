<script>
    /**
     * What is in a collection, and in what order.
     *
     * This is the axis the panel never had. A product editor sets which
     * collections a product is in; nothing could say which products a
     * collection holds, let alone in what sequence — and sequence is the whole
     * point of a collection, which the engine describes as curated by hand.
     *
     * The order is `member_position`, a column of its own precisely because it
     * is not `position`: that one is where the collection sits in each
     * product's list, and writing one where the other was meant is the bug the
     * engine's own migration exists to correct. `PUT /collections/{id}/products`
     * takes an ordered array of ids and writes the index of each, so the list
     * below IS the request — there is no add call and no move call.
     *
     * Which is also why nothing here saves. The whole membership travels in one
     * PUT, so a screen that wrote on every drag would replace the membership
     * five times to move one row three places; the host owns a save bar and
     * this owns the list it will send.
     */
    import { base } from "$app/paths";
    import { api, query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { productStatusClass, pluralize } from "$lib/format.js";
    import Drawer from "$lib/components/Drawer.svelte";

    let {
        members = $bindable([]),
        disabled = false,
        /** True while the collection holds more members than were read. The
         *  host disables saving; this stops offering moves that would be
         *  written against a list it does not fully hold. */
        partial = false,
    } = $props();

    const SEARCH_LIMIT = 20;

    let dragIndex = $state(-1);
    let overIndex = $state(-1);

    let pickerOpen = $state(false);
    let search = $state("");
    let results = $state([]);
    let searching = $state(false);
    let searched = $state(false);
    let searchTimer = null;

    const memberIds = $derived(new Set(members.map((p) => p.id)));

    function move(from, to) {
        if (disabled || partial || from === to || to < 0 || to >= members.length) return;
        const next = [...members];
        const [row] = next.splice(from, 1);
        next.splice(to, 0, row);
        members = next;
    }

    function remove(index) {
        if (disabled) return;
        members = members.filter((_, i) => i !== index);
    }

    function startDrag(event, index) {
        if (disabled || partial) return;
        dragIndex = index;
        // Firefox will not begin a drag unless the payload is set.
        event.dataTransfer?.setData("text/plain", String(index));
        if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
    }

    function dragOverRow(event, index) {
        if (dragIndex < 0) return;
        event.preventDefault();
        overIndex = index;
    }

    function dropRow(event, index) {
        if (dragIndex < 0) return;
        event.preventDefault();
        const from = dragIndex;
        dragIndex = -1;
        overIndex = -1;
        move(from, index);
    }

    function endDrag() {
        dragIndex = -1;
        overIndex = -1;
    }

    /**
     * The arrow keys on the handle are not a nicety: dragging is the only
     * pointer gesture for this, and it has no keyboard equivalent unless one is
     * written.
     */
    function onHandleKey(event, index) {
        if (event.key === "ArrowUp") {
            event.preventDefault();
            move(index, index - 1);
        } else if (event.key === "ArrowDown") {
            event.preventDefault();
            move(index, index + 1);
        }
    }

    function openPicker() {
        search = "";
        results = [];
        searched = false;
        pickerOpen = true;
        runSearch();
    }

    /* Debounced for the same reason every search box in the panel is: a request
       per keystroke sends five for "shirt" and the answers can land out of
       order. */
    function onSearchInput() {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(runSearch, 250);
    }

    async function runSearch() {
        searching = true;
        try {
            const result = await api.get(
                "/api/admin/products" + query({ q: search.trim(), limit: SEARCH_LIMIT }),
            );
            results = result.data ?? [];
            searched = true;
        } catch (err) {
            toast.error(err);
        } finally {
            searching = false;
        }
    }

    /** Added at the end, which is where a new member belongs: the top of a
     *  curated list is a decision, and a picker should not make it. */
    function add(product) {
        if (memberIds.has(product.id)) return;
        members = [...members, product];
    }
</script>

<div class="inline-flex gap-sm flex-wrap m-b-sm">
    <button type="button" class="btn sm secondary" {disabled} onclick={openPicker}>
        <i class="ri-add-line" aria-hidden="true"></i>
        <span class="txt">Add products</span>
    </button>
    <span class="txt-hint txt-sm">
        {members.length}
        {pluralize(members.length, "product")}
    </span>
</div>

{#if partial}
    <div class="field-help error">
        This collection holds more products than one screen can safely curate, so the order is
        shown but not editable: saving would send only the rows read and drop the rest. Curating
        a list this long is a job for the API.
    </div>
{/if}

{#if members.length}
    <div class="table-scroll">
        <table class="table">
            <thead>
                <tr>
                    <th class="min-width"></th>
                    <th>Product</th>
                    <th class="col-field-type-select">Status</th>
                    <th class="col-meta min-width"></th>
                </tr>
            </thead>
            <tbody>
                {#each members as product, index (product.id)}
                    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                    <tr
                        draggable={!disabled && !partial}
                        data-dragging={dragIndex === index}
                        data-dropinto={overIndex === index && dragIndex !== index}
                        ondragstart={(e) => startDrag(e, index)}
                        ondragover={(e) => dragOverRow(e, index)}
                        ondrop={(e) => dropRow(e, index)}
                        ondragend={endDrag}
                    >
                        <td class="min-width">
                            <div class="inline-flex gap-0">
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    disabled={disabled || partial}
                                    title="Drag to reorder, or use the arrow keys"
                                    aria-label="Reorder {product.title} — use the arrow keys to move it"
                                    onkeydown={(e) => onHandleKey(e, index)}
                                >
                                    <i class="ri-draggable" aria-hidden="true"></i>
                                </button>
                                <span class="txt-hint txt-sm collection-rank">{index + 1}</span>
                            </div>
                        </td>
                        <td data-name="Product">
                            <div class="row-product">
                                <div class="row-thumb">
                                    {#if product.image_url}
                                        <img src={product.image_url} alt="" loading="lazy" />
                                    {:else}
                                        <i class="ri-image-line" aria-hidden="true"></i>
                                    {/if}
                                </div>
                                <div class="row-name">
                                    <a
                                        class="txt-bold txt-ellipsis"
                                        href="{base}/products/{product.id}"
                                    >
                                        {product.title}
                                    </a>
                                    <span class="txt-hint txt-sm txt-code row-handle">
                                        {product.slug}
                                    </span>
                                </div>
                            </div>
                        </td>
                        <td class="col-field-type-select" data-name="Status">
                            <span class="label status-chip {productStatusClass(product.status)}">
                                {product.status}
                            </span>
                        </td>
                        <td class="col-meta min-width">
                            <button
                                type="button"
                                class="btn circle sm transparent secondary row-delete"
                                {disabled}
                                title="Remove from this collection"
                                aria-label="Remove {product.title} from this collection"
                                onclick={() => remove(index)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>
    </div>
{:else}
    <div class="txt-hint txt-center p-base">
        Nothing in this collection yet. The order you put products in here is the order a
        storefront shows them.
    </div>
{/if}

<Drawer open={pickerOpen} title="Add products" size="sm" onclose={() => (pickerOpen = false)}>
    <div class="fields searchbar m-b-sm">
        <div class="field">
            <input
                type="text"
                class="p-l-30"
                placeholder="Search products"
                aria-label="Search products"
                bind:value={search}
                oninput={onSearchInput}
            />
            <!-- After the input, not before it: `.field` is display:block and
                 the input is width:100%, so a leading sibling would stack above
                 it. See gocommerce.css. -->
            <i class="ri-search-line searchbar-icon" aria-hidden="true"></i>
        </div>
    </div>

    <div class="table-scroll">
        <table class="table">
            <tbody>
                {#each results as product (product.id)}
                    {@const already = memberIds.has(product.id)}
                    <tr>
                        <td>
                            <div class="row-product">
                                <div class="row-thumb">
                                    {#if product.image_url}
                                        <img src={product.image_url} alt="" loading="lazy" />
                                    {:else}
                                        <i class="ri-image-line" aria-hidden="true"></i>
                                    {/if}
                                </div>
                                <div class="row-name">
                                    <span class="txt-bold txt-ellipsis">{product.title}</span>
                                    <span class="txt-hint txt-sm txt-code row-handle">
                                        {product.slug}
                                    </span>
                                </div>
                            </div>
                        </td>
                        <td class="col-meta min-width">
                            {#if already}
                                <span class="txt-hint txt-sm">Added</span>
                            {:else}
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    onclick={() => add(product)}
                                >
                                    <span class="txt">Add</span>
                                </button>
                            {/if}
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>
    </div>

    {#if searching && !results.length}
        <div class="block txt-center p-base"><span class="loader lg"></span></div>
    {:else if searched && !results.length}
        <div class="txt-center txt-hint p-base">No products match that.</div>
    {/if}

    <div class="field-help">
        Added products go to the end of the list, and nothing is written until the collection is
        saved.
    </div>

    {#snippet footer()}
        <button type="button" class="btn m-l-auto" onclick={() => (pickerOpen = false)}>
            <span class="txt">Done</span>
        </button>
    {/snippet}
</Drawer>

<style>
    /*
     * The two states a row takes while it is being dragged. The grid's own
     * tiles get this from `.media-tile[data-dragging]` in gocommerce.css; a
     * table row is a different box and a 2px inset on the cells is the only
     * shape a drop indicator can take on one.
     */
    tr[data-dragging="true"] {
        opacity: 0.35;
    }
    tr[data-dropinto="true"] > td {
        box-shadow: inset 0 2px 0 var(--accentColor);
    }
    /* The rank sits against the handle rather than in a column of its own,
       which would cost a whole column to show a number nobody reads twice. */
    .collection-rank {
        min-width: 18px;
        text-align: right;
    }
</style>
