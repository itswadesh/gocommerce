<script>
    /**
     * The product list.
     *
     * Editing moved to `/products/{id}` — a product now has media, an option
     * matrix, per-variant stock and an SEO listing, and a drawer that has to
     * hold all of that beside the row it belongs to stops being a drawer.
     *
     * Creating stayed here, and stayed small. A product needs at least one
     * variant to exist at all, so the create form asks for exactly what the
     * engine requires and nothing else, then hands over to the editor — which
     * is where the interesting fields are and where the operator was going
     * anyway.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import {
        formatMoney,
        formatDate,
        relativeTime,
        toMinor,
        isValidMoney,
        stockClass,
        pluralize,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const PER_PAGE = 30;

    /* Search, status, ordering and page all live in the URL, and the window
       replaces the rows rather than accumulating them — a sorted list an
       operator can send to somebody is the whole point, and page 4 of a new
       ordering appended onto three pages of the old one is two orderings
       interleaved in one table. */
    const list = listState({ q: "", status: "", sort: "", order: "", page: 1 });
    const SORT_FIELDS = ["title", "status", "price", "available", "created_at", "updated_at"];

    let loading = $state(true);
    let saving = $state(false);
    let products = $state([]);
    let meta = $state(null);

    const search = $derived(list.params.q);
    const status = $derived(list.params.status);
    const sort = $derived(readSort(list.params, SORT_FIELDS));
    let draftSearch = $state(list.params.q);

    /* A fast second header click leaves two requests in flight; without this
       the table settles on the reply that lost rather than on the header that
       is lit. */
    let reqId = 0;

    // The store answers this once, at sign-in; before it does, the getter
    // returns the same USD this screen used to assume outright.
    const currency = $derived(settings.currency);

    let createOpen = $state(false);
    let form = $state(blankForm());
    let errors = $state({});

    let confirmOpen = $state(false);
    let pendingDelete = $state(null);

    function blankForm() {
        return { title: "", slug: "", status: "draft", sku: "", price: "", stock: 0 };
    }

    $effect(() => {
        // Re-runs whenever any parameter changes: the search, the status, the
        // ordering, the page.
        list.params;
        load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/products" + list.query({ limit: PER_PAGE, ...sortQuery(sort) }),
            );
            if (mine !== reqId) return;
            products = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draftSearch });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "" });
    }

    function openCreate() {
        form = blankForm();
        errors = {};
        createOpen = true;
    }

    function openEdit(product) {
        goto(`${base}/products/${product.id}`);
    }

    async function create(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.title.trim()) errors.title = "A title is required.";
        if (!form.sku.trim()) errors.sku = "A SKU is required.";
        // isValidMoney rather than isNaN(parseFloat(...)): parseFloat reads
        // "24,99" as 24, and this guard is what keeps toMinor from having to
        // answer null on the way to the wire.
        if (!isValidMoney(form.price)) {
            errors.price = "A price is required.";
        }
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            const created = await api.post("/api/admin/products", {
                title: form.title.trim(),
                slug: form.slug.trim() || undefined,
                status: form.status,
                sku: form.sku.trim(),
                price_minor: toMinor(form.price, currency),
                stock: parseInt(form.stock, 10) || 0,
            });
            createOpen = false;
            toast.success("Product created");
            // Straight into the editor: everything else a product has — media,
            // options, SEO — lives there, and nobody creates one meaning to
            // stop at a title and a price.
            await goto(`${base}/products/${created.id}`);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(product, event) {
        event.stopPropagation();
        pendingDelete = product;
        confirmOpen = true;
    }

    async function doDelete() {
        try {
            await api.delete(`/api/admin/products/${pendingDelete.id}`);
            toast.success(`Deleted ${pendingDelete.title}`);
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    function priceRange(product) {
        const prices = (product.variants || []).map((v) => v.price.amount_minor);
        if (!prices.length) return "—";
        const min = Math.min(...prices);
        const max = Math.max(...prices);
        const cur = product.variants[0].price.currency;
        if (min === max) return formatMoney({ amount_minor: min, currency: cur });
        return (
            formatMoney({ amount_minor: min, currency: cur }) +
            " – " +
            formatMoney({ amount_minor: max, currency: cur })
        );
    }

    function totalAvailable(product) {
        const tracked = (product.variants || []).filter((v) => v.track_inventory);
        if (!tracked.length) return null;
        return tracked.reduce((sum, v) => sum + v.available, 0);
    }

    // Tones, not verdicts: a draft is a state the operator chose, not a warning
    // about one. Green for on sale, blue for not yet, grey for retired.
    const statusLabel = { active: "success", draft: "info", archived: "" };
</script>

<div class="page page-products">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Products</div></nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            <form class="fields searchbar" onsubmit={submitSearch}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search products by title or slug"
                        bind:value={draftSearch}
                    />
                </div>
                {#if draftSearch || search}
                    <div class="field addon p-r-5">
                        {#if draftSearch !== search}
                            <button type="submit" class="btn sm pill warning">Search</button>
                        {/if}
                        <button
                            type="button"
                            class="btn sm pill secondary transparent"
                            onclick={clearSearch}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            <div class="page-header-primary-btns">
                <div class="field">
                    <Select
                        id="status-filter"
                        placeholder="Any status"
                        value={status}
                        onchange={(v) => list.set({ status: v })}
                        options={[
                            { value: "", label: "Any status" },
                            { value: "active", label: "Active" },
                            { value: "draft", label: "Draft" },
                            { value: "archived", label: "Archived" },
                        ]}
                    />
                </div>

                <button type="button" class="btn" onclick={openCreate}>
                    <i class="ri-add-line" aria-hidden="true"></i>
                    <span class="txt">New product</span>
                </button>
            </div>
        </header>

        <div class="page-table-wrapper">
            <table class="table responsive-table" class:optimize={products.length > 60}>
                <thead class="sticky">
                    <tr>
                        <SortHeader
                            field="title"
                            label="Product"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="status"
                            label="Status"
                            class="col-field-type-select"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Variants counts an embedded array, not a column. A
                             header with no arrow and no hover reads as a fact
                             rather than as a broken control. -->
                        <th class="col-field-type-number min-width">Variants</th>
                        <SortHeader
                            field="price"
                            label="Price"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="available"
                            label="Available"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="updated_at"
                            label="Updated"
                            class="col-field-type-date min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each products as product (product.id)}
                        {@const available = totalAvailable(product)}
                        <tr class="handle" onclick={() => openEdit(product)}>
                            <!-- Name and handle on one line. The second line was
                                 costing every row 15px on a screen whose whole job
                                 is to fit rows, and the handle reads as what it is
                                 from its face and colour, not from its position. -->
                            <td class="col-field-name-id" data-name="Product">
                                <div class="row-product">
                                    <!-- A fixed frame whether or not there is a
                                         picture: a thumbnail that collapses when
                                         a product has none takes the whole
                                         column out of line with the rest. -->
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
                            <td class="col-field-type-select" data-name="Status">
                                <span class="label status-chip {statusLabel[product.status] ?? ''}">
                                    {product.status}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Variants">
                                {product.variants?.length ?? 0}
                                <span class="txt-hint txt-sm">
                                    {pluralize(product.variants?.length ?? 0, "variant")}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Price">
                                {priceRange(product)}
                            </td>
                            <td
                                class="col-field-type-number min-width {available === null
                                    ? 'txt-hint'
                                    : stockClass(available)}"
                                data-name="Available"
                            >
                                {available === null ? "not tracked" : available}
                            </td>
                            <!-- updated_at already rides on every row, so this
                                 column costs the engine nothing — it is data
                                 the screen was throwing away, and it is what
                                 gives the Updated sort a header to live on. -->
                            <td
                                class="col-field-type-date min-width txt-hint"
                                data-name="Updated"
                                title={formatDate(product.updated_at)}
                            >
                                {relativeTime(product.updated_at)}
                            </td>
                            <td class="col-meta min-width">
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary row-delete"
                                    aria-label="Delete {product.title}"
                                    title="Delete"
                                    onclick={(e) => askDelete(product, e)}
                                >
                                    <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                </button>
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !products.length}
                        {#each Array(6) as _, i (i)}
                            <tr>
                                <td colspan="7"><span class="skeleton-loader"></span></td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !products.length}
                        <tr>
                            <td colspan="7" class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i class="ri-price-tag-3-line" style="font-size: 32px" aria-hidden="true"></i>
                                </div>
                                {#if search || status}
                                    No products match that. Try a different search or clear the filter.
                                {:else}
                                    No products yet. A product needs at least one variant — the thing
                                    that actually gets sold.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        <footer class="page-footer">
            <Pager {meta} {loading} noun="product" onpage={(n) => list.setPage(n)} />
            <div class="flex-fill"></div>
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer open={createOpen} size="sm" title="New product" onclose={() => (createOpen = false)}>
    <form id="product-form" onsubmit={create}>
        <div class="field required" class:error={!!errors.title}>
            <label for="title">Title</label>
            <input id="title" type="text" bind:value={form.title} />
        </div>
        {#if errors.title}<div class="field-help error">{errors.title}</div>{/if}

        <div class="field m-t-sm">
            <label for="slug">Slug</label>
            <input
                id="slug"
                type="text"
                placeholder="Derived from the title if left empty"
                bind:value={form.slug}
            />
        </div>

        <div class="field m-t-sm">
            <label for="status">Status</label>
            <Select
                id="status"
                bind:value={form.status}
                options={[
                    { value: "draft", label: "Draft — hidden from shoppers" },
                    { value: "active", label: "Active — on sale" },
                    { value: "archived", label: "Archived" },
                ]}
            />
        </div>

        <h6 class="section-title">
            <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
            The first variant
        </h6>
        <div class="field-help m-b-sm">
            A variant is what actually gets sold. A product with no options still has exactly one,
            and options can be added to it afterwards.
        </div>

        <div class="field required" class:error={!!errors.sku}>
            <label for="sku">SKU</label>
            <input id="sku" type="text" bind:value={form.sku} placeholder="TEE-001" />
        </div>
        {#if errors.sku}<div class="field-help error">{errors.sku}</div>{/if}

        <div class="fields m-t-sm">
            <div class="field required" class:error={!!errors.price}>
                <label for="price">Price ({currency})</label>
                <input id="price" type="text" inputmode="decimal" bind:value={form.price} />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <label for="stock">Stock on hand</label>
                <input id="stock" type="number" min="0" bind:value={form.stock} />
            </div>
        </div>
        {#if errors.price}<div class="field-help error">{errors.price}</div>{/if}

        <div class="field-help m-t-sm">
            Description, media, options and the search listing are on the editor, which this
            opens as soon as the product exists.
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (createOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="product-form"
            class="btn"
            class:loading={saving}
            disabled={saving}
        >
            <span class="txt">Create product</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Delete this product?"
    message={pendingDelete
        ? `"${pendingDelete.title}" and its variants will be removed. Orders that included it keep their own snapshot, so history stays readable.`
        : ""}
    confirmLabel="Delete"
    danger
    onconfirm={doDelete}
/>
