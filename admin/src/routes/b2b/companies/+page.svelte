<script>
    /**
     * Companies: the businesses that buy from this store on their own terms,
     * and — on the second tab — the territories they serve as dealers.
     *
     * Not the same thing as Customers, which is every order grouped by the
     * address on it. A company is a record the store creates — a credit limit,
     * payment terms, a customer group whose prices its buyers get — and its
     * buyers are shopper accounts that joined it.
     *
     * Territories are a tab here rather than a screen of their own because the
     * question they answer, "who covers where", is asked while managing
     * dealers: typically just after adding a territory was refused because
     * somebody already has it. A tab keeps that one click away without a
     * fifth B2B entry in the nav, and the tab, its filters and its page all
     * ride in the URL like any list's.
     *
     * Adding a company goes straight to its page, because a company with no
     * buyers cannot order and the next thing anybody does is add them.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { COUNTRIES } from "$lib/countries.js";
    import {
        companyStatusClass,
        companyStatusLabel,
        countryName,
        dealerOption,
        loadDealers,
        termsLabel,
    } from "$lib/b2b.js";
    import CompanyEditor from "$lib/components/CompanyEditor.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ view: "", q: "", country: "", company_id: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    const territoriesView = $derived(list.params.view === "territories");

    const readable = $derived(can("companies.read"));
    const writable = $derived(can("companies.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);
    let editorOpen = $state(false);
    let reqId = 0;

    let dealers = $state([]);
    let dealersLoaded = $state(false);

    $effect(() => {
        list.params;
        if (readable && hasModule("b2b")) load();
    });

    $effect(() => {
        if (territoriesView && readable && hasModule("b2b") && !dealersLoaded) fetchDealers();
    });

    $effect(() => onNewShortcut(openNew));

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = territoriesView
                ? await api.get(
                      "/api/admin/x/b2b/territories" +
                          query({ country: p.country, company_id: p.company_id, page: p.page, limit: p.limit }),
                  )
                : await api.get("/api/admin/x/b2b/companies" + query({ q: p.q, page: p.page, limit: p.limit }));
            // A second search typed while the first was in flight must not be
            // overwritten by the first one's late answer — nor a tab's rows by
            // the other tab's.
            if (mine !== reqId) return;
            rows = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function fetchDealers() {
        dealersLoaded = true;
        try {
            dealers = (await loadDealers(api, { dealersOnly: true })).dealers;
        } catch (err) {
            dealersLoaded = false;
            toast.error(err);
        }
    }

    function showView(view) {
        if (view === list.params.view) return;
        // Rows of the other shape must not flash under the new headings.
        rows = [];
        meta = null;
        draft = "";
        list.set({ view, q: "", country: "", company_id: "" });
    }

    function openNew() {
        if (writable && !missing) editorOpen = true;
    }

    function search(event) {
        event.preventDefault();
        list.set({ q: draft.trim() });
    }

    async function created(company) {
        editorOpen = false;
        await goto(`${base}/b2b/companies/${company.id}`);
    }

    const open = (companyId) => goto(`${base}/b2b/companies/${companyId}`);

    const countryOptions = [
        { value: "", label: "Every country" },
        ...COUNTRIES.map((c) => ({ value: c.value, label: `${c.label} (${c.value})`, short: c.label })),
    ];
    const dealerOptions = $derived([{ value: "", label: "Every dealer" }, ...dealers.map(dealerOption)]);
</script>

<svelte:head><title>{territoriesView ? "Territories" : "Companies"} · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="companies" />
{:else}
    <div class="page page-companies b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Customers</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Companies</div>
                </nav>
                <div class="flex-fill"></div>
                {#if writable && !missing && !territoriesView}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn" aria-label="New company" onclick={openNew}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">New company</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Companies are served by ext/b2b, and this binary does not have it." />
            {:else}
                <div class="tabs-header m-b-base" role="tablist" aria-label="What to list">
                    <button
                        type="button"
                        role="tab"
                        class="tab-item"
                        class:active={!territoriesView}
                        aria-selected={!territoriesView}
                        onclick={() => showView("")}
                    >
                        Companies
                    </button>
                    <button
                        type="button"
                        role="tab"
                        class="tab-item"
                        class:active={territoriesView}
                        aria-selected={territoriesView}
                        onclick={() => showView("territories")}
                    >
                        Territories
                    </button>
                </div>

                {#if territoriesView}
                    <p class="field-help m-b-base b2b-notice">
                        Every dealer's territories, widest first. An enquiry goes to the most specific one
                        covering its address; a territory is added and taken back on its dealer's page.
                    </p>
                    <div class="b2b-inline-form m-b-base">
                        <div class="field b2b-wide">
                            <label for="territory-country-filter">Country</label>
                            <Select
                                id="territory-country-filter"
                                value={list.params.country}
                                options={countryOptions}
                                onchange={(v) => list.set({ country: v })}
                            />
                        </div>
                        <div class="field b2b-wide">
                            <label for="territory-dealer-filter">Dealer</label>
                            <Select
                                id="territory-dealer-filter"
                                value={list.params.company_id}
                                options={dealerOptions}
                                disabled={!dealersLoaded}
                                onchange={(v) => list.set({ company_id: v })}
                            />
                        </div>
                    </div>
                {:else}
                    <p class="field-help m-b-base b2b-notice">
                        A business that buys from this store on its own terms: a credit limit, payment
                        terms, and the prices of the customer group its buyers join. Its buyers are
                        shopper accounts, added on the company's page.
                    </p>

                    <form class="fields m-b-base" onsubmit={search} role="search">
                        <div class="field">
                            <label for="company-search">Search</label>
                            <input
                                id="company-search"
                                type="search"
                                placeholder="Name or code"
                                autocomplete="off"
                                bind:value={draft}
                                oninput={(e) => {
                                    // Typing narrows as it goes; Enter only commits
                                    // what is already there.
                                    list.set({ q: e.currentTarget.value.trim() });
                                }}
                            />
                        </div>
                    </form>
                {/if}

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && rows.length > 0}>
                    {#if territoriesView}
                        <table class="table responsive-table">
                            <thead class="sticky">
                                <tr>
                                    <th class="col-field-name-id">Country</th>
                                    <th class="min-width">State</th>
                                    <th class="min-width">Postcodes</th>
                                    <th>Dealer</th>
                                    <th class="min-width">Added</th>
                                </tr>
                            </thead>
                            <tbody>
                                {#if loading && !rows.length}
                                    {#each Array(4) as _, i (i)}
                                        <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                    {/each}
                                {/if}
                                {#each rows as t (t.id)}
                                    <tr
                                        class="handle"
                                        tabindex="0"
                                        onclick={() => open(t.company_id)}
                                        onkeydown={(e) => rowKey(e, () => open(t.company_id))}
                                    >
                                        <td class="col-field-name-id" data-name="Country">
                                            <div class="row-name row-name-stacked">
                                                <span class="txt-bold">{countryName(t.country)}</span>
                                                <span class="txt-hint txt-sm txt-code">{t.country}</span>
                                            </div>
                                        </td>
                                        <td class="min-width" data-name="State">
                                            {#if t.state}{t.state}{:else}<span class="txt-hint">Every state</span>{/if}
                                        </td>
                                        <td class="min-width" data-name="Postcodes">
                                            {#if t.postal_prefix}
                                                <span class="txt-code">{t.postal_prefix}…</span>
                                            {:else}
                                                <span class="txt-hint">Any</span>
                                            {/if}
                                        </td>
                                        <td data-name="Dealer">
                                            <a
                                                href="{base}/b2b/companies/{t.company_id}"
                                                class="txt-bold"
                                                onclick={(e) => e.stopPropagation()}>{t.company_name}</a
                                            >
                                        </td>
                                        <td class="min-width txt-hint txt-sm" data-name="Added">
                                            {formatDate(t.created_at, { withTime: false })}
                                        </td>
                                    </tr>
                                {/each}
                                {#if !loading && !rows.length}
                                    <tr>
                                        <td colspan="5" class="txt-hint txt-center p-base">
                                            {#if list.params.country || list.params.company_id}
                                                No territory matches that.
                                            {:else}
                                                No dealer has a territory yet, so every enquiry stays with
                                                the store. Give one on a company's page.
                                            {/if}
                                        </td>
                                    </tr>
                                {/if}
                            </tbody>
                        </table>
                    {:else}
                        <table class="table responsive-table">
                            <thead class="sticky">
                                <tr>
                                    <th class="col-field-name-id">Company</th>
                                    <th class="min-width">Status</th>
                                    <th class="col-field-type-number min-width">Credit limit</th>
                                    <th class="min-width">Terms</th>
                                    <th class="col-field-type-number min-width">Buyers</th>
                                    <th class="min-width">Added</th>
                                </tr>
                            </thead>
                            <tbody>
                                {#if loading && !rows.length}
                                    {#each Array(4) as _, i (i)}
                                        <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                    {/each}
                                {/if}
                                {#each rows as company (company.id)}
                                    <tr
                                        class="handle"
                                        tabindex="0"
                                        onclick={() => open(company.id)}
                                        onkeydown={(e) => rowKey(e, () => open(company.id))}
                                    >
                                        <td class="col-field-name-id" data-name="Company">
                                            <div class="row-name row-name-stacked">
                                                <a
                                                    href="{base}/b2b/companies/{company.id}"
                                                    class="txt-bold"
                                                    onclick={(e) => e.stopPropagation()}>{company.name}</a
                                                >
                                                <span class="txt-hint txt-sm txt-code">{company.code}</span>
                                            </div>
                                        </td>
                                        <td class="min-width" data-name="Status">
                                            <span class="label {companyStatusClass(company.status)}">
                                                {companyStatusLabel(company.status)}
                                            </span>
                                        </td>
                                        <td class="col-field-type-number min-width" data-name="Credit limit">
                                            {#if company.credit_limit}
                                                {formatMoney(company.credit_limit)}
                                            {:else}
                                                <span class="txt-hint">No account</span>
                                            {/if}
                                        </td>
                                        <td class="min-width" data-name="Terms">
                                            <span class:txt-hint={!company.credit_limit}>{termsLabel(company)}</span>
                                        </td>
                                        <td class="col-field-type-number min-width" data-name="Buyers">
                                            {company.member_count}
                                        </td>
                                        <td class="min-width txt-hint txt-sm" data-name="Added">
                                            {formatDate(company.created_at, { withTime: false })}
                                        </td>
                                    </tr>
                                {/each}
                                {#if !loading && !rows.length}
                                    <tr>
                                        <td colspan="6" class="txt-hint txt-center p-base">
                                            {#if list.params.q}
                                                No company's name or code matches “{list.params.q}”.
                                            {:else}
                                                No companies yet. Add one, then add the people who buy for
                                                it.
                                            {/if}
                                        </td>
                                    </tr>
                                {/if}
                            </tbody>
                        </table>
                    {/if}
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <Pager
                        {meta}
                        {loading}
                        noun={territoriesView ? "territory" : "company"}
                        plural={territoriesView ? "territories" : "companies"}
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <CompanyEditor open={editorOpen} onclose={() => (editorOpen = false)} onsaved={created} />
{/if}
