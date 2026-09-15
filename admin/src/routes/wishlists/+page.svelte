<script>
    /**
     * Wishlists: what shoppers wanted and did not buy.
     *
     * The ranking is the half worth opening — what is wanted most, and how
     * much of it the store has run out of, which is a demand signal no other
     * screen carries. The lists themselves are the other half, and they show
     * how many people a restock notice could reach. Neither shows a token:
     * that is the shopper's own credential.
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatMoney, formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 25;
    const readable = $derived(can("customers.read"));
    const missing = $derived(modulesKnown() && !hasModule("wishlist"));

    /* The view, the filter and the page live in the URL, so a link to "the
       out-of-stock ones" reopens on them. */
    const list = listState({ view: "wanted", stock: "", q: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    const view = $derived(list.params.view);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    $effect(() => {
        list.params;
        if (readable && hasModule("wishlist")) load();
    });

    async function load() {
        loading = true;
        try {
            const p = list.params;
            const path =
                p.view === "lists"
                    ? "/api/admin/x/wishlist/lists" +
                      query({ q: p.q, with_email: p.stock === "reachable" ? "1" : "", page: list.page, limit: perPage })
                    : "/api/admin/x/wishlist/wanted" +
                      query({ out_of_stock: p.stock === "out" ? "1" : "", page: list.page, limit: perPage });
            const result = await api.get(path);
            rows = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draft.trim() });
    }

    const waitingTotal = $derived(view === "wanted" ? rows.reduce((n, r) => n + r.waiting, 0) : 0);
</script>

<svelte:head><title>Wishlists · GoCommerce</title></svelte:head>

<div class="page page-wishlists shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Customers</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Wishlists</div>
            </nav>

            {#if readable && !missing}
                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if view === "lists"}
                    <form class="fields searchbar" onsubmit={submitSearch}>
                        <div class="field">
                            <input type="text" class="p-l-20" placeholder="Search by email" bind:value={draft} />
                        </div>
                        {#if draft || list.params.q}
                            <div class="field addon p-r-5">
                                {#if draft !== list.params.q}
                                    <button type="submit" class="btn sm pill warning">Search</button>
                                {/if}
                                <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>Clear</button>
                            </div>
                        {/if}
                    </form>
                {/if}

                <div class="page-header-primary-btns">
                    <div class="field">
                        <Select
                            id="wishlist-view"
                            ariaLabel="View"
                            value={view}
                            options={[
                                { value: "wanted", label: "Most wanted" },
                                { value: "lists", label: "The lists" },
                            ]}
                            onchange={(v) => list.set({ view: v, stock: "", page: 1 })}
                        />
                    </div>
                    <div class="field">
                        {#if view === "wanted"}
                            <Select
                                id="wishlist-stock"
                                ariaLabel="Stock"
                                value={list.params.stock}
                                options={[
                                    { value: "", label: "Everything wanted" },
                                    { value: "out", label: "Out of stock only" },
                                ]}
                                onchange={(v) => list.set({ stock: v, page: 1 })}
                            />
                        {:else}
                            <Select
                                id="wishlist-reach"
                                ariaLabel="Reachable"
                                value={list.params.stock}
                                options={[
                                    { value: "", label: "Every list" },
                                    { value: "reachable", label: "With an email" },
                                ]}
                                onchange={(v) => list.set({ stock: v, page: 1 })}
                            />
                        {/if}
                    </div>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="customers.read" what="wishlists" />
        {:else if missing}
            <ModuleMissing module="wishlist" what="Wishlists are served by ext/wishlist, and this binary does not have it." />
        {:else if view === "wanted"}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Product</th>
                            <th class="col-field-type-number min-width">On lists</th>
                            <th class="col-field-type-number min-width">Reachable</th>
                            <th class="col-field-type-number min-width">Available</th>
                            <th class="col-field-type-number min-width">Price</th>
                            <th class="col-field-type-select min-width">Status</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.product_id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Product">
                                    <div class="row-name row-name-stacked">
                                        {#if row.title}
                                            <a href="{base}/products/{row.product_id}" class="txt-bold txt-ellipsis">{row.title}</a>
                                        {:else}
                                            <span class="txt-bold">product #{row.product_id}</span>
                                        {/if}
                                        <span class="txt-hint txt-sm txt-code">{row.slug || "deleted"}</span>
                                    </div>
                                </td>
                                <td class="col-field-type-number min-width txt-bold" data-name="On lists">{row.lists}</td>
                                <td class="col-field-type-number min-width txt-hint" data-name="Reachable" title="Lists that gave an email, so a restock notice would reach somebody">
                                    {row.waiting}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Available">
                                    {#if row.in_stock}
                                        {row.available}
                                    {:else}
                                        <span class="label label-danger">none</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Price">{row.price ? formatMoney(row.price) : "—"}</td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    <span class="label {row.status === 'active' ? 'label-success' : ''}">{row.status || "deleted"}</span>
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr>
                                <td colspan="6" class="txt-center txt-hint p-base">
                                    {list.params.stock === "out"
                                        ? "Nothing wanted is out of stock — every saved product can be bought."
                                        : "Nothing saved yet. A heart on the product page posts to /x/wishlist."}
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="product" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
                {#if waitingTotal > 0}
                    <span class="txt txt-hint">{waitingTotal} shoppers on this page left an email, so a restock notice would reach them.</span>
                {/if}
            </footer>
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Shopper</th>
                            <th class="col-field-type-number min-width">Saved</th>
                            <th class="col-field-type-date min-width">Started</th>
                            <th class="col-field-type-date min-width">Last touched</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Shopper">
                                    {#if row.email}
                                        <a href="mailto:{row.email}" class="txt-bold">{row.email}</a>
                                    {:else}
                                        <span class="txt-hint">a guest, no email</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Saved">{row.items}</td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Started" title={formatDate(row.created_at)}>{relativeTime(row.created_at)}</td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Last touched" title={formatDate(row.updated_at)}>{relativeTime(row.updated_at)}</td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr><td colspan="4" class="txt-center txt-hint p-base">No lists yet.</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="list" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
                <span class="txt txt-hint">A list's token is the shopper's own credential and is never shown here.</span>
            </footer>
        {/if}
    </div>
</div>
