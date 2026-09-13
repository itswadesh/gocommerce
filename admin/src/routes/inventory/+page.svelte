<script>
    import { api } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import { selection } from "$lib/selection.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { stockClass, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";
    import StockHistory from "$lib/components/StockHistory.svelte";
    import ReceiveDelivery from "$lib/components/ReceiveDelivery.svelte";
    import MoveStock from "$lib/components/MoveStock.svelte";
    import { findVariants } from "$lib/components/VariantSearch.svelte";

    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    const PER_PAGE = 25;
    const DEFAULT_THRESHOLD = 5;

    /* The search, the threshold, the location and the page live in the URL: a
       filtered list with no filter in its address cannot be sent to anybody, and
       the window replaces the rows rather than accumulating them, so `page=3` in
       the URL is page 3 of the same list when it is opened again.

       The threshold is declared as a STRING even though it is a number. A
       numeric default makes listState parse it with `page`'s rule — `n >= 1` or
       the default — and 0 is the one threshold this screen most needs: "at or
       below 0" is the out-of-stock view. Empty means "the default, 5". */
    const list = listState({
        q: "",
        threshold: "",
        location_id: 0,
        sort: "",
        order: "",
        page: 1,
        // The starting page size, not the only one: the operator can change it
        // and the choice rides in the URL with the rest of the screen's state.
        limit: PER_PAGE,
    });
    const perPage = $derived(list.params.limit);

    /* inventory.read is the nav's gate on this screen; inventory.write is what
       every stock move needs. Both are said again here, so a typed address gets
       the refusal rather than a screen where each button 403s. */
    const readable = $derived(can("inventory.read"));
    const writable = $derived(can("inventory.write"));

    let loading = $state(true);
    let variants = $state([]);
    let meta = $state(null);

    /* A typed or truncated URL is not a request: anything that is not a whole
       number at or above zero reads as the default, the same answer listState
       gives its own numeric keys. */
    function readThreshold(raw) {
        const n = parseInt(raw, 10);
        return raw !== "" && Number.isFinite(n) && n >= 0 ? n : DEFAULT_THRESHOLD;
    }

    const threshold = $derived(readThreshold(list.params.threshold));
    let draftThreshold = $state(readThreshold(list.params.threshold));

    /* Two modes, one table. A search term switches the screen from the low-stock
       report to the catalog, because that is exactly the question being asked:
       "where is this SKU" cannot be answered by a report that only lists what is
       nearly gone. Clearing the box comes back. */
    const term = $derived(list.params.q.trim());
    const searching = $derived(!!term);
    let draftSearch = $state(list.params.q);
    const canSearch = $derived(can("catalog.read"));

    /*
     * A header is clickable exactly when the request behind it can carry that
     * ordering, which on this screen means the two modes offer different ones.
     *
     * The report is GET /api/admin/inventory/low-stock, whose allow-list is the
     * four numbers on the row — and its stock keys follow the location filter,
     * so the column being sorted is always the column being read.
     *
     * A search is the PRODUCT listing flattened into variants, and the only key
     * of its allow-list that is honest about these rows is the product's title:
     * its `available` orders products by their summed availability, which is
     * not the per-variant figure in the column. Sorting the page in hand
     * instead was the alternative and it would be a lie — twenty-five rows out
     * of four hundred, alphabetised, reads as the whole list ordered.
     *
     * readSort answers NO_SORT for a key outside the mode's list, so switching
     * modes with `sort=sku` still on the URL falls back to the default order
     * rather than to a 400, and coming back restores it.
     */
    const REPORT_SORTS = ["sku", "on_hand", "reserved", "available"];
    const SEARCH_SORTS = ["title"];
    const sortFields = $derived(searching ? SEARCH_SORTS : REPORT_SORTS);
    const sort = $derived(readSort(list.params, sortFields));
    const sortBy = (field, firstDesc) => list.set(cycleSort(sort, field, firstDesc));

    /* The rows ticked for a bulk move. Cleared by a filter change, as
       selection.svelte.js asks: acting on rows nobody can see is how forty
       SKUs get moved off the wrong shelf. */
    const sel = selection();
    $effect(() => {
        list.params.q;
        list.params.threshold;
        list.params.location_id;
        sel.clear();
    });

    /* Two searches in flight settle on whichever arrived last unless one of them
       is told it is stale. */
    let reqId = 0;

    /* Deliberately not `locationID`, which is the drawer's selected source
       shelf: one name for two things would make picking a shelf inside the
       drawer silently re-query the list behind it. */
    const filterLocationID = $derived(list.params.location_id);
    let filterLocations = $state([]);
    const chosenLocation = $derived(
        filterLocations.find((l) => l.id === filterLocationID) ?? null,
    );

    /* The one bulk action these rows have is the transfer, so a store with
       nowhere to move stock to gets no checkbox column: a selection whose only
       verb is impossible is a column of controls that do nothing. */
    const selectable = $derived(writable && filterLocations.length > 1);

    /* Which product a row belongs to.
     *
     * The low-stock report answers with VARIANTS, which carry a product_id and
     * no title — so a row could only ever say `TS-RED-M`, and an operator who
     * does not know the SKU scheme by heart could not tell what it was. One GET
     * per distinct product on the page fills that in, cached for the session, so
     * paging back and forth costs nothing. A join belongs in the report itself,
     * and that is reported rather than worked around here; what the panel must
     * not do is guess a name it was not given. */
    let products = $state(new Map());

    let adjustOpen = $state(false);
    let target = $state(null);
    let mode = $state("adjust"); // adjust | set | move | history
    let amount = $state("");
    // Why the stock moved, in the operator's words. It is the row an audit
    // reads, and the only place in the panel that collects one — the product
    // form and the variant matrix deliberately send none rather than a canned
    // sentence no human typed.
    let reason = $state("");

    let receiveOpen = $state(false);

    /* The bulk transfer. `moveSeed` is what the drawer starts its list from —
       the ticked rows, or nothing when it is opened from the header — and it is
       held rather than read live so that clearing the selection behind the
       drawer does not empty the form in front of it. */
    let moveOpen = $state(false);
    let moveSeed = $state([]);

    function openMove(rows) {
        moveSeed = rows;
        moveOpen = true;
    }

    /* Presets rather than free text alone: an operator with a case to put away
       should not have to compose prose, and a short shared vocabulary is what
       makes the ledger searchable three weeks later. */
    const REASONS = {
        adjust: ["Delivery received", "Damaged", "Shrinkage", "Returned to supplier", "Found"],
        set: ["Stock count", "Correction"],
        move: ["Rebalancing", "Store request"],
    };
    let saving = $state(false);
    let error = $state("");

    // Where the variant in the drawer actually is. Loaded per open rather than
    // with the list: the table is about totals, and one row's breakdown is a
    // question only asked when somebody is about to move something.
    let rows = $state([]);
    let rowsLoading = $state(false);
    let locationID = $state(0);
    let toID = $state(0);

    const multi = $derived(rows.length > 1);
    const here = $derived(rows.find((r) => r.location_id === locationID) ?? null);
    const elsewhere = $derived(rows.filter((r) => r.location_id !== locationID));

    $effect(() => {
        // Any parameter changing reloads: the search, the threshold, the
        // location, the page.
        list.params;
        load();
    });

    $effect(() => {
        loadLocations();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        try {
            if (searching) {
                if (!canSearch) {
                    variants = [];
                    meta = null;
                    return;
                }
                const found = await findVariants(term, {
                    page: list.page,
                    limit: perPage,
                    ...sortQuery(sort),
                });
                if (mine !== reqId) return;
                variants = found.rows;
                meta = found.meta;
            } else {
                const result = await api.get(
                    "/api/admin/inventory/low-stock" +
                        // sortQuery rather than the URL's own two strings: a
                        // hand-typed `order=desc` with no `sort` is a 400 at the
                        // engine, and a stale key from the other mode is not a
                        // key this request can carry.
                        list.query({ threshold, limit: perPage, ...sortQuery(sort) }),
                );
                if (mine !== reqId) return;
                variants = result.data ?? [];
                meta = result.meta;
                nameProducts(variants);
            }
        } catch (err) {
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    /** The product each row belongs to, for the rows that arrived without one. */
    async function nameProducts(pageRows) {
        if (!canSearch) return;
        const wanted = [
            ...new Set(pageRows.map((v) => v.product_id).filter((id) => id && !products.has(id))),
        ];
        if (!wanted.length) return;
        const found = await Promise.all(
            wanted.map(async (id) => {
                try {
                    const p = await api.get(`/api/admin/products/${id}`);
                    return [id, { title: p.title, image_url: p.image_url ?? "" }];
                } catch {
                    // A row that keeps only its SKU is a worse label, not a
                    // broken screen.
                    return null;
                }
            }),
        );
        const next = new Map(products);
        for (const entry of found) if (entry) next.set(entry[0], entry[1]);
        products = next;
    }

    /** Whatever is known about the product behind a row, from either mode. */
    function productOf(variant) {
        return variant.product ?? products.get(variant.product_id) ?? null;
    }

    /* Loaded once, and the picker is hidden entirely below two — a
       single-location store should never have to learn the word, which is the
       stance the engine itself takes. */
    async function loadLocations() {
        try {
            const result = await api.get("/api/admin/locations");
            filterLocations = result.data ?? [];
        } catch {
            // The filter is an extra; the list below it still works without it.
        }
    }

    function applyThreshold(e) {
        e.preventDefault();
        // An emptied or negative box is not a threshold; leave the applied one
        // alone rather than asking the server about a count that cannot exist.
        if (draftThreshold === undefined || draftThreshold === null || draftThreshold < 0) return;
        list.set({ threshold: String(draftThreshold) });
    }

    function resetThreshold() {
        draftThreshold = DEFAULT_THRESHOLD;
        list.set({ threshold: "" });
    }

    /** Out of stock is not a separate report: it is this one at zero. */
    function onlyEmpty() {
        draftThreshold = 0;
        list.set({ threshold: "0" });
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draftSearch });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "" });
    }

    function openAdjust(variant, initialMode, event) {
        event?.stopPropagation();
        target = variant;
        mode = initialMode;
        amount = "";
        reason = "";
        error = "";
        rows = [];
        locationID = 0;
        toID = 0;
        adjustOpen = true;
        loadRows(variant.id, initialMode);
    }

    async function loadRows(variantID, initialMode) {
        rowsLoading = true;
        try {
            const result = await api.get(`/api/admin/variants/${variantID}/stock`);
            rows = result.data ?? [];
            // Rows arrive in priority order, so the first one holding anything is
            // the shelf an order would come off — the same reading the locations
            // page states in its footer. Falling back to the first row keeps a
            // variant that is nowhere yet pointed at somewhere real.
            //
            // An open shelf first, though, and that is not cosmetic: stock may
            // not arrive at a closed location, so pre-selecting one whose only
            // units are stranded would make the drawer's default action a 409.
            //
            // A filtered screen overrides all of that: somebody looking at one
            // shop's shelf means that shop's shelf.
            const filtered = filterLocationID
                ? rows.find((r) => r.location_id === filterLocationID)
                : null;
            const start =
                filtered ??
                rows.find((r) => r.active && r.on_hand !== 0) ??
                rows.find((r) => r.active) ??
                rows[0];
            locationID = start?.location_id ?? 0;
            toID =
                rows.find((r) => r.location_id !== locationID && r.active)?.location_id ?? 0;
            if (initialMode === "set") amount = String(start?.on_hand ?? 0);
        } catch (err) {
            error = err.message;
        } finally {
            rowsLoading = false;
        }
    }

    function pickLocation(id) {
        locationID = id;
        if (mode === "set") amount = String(rows.find((r) => r.location_id === id)?.on_hand ?? 0);
        if (toID === id) toID = elsewhere.find((r) => r.active)?.location_id ?? 0;
        error = "";
    }

    /* The engine's own sentence, so the two surfaces do not paraphrase each
       other. It is a pre-check rather than a replacement for the 409: the
       location could close between the page loading and the button. */
    const CLOSED = "That location is closed. Stock moves out of a closed location, never into it.";

    async function save(event) {
        event?.preventDefault();
        const value = parseInt(amount, 10);
        if (isNaN(value)) {
            error = "Enter a whole number.";
            return;
        }
        if (mode === "set" && value < 0) {
            error = "Stock cannot be negative.";
            return;
        }
        if (mode === "move" && value <= 0) {
            error = "Move a positive number of units.";
            return;
        }
        if (mode === "move" && !toID) {
            error = "Choose where the units are going.";
            return;
        }
        // All three movements, not just the transfer: receiving and counting up
        // are arrivals too, and each of them is refused at a closed shelf.
        const destination = mode === "move" ? rows.find((r) => r.location_id === toID) : here;
        const arriving =
            mode === "move" ||
            (mode === "adjust" && value > 0) ||
            (mode === "set" && value > (here?.on_hand ?? 0));
        if (arriving && destination && !destination.active) {
            error = CLOSED;
            return;
        }
        // The panel's rule, not the API's: the engine accepts a blank reason
        // from any client, and a script or the MCP tool may well send one. But
        // a count that replaced the number and a write-off are exactly the two
        // movements somebody asks about later, so the panel insists.
        if ((mode === "set" || (mode === "adjust" && value < 0)) && !reason.trim()) {
            error = "Say why — this is the row an audit will read.";
            return;
        }

        saving = true;
        error = "";
        try {
            if (mode === "move") {
                await api.post(`/api/admin/variants/${target.id}/stock/transfer`, {
                    from_location_id: locationID,
                    to_location_id: toID,
                    quantity: value,
                    reason,
                });
                toast.success(`Moved ${value} × ${target.sku}`);
            } else {
                const body =
                    mode === "adjust"
                        ? { adjust: value, location_id: locationID, reason }
                        : { set: value, location_id: locationID, reason };
                await api.post(`/api/admin/variants/${target.id}/inventory`, body);
                toast.success(`Updated ${target.sku}`);
            }
            adjustOpen = false;
            // Back to the first page: the row that was just fixed usually leaves
            // the low-stock list, and page 4 of a shorter list is a screen nobody
            // asked for. A search is not a report of what is wrong, so its rows
            // stay exactly where they were.
            if (searching) await load();
            else list.setPage(1);
        } catch (err) {
            // The engine refuses to drop stock below what is reserved for open
            // orders, and explains why — show that rather than a generic error.
            error = err.message;
        } finally {
            saving = false;
        }
    }
</script>

<svelte:head><title>Inventory · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="inventory.read" what="stock levels" />
{:else}

<div class="page page-inventory shopify-skin">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Inventory</div></nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={() => load()}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            {#if canSearch}
                <!-- The way to a SKU that is not low. Typing here is what turns
                     the report into the catalog; the address bar says which. -->
                <form class="fields searchbar" onsubmit={submitSearch}>
                    <div class="field">
                        <input
                            type="text"
                            class="p-l-20"
                            placeholder="Find any SKU or product"
                            aria-label="Find any SKU or product"
                            bind:value={draftSearch}
                        />
                    </div>
                    {#if draftSearch || term}
                        <div class="field addon p-r-5">
                            {#if draftSearch !== list.params.q}
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
            {/if}

            {#if !searching}
                <form class="fields searchbar" onsubmit={applyThreshold}>
                    <div class="field addon">
                        <label class="txt-nowrap" for="threshold">Available at or below</label>
                    </div>
                    <div class="field">
                        <input id="threshold" type="number" min="0" bind:value={draftThreshold} />
                    </div>
                    <div class="field addon p-r-5">
                        {#if draftThreshold !== threshold}
                            <button type="submit" class="btn sm pill warning">Apply</button>
                        {/if}
                        {#if threshold !== 0}
                            <button
                                type="button"
                                class="btn sm pill secondary transparent"
                                onclick={onlyEmpty}
                            >
                                Out of stock
                            </button>
                        {/if}
                        {#if threshold !== DEFAULT_THRESHOLD}
                            <button
                                type="button"
                                class="btn sm pill secondary transparent"
                                onclick={resetThreshold}
                            >
                                Reset
                            </button>
                        {/if}
                    </div>
                </form>
            {/if}

            <!-- Filters and the one primary action share the right-hand group,
                 as the orders screen's do: a bare `.field` in the header is
                 `width: 100%` and takes a line of its own. -->
            <div class="page-header-primary-btns">
                {#if !searching && filterLocations.length > 1}
                    <!-- Only while the report is showing. The catalog search
                         reaches the whole store — the product listing takes no
                         location — so offering the picker there would be a
                         control that silently does nothing. -->
                    <div class="field">
                        <Select
                            id="location-filter"
                            ariaLabel="Location"
                            value={filterLocationID}
                            options={[
                                { value: 0, label: "Whole store" },
                                ...filterLocations.map((l) => ({
                                    value: l.id,
                                    label: l.name + (l.active ? "" : " — closed"),
                                })),
                            ]}
                            onchange={(v) => list.set({ location_id: Number(v) || 0 })}
                        />
                    </div>
                {/if}

                {#if writable && canSearch && filterLocations.length > 1}
                    <!-- Only where there is somewhere to move stock TO. A
                         single-location store has no transfer, which is the
                         stance the engine takes as well. -->
                    <button
                        type="button"
                        class="btn secondary"
                        onclick={() => openMove([])}
                    >
                        <i class="ri-arrow-left-right-line" aria-hidden="true"></i>
                        <span class="txt">Move</span>
                    </button>
                {/if}

                {#if writable && canSearch}
                    <button type="button" class="btn" onclick={() => (receiveOpen = true)}>
                        <i class="ri-inbox-archive-line" aria-hidden="true"></i>
                        <span class="txt">Receive</span>
                    </button>
                {/if}
            </div>
        </header>

        <div class="alert info m-b-sm">
            <p>
                <i class="ri-information-line" aria-hidden="true"></i>
                {#if searching}
                    Every variant of every product matching <strong>{term}</strong>, whatever it
                    holds. The figures are the store's total — open a row to see each shelf and
                    correct one.
                {:else}
                    <strong>Available</strong> is on hand minus what is reserved for orders in
                    flight. Stock moves as a delta or an absolute count, never as a blind
                    overwrite — so a sale that lands mid-edit cannot be lost.
                    {#if filterLocations.length > 1}
                        Without a location chosen, the threshold is against the store's total — a
                        variant with one unit in each of five shops is not low by that reading,
                        even though every shelf looks it.
                    {/if}
                {/if}
            </p>
        </div>

        <div class="page-table-wrapper">
            <table class="table responsive-table" class:optimize={variants.length > 60}>
                <thead class="sticky">
                    <tr>
                        {#if selectable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-stock"
                                        type="checkbox"
                                        checked={sel.allSelected(variants)}
                                        onchange={() => sel.toggleAll(variants)}
                                    />
                                    <!-- "on this page", not "all": the panel does
                                         not hold the other pages and the API has
                                         no select-everything call. -->
                                    <label
                                        for="select-all-stock"
                                        aria-label="Select every row on this page"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <!-- Sortable in one mode each, because the two modes are
                             two different requests: the report orders by the
                             numbers on the row and knows nothing about a product
                             title, and the search is the product listing, which
                             orders by title and by nothing else that is true of
                             a single variant. A header that cannot be carried is
                             drawn as a fact rather than as a broken control. -->
                        {#if searching}
                            <SortHeader
                                field="title"
                                label="Product"
                                class="col-field-name-id"
                                {sort}
                                onsort={sortBy}
                            />
                            <th class="col-field-type-text">SKU</th>
                        {:else}
                            <th class="col-field-name-id">Product</th>
                            <SortHeader
                                field="sku"
                                label="SKU"
                                class="col-field-type-text"
                                {sort}
                                onsort={sortBy}
                            />
                        {/if}
                        {#if searching}
                            <th class="col-field-type-number min-width">On hand</th>
                            <th class="col-field-type-number min-width">Reserved</th>
                            <th class="col-field-type-number min-width">Available</th>
                        {:else}
                            <SortHeader
                                field="on_hand"
                                label="On hand{chosenLocation ? ' here' : ''}"
                                class="col-field-type-number min-width"
                                firstDesc
                                {sort}
                                onsort={sortBy}
                            />
                            <SortHeader
                                field="reserved"
                                label="Reserved{chosenLocation ? ' here' : ''}"
                                class="col-field-type-number min-width"
                                firstDesc
                                {sort}
                                onsort={sortBy}
                            />
                            <SortHeader
                                field="available"
                                label="Available{chosenLocation ? ' here' : ''}"
                                class="col-field-type-number min-width"
                                firstDesc
                                {sort}
                                onsort={sortBy}
                            />
                        {/if}
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each variants as variant (variant.id)}
                        <!-- Derived per row rather than by swapping objects: the
                             two shapes do not share key names, so
                             `variant.at_location ?? variant` would render two
                             blank columns in exactly the mode this exists for. -->
                        {@const al = variant.at_location}
                        {@const onHand = al ? al.on_hand : variant.stock_on_hand}
                        {@const reserved = al ? al.reserved : variant.stock_reserved}
                        {@const available = al ? al.available : variant.available}
                        {@const product = productOf(variant)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => openAdjust(variant, "adjust")}
                            onkeydown={(e) => rowKey(e, () => openAdjust(variant, "adjust"))}
                        >
                            {#if selectable}
                                <!-- stopPropagation rather than a guard inside
                                     the row handler: ticking a box must not also
                                     open the adjust drawer over the table. -->
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-stock-{variant.id}"
                                            type="checkbox"
                                            checked={sel.has(variant.id)}
                                            onchange={() => sel.toggle(variant.id)}
                                        />
                                        <label
                                            for="select-stock-{variant.id}"
                                            aria-label="Select {variant.sku}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Product">
                                <div class="row-product">
                                    <!-- A fixed frame whether or not there is a
                                         picture, so a catalog half of whose
                                         products have one still reads as a
                                         column. -->
                                    <div class="row-thumb">
                                        {#if product?.image_url}
                                            <img src={product.image_url} alt="" loading="lazy" />
                                        {:else}
                                            <i class="ri-image-line" aria-hidden="true"></i>
                                        {/if}
                                    </div>
                                    <div class="row-name">
                                        {#if product}
                                            <span class="txt-bold txt-ellipsis">
                                                {product.title}
                                            </span>
                                        {:else}
                                            <span class="txt-hint txt-ellipsis">
                                                {variant.label || "—"}
                                            </span>
                                        {/if}
                                        {#if product && variant.label}
                                            <span class="txt-hint txt-sm row-handle">
                                                {variant.label}
                                            </span>
                                        {/if}
                                    </div>
                                </div>
                            </td>
                            <td class="col-field-type-text" data-name="SKU">
                                <span class="txt-bold txt-code">{variant.sku}</span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="On hand">
                                {onHand}
                            </td>
                            <td
                                class="col-field-type-number min-width txt-hint"
                                data-name="Reserved"
                            >
                                {reserved}
                            </td>
                            <td
                                class="col-field-type-number min-width txt-bold {stockClass(
                                    available,
                                    variant.track_inventory !== false,
                                )}"
                                data-name="Available"
                            >
                                {variant.track_inventory === false ? "not tracked" : available}
                                {#if al}
                                    <div class="txt-hint txt-sm txt-nowrap">
                                        {variant.available} in the store
                                    </div>
                                {/if}
                            </td>
                            <td class="col-meta min-width">
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Stock take for {variant.sku}"
                                        title="Stock take"
                                        onclick={(e) => openAdjust(variant, "set", e)}
                                    >
                                        <i class="ri-list-check-2" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !variants.length}
                        {#each Array(6) as _, i (i)}
                            <tr>
                                <td colspan={selectable ? 7 : 6}><span class="skeleton-loader"></span></td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !variants.length}
                        <tr>
                            <td colspan={selectable ? 7 : 6} class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i
                                        class={searching
                                            ? "ri-search-line"
                                            : "ri-checkbox-circle-line"}
                                        style="font-size: 32px"
                                        aria-hidden="true"
                                    ></i>
                                </div>
                                {#if searching}
                                    Nothing matches <strong>{term}</strong>. Product search reads
                                    titles and descriptions, so a SKU only answers when it is
                                    typed in full and its product is active.
                                    <a
                                        href="#clear"
                                        onclick={(e) => (e.preventDefault(), clearSearch())}
                                    >
                                        Back to what is running low</a
                                    >.
                                {:else}
                                    Nothing is {threshold === 0 ? "out of stock" : "running low"}{chosenLocation
                                        ? ` at ${chosenLocation.name}`
                                        : ""}. Every tracked variant has more than
                                    {threshold} available{chosenLocation ? " there" : ""}.
                                    {#if canSearch}
                                        Search above to reach any SKU, low or not.
                                    {/if}
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>

        </div>

        {#if selectable}
            <BulkBar count={sel.count} noun="SKU" onclear={() => sel.clear()}>
                <!-- One action, because there is exactly one thing a set of
                     SKUs can be asked to do that is not a per-row correction:
                     go somewhere else. It is the same per-row transfer route a
                     single row already uses, run once per line. -->
                <button
                    type="button"
                    class="btn sm secondary"
                    onclick={() => openMove(sel.pick(variants))}
                >
                    <i class="ri-arrow-left-right-line" aria-hidden="true"></i>
                    <span class="txt">Move stock…</span>
                </button>
            </BulkBar>
        {/if}

        <footer class="page-footer">
            <!-- "product", not "variant", in search mode: the total is the
                 listing's own count of PRODUCTS that matched, and the rows are
                 their variants. Describing rows with somebody else's total is
                 how a footer starts lying. -->
            <Pager
                {meta}
                {loading}
                noun={searching ? "product" : "variant"}
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <span class="txt txt-hint">
                {#if searching}
                    matching {term}
                {:else}
                    at or below {threshold}{chosenLocation
                        ? ` — ${pluralize(meta?.total ?? 0, "variant")} this location carries`
                        : ""}
                {/if}
            </span>
            <div class="flex-fill"></div>
            <ThemeToggle />
        </footer>
    </div>
</div>

<ReceiveDelivery
    open={receiveOpen}
    locations={filterLocations}
    locationID={filterLocationID}
    onclose={() => (receiveOpen = false)}
    ondone={() => load()}
/>

<!--
    The bulk transfer. Its source defaults to whichever shelf the screen is
    filtered to, because that is the shelf whose rows were just ticked.
-->
<MoveStock
    open={moveOpen}
    locations={filterLocations}
    fromID={filterLocationID}
    seed={moveSeed}
    onclose={() => (moveOpen = false)}
    ondone={() => (sel.clear(), load())}
/>

<Drawer
    open={adjustOpen}
    size={mode === "history" ? "lg" : "sm"}
    title={target
        ? mode === "adjust"
            ? `Receive stock — ${target.sku}`
            : mode === "move"
              ? `Move stock — ${target.sku}`
              : mode === "history"
                ? `History — ${target.sku}`
                : `Stock take — ${target.sku}`
        : ""}
    onclose={() => (adjustOpen = false)}
>
    {#if target}
        <form id="stock-form" onsubmit={save}>
            {#if productOf(target)}
                <!-- Which product this SKU is, once, at the top: the drawer is
                     reached from a search as often as from the report now, and a
                     bare SKU in a title is not an answer to "is this the right
                     one". -->
                <div class="txt-hint m-b-sm">
                    {productOf(target).title}{target.label ? ` — ${target.label}` : ""}
                </div>
            {/if}

            {#if multi}
                <!--
                    With more than one location the totals are a sum, and every
                    movement below acts on exactly one row — so the row has to be
                    the thing being pointed at, not a setting buried in a select.
                -->
                <table class="table stock-locations m-b-base">
                    <thead>
                        <tr>
                            <th>Location</th>
                            <th class="txt-right">On hand</th>
                            <th class="txt-right">Reserved</th>
                            <th class="txt-right">Available</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as r (r.location_id)}
                            <!-- Reachable from the keyboard, because this row is
                                 the only control that says which shelf the form
                                 below acts on: without it a keyboard operator on
                                 a multi-location store is stuck with whatever
                                 openAdjust defaulted to and can neither adjust
                                 nor move stock anywhere else. -->
                            <tr
                                class="handle"
                                tabindex="0"
                                aria-selected={r.location_id === locationID}
                                class:selected={r.location_id === locationID}
                                onclick={() => pickLocation(r.location_id)}
                                onkeydown={(e) => rowKey(e, () => pickLocation(r.location_id))}
                            >
                                <td>
                                    <span class="txt-bold">{r.location_name}</span>
                                    {#if !r.active}
                                        <span class="label">closed</span>
                                    {/if}
                                </td>
                                <td class="txt-right">{r.on_hand}</td>
                                <td class="txt-right txt-hint">{r.reserved}</td>
                                <td class="txt-right txt-bold {stockClass(r.available)}">
                                    {r.available}
                                </td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            {:else}
                <div class="flex m-b-sm">
                    <span class="txt-hint">On hand</span>
                    <div class="flex-fill"></div>
                    <strong>{target.stock_on_hand}</strong>
                </div>
                <div class="flex m-b-base">
                    <span class="txt-hint">Reserved for open orders</span>
                    <div class="flex-fill"></div>
                    <strong>{target.stock_reserved}</strong>
                </div>
            {/if}

            {#if mode === "history"}
                <!--
                    The location table above stays visible, so the operator can
                    see where the units are while reading how they got there.
                -->
                <!-- Keyed, so moving to another variant remounts the table
                     rather than asking for page 3 of a history it has not
                     started reading. -->
                {#key target.id}
                    <StockHistory scope="variant" id={target.id} />
                {/key}
            {:else}
                {#if mode === "move"}
                    <div class="field required">
                        <label for="move-to">Move to</label>
                        <!-- Closed destinations are disabled with the reason
                             rather than hidden: the table two rows above still
                             lists them, and a silently shorter list explains
                             nothing. -->
                        <select id="move-to" bind:value={toID}>
                            {#each elsewhere as r (r.location_id)}
                                <option value={r.location_id} disabled={!r.active}>
                                    {r.location_name}{r.active ? "" : " — closed"}
                                </option>
                            {/each}
                        </select>
                    </div>
                {/if}

                <div class="field required" class:error={!!error}>
                    <label for="amount">
                        {mode === "adjust"
                            ? "Add (or subtract) this many"
                            : mode === "move"
                              ? "How many units"
                              : "Set the count to"}
                    </label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="amount"
                        type="number"
                        autofocus
                        bind:value={amount}
                        oninput={() => (error = "")}
                    />
                </div>
                {#if error}<div class="field-help error">{error}</div>{/if}
                <div class="field-help">
                    {#if mode === "adjust"}
                        A delta: 25 receives a case, -1 writes one off.
                        {#if multi && here}Applied at {here.location_name}.{/if}
                    {:else if mode === "move"}
                        Reserved units stay where they are — they are promised to orders that will be
                        picked from {here?.location_name ?? "here"}.
                    {:else}
                        Cannot go below the {multi
                            ? (here?.reserved ?? 0)
                            : target.stock_reserved} already promised to open orders{multi && here
                            ? ` at ${here.location_name}`
                            : ""}.
                    {/if}
                </div>

                <div class="field" class:required={mode === "set"}>
                    <label for="reason">Why</label>
                    <div class="inline-flex gap-5 m-b-5 flex-wrap">
                        {#each REASONS[mode] ?? [] as preset (preset)}
                            <button
                                type="button"
                                class="btn sm pill secondary"
                                onclick={() => ((reason = preset), (error = ""))}
                            >
                                {preset}
                            </button>
                        {/each}
                    </div>
                    <input
                        id="reason"
                        type="text"
                        maxlength="200"
                        bind:value={reason}
                        oninput={() => (error = "")}
                    />
                </div>
                <div class="field-help">
                    This is the row an audit will read — "the count says 4 and the shelf has 2" is
                    answered here or nowhere.
                </div>

                <h6 class="section-title m-t-base">Recent movements</h6>
                <!--
                    The person about to change a count is exactly the person who
                    needs to see what happened last time, so it is here rather than
                    behind another click.
                -->
                {#key target.id}
                    <StockHistory scope="variant" id={target.id} compact limit={3} />
                {/key}
            {/if}
        </form>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (adjustOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        {#if multi && mode !== "move" && mode !== "history"}
            <button
                type="button"
                class="btn secondary"
                onclick={() => ((mode = "move"), (amount = ""), (error = ""))}
            >
                <i class="ri-arrow-left-right-line" aria-hidden="true"></i>
                <span class="txt">Move</span>
            </button>
        {/if}
        {#if mode === "history"}
            <button type="button" class="btn" onclick={() => ((mode = "adjust"), (error = ""))}>
                <span class="txt">Back</span>
            </button>
        {:else}
            <button
                type="button"
                class="btn secondary"
                onclick={() => ((mode = "history"), (error = ""))}
            >
                <i class="ri-history-line" aria-hidden="true"></i>
                <span class="txt">History</span>
            </button>
            {#if writable}
                <button
                    type="submit"
                    form="stock-form"
                    class="btn"
                    class:loading={saving || rowsLoading}
                    disabled={saving || rowsLoading}
                >
                    <span class="txt">{mode === "move" ? "Move" : "Apply"}</span>
                </button>
            {/if}
        {/if}
    {/snippet}
</Drawer>
{/if}
