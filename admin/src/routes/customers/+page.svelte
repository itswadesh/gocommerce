<script>
    /**
     * Customers: the orders, grouped by who placed them.
     *
     * There is no customer record to open — see customers.go — so a row's only
     * action is to show that person's orders, which the Orders screen already
     * does. The row links there rather than opening a drawer over a thing that
     * does not exist.
     */
    import { base } from "$app/paths";
    import { api } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import { formatMoney, formatDate, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Pager from "$lib/components/Pager.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const PER_PAGE = 25;

    /* The search, the ordering and the page live in the URL, and the window
       replaces the rows rather than accumulating them: an accumulator holding
       four pages cannot honestly write page=4 in the address bar, and it is
       what interleaves two orderings into one table when the sort changes. */
    const list = listState({ q: "", sort: "", order: "", page: 1 });

    /* first_order_at is accepted by the engine and has no column here to click.
       That is deliberate and recorded in docs/admin-panel.md: the API is
       complete, the screen shows the four things an operator asked for. */
    const SORT_FIELDS = ["email", "orders", "spent", "first_order_at", "last_order_at"];

    let customers = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    const search = $derived(list.params.q);
    const sort = $derived(readSort(list.params, SORT_FIELDS));
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
        const mine = ++reqId;
        loading = true;
        try {
            const res = await api.get(
                "/api/admin/customers" + list.query({ limit: PER_PAGE, ...sortQuery(sort) }),
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

<div class="page page-customers">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Customers</div></nav>

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
        </header>

        <div class="page-table-wrapper">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        <SortHeader
                            field="email"
                            label="Customer"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Location is one line of the address snapshot the
                             most recent order carries, not a column: there is
                             nothing to order it by. -->
                        <th class="col-field-type-text">Location</th>
                        <SortHeader
                            field="orders"
                            label="Orders"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="spent"
                            label="Spent"
                            class="col-field-type-number min-width"
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
                    {#each customers as customer (customer.email)}
                        <!-- Their orders, which is the only thing behind a
                             customer here. -->
                        <tr
                            class="handle"
                            onclick={() =>
                                (window.location.href = `${base}/orders?email=${encodeURIComponent(customer.email)}`)}
                        >
                            <td class="col-field-name-id" data-name="Customer">
                                <div class="row-name">
                                    <span class="txt-bold txt-ellipsis">
                                        {customer.name || customer.email}
                                    </span>
                                    {#if customer.name}
                                        <span class="txt-hint txt-sm row-handle">{customer.email}</span>
                                    {/if}
                                </div>
                            </td>
                            <td class="col-field-type-text txt-hint" data-name="Location">
                                {[customer.address?.city, customer.address?.country]
                                    .filter(Boolean)
                                    .join(", ") || "—"}
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
                            <td class="col-field-type-date min-width txt-hint" data-name="Last order">
                                {formatDate(customer.last_order_at)}
                            </td>
                            <td class="col-meta min-width">
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !customers.length}
                        {#each Array(6) as _, i (i)}
                            <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {:else if !customers.length}
                        <tr>
                            <td colspan="6" class="txt-center txt-hint p-base">
                                {search
                                    ? "No customer matches that."
                                    : "Nobody has ordered yet. A customer appears here with their first order."}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        <footer class="page-footer">
            <Pager {meta} {loading} noun="customer" onpage={(n) => list.setPage(n)} />
            <div class="flex-fill"></div>
            <ThemeToggle />
        </footer>
    </div>
</div>
