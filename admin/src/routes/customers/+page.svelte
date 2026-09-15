<script>
    /**
     * Customers: the orders, grouped by who placed them.
     *
     * There is no customer record to open — see customers.go — and there is not
     * going to be one: D22 makes guest checkout permanent, so a customer is an
     * email that several orders share and nothing else. A detail view is still
     * possible and worth having, because everything it would show is already
     * reachable: the lifetime figures come with the row, and the orders come
     * from `GET /api/admin/orders?email=`, the same filter this screen has
     * always linked to. CustomerDetail composes the two.
     *
     * The row opens that drawer; the arrow beside it is a real link to the
     * filtered Orders screen, so middle-click and "open in new tab" still work.
     * Neither is a full page reload any more.
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { downloadFile } from "$lib/download.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { selection } from "$lib/selection.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import { formatMoney, formatDate, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import CustomerDetail from "$lib/components/CustomerDetail.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    /* The search, the ordering and the page live in the URL, and the window
       replaces the rows rather than accumulating them: an accumulator holding
       four pages cannot honestly write page=4 in the address bar, and it is
       what interleaves two orderings into one table when the sort changes. */
    const list = listState({ q: "", sort: "", order: "", page: 1, limit: PER_PAGE });

    const SORT_FIELDS = ["email", "orders", "spent", "first_order_at", "last_order_at"];

    /* Read-only by nature: a customer is an email several orders share and
       there is nothing here to write. The gate is the nav's own. */
    const readable = $derived(can("customers.read"));

    let customers = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let detailOpen = $state(false);
    let selected = $state(null);

    function openCustomer(customer) {
        selected = customer;
        detailOpen = true;
    }

    const search = $derived(list.params.q);
    const sort = $derived(readSort(list.params, SORT_FIELDS));
    const perPage = $derived(list.params.limit);
    let draftSearch = $state(list.params.q);

    /* Two header clicks leave two requests in flight, and the table would
       otherwise settle on whichever reply arrived last rather than on the
       header that is lit. */
    let reqId = 0;

    $effect(() => {
        list.params;
        load();
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
            const res = await api.get(
                "/api/admin/customers" + list.query({ limit: perPage, ...sortQuery(sort) }),
            );
            if (mine !== reqId) return;
            customers = res.data ?? [];
            meta = res.meta ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    // ------------------------------------------------------------ selection

    /*
     * selection() keys on `row.id`, and a customer has none: the email IS the
     * identity here (customers.go returns no id, and D22 says there will never
     * be a customer record to have one). So the rows the table and the
     * selection work over carry the email as their id.
     */
    const rows = $derived(customers.map((c) => ({ ...c, id: c.email })));

    const sel = selection();

    /* Cleared by a filter change, kept across a page change — the rule every
       other list follows, for the reason selection.svelte.js gives. */
    $effect(() => {
        list.params.q;
        sel.clear();
    });

    const picked = $derived(sel.pick(rows));

    /**
     * What a selection of customers can be acted on with.
     *
     * Exactly one thing, and that is the engine's shape rather than an
     * omission: there is no customer record to write — a customer is an email
     * several orders share — so this screen has no per-row write route for a
     * bulk action to be N calls to. Copying the addresses needs no endpoint at
     * all, which is why it is the one action here and why it is not a request.
     */
    async function copyEmails() {
        const text = picked.map((c) => c.email).join(", ");
        if (!text) return;
        try {
            await navigator.clipboard.writeText(text);
            toast.success(`Copied ${picked.length} ${pluralize(picked.length, "address", "addresses")}`);
        } catch {
            // Clipboard access is refused in plenty of ordinary situations — an
            // insecure origin, a browser setting — and pretending it worked is
            // worse than saying it did not.
            toast.error("This browser would not let the panel use the clipboard");
        }
    }

    /**
     * The mailing list, which is the thing operators actually come here for.
     *
     * Products and orders have had an export since the beginning and the people
     * did not, so "send the newsletter to everyone who has bought something"
     * meant writing SQL. `GET /api/admin/export/admin-customers` is the same
     * streaming shape as the other two, behind the same `data.export`.
     *
     * It carries the whole of this screen's state — `q`, `sort`, `order` are
     * every parameter the listing has and the endpoint takes all three — so
     * unlike the orders export there is no filter it quietly drops, and the
     * title does not have to apologise for one. It exports every row that
     * matched, not the page on screen.
     */
    function exportCSV() {
        const suffix = query({ q: search, ...sortQuery(sort) });
        downloadFile("/api/admin/export/admin-customers" + suffix, "customers.csv", "text/csv");
    }

    function submitSearch(event) {
        event.preventDefault();
        list.set({ q: draftSearch.trim() });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "" });
    }
</script>

<svelte:head><title>Customers · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="customers.read" what="customers" />
{:else}

<div class="page page-customers shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Customers</div>
            </nav>

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
                        placeholder="Search customers by email or name"
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

            {#if can("data.export")}
                <div class="page-header-primary-btns">
                    <button
                        type="button"
                        class="btn sm secondary"
                        onclick={exportCSV}
                        title="One row per customer, for the search and the ordering on screen — every matching row, not just this page"
                    >
                        <i class="ri-download-2-line" aria-hidden="true"></i>
                        <span class="txt">Export CSV</span>
                    </button>
                </div>
            {/if}
        </header>

        <!-- DESIGN.md §5: a border and a background step, never a shadow. No
             `overflow-hidden` — this element is the table's scroller. -->
        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        <th class="col-bulk-select min-width">
                            <div class="field">
                                <input
                                    id="select-all-customers"
                                    type="checkbox"
                                    checked={sel.allSelected(rows)}
                                    onchange={() => sel.toggleAll(rows)}
                                />
                                <!-- "on this page", not "all": the panel does
                                     not hold the other pages and the API has no
                                     select-everything call. -->
                                <label
                                    for="select-all-customers"
                                    aria-label="Select every customer on this page"
                                ></label>
                            </div>
                        </th>
                        <SortHeader
                            field="email"
                            label="Customer"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Location is the address snapshot the most recent
                             order carries, not a column: there is nothing to
                             order it by. -->
                        <th class="col-field-type-text">Location</th>
                        <!-- The two denominators are different, and a reader
                             seeing "3 orders / $0.00" without knowing that will
                             read it as a bug rather than as a cash-on-delivery
                             shopper with three parcels in the post. So each
                             column still says what it counts — in its tooltip
                             and in the footer, not in a parenthetical that made
                             the header the widest cell in its column and pushed
                             the table past the edge of the page. -->
                        <SortHeader
                            field="orders"
                            label="Orders"
                            note="Not counting cancelled orders"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="spent"
                            label="Spent"
                            note="Only money that has arrived"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- "Customer since" was on the wire and sortable by
                             hand-typed URL, with no column to click. -->
                        <SortHeader
                            field="first_order_at"
                            label="Since"
                            class="col-field-type-date min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="last_order_at"
                            label="Last order"
                            class="col-field-type-date min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each rows as customer (customer.id)}
                        <!-- The drawer, which is their orders and their figures
                             put back together. The arrow beside it is the same
                             destination the whole row used to be: a real link,
                             so it can be opened in a tab. -->
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => openCustomer(customer)}
                            onkeydown={(e) => rowKey(e, () => openCustomer(customer))}
                        >
                            <!-- stopPropagation rather than a guard inside the
                                 row handler: ticking a box must not also open
                                 the customer. -->
                            <td class="col-bulk-select min-width" onclick={(e) => e.stopPropagation()}>
                                <div class="field">
                                    <input
                                        id="select-customer-{customer.id}"
                                        type="checkbox"
                                        checked={sel.has(customer.id)}
                                        onchange={() => sel.toggle(customer.id)}
                                    />
                                    <label
                                        for="select-customer-{customer.id}"
                                        aria-label="Select {customer.email}"
                                    ></label>
                                </div>
                            </td>
                            <td class="col-field-name-id" data-name="Customer">
                                <div class="row-name">
                                    <span class="txt-bold txt-ellipsis">
                                        {customer.name || customer.email}
                                    </span>
                                    {#if customer.name}
                                        <span class="txt-hint txt-sm row-handle">{customer.email}</span>
                                    {/if}
                                    <!-- The number you would ring about an
                                         order. It arrived on every row and was
                                         thrown away. -->
                                    {#if customer.phone}
                                        <span class="txt-hint txt-sm row-handle">
                                            {customer.phone}
                                        </span>
                                    {/if}
                                </div>
                            </td>
                            <td class="col-field-type-text txt-hint" data-name="Location">
                                <!-- City and country, not the full postal
                                     address. The street and postcode pushed
                                     this column to roughly 40 characters, which
                                     made the table wider than the page at 1440
                                     and left "Last order" sitting underneath
                                     the sticky action column — clipped to its
                                     first letter, so the screen read as broken
                                     at rest. The column is called Location: a
                                     line1 and a postcode are for addressing a
                                     parcel, and they are on the order. -->
                                <span class="txt-ellipsis">
                                    {[
                                        customer.address?.city,
                                        customer.address?.state,
                                        customer.address?.country,
                                    ]
                                        .filter(Boolean)
                                        .join(", ") || "—"}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Orders">
                                {customer.orders}
                                <span class="txt-hint txt-sm">
                                    {pluralize(customer.orders, "order")}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width txt-bold" data-name="Spent">
                                {formatMoney(customer.spent)}
                            </td>
                            <td class="col-field-type-date min-width txt-hint" data-name="Since">
                                {formatDate(customer.first_order_at, { withTime: false })}
                            </td>
                            <td class="col-field-type-date min-width txt-hint" data-name="Last order">
                                {formatDate(customer.last_order_at)}
                            </td>
                            <td class="col-meta min-width">
                                <a
                                    href="{base}/orders?email={encodeURIComponent(customer.email)}"
                                    class="btn circle sm transparent secondary"
                                    title="Their orders"
                                    aria-label="Orders placed by {customer.email}"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                </a>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !customers.length}
                        {#each Array(6) as _, i (i)}
                            <tr><td colspan="8"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {:else if !customers.length}
                        <tr>
                            <td colspan="8" class="txt-center txt-hint p-base">
                                {search
                                    ? "No customer matches that."
                                    : "Nobody has ordered yet. A customer appears here with their first order."}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        <!-- One action, because one is all this screen honestly has: see
             copyEmails. It is still a selection the same shape as every other
             list's, so ticking rows means the same thing here as it does on
             products. -->
        <BulkBar count={sel.count} noun="customer" onclear={() => sel.clear()}>
            <button type="button" class="btn sm secondary" onclick={copyEmails}>
                <i class="ri-file-copy-line" aria-hidden="true"></i>
                <span class="txt">Copy email addresses</span>
            </button>
        </BulkBar>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager
                {meta}
                {loading}
                noun="customer"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <div class="flex-fill"></div>
            <!-- The two counts have different denominators, and the columns say
                 so in their own headings. This says why once, in prose. -->
            <span class="txt-hint">
                A cancelled order is still an order but not a sale, and money only counts once it
                has arrived — so the two figures rarely match.
            </span>
        </footer>
    </div>
</div>

<CustomerDetail
    open={detailOpen}
    customer={selected}
    onclose={() => (detailOpen = false)}
/>
{/if}
